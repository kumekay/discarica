// Package web implements discarica's HTTP surface: pages API, viewer,
// dashboard and login.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kumekay/discarica/internal/auth"
	"github.com/kumekay/discarica/internal/store"
)

type Config struct {
	Store     *store.Store
	BaseURL   string // external base URL without trailing slash, e.g. https://discarica.kumekay.com
	Providers []*auth.Provider
	// MaxUploadBytes caps page uploads; 0 means the 5 MiB default.
	MaxUploadBytes int64
}

const defaultMaxUploadBytes = 5 << 20

type server struct {
	cfg       Config
	providers map[string]*auth.Provider
}

// New returns the root HTTP handler.
func New(cfg Config) http.Handler {
	if cfg.MaxUploadBytes == 0 {
		cfg.MaxUploadBytes = defaultMaxUploadBytes
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	s := &server{cfg: cfg, providers: make(map[string]*auth.Provider)}
	for _, p := range cfg.Providers {
		s.providers[p.Name()] = p
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/pages", s.withToken(s.handleUploadPage))
	mux.Handle("GET /api/pages", s.withToken(s.handleListPages))
	mux.Handle("DELETE /api/pages/{id}", s.withToken(s.handleDeletePage))
	mux.Handle("PATCH /api/pages/{id}", s.withToken(s.handlePatchPage))

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("GET /auth/{provider}", s.handleAuthStart)
	mux.HandleFunc("GET /auth/{provider}/callback", s.handleAuthCallback)
	mux.HandleFunc("POST /logout", s.handleLogout)
	return mux
}

type ctxKey int

const userKey ctxKey = 0

// withToken authenticates API requests via "Authorization: Bearer dsc_...".
func (s *server) withToken(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			jsonError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		u, err := s.cfg.Store.UserByToken(r.Context(), token)
		if err != nil {
			jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func currentUser(r *http.Request) store.User {
	u, _ := r.Context().Value(userKey).(store.User)
	return u
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
