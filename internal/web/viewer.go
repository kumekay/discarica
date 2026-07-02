package web

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"net/url"

	"github.com/kumekay/discarica/internal/store"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// markdown renderer: GFM, raw HTML escaped/omitted (default), so rendered
// pages cannot smuggle script tags. Uploaded HTML pages are covered by the
// sandbox CSP instead.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

var viewTmpl = template.Must(template.New("view").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{max-width:48rem;margin:2rem auto;padding:0 1rem;font-family:system-ui,sans-serif;line-height:1.6;color:#1a1a1a}
pre{background:#f5f5f5;padding:1rem;overflow-x:auto;border-radius:4px}
code{background:#f5f5f5;padding:.1em .3em;border-radius:3px}
pre code{padding:0}
img{max-width:100%}
table{border-collapse:collapse}
td,th{border:1px solid #ccc;padding:.3em .6em}
blockquote{border-left:3px solid #ccc;margin-left:0;padding-left:1em;color:#555}
</style>
</head><body>
{{.Body}}
</body></html>`))

func (s *server) sessionUser(r *http.Request) (store.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return store.User{}, false
	}
	u, err := s.cfg.Store.UserBySession(r.Context(), c.Value)
	if err != nil {
		return store.User{}, false
	}
	return u, true
}

func (s *server) loadPage(w http.ResponseWriter, r *http.Request) (store.Page, bool) {
	p, err := s.cfg.Store.PageByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return store.Page{}, false
	}
	if err != nil {
		http.Error(w, "load page", http.StatusInternalServerError)
		return store.Page{}, false
	}
	return p, true
}

// handlePrivate serves /p/{id} and /p/{id}/raw: session required, except that
// anonymous hits on a now-public page bounce to its /pub/ URL.
func (s *server) handlePrivate(raw bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.loadPage(w, r)
		if !ok {
			return
		}
		if _, loggedIn := s.sessionUser(r); !loggedIn {
			if p.Public {
				http.Redirect(w, r, "/pub/"+p.ID+suffix(raw), http.StatusFound)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
			return
		}
		s.servePage(w, p, raw)
	}
}

// handlePublic serves /pub/{id} and /pub/{id}/raw; pages flipped back to
// private redirect to their login-protected URL.
func (s *server) handlePublic(raw bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.loadPage(w, r)
		if !ok {
			return
		}
		if !p.Public {
			http.Redirect(w, r, "/p/"+p.ID+suffix(raw), http.StatusFound)
			return
		}
		s.servePage(w, p, raw)
	}
}

func suffix(raw bool) string {
	if raw {
		return "/raw"
	}
	return ""
}

func (s *server) servePage(w http.ResponseWriter, p store.Page, raw bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if raw {
		if p.Kind == "md" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		_, _ = w.Write(p.Content)
		return
	}

	// User content never gets same-origin access; scripts run in an opaque origin.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if p.Kind == "html" {
		_, _ = w.Write(p.Content)
		return
	}
	var buf bytes.Buffer
	if err := markdown.Convert(p.Content, &buf); err != nil {
		http.Error(w, "render markdown", http.StatusInternalServerError)
		return
	}
	_ = viewTmpl.Execute(w, map[string]any{
		"Title": p.Name,
		"Body":  template.HTML(buf.String()), //nolint:gosec // goldmark output with raw HTML escaping enabled
	})
}
