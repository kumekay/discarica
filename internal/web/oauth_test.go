package web_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const verifier = "test-verifier-string-of-decent-length"

func challenge() string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (e *env) registerClient(redirectURI string) string {
	e.t.Helper()
	resp := e.call("POST", "/oauth/register", "application/json",
		strings.NewReader(`{"client_name":"test-mcp","redirect_uris":["`+redirectURI+`"]}`), "")
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("register: status %d", resp.StatusCode)
	}
	m := decode(e.t, resp)
	id, _ := m["client_id"].(string)
	if id == "" {
		e.t.Fatalf("no client_id in %v", m)
	}
	return id
}

// authorize runs GET+POST /oauth/authorize with a session and returns the code.
func (e *env) authorize(clientID, redirectURI, state, sess string) string {
	e.t.Helper()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"state":                 {state},
		"code_challenge":        {challenge()},
		"code_challenge_method": {"S256"},
	}
	resp := e.get("/oauth/authorize?"+q.Encode(), sess)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("authorize page: status %d", resp.StatusCode)
	}

	resp = e.post("/oauth/authorize", sess, q)
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("approve: status %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), redirectURI) {
		e.t.Fatalf("redirected to %s, want %s", loc, redirectURI)
	}
	if loc.Query().Get("state") != state {
		e.t.Errorf("state mismatch: %q", loc.Query().Get("state"))
	}
	code := loc.Query().Get("code")
	if code == "" {
		e.t.Fatal("no code in redirect")
	}
	return code
}

func (e *env) exchangeCode(clientID, redirectURI, code, verifier string) *http.Response {
	e.t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	return e.call("POST", "/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()), "")
}

func TestOAuthMetadata(t *testing.T) {
	e := newEnv(t)

	resp := e.call("GET", "/.well-known/oauth-authorization-server", "", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("as metadata: status %d", resp.StatusCode)
	}
	m := decode(t, resp)
	for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint", "registration_endpoint"} {
		if m[k] == "" || m[k] == nil {
			t.Errorf("missing %s in %v", k, m)
		}
	}

	resp = e.call("GET", "/.well-known/oauth-protected-resource", "", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pr metadata: status %d", resp.StatusCode)
	}
	m = decode(t, resp)
	if m["resource"] == nil || m["authorization_servers"] == nil {
		t.Errorf("bad protected resource metadata: %v", m)
	}
}

func TestOAuthFullFlow(t *testing.T) {
	e := newEnv(t)
	const redirectURI = "http://127.0.0.1:33418/callback"
	clientID := e.registerClient(redirectURI)
	sess := e.ownerSession()

	code := e.authorize(clientID, redirectURI, "st4te", sess)

	resp := e.exchangeCode(clientID, redirectURI, code, verifier)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token: status %d", resp.StatusCode)
	}
	m := decode(t, resp)
	access, _ := m["access_token"].(string)
	if access == "" || m["token_type"] != "bearer" {
		t.Fatalf("bad token response: %v", m)
	}

	// The issued token works against the regular API.
	r := e.call("GET", "/api/pages", "", nil, access)
	if r.StatusCode != http.StatusOK {
		t.Errorf("oauth token on API: got %d, want 200", r.StatusCode)
	}
}

func TestOAuthAuthorizeRequiresLogin(t *testing.T) {
	e := newEnv(t)
	clientID := e.registerClient("http://127.0.0.1:1/cb")
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:1/cb"},
		"response_type": {"code"}, "code_challenge": {challenge()}, "code_challenge_method": {"S256"},
	}
	resp := e.get("/oauth/authorize?"+q.Encode(), "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("got %d, want login redirect", resp.StatusCode)
	}
	next := resp.Header.Get("Location")
	// The login redirect must preserve the full query so the flow resumes.
	if !strings.Contains(next, url.QueryEscape("code_challenge")) {
		t.Errorf("login next should keep the query: %q", next)
	}
}

func TestOAuthRejectsWrongVerifier(t *testing.T) {
	e := newEnv(t)
	const redirectURI = "http://127.0.0.1:33418/callback"
	clientID := e.registerClient(redirectURI)
	code := e.authorize(clientID, redirectURI, "s", e.ownerSession())

	resp := e.exchangeCode(clientID, redirectURI, code, "wrong-verifier")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong verifier: got %d, want 400", resp.StatusCode)
	}
}

func TestOAuthCodeSingleUse(t *testing.T) {
	e := newEnv(t)
	const redirectURI = "http://127.0.0.1:33418/callback"
	clientID := e.registerClient(redirectURI)
	code := e.authorize(clientID, redirectURI, "s", e.ownerSession())

	if resp := e.exchangeCode(clientID, redirectURI, code, verifier); resp.StatusCode != http.StatusOK {
		t.Fatalf("first exchange failed: %d", resp.StatusCode)
	}
	if resp := e.exchangeCode(clientID, redirectURI, code, verifier); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("code reuse: got %d, want 400", resp.StatusCode)
	}
}

func TestOAuthRejectsUnregisteredRedirect(t *testing.T) {
	e := newEnv(t)
	clientID := e.registerClient("http://127.0.0.1:1/cb")
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {"https://evil.example.com/cb"},
		"response_type": {"code"}, "code_challenge": {challenge()}, "code_challenge_method": {"S256"},
	}
	resp := e.get("/oauth/authorize?"+q.Encode(), e.ownerSession())
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unregistered redirect_uri: got %d, want 400", resp.StatusCode)
	}
	// POST must validate too.
	resp = e.post("/oauth/authorize", e.ownerSession(), q)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unregistered redirect_uri on POST: got %d, want 400", resp.StatusCode)
	}
}

func TestOAuthRegisterValidation(t *testing.T) {
	e := newEnv(t)
	resp := e.call("POST", "/oauth/register", "application/json",
		strings.NewReader(`{"client_name":"x","redirect_uris":[]}`), "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty redirect_uris: got %d, want 400", resp.StatusCode)
	}
}
