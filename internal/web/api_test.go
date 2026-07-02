package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kumekay/discarica/internal/store"
	"github.com/kumekay/discarica/internal/web"
)

type env struct {
	t     *testing.T
	srv   *httptest.Server
	store *store.Store
	token string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h := web.New(web.Config{Store: s, BaseURL: "https://discarica.example.com"})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u, err := s.UpsertUser(t.Context(), "github", "1", "alice")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	tok, err := s.ResetAPIToken(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("reset token: %v", err)
	}
	return &env{t: t, srv: srv, store: s, token: tok}
}

// call sends an authenticated API request and returns the response.
func (e *env) call(method, path, contentType string, body io.Reader, token string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// upload posts a multipart page upload and returns the decoded response.
func (e *env) upload(filename, content string, fields map[string]string) map[string]any {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", filename)
	_, _ = fw.Write([]byte(content))
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	_ = w.Close()

	resp := e.call("POST", "/api/pages", w.FormDataContentType(), &buf, e.token)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		e.t.Fatalf("upload: status %d: %s", resp.StatusCode, b)
	}
	return decode(e.t, resp)
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestAPIRequiresToken(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/pages"},
		{"GET", "/api/pages"},
		{"DELETE", "/api/pages/someid"},
		{"PATCH", "/api/pages/someid"},
	} {
		resp := e.call(tc.method, tc.path, "", nil, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without token: got %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
		resp = e.call(tc.method, tc.path, "", nil, "dsc_wrong")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with bad token: got %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestUploadPrivateHTML(t *testing.T) {
	e := newEnv(t)
	m := e.upload("report.html", "<h1>hi</h1>", map[string]string{"name": "My report"})

	if m["name"] != "My report" || m["kind"] != "html" || m["public"] != false {
		t.Errorf("unexpected response: %v", m)
	}
	url, _ := m["url"].(string)
	if !strings.HasPrefix(url, "https://discarica.example.com/p/") {
		t.Errorf("expected private /p/ url, got %q", url)
	}
}

func TestUploadPublicMarkdown(t *testing.T) {
	e := newEnv(t)
	m := e.upload("notes.md", "# hello", map[string]string{"public": "true"})

	if m["kind"] != "md" || m["public"] != true {
		t.Errorf("unexpected response: %v", m)
	}
	if m["name"] != "notes.md" {
		t.Errorf("name should default to filename, got %v", m["name"])
	}
	url, _ := m["url"].(string)
	if !strings.HasPrefix(url, "https://discarica.example.com/pub/") {
		t.Errorf("expected public /pub/ url, got %q", url)
	}
	raw, _ := m["raw_url"].(string)
	if !strings.HasSuffix(raw, "/raw") {
		t.Errorf("markdown should have raw_url, got %q", raw)
	}
}

func TestListPages(t *testing.T) {
	e := newEnv(t)
	e.upload("a.html", "<p>a</p>", nil)
	e.upload("b.md", "# b", nil)

	resp := e.call("GET", "/api/pages", "", nil, e.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d", resp.StatusCode)
	}
	var list []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(list))
	}
	if list[0]["name"] != "b.md" {
		t.Errorf("expected newest first, got %v", list[0]["name"])
	}
}

func TestGetPageWithContent(t *testing.T) {
	e := newEnv(t)
	m := e.upload("notes.md", "# hello agent", nil)
	id := m["id"].(string)

	resp := e.call("GET", "/api/pages/"+id, "", nil, e.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", resp.StatusCode)
	}
	got := decode(t, resp)
	if got["content"] != "# hello agent" || got["kind"] != "md" {
		t.Errorf("unexpected page: %v", got)
	}

	// Another user's token cannot read it.
	u2, _ := e.store.UpsertUser(t.Context(), "github", "2", "bob")
	tok2, _ := e.store.ResetAPIToken(t.Context(), u2.ID)
	resp = e.call("GET", "/api/pages/"+id, "", nil, tok2)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign get: got %d, want 404", resp.StatusCode)
	}
}

func TestVisibilityPatch(t *testing.T) {
	e := newEnv(t)
	m := e.upload("a.html", "<p>a</p>", nil)
	id := m["id"].(string)

	resp := e.call("PATCH", "/api/pages/"+id, "application/json",
		strings.NewReader(`{"public": true}`), e.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: status %d", resp.StatusCode)
	}
	got := decode(t, resp)
	if got["public"] != true {
		t.Errorf("expected public=true, got %v", got)
	}
	if !strings.Contains(got["url"].(string), "/pub/") {
		t.Errorf("url should switch to /pub/, got %v", got["url"])
	}
}

func TestDeletePage(t *testing.T) {
	e := newEnv(t)
	m := e.upload("a.html", "<p>a</p>", nil)
	id := m["id"].(string)

	resp := e.call("DELETE", "/api/pages/"+id, "", nil, e.token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
	// Deleting again: gone.
	resp = e.call("DELETE", "/api/pages/"+id, "", nil, e.token)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: got %d, want 404", resp.StatusCode)
	}
}

func TestCannotTouchForeignPage(t *testing.T) {
	e := newEnv(t)
	m := e.upload("a.html", "<p>a</p>", nil)
	id := m["id"].(string)

	// Second user with their own token.
	u2, _ := e.store.UpsertUser(t.Context(), "github", "2", "bob")
	tok2, _ := e.store.ResetAPIToken(t.Context(), u2.ID)

	resp := e.call("DELETE", "/api/pages/"+id, "", nil, tok2)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign delete: got %d, want 404", resp.StatusCode)
	}
	resp = e.call("PATCH", "/api/pages/"+id, "application/json",
		strings.NewReader(`{"public": true}`), tok2)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign patch: got %d, want 404", resp.StatusCode)
	}
	// Bob's list is empty.
	resp = e.call("GET", "/api/pages", "", nil, tok2)
	var list []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("bob should see no pages, got %d", len(list))
	}
}

func TestUploadRejectsMissingFile(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("name", "no file")
	_ = w.Close()

	resp := e.call("POST", "/api/pages", w.FormDataContentType(), &buf, e.token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing file, got %d", resp.StatusCode)
	}
}
