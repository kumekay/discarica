package mcpsrv_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kumekay/discarica/internal/mcpsrv"
	"github.com/kumekay/discarica/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type authRT struct {
	token string
}

func (a authRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}

type mcpEnv struct {
	t     *testing.T
	store *store.Store
	srv   *httptest.Server
	sess  *mcp.ClientSession
	user  store.User
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle("/mcp", mcpsrv.Handler(s, srv.URL))

	u, _ := s.UpsertUser(t.Context(), "github", "1", "alice")
	token, _ := s.GetOrCreateAPIToken(t.Context(), u.ID)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authRT{token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return &mcpEnv{t: t, store: s, srv: srv, sess: sess, user: u}
}

func (e *mcpEnv) callTool(name string, args map[string]any) map[string]any {
	e.t.Helper()
	res, err := e.sess.CallTool(e.t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		e.t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		e.t.Fatalf("call %s: tool error: %+v", name, res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		e.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func TestMCPRequiresAuth(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := httptest.NewServer(mcpsrv.Handler(s, "https://discarica.example.com"))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), "POST", srv.URL, strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
	if wa := resp.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "resource_metadata") {
		t.Errorf("WWW-Authenticate should point at resource metadata, got %q", wa)
	}
}

func TestMCPListTools(t *testing.T) {
	e := newMCPEnv(t)
	res, err := e.sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"upload_page": false, "list_pages": false, "read_page": false, "delete_page": false, "set_visibility": false}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %s", name)
		}
	}
}

func TestMCPUploadListDelete(t *testing.T) {
	e := newMCPEnv(t)

	up := e.callTool("upload_page", map[string]any{
		"content": "# hello", "name": "Hello", "format": "md", "public": true,
	})
	id, _ := up["id"].(string)
	if id == "" {
		t.Fatalf("no id: %v", up)
	}
	if url, _ := up["url"].(string); !strings.Contains(url, "/pub/") {
		t.Errorf("public page should get /pub/ url, got %v", up["url"])
	}
	if raw, _ := up["raw_url"].(string); !strings.HasSuffix(raw, "/raw") {
		t.Errorf("md upload should include raw_url, got %v", up)
	}

	// Page really exists and belongs to the token's user.
	p, err := e.store.PageByID(t.Context(), id)
	if err != nil || p.UserID != e.user.ID || string(p.Content) != "# hello" {
		t.Errorf("page not stored correctly: %+v err=%v", p, err)
	}

	list := e.callTool("list_pages", nil)
	pages, _ := list["pages"].([]any)
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %v", list)
	}

	// Another agent of the same user can read the content back.
	read := e.callTool("read_page", map[string]any{"id": id})
	if read["content"] != "# hello" || read["name"] != "Hello" {
		t.Errorf("read_page: %v", read)
	}

	vis := e.callTool("set_visibility", map[string]any{"id": id, "public": false})
	if url, _ := vis["url"].(string); !strings.Contains(url, "/p/") {
		t.Errorf("private page should get /p/ url, got %v", vis)
	}

	e.callTool("delete_page", map[string]any{"id": id})
	if _, err := e.store.PageByID(t.Context(), id); err == nil {
		t.Error("page should be deleted")
	}
}

func TestMCPDefaultsToPrivateHTML(t *testing.T) {
	e := newMCPEnv(t)
	up := e.callTool("upload_page", map[string]any{"content": "<h1>x</h1>", "name": "x"})
	if url, _ := up["url"].(string); !strings.Contains(url, "/p/") {
		t.Errorf("default should be private, got %v", up["url"])
	}
	id, _ := up["id"].(string)
	p, err := e.store.PageByID(t.Context(), id)
	if err != nil || p.Kind != "html" || p.Public {
		t.Errorf("expected private html page: %+v err=%v", p, err)
	}
}
