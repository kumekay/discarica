package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookie  = "discarica_session"
	stateCookie    = "discarica_oauth_state"
	sessionTTL     = 30 * 24 * time.Hour
	defaultNextURL = "/dashboard"
)

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>discarica — log in</title></head>
<body>
<h1>discarica</h1>
<p>Log in to view private pages and manage your uploads.</p>
<ul>
{{- range .Providers}}
<li><a href="/auth/{{.Name}}?next={{$.Next}}">Log in with {{.Label}}</a></li>
{{- end}}
</ul>
</body></html>`))

func (s *server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.BaseURL, "https://")
}

// safeNext keeps redirect targets on-site to prevent open redirects.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return defaultNextURL
}

// loginProvider is one row of the login page: the URL segment and the name
// shown to the user.
type loginProvider struct {
	Name  string
	Label string
}

func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	providers := make([]loginProvider, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		providers = append(providers, loginProvider{Name: p.Name(), Label: p.Label()})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginTmpl.Execute(w, map[string]any{
		"Providers": providers,
		"Next":      safeNext(r.FormValue("next")),
	})
}

func (s *server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	state := randomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state + "|" + safeNext(r.FormValue("next")),
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthCodeURL(state), http.StatusFound)
}

func (s *server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	state, next, _ := strings.Cut(cookie.Value, "|")
	if state == "" || r.FormValue("state") != state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// State is single-use.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/", MaxAge: -1})

	id, err := p.Exchange(r.Context(), r.FormValue("code"))
	if err != nil {
		http.Error(w, fmt.Sprintf("login failed: %v", err), http.StatusBadGateway)
		return
	}
	u, err := s.cfg.Store.UpsertUser(r.Context(), id.Provider, id.UID, id.Login)
	if err != nil {
		http.Error(w, "create user", http.StatusInternalServerError)
		return
	}
	token, err := s.cfg.Store.CreateSession(r.Context(), u.ID, time.Now().Add(sessionTTL))
	if err != nil {
		http.Error(w, "create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(next), http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.cfg.Store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(buf)
}
