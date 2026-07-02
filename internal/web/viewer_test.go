package web_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pageEnv uploads one page and returns its id.
func (e *env) makePage(name, filename, content string, public bool) string {
	e.t.Helper()
	fields := map[string]string{"name": name}
	if public {
		fields["public"] = "true"
	}
	m := e.upload(filename, content, fields)
	return m["id"].(string)
}

// get fetches a path without following redirects, optionally with a session cookie.
func (e *env) get(path, session string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(e.t.Context(), "GET", e.srv.URL+path, nil)
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "discarica_session", Value: session})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *env) session() string {
	e.t.Helper()
	u, err := e.store.UpsertUser(e.t.Context(), "github", "99", "viewer")
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := e.store.CreateSession(e.t.Context(), u.ID, time.Now().Add(time.Hour))
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPublicPageView(t *testing.T) {
	e := newEnv(t)
	id := e.makePage("hi", "x.html", "<h1>public html</h1>", true)

	resp := e.get("/pub/"+id, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("expected sandbox CSP, got %q", csp)
	}
	if got := body(t, resp); got != "<h1>public html</h1>" {
		t.Errorf("unexpected body: %q", got)
	}
}

func TestPrivatePageOwnerOnly(t *testing.T) {
	e := newEnv(t)
	id := e.makePage("hi", "x.html", "<h1>secret</h1>", false)

	resp := e.get("/p/"+id, "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected redirect to login, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/login?next=") || !strings.Contains(loc, id) {
		t.Errorf("unexpected redirect: %q", loc)
	}

	// The owner can view.
	resp = e.get("/p/"+id, e.ownerSession())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner session: status %d", resp.StatusCode)
	}
	if got := body(t, resp); got != "<h1>secret</h1>" {
		t.Errorf("unexpected body: %q", got)
	}

	// A different logged-in user gets 404 — private means owner-only,
	// and existence is not leaked.
	for _, path := range []string{"/p/" + id, "/p/" + id + "/raw"} {
		resp = e.get(path, e.session())
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s as non-owner: got %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestVisibilityPathRedirects(t *testing.T) {
	e := newEnv(t)
	pub := e.makePage("a", "a.html", "<p>a</p>", true)
	priv := e.makePage("b", "b.html", "<p>b</p>", false)

	// /p/ access to a public page bounces to /pub/ for anyone but the owner.
	resp := e.get("/p/"+pub, "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/pub/"+pub {
		t.Errorf("anon /p/ on public page: got %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = e.get("/p/"+pub, e.session())
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/pub/"+pub {
		t.Errorf("non-owner /p/ on public page: got %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Private page via /pub/ redirects to the login-protected path.
	resp = e.get("/pub/"+priv, "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/p/"+priv {
		t.Errorf("/pub/ on private page: got %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestMarkdownRenderAndRaw(t *testing.T) {
	e := newEnv(t)
	id := e.makePage("Notes", "n.md", "# Title\n\nsome *text*", true)

	resp := e.get("/pub/"+id, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := body(t, resp)
	if !strings.Contains(got, "<h1") || !strings.Contains(got, "Title") {
		t.Errorf("markdown not rendered: %q", got)
	}
	if !strings.Contains(got, "<title>Notes</title>") {
		t.Errorf("page name should be the title: %q", got)
	}

	resp = e.get("/pub/"+id+"/raw", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("raw content-type: %q", ct)
	}
	if got := body(t, resp); got != "# Title\n\nsome *text*" {
		t.Errorf("raw body: %q", got)
	}
}

func TestMarkdownEscapesRawHTML(t *testing.T) {
	e := newEnv(t)
	id := e.makePage("evil", "e.md", "hello <script>alert(1)</script>", true)

	resp := e.get("/pub/"+id, "")
	if got := body(t, resp); strings.Contains(got, "<script>") {
		t.Errorf("raw HTML in markdown must be escaped: %q", got)
	}
}

func TestViewerNotFound(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/pub/nonexistent16chr", "/pub/nonexistent16chr/raw"} {
		resp := e.get(path, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, resp.StatusCode)
		}
	}
	// Private path with a session but missing page.
	resp := e.get("/p/nonexistent16chr", e.session())
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing private page: got %d, want 404", resp.StatusCode)
	}
}
