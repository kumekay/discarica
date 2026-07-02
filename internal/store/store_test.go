package store_test

import (
	"testing"
	"time"

	"github.com/kumekay/discarica/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpsertUser(t *testing.T) {
	s := newStore(t)

	u, err := s.UpsertUser(t.Context(), "github", "42", "kumekay")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if u.ID == 0 || u.Provider != "github" || u.ProviderUID != "42" || u.Login != "kumekay" {
		t.Errorf("unexpected user: %+v", u)
	}

	// Same identity again: same ID, updated login.
	u2, err := s.UpsertUser(t.Context(), "github", "42", "renamed")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if u2.ID != u.ID {
		t.Errorf("expected same user ID, got %d and %d", u.ID, u2.ID)
	}
	if u2.Login != "renamed" {
		t.Errorf("login not updated: %+v", u2)
	}

	// Same UID on a different provider is a different user.
	u3, err := s.UpsertUser(t.Context(), "gitea", "42", "kumekay")
	if err != nil {
		t.Fatalf("gitea upsert: %v", err)
	}
	if u3.ID == u.ID {
		t.Error("gitea user should not collide with github user")
	}
}

func TestPageCRUD(t *testing.T) {
	s := newStore(t)
	u, _ := s.UpsertUser(t.Context(), "github", "1", "alice")

	p, err := s.CreatePage(t.Context(), u.ID, "My report", "html", []byte("<h1>hi</h1>"), false)
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	if len(p.ID) != 16 {
		t.Errorf("expected 16-char id, got %q", p.ID)
	}
	if p.Public {
		t.Error("page should be private by default")
	}

	got, err := s.PageByID(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if string(got.Content) != "<h1>hi</h1>" || got.Name != "My report" || got.Kind != "html" || got.UserID != u.ID {
		t.Errorf("unexpected page: %+v", got)
	}

	// Second page has a distinct id.
	p2, _ := s.CreatePage(t.Context(), u.ID, "Second", "md", []byte("# two"), true)
	if p2.ID == p.ID {
		t.Error("page ids must be unique")
	}
	if !p2.Public {
		t.Error("expected public page")
	}

	// List returns both, newest first.
	pages, err := s.ListPages(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
	if pages[0].ID != p2.ID {
		t.Errorf("expected newest first, got %s", pages[0].ID)
	}
	// List must not include content blobs.
	if pages[0].Content != nil {
		t.Error("list should not return content")
	}

	// Visibility toggle.
	if err := s.SetPageVisibility(t.Context(), p.ID, u.ID, true); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
	got, _ = s.PageByID(t.Context(), p.ID)
	if !got.Public {
		t.Error("page should be public after toggle")
	}

	// Another user cannot toggle or delete.
	other, _ := s.UpsertUser(t.Context(), "github", "2", "bob")
	if err := s.SetPageVisibility(t.Context(), p.ID, other.ID, false); err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound for foreign toggle, got %v", err)
	}
	if err := s.DeletePage(t.Context(), p.ID, other.ID); err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound for foreign delete, got %v", err)
	}

	// Owner deletes.
	if err := s.DeletePage(t.Context(), p.ID, u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.PageByID(t.Context(), p.ID); err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAPIToken(t *testing.T) {
	s := newStore(t)
	u, _ := s.UpsertUser(t.Context(), "github", "1", "alice")

	tok, err := s.GetOrCreateAPIToken(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if len(tok) < 20 || tok[:4] != "dsc_" {
		t.Errorf("unexpected token format: %q", tok)
	}

	// Stable until reset: the dashboard re-displays it.
	again, err := s.GetOrCreateAPIToken(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("get token again: %v", err)
	}
	if again != tok {
		t.Errorf("token should be stable, got %q then %q", tok, again)
	}

	got, err := s.UserByToken(t.Context(), tok)
	if err != nil {
		t.Fatalf("user by token: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("wrong user: %+v", got)
	}

	// Refresh invalidates the old token.
	tok2, err := s.ResetAPIToken(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if tok2 == tok {
		t.Error("refreshed token must differ")
	}
	if _, err := s.UserByToken(t.Context(), tok); err != store.ErrNotFound {
		t.Errorf("old token should be invalid, got %v", err)
	}
	if _, err := s.UserByToken(t.Context(), "dsc_bogus"); err != store.ErrNotFound {
		t.Errorf("bogus token should be invalid, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	s := newStore(t)
	u, _ := s.UpsertUser(t.Context(), "github", "1", "alice")

	tok, err := s.CreateSession(t.Context(), u.ID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	got, err := s.UserBySession(t.Context(), tok)
	if err != nil {
		t.Fatalf("user by session: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("wrong user: %+v", got)
	}

	// Expired session is invalid.
	expired, _ := s.CreateSession(t.Context(), u.ID, time.Now().Add(-time.Minute))
	if _, err := s.UserBySession(t.Context(), expired); err != store.ErrNotFound {
		t.Errorf("expired session should be invalid, got %v", err)
	}

	// Deleted session is invalid.
	if err := s.DeleteSession(t.Context(), tok); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := s.UserBySession(t.Context(), tok); err != store.ErrNotFound {
		t.Errorf("deleted session should be invalid, got %v", err)
	}
}
