# discarica

Share HTML and Markdown pages via unguessable links. Users log in with GitHub,
Gitea or Forgejo OAuth, get a bearer upload token, and push pages through the HTTP API or the
MCP server at `/mcp`. Pages are private by default (`/p/{id}`, viewing requires
login); public pages live under `/pub/{id}`. Deployed at
https://discarica.kumekay.com.

## Commands

- `go test ./...` — run all tests
- `golangci-lint run` — lint (config in `.golangci.yml`)
- `go run ./cmd/discarica` — start the server locally
- `git config core.hooksPath .githooks` — enable the pre-commit hook (lint + tests); run once after clone

## Development rules

- **Red-green TDD**: write a failing test first, then the minimal code to pass it.
  Handlers are tested end-to-end with `httptest` against the real mux and an
  in-memory SQLite store; OAuth flows use the fake provider in `internal/auth`.
- Never commit with failing tests or lint errors (the hook enforces this).
- Plain `net/http` (Go 1.22+ method routing) — no web framework.

## Architecture

- `cmd/discarica` — main: config from env, wiring, listen.
- `internal/store` — SQLite (modernc.org/sqlite, no CGO). Single DB file
  (`DB_PATH`). All secrets (session cookies, API tokens, OAuth codes/tokens) are
  stored **hashed** (SHA-256); plaintext exists only in the response that issues it.
- `internal/auth` — OAuth2 login providers (GitHub, Gitea, Forgejo) behind a small
  `Provider` interface; server-side sessions.
- `internal/oauthsrv` — minimal OAuth 2.1 authorization server for MCP clients:
  dynamic client registration, `/oauth/authorize` (reuses browser session),
  `/oauth/token` with PKCE S256. No scopes, public clients only.
- `internal/web` — HTTP handlers, embedded templates (dashboard, viewer, login),
  pages API.
- `internal/mcpsrv` — MCP server (official go-sdk, streamable HTTP) exposing
  upload/list/delete/set-visibility tools; bearer auth via the same token store.

Page IDs are 16 base62 chars from crypto/rand — possession of a `/pub/` link is
the only access control for public pages. User-uploaded pages are always served
with `Content-Security-Policy: sandbox allow-scripts` so they run in an opaque
origin and cannot touch cookies or make credentialed requests.
