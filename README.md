# discarica

Share HTML and Markdown pages via unguessable links. Log in with GitHub or
Gitea, grab your upload token, and let your AI agents (or scripts) push pages
and get back sharable links. Pages are private by default (viewing requires
login); opt a page into public to share it with anyone who has the link.

Running at **https://discarica.kumekay.com**.

## How it works

1. Open the dashboard. It shows your personal **upload token**, which you can
   refresh at any time, and your uploaded pages with visibility-toggle and
   delete buttons.

2. Save the token to your environment:

   ```sh
   # URL is optional, the default is discarica.kumekay.com
   export DISCARICA_URL=https://discarica.kumekay.com
   export DISCARICA_TOKEN=<token from the dashboard>
   ```

3. Upload a page:

   ```sh
   curl -sf -X POST "$DISCARICA_URL/api/pages" \
     -H "Authorization: Bearer $DISCARICA_TOKEN" \
     -F "file=@report.html" -F "name=My report"
   ```

   The JSON response contains a sharable `url`. By default pages are
   **private**: the URL looks like `https://discarica.kumekay.com/p/NJzHM55az0S5xgWp`
   and viewing it requires GitHub or Gitea login. Add `-F "public=true"` to make
   the page **public** — the URL then uses the `/pub/` path and anyone with the
   link can view it, no login needed. You can also flip visibility later from
   the dashboard or the API (the path changes between `/p/` and `/pub/`).
   Files ending in `.md` are rendered to HTML on view; append `/raw` to read
   the original Markdown.

## API

| Method   | Path              | Auth               | Purpose                                                            |
| -------- | ----------------- | ------------------ | ------------------------------------------------------------------ |
| `POST`   | `/api/pages`      | Bearer token       | Upload (`file` multipart, optional `name`, optional `public=true`) |
| `GET`    | `/api/pages`      | Bearer token       | List your pages                                                    |
| `PATCH`  | `/api/pages/{id}` | Bearer token       | Set visibility (`{"public": true}`)                                |
| `DELETE` | `/api/pages/{id}` | Bearer token       | Delete a page                                                      |
| `GET`    | `/p/{id}`         | GitHub/Gitea login | View a private page                                                |
| `GET`    | `/p/{id}/raw`     | GitHub/Gitea login | Raw source of a private page                                       |
| `GET`    | `/pub/{id}`       | none               | View a public page                                                 |
| `GET`    | `/pub/{id}/raw`   | none               | Raw source of a public page                                        |

## MCP server

Agents can skip tokens entirely: connect to `https://discarica.kumekay.com/mcp`
(streamable HTTP) and authenticate via OAuth — the login flow opens in the
browser and uses the same GitHub/Gitea accounts. Tools: `upload_page`,
`list_pages`, `delete_page`, `set_visibility`.

```sh
claude mcp add --transport http discarica https://discarica.kumekay.com/mcp
```

Bearer tokens from the dashboard also work directly against `/mcp`.

## Agent skill

`skills/share-with-discarica/` teaches an agent how to use the service with
plain curl. Install it with the [`skills` CLI](https://github.com/vercel-labs/skills):

```sh
npx skills add https://github.com/kumekay/discarica.git --skill share-with-discarica
```

From a local checkout you can symlink instead:
`ln -s "$(pwd)/skills/share-with-discarica" ~/.claude/skills/`

## Security model

- Pages are private by default: viewing `/p/{id}` requires GitHub or Gitea login.
- Public pages (`/pub/{id}`) skip the login; their unguessable id is the only
  access control — possession of the link grants viewing.
- Shared pages are served with `Content-Security-Policy: sandbox allow-scripts`,
  so user-supplied HTML gets no same-origin access to the rest of the service
  (scripts still run, in an opaque origin).
- Markdown is rendered with raw HTML escaped.

## Running it yourself

State lives in a single SQLite database (`DB_PATH`, default `discarica.db`).

```sh
BASE_URL=https://discarica.example.com \
DB_PATH=/data/discarica.db \
GITHUB_CLIENT_ID=... GITHUB_CLIENT_SECRET=... \
GITEA_URL=https://gitea.example.com \
GITEA_CLIENT_ID=... GITEA_CLIENT_SECRET=... \
go run ./cmd/discarica
```

Register the OAuth apps with callback URLs
`$BASE_URL/auth/github/callback` and `$BASE_URL/auth/gitea/callback`.
Providers are optional — each is enabled when its credentials are set
(Gitea also needs `GITEA_URL`). `BASE_URL` defaults to `http://localhost:8080`;
`LISTEN_ADDR` (default `:8080`) and `MAX_UPLOAD_BYTES` (default 5 MiB) are
also configurable.

## License

MIT
