package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"

	"github.com/kumekay/discarica/internal/store"
)

var dashTmpl = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>discarica — dashboard</title>
<style>
body{max-width:56rem;margin:2rem auto;padding:0 1rem;font-family:system-ui,sans-serif;line-height:1.5;color:#1a1a1a}
code{background:#f5f5f5;padding:.2em .4em;border-radius:3px}
table{border-collapse:collapse;width:100%}
td,th{border:1px solid #ddd;padding:.4em .6em;text-align:left}
form{display:inline}
button{cursor:pointer}
.muted{color:#666;font-size:.9em}
pre{background:#f5f5f5;padding:1rem;overflow-x:auto;border-radius:4px}
</style></head><body>
<h1>discarica</h1>
<p class="muted">Logged in as <strong>{{.User.Login}}</strong> ({{.User.Provider}})
<form method="post" action="/logout"><button>Log out</button></form></p>

<h2>Upload token</h2>
<p><code id="token">{{.Token}}</code>
<button onclick="navigator.clipboard.writeText(document.getElementById('token').textContent)">Copy</button>
<form method="post" action="/dashboard/token"><button>Refresh</button></form></p>
<pre>export DISCARICA_URL={{.BaseURL}}
export DISCARICA_TOKEN={{.Token}}

curl -sf -X POST "$DISCARICA_URL/api/pages" \
  -H "Authorization: Bearer $DISCARICA_TOKEN" \
  -F "file=@report.html" -F "name=My report"</pre>

<h2>Pages</h2>
{{if .Pages}}
<table>
<tr><th>Name</th><th>Kind</th><th>Visibility</th><th>Created</th><th></th></tr>
{{range .Pages}}
<tr>
<td><a href="{{.URL}}">{{.Name}}</a></td>
<td>{{.Kind}}</td>
<td>
<form method="post" action="/dashboard/pages/{{.ID}}/visibility">
{{if .Public}}<input type="hidden" name="public" value="false"><button>public → make private</button>
{{else}}<input type="hidden" name="public" value="true"><button>private → make public</button>{{end}}
</form>
</td>
<td class="muted">{{.CreatedAt.Format "2006-01-02 15:04"}}</td>
<td><form method="post" action="/dashboard/pages/{{.ID}}/delete"><button>Delete</button></form></td>
</tr>
{{end}}
</table>
{{else}}<p class="muted">No pages yet.</p>{{end}}
</body></html>`))

// withSession guards browser pages, bouncing anonymous users to /login.
func (s *server) withSession(next func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.sessionUser(r)
		if !ok {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
			return
		}
		next(w, r, u)
	}
}

func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request, u store.User) {
	token, err := s.cfg.Store.GetOrCreateAPIToken(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "load token", http.StatusInternalServerError)
		return
	}
	pages, err := s.cfg.Store.ListPages(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "list pages", http.StatusInternalServerError)
		return
	}
	view := make([]pageResponse, 0, len(pages))
	for _, p := range pages {
		view = append(view, s.pageResponse(p))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashTmpl.Execute(w, map[string]any{
		"User":    u,
		"Token":   token,
		"BaseURL": s.cfg.BaseURL,
		"Pages":   view,
	})
}

func (s *server) handleTokenRefresh(w http.ResponseWriter, r *http.Request, u store.User) {
	if _, err := s.cfg.Store.ResetAPIToken(r.Context(), u.ID); err != nil {
		http.Error(w, "reset token", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (s *server) handleDashVisibility(w http.ResponseWriter, r *http.Request, u store.User) {
	public := r.FormValue("public") == "true"
	err := s.cfg.Store.SetPageVisibility(r.Context(), r.PathValue("id"), u.ID, public)
	if s.dashMutationErr(w, err) {
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (s *server) handleDashDelete(w http.ResponseWriter, r *http.Request, u store.User) {
	err := s.cfg.Store.DeletePage(r.Context(), r.PathValue("id"), u.ID)
	if s.dashMutationErr(w, err) {
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (s *server) dashMutationErr(w http.ResponseWriter, err error) bool {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "page not found", http.StatusNotFound)
		return true
	}
	if err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return true
	}
	return false
}
