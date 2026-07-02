// Package store persists all discarica state in a single SQLite database.
// Secrets (API tokens, session tokens) are stored as SHA-256 hashes; the
// plaintext is returned once at creation and never kept.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist or is not visible to the caller.
var ErrNotFound = errors.New("not found")

type User struct {
	ID          int64
	Provider    string
	ProviderUID string
	Login       string
	CreatedAt   time.Time
}

type Page struct {
	ID        string
	UserID    int64
	Name      string
	Kind      string // "html" or "md"
	Content   []byte // nil in ListPages results
	Public    bool
	CreatedAt time.Time
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY,
	provider      TEXT NOT NULL,
	provider_uid  TEXT NOT NULL,
	login         TEXT NOT NULL,
	created_at    INTEGER NOT NULL,
	UNIQUE (provider, provider_uid)
);
CREATE TABLE IF NOT EXISTS pages (
	id         TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	name       TEXT NOT NULL,
	kind       TEXT NOT NULL,
	content    BLOB NOT NULL,
	public     INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS pages_user ON pages(user_id, created_at DESC);
CREATE TABLE IF NOT EXISTS api_tokens (
	user_id    INTEGER PRIMARY KEY REFERENCES users(id),
	token      TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_clients (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	redirect_uris TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_codes (
	code           TEXT PRIMARY KEY,
	client_id      TEXT NOT NULL REFERENCES oauth_clients(id),
	user_id        INTEGER NOT NULL REFERENCES users(id),
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL,
	expires_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_tokens (
	token      TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	client_id  TEXT NOT NULL REFERENCES oauth_clients(id),
	created_at INTEGER NOT NULL
);
`

// Open opens (creating if needed) the SQLite database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite serializes writes anyway; a single connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// UpsertUser creates or updates the user identified by (provider, providerUID).
func (s *Store) UpsertUser(ctx context.Context, provider, providerUID, login string) (User, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (provider, provider_uid, login, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (provider, provider_uid) DO UPDATE SET login = excluded.login`,
		provider, providerUID, login, time.Now().Unix())
	if err != nil {
		return User{}, err
	}
	return s.userBy(ctx, "provider = ? AND provider_uid = ?", provider, providerUID)
}

func (s *Store) userBy(ctx context.Context, where string, args ...any) (User, error) {
	var u User
	var created int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, provider, provider_uid, login, created_at FROM users WHERE "+where, args...).
		Scan(&u.ID, &u.Provider, &u.ProviderUID, &u.Login, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

// CreatePage stores a new page and returns it with a fresh unguessable ID.
func (s *Store) CreatePage(ctx context.Context, userID int64, name, kind string, content []byte, public bool) (Page, error) {
	p := Page{
		ID:        randomBase62(16),
		UserID:    userID,
		Name:      name,
		Kind:      kind,
		Content:   content,
		Public:    public,
		CreatedAt: time.Now(),
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO pages (id, user_id, name, kind, content, public, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		p.ID, p.UserID, p.Name, p.Kind, p.Content, p.Public, p.CreatedAt.Unix())
	if err != nil {
		return Page{}, err
	}
	return p, nil
}

// PageByID returns a page with content regardless of owner or visibility;
// access control is the caller's job.
func (s *Store) PageByID(ctx context.Context, id string) (Page, error) {
	var p Page
	var created int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, user_id, name, kind, content, public, created_at FROM pages WHERE id = ?", id).
		Scan(&p.ID, &p.UserID, &p.Name, &p.Kind, &p.Content, &p.Public, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	if err != nil {
		return Page{}, err
	}
	p.CreatedAt = time.Unix(created, 0)
	return p, nil
}

// ListPages returns the user's pages, newest first, without content.
func (s *Store) ListPages(ctx context.Context, userID int64) ([]Page, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, user_id, name, kind, public, created_at FROM pages WHERE user_id = ? ORDER BY created_at DESC, rowid DESC",
		userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var pages []Page
	for rows.Next() {
		var p Page
		var created int64
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Kind, &p.Public, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(created, 0)
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

// SetPageVisibility updates public for a page owned by userID.
func (s *Store) SetPageVisibility(ctx context.Context, id string, userID int64, public bool) error {
	return s.mustAffect(s.db.ExecContext(ctx,
		"UPDATE pages SET public = ? WHERE id = ? AND user_id = ?", public, id, userID))
}

// DeletePage removes a page owned by userID.
func (s *Store) DeletePage(ctx context.Context, id string, userID int64) error {
	return s.mustAffect(s.db.ExecContext(ctx,
		"DELETE FROM pages WHERE id = ? AND user_id = ?", id, userID))
}

func (s *Store) mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetAPIToken issues a new upload token for the user, replacing any previous one.
// The token is stored in plaintext so the dashboard can re-display it; the DB
// also holds the pages it protects, so hashing it would not raise the stakes
// of a DB compromise.
func (s *Store) ResetAPIToken(ctx context.Context, userID int64) (string, error) {
	token := "dsc_" + randomBase62(32)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (user_id, token, created_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET token = excluded.token, created_at = excluded.created_at`,
		userID, token, time.Now().Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetOrCreateAPIToken returns the user's current upload token, creating one on first use.
func (s *Store) GetOrCreateAPIToken(ctx context.Context, userID int64) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx,
		"SELECT token FROM api_tokens WHERE user_id = ?", userID).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return s.ResetAPIToken(ctx, userID)
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

// UserByToken resolves a bearer token — either a dashboard upload token or an
// OAuth-issued MCP token — to its user.
func (s *Store) UserByToken(ctx context.Context, token string) (User, error) {
	return s.userBy(ctx, `id IN (
		SELECT user_id FROM api_tokens WHERE token = ?1
		UNION SELECT user_id FROM oauth_tokens WHERE token = ?1)`, token)
}

// CreateSession issues a browser session token for the user.
func (s *Store) CreateSession(ctx context.Context, userID int64, expiresAt time.Time) (string, error) {
	token := randomBase62(32)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		hashToken(token), userID, expiresAt.Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// UserBySession resolves a session token to its user if the session has not expired.
func (s *Store) UserBySession(ctx context.Context, token string) (User, error) {
	return s.userBy(ctx,
		"id = (SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?)",
		hashToken(token), time.Now().Unix())
}

// DeleteSession logs the session out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hashToken(token))
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randomBase62 returns n crypto-random base62 characters.
func randomBase62(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	for i, b := range buf {
		buf[i] = base62[int(b)%len(base62)]
	}
	return string(buf)
}
