package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kumekay/discarica/internal/store"
)

type pageResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Public    bool      `json:"public"`
	URL       string    `json:"url"`
	RawURL    string    `json:"raw_url,omitempty"`
	Content   string    `json:"content,omitempty"` // set only on single-page GET
	CreatedAt time.Time `json:"created_at"`
}

// pageURL returns the sharable viewer URL for a page: /pub/{id} when public,
// /p/{id} (login required) when private.
func (s *server) pageURL(p store.Page) string {
	if p.Public {
		return s.cfg.BaseURL + "/pub/" + p.ID
	}
	return s.cfg.BaseURL + "/p/" + p.ID
}

func (s *server) pageResponse(p store.Page) pageResponse {
	resp := pageResponse{
		ID:        p.ID,
		Name:      p.Name,
		Kind:      p.Kind,
		Public:    p.Public,
		URL:       s.pageURL(p),
		CreatedAt: p.CreatedAt,
	}
	if p.Kind == "md" {
		resp.RawURL = resp.URL + "/raw"
	}
	return resp
}

func (s *server) handleUploadPage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read file: "+err.Error())
		return
	}

	name := r.FormValue("name")
	if name == "" {
		name = header.Filename
	}
	kind := "html"
	if strings.HasSuffix(strings.ToLower(header.Filename), ".md") {
		kind = "md"
	}
	public := r.FormValue("public") == "true"

	p, err := s.cfg.Store.CreatePage(r.Context(), currentUser(r).ID, name, kind, content, public)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "create page")
		return
	}
	writeJSON(w, http.StatusCreated, s.pageResponse(p))
}

func (s *server) handleListPages(w http.ResponseWriter, r *http.Request) {
	pages, err := s.cfg.Store.ListPages(r.Context(), currentUser(r).ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "list pages")
		return
	}
	resp := make([]pageResponse, 0, len(pages))
	for _, p := range pages {
		resp = append(resp, s.pageResponse(p))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGetPage returns one of the caller's pages including its content, so
// agents can read notes back (e.g. handover between agents of one user).
func (s *server) handleGetPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.cfg.Store.PageByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.UserID != currentUser(r).ID) {
		jsonError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "load page")
		return
	}
	resp := s.pageResponse(p)
	resp.Content = string(p.Content)
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleDeletePage(w http.ResponseWriter, r *http.Request) {
	err := s.cfg.Store.DeletePage(r.Context(), r.PathValue("id"), currentUser(r).ID)
	if errors.Is(err, store.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "delete page")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handlePatchPage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Public *bool `json:"public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Public == nil {
		jsonError(w, http.StatusBadRequest, `body must be JSON like {"public": true}`)
		return
	}
	id := r.PathValue("id")
	err := s.cfg.Store.SetPageVisibility(r.Context(), id, currentUser(r).ID, *body.Public)
	if errors.Is(err, store.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "update page")
		return
	}
	p, err := s.cfg.Store.PageByID(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "reload page")
		return
	}
	writeJSON(w, http.StatusOK, s.pageResponse(p))
}
