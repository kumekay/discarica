// Command discarica serves the page-sharing service: web UI, pages API,
// OAuth login and the MCP endpoint.
package main

import (
	"cmp"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/kumekay/discarica/internal/auth"
	"github.com/kumekay/discarica/internal/mcpsrv"
	"github.com/kumekay/discarica/internal/store"
	"github.com/kumekay/discarica/internal/web"
)

func main() {
	baseURL := cmp.Or(os.Getenv("BASE_URL"), "https://discarica.kumekay.com")
	dbPath := cmp.Or(os.Getenv("DB_PATH"), "discarica.db")
	addr := cmp.Or(os.Getenv("LISTEN_ADDR"), ":8080")

	st, err := store.Open(dbPath)
	if err != nil {
		slog.Error("open store", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	var providers []*auth.Provider
	if id, secret := os.Getenv("GITHUB_CLIENT_ID"), os.Getenv("GITHUB_CLIENT_SECRET"); id != "" {
		providers = append(providers, auth.GitHub(id, secret, baseURL+"/auth/github/callback"))
	}
	if id, secret := os.Getenv("GITEA_CLIENT_ID"), os.Getenv("GITEA_CLIENT_SECRET"); id != "" {
		giteaURL := cmp.Or(os.Getenv("GITEA_URL"), "https://gitea.kumekay.com")
		providers = append(providers, auth.Gitea(giteaURL, id, secret, baseURL+"/auth/gitea/callback"))
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
