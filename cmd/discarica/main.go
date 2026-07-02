// Command discarica serves the page-sharing service: web UI, pages API,
// OAuth login and the MCP endpoint.
package main

import (
	"cmp"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kumekay/discarica/internal/auth"
	"github.com/kumekay/discarica/internal/mcpsrv"
	"github.com/kumekay/discarica/internal/store"
	"github.com/kumekay/discarica/internal/web"
)

// env reads a variable with surrounding whitespace trimmed — secrets pasted
// into CI often carry a stray newline or space.
func env(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func main() {
	baseURL := cmp.Or(env("BASE_URL"), "http://localhost:8080")
	dbPath := cmp.Or(env("DB_PATH"), "discarica.db")
	addr := cmp.Or(env("LISTEN_ADDR"), ":8080")

	st, err := store.Open(dbPath)
	if err != nil {
		slog.Error("open store", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	var providers []*auth.Provider
	if id, secret := env("GITHUB_CLIENT_ID"), env("GITHUB_CLIENT_SECRET"); id != "" {
		providers = append(providers, auth.GitHub(id, secret, baseURL+"/auth/github/callback"))
	}
	if id, secret := env("GITEA_CLIENT_ID"), env("GITEA_CLIENT_SECRET"); id != "" {
		if giteaURL := env("GITEA_URL"); giteaURL != "" {
			providers = append(providers, auth.Gitea(giteaURL, id, secret, baseURL+"/auth/gitea/callback"))
		} else {
			slog.Warn("GITEA_CLIENT_ID set but GITEA_URL missing; gitea login disabled")
		}
	}
	if len(providers) == 0 {
		slog.Warn("no OAuth providers configured; browser login is unavailable")
	}

	var maxUpload int64
	if v := os.Getenv("MAX_UPLOAD_BYTES"); v != "" {
		maxUpload, _ = strconv.ParseInt(v, 10, 64)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpsrv.Handler(st, baseURL))
	mux.Handle("/", web.New(web.Config{
		Store:          st,
		BaseURL:        baseURL,
		Providers:      providers,
		MaxUploadBytes: maxUpload,
	}))

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("listening", "addr", addr, "base_url", baseURL, "db", dbPath)
	if err := server.ListenAndServe(); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
