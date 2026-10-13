package web_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kumekay/discarica/internal/auth"
	"github.com/kumekay/discarica/internal/store"
	"github.com/kumekay/discarica/internal/web"
	"golang.org/x/oauth2"
)

// fakeIdP is a minimal OAuth2 identity provider for tests.
type fakeIdP struct {
	srv   *httptest.Server
	uid   int64
	login string
}

func newFakeIdP(t *testing.T, uid int64, login string) *fakeIdP {
	t.Helper()
	f := &fakeIdP{uid: uid, login: login}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(r.FormValue("redirect_uri"))
		q := u.Query()
		q.Set("code", "fake-code")
		q.Set("state", r.FormValue("state"))
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"fake-token","token_type":"bearer"}`)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.uid, "login": f.login})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) provider(name, redirectURL string) *auth.Provider {
	return auth.New(name, "client-id", "client-secret", oauth2.Endpoint{
		AuthURL:  f.srv.URL + "/authorize",
		TokenURL: f.srv.URL + "/token",
	}, f.srv.URL+"/user", redirectURL)
}

// loginEnv wires an app server whose "github" and "forgejo" providers are
// fake IdPs.
func loginEnv(t *testing.T) (*httptest.Server, *store.Store, *http.Client) {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	idp := newFakeIdP(t, 7, "alice")
	forge := newFakeIdP(t, 1, "ku")
	var srv *httptest.Server
	// The provider needs the app's callback URL, which we only know after
	// starting the server, so wire it up via a placeholder-then-rewrite.
	handler := func(w http.ResponseWriter, r *http.Request) {
		h := web.New(web.Config{
			Store:   s,
			BaseURL: srv.URL,
			Providers: []*auth.Provider{
				idp.provider("github", srv.URL+"/auth/github/callback"),
				forge.provider("forgejo", srv.URL+"/auth/forgejo/callback"),
			},
		})
		h.ServeHTTP(w, r)
	}
	srv = httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	return srv, s, client
}

func sessionCookie(t *testing.T, client *http.Client, srvURL string) string {
	t.Helper()
	u, _ := url.Parse(srvURL)
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == "discarica_session" {
			return c.Value
		}
	}
	return ""
}

func TestLoginFlow(t *testing.T) {
	srv, s, client := loginEnv(t)

	resp, err := client.Get(srv.URL + "/auth/github?next=/dashboard")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Followed redirects should land on /dashboard.
	if got := resp.Request.URL.Path; got != "/dashboard" {
		t.Errorf("expected to land on /dashboard, got %s", got)
	}

	tok := sessionCookie(t, client, srv.URL)
	if tok == "" {
		t.Fatal("no session cookie set")
	}
	u, err := s.UserBySession(t.Context(), tok)
	if err != nil {
		t.Fatalf("session not valid: %v", err)
	}
	if u.Provider != "github" || u.ProviderUID != "7" || u.Login != "alice" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	srv, _, client := loginEnv(t)

	// Start the flow to get a state cookie, but don't follow redirects.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Get(srv.URL + "/auth/github")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = resp.Body.Close()

	// Callback with a forged state must fail.
	resp, err = client.Get(srv.URL + "/auth/github/callback?code=fake-code&state=forged")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for forged state, got %d", resp.StatusCode)
	}
	if tok := sessionCookie(t, client, srv.URL); tok != "" {
		t.Error("no session should be set on forged state")
	}
}

func TestLoginRejectsOpenRedirect(t *testing.T) {
	srv, s, client := loginEnv(t)

	resp, err := client.Get(srv.URL + "/auth/github?next=https://evil.example.com/x")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if host := resp.Request.URL.Host; host != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("redirected off-site to %s", host)
	}
	_ = s
}

func TestLoginPageListsProviders(t *testing.T) {
	srv, _, client := loginEnv(t)

	resp, err := client.Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("login page: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/auth/github") {
		t.Error("login page should link to /auth/github")
	}
	// The forgejo route exists and is labelled for humans, not with the raw
	// provider key.
	if !strings.Contains(string(body), `/auth/forgejo?`) {
		t.Errorf("login page should link to /auth/forgejo, got:\n%s", body)
	}
	if !strings.Contains(string(body), "Log in with Forgejo") {
		t.Errorf("login page should say \"Log in with Forgejo\", got:\n%s", body)
	}
}

func TestForgejoLoginFlow(t *testing.T) {
	srv, s, client := loginEnv(t)

	resp, err := client.Get(srv.URL + "/auth/forgejo?next=/dashboard")
	if err != nil {
		t.Fatalf("forgejo login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Request.URL.Path; got != "/dashboard" {
		t.Errorf("expected to land on /dashboard, got %s", got)
	}

	tok := sessionCookie(t, client, srv.URL)
	if tok == "" {
		t.Fatal("no session cookie set")
	}
	u, err := s.UserBySession(t.Context(), tok)
	if err != nil {
		t.Fatalf("session not valid: %v", err)
	}
	if u.Provider != "forgejo" || u.ProviderUID != "1" || u.Login != "ku" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestLogout(t *testing.T) {
	srv, s, client := loginEnv(t)

	if _, err := client.Get(srv.URL + "/auth/github"); err != nil {
		t.Fatalf("login: %v", err)
	}
	tok := sessionCookie(t, client, srv.URL)
	if tok == "" {
		t.Fatal("no session after login")
	}

	resp, err := client.Post(srv.URL+"/logout", "", nil)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	_ = resp.Body.Close()
	if _, err := s.UserBySession(t.Context(), tok); err != store.ErrNotFound {
		t.Errorf("session should be gone after logout, got %v", err)
	}
}
