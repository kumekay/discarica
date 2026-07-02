package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/kumekay/discarica/internal/store"
)

// Minimal OAuth 2.1 authorization server for MCP clients: dynamic client
// registration, PKCE-only public clients, no scopes, no refresh tokens.
// Authentication itself is delegated to the browser session (GitHub/Gitea).

const oauthCodeTTL = 5 * time.Minute

var approveTmpl = template.Must(template.New("approve").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>discarica — authorize</title>
<style>body{max-width:32rem;margin:4rem auto;padding:0 1rem;font-family:system-ui,sans-serif;line-height:1.5}</style>
</head><body>
<h1>Authorize {{.ClientName}}?</h1>
<p><strong>{{.ClientName}}</strong> wants to upload and manage pages on your behalf as <strong>{{.Login}}</strong>.</p>
<form method="post" action="/oauth/authorize">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">
{{end}}<button type="submit">Approve</button>
</form>
</body></html>`))

func (s *server) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.cfg.BaseURL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (s *server) handlePRMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              s.cfg.BaseURL,
		"authorization_servers": []string{s.cfg.BaseURL},
		"bearer_methods_supported": []string{
			"header",
		},
	})
}

func (s *server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris required")
		return
	}
	for _, u := range req.RedirectURIs {
		parsed, err := url.Parse(u)
		if err != nil || !parsed.IsAbs() {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be absolute")
			return
		}
	}
	if req.ClientName == "" {
		req.ClientName = "MCP client"
	}
	id, err := s.cfg.Store.CreateOAuthClient(r.Context(), req.ClientName, req.RedirectURIs)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "store client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"client_name":                req.ClientName,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	})
}

// validAuthorizeRequest checks client, redirect_uri and PKCE params.
func (s *server) validAuthorizeRequest(w http.ResponseWriter, r *http.Request) bool {
	uris, err := s.cfg.Store.OAuthClientRedirectURIs(r.Context(), r.FormValue("client_id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return false
	}
	if err != nil {
		http.Error(w, "load client", http.StatusInternalServerError)
		return false
	}
	if !slices.Contains(uris, r.FormValue("redirect_uri")) {
		http.Error(w, "redirect_uri not registered", http.StatusBadRequest)
		return false
	}
	if r.FormValue("response_type") != "code" {
		http.Error(w, "response_type must be code", http.StatusBadRequest)
		return false
	}
	if r.FormValue("code_challenge") == "" || r.FormValue("code_challenge_method") != "S256" {
		http.Error(w, "PKCE with S256 is required", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) handleAuthorizePage(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.validAuthorizeRequest(w, r) {
		return
	}
	params := map[string]string{}
	for _, k := range []string{"client_id", "redirect_uri", "response_type", "state", "code_challenge", "code_challenge_method"} {
		params[k] = r.FormValue(k)
	}
	clientName, err := s.cfg.Store.OAuthClientName(r.Context(), r.FormValue("client_id"))
	if err != nil {
		clientName = "MCP client"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = approveTmpl.Execute(w, map[string]any{
		"ClientName": clientName,
		"Login":      u.Login,
		"Params":     params,
	})
}

func (s *server) handleAuthorizeApprove(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.validAuthorizeRequest(w, r) {
		return
	}
	redirectURI := r.FormValue("redirect_uri")
	code, err := s.cfg.Store.CreateOAuthCode(r.Context(), store.OAuthCode{
		ClientID:      r.FormValue("client_id"),
		UserID:        u.ID,
		RedirectURI:   redirectURI,
		CodeChallenge: r.FormValue("code_challenge"),
	}, time.Now().Add(oauthCodeTTL))
	if err != nil {
		http.Error(w, "issue code", http.StatusInternalServerError)
		return
	}
	target, _ := url.Parse(redirectURI)
	q := target.Query()
	q.Set("code", code)
	if state := r.FormValue("state"); state != "" {
		q.Set("state", state)
	}
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code")
		return
	}
	c, err := s.cfg.Store.ConsumeOAuthCode(r.Context(), r.FormValue("code"))
	if errors.Is(err, store.ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code")
		return
	}
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "consume code")
		return
	}
	if c.ClientID != r.FormValue("client_id") || c.RedirectURI != r.FormValue("redirect_uri") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri mismatch")
		return
	}
	sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(c.CodeChallenge)) != 1 {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	token, err := s.cfg.Store.CreateOAuthToken(r.Context(), c.UserID, c.ClientID)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "issue token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
	})
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}
