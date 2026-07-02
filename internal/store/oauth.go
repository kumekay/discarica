package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// OAuth authorization-server state: dynamically registered clients,
// single-use authorization codes, and issued access tokens.

type OAuthCode struct {
	ClientID      string
	UserID        int64
	RedirectURI   string
	CodeChallenge string
}

// CreateOAuthClient registers a client (RFC 7591) and returns its id.
func (s *Store) CreateOAuthClient(ctx context.Context, name string, redirectURIs []string) (string, error) {
	id := randomBase62(24)
	uris, err := json.Marshal(redirectURIs)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO oauth_clients (id, name, redirect_uris, created_at) VALUES (?, ?, ?, ?)",
		id, name, string(uris), time.Now().Unix())
	if err != nil {
		return "", err
	}
	return id, nil
}

// OAuthClientName returns the registered client_name for a client.
func (s *Store) OAuthClientName(ctx context.Context, clientID string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM oauth_clients WHERE id = ?", clientID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// OAuthClientRedirectURIs returns the registered redirect URIs for a client.
func (s *Store) OAuthClientRedirectURIs(ctx context.Context, clientID string) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		"SELECT redirect_uris FROM oauth_clients WHERE id = ?", clientID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var uris []string
	if err := json.Unmarshal([]byte(raw), &uris); err != nil {
		return nil, err
	}
	return uris, nil
}

// CreateOAuthCode issues a single-use authorization code bound to a PKCE challenge.
func (s *Store) CreateOAuthCode(ctx context.Context, c OAuthCode, expiresAt time.Time) (string, error) {
	code := randomBase62(32)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_codes (code, client_id, user_id, redirect_uri, code_challenge, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		code, c.ClientID, c.UserID, c.RedirectURI, c.CodeChallenge, expiresAt.Unix())
	if err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeOAuthCode returns and deletes the code; expired or unknown codes yield ErrNotFound.
func (s *Store) ConsumeOAuthCode(ctx context.Context, code string) (OAuthCode, error) {
	var c OAuthCode
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM oauth_codes WHERE code = ? AND expires_at > ?
		RETURNING client_id, user_id, redirect_uri, code_challenge`,
		code, time.Now().Unix()).
		Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthCode{}, ErrNotFound
	}
	if err != nil {
		return OAuthCode{}, err
	}
	return c, nil
}

// CreateOAuthToken issues an access token for the user on behalf of a client.
func (s *Store) CreateOAuthToken(ctx context.Context, userID int64, clientID string) (string, error) {
	token := "dscm_" + randomBase62(32)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO oauth_tokens (token, user_id, client_id, created_at) VALUES (?, ?, ?, ?)",
		token, userID, clientID, time.Now().Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}
