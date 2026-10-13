package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kumekay/discarica/internal/auth"
	"golang.org/x/oauth2"
)

// fakeForge serves the OAuth2 + user API surface a self-hosted Gitea or Forgejo
// exposes, so the provider can be pointed at it as if it were the real thing.
func fakeForge(t *testing.T, uid int64, login string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"forge-token","token_type":"bearer"}`))
	})
	mux.HandleFunc("GET /api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer forge-token" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": uid, "login": login})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Self-hosted forges share the GitHub-compatible endpoints; both providers must
// derive the same URLs from the instance base URL, with a trailing slash tolerated.
func TestSelfHostedProvidersDeriveEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *auth.Provider
	}{
		{"gitea", auth.Gitea("https://forge.example.com/", "cid", "sec", "https://app.example.com/auth/gitea/callback")},
		{"forgejo", auth.Forgejo("https://forge.example.com", "cid", "sec", "https://app.example.com/auth/forgejo/callback")},
	} {
		if tc.p.Name() != tc.name {
			t.Errorf("Name() = %q, want %q", tc.p.Name(), tc.name)
		}
		want := "https://forge.example.com/login/oauth/authorize?client_id=cid"
		if got := tc.p.AuthCodeURL("state"); !strings.HasPrefix(got, want) {
			t.Errorf("%s AuthCodeURL = %q, want prefix %q", tc.name, got, want)
		}
	}
}

// Identity is keyed on (provider, uid), so gitea and forgejo must stay distinct
// providers even when the same numeric account id comes back from both.
func TestExchangeSelfHosted(t *testing.T) {
	srv := fakeForge(t, 1, "ku")

	for _, tc := range []struct {
		build func(base, cid, sec, red string) *auth.Provider
		name  string
		label string
	}{
		{auth.Gitea, "gitea", "Gitea"},
		{auth.Forgejo, "forgejo", "Forgejo"},
	} {
		p := tc.build(srv.URL, "cid", "sec", srv.URL+"/auth/"+tc.name+"/callback")
		if p.Label() != tc.label {
			t.Errorf("Label() = %q, want %q", p.Label(), tc.label)
		}
		id, err := p.Exchange(t.Context(), "code")
		if err != nil {
			t.Fatalf("%s exchange: %v", tc.name, err)
		}
		if id.Provider != tc.name || id.UID != "1" || id.Login != "ku" {
			t.Errorf("unexpected identity: %+v", id)
		}
	}
}

func TestProviderLabel(t *testing.T) {
	gh := auth.GitHub("cid", "sec", "https://app.example.com/auth/github/callback")
	if gh.Label() != "GitHub" {
		t.Errorf("GitHub label = %q", gh.Label())
	}
	// Unknown provider names fall back to the name itself.
	other := auth.New("zotero", "cid", "sec", oauth2.Endpoint{
		AuthURL:  "https://example.com/authorize",
		TokenURL: "https://example.com/token",
	}, "https://example.com/user", "https://app.example.com/cb")
	if other.Label() != "zotero" {
		t.Errorf("unknown label = %q, want the provider name", other.Label())
	}
}
