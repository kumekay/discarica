package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ownerSession creates a session for the same user that owns e.token's pages.
func (e *env) ownerSession() string {
	e.t.Helper()
	u, err := e.store.UserByToken(e.t.Context(), e.token)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := e.store.CreateSession(e.t.Context(), u.ID, time.Now().Add(time.Hour))
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// post sends a form POST with an optional session cookie, without following redirects.
func (e *env) post(path, session string, form url.Values) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(e.t.Context(), "POST", e.srv.URL+path,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "discarica_session", Value: session})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestDashboardRequiresLogin(t *testing.T) {
	e := newEnv(t)
	resp := e.get("/dashboard", "")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("got %d -> %q, want redirect to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestDashboardShowsTokenAndPages(t *testing.T) {
	e := newEnv(t)
	e.makePage("My report", "r.html", "<p>x</p>", false)
	sess := e.ownerSession()

	resp := e.get("/dashboard", sess)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := body(t, resp)
	if !strings.Contains(got, e.token) {
		t.Error("dashboard should show the upload token")
	}
	if !strings.Contains(got, "My report") {
		t.Error("dashboard should list pages")
	}
}

func TestDashboardTokenRefresh(t *testing.T) {
	e := newEnv(t)
	sess := e.ownerSession()

	resp := e.post("/dashboard/token", sess, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("refresh: status %d", resp.StatusCode)
	}
	// Old token is dead.
	r := e.call("GET", "/api/pages", "", nil, e.token)
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token should be invalid, got %d", r.StatusCode)
	}
	// New token appears on the dashboard.
	resp = e.get("/dashboard", sess)
	if strings.Contains(body(t, resp), e.token) {
		t.Error("dashboard still shows the old token")
	}
}

func TestDashboardVisibilityAndDelete(t *testing.T) {
	e := newEnv(t)
	id := e.makePage("a", "a.html", "<p>a</p>", false)
	sess := e.ownerSession()

	resp := e.post("/dashboard/pages/"+id+"/visibility", sess, url.Values{"public": {"true"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("toggle: status %d", resp.StatusCode)
	}
	p, err := e.store.PageByID(t.Context(), id)
	if err != nil || !p.Public {
		t.Errorf("page should be public: %+v err=%v", p, err)
	}

	resp = e.post("/dashboard/pages/"+id+"/delete", sess, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
	if _, err := e.store.PageByID(t.Context(), id); err == nil {
		t.Error("page should be deleted")
	}
}

func TestDashboardCannotTouchForeignPages(t *testing.T) {
	e := newEnv(t)
	id := e.makePage("a", "a.html", "<p>a</p>", false)
	// A different logged-in user.
	sess := e.session()

	resp := e.post("/dashboard/pages/"+id+"/delete", sess, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign delete: got %d, want 404", resp.StatusCode)
	}
	if _, err := e.store.PageByID(t.Context(), id); err != nil {
		t.Error("page should still exist")
	}
}
