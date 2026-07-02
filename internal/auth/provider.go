// Package auth implements OAuth2 login providers (GitHub, Gitea).
// Both expose GitHub-compatible user endpoints returning {id, login},
// so a single generic Provider covers them.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// Identity is the result of a completed login.
type Identity struct {
	Provider string
	UID      string
	Login    string
}

type Provider struct {
	name    string
	cfg     *oauth2.Config
	userURL string
}

// New builds a provider from raw OAuth2 endpoints; used directly only in tests,
// production code uses GitHub and Gitea.
func New(name, clientID, clientSecret string, endpoint oauth2.Endpoint, userURL, redirectURL string) *Provider {
	return &Provider{
		name: name,
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     endpoint,
			RedirectURL:  redirectURL,
		},
		userURL: userURL,
	}
}

// GitHub returns a provider for github.com. No scopes: only the public profile is read.
func GitHub(clientID, clientSecret, redirectURL string) *Provider {
	return New("github", clientID, clientSecret, github.Endpoint, "https://api.github.com/user", redirectURL)
}

// Gitea returns a provider for a self-hosted Gitea at baseURL.
func Gitea(baseURL, clientID, clientSecret, redirectURL string) *Provider {
	base := strings.TrimSuffix(baseURL, "/")
	endpoint := oauth2.Endpoint{
		AuthURL:  base + "/login/oauth/authorize",
		TokenURL: base + "/login/oauth/access_token",
	}
	return New("gitea", clientID, clientSecret, endpoint, base+"/api/v1/user", redirectURL)
}

func (p *Provider) Name() string { return p.name }

// AuthCodeURL returns the provider page to redirect the user to.
func (p *Provider) AuthCodeURL(state string) string {
	return p.cfg.AuthCodeURL(state)
}

// Exchange trades the callback code for a token and fetches the user identity.
func (p *Provider) Exchange(ctx context.Context, code string) (Identity, error) {
	tok, err := p.cfg.Exchange(ctx, code)
	if err != nil {
		return Identity{}, fmt.Errorf("exchange code: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userURL, nil)
	if err != nil {
		return Identity{}, err
	}
	resp, err := p.cfg.Client(ctx, tok).Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("fetch user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("fetch user: status %d", resp.StatusCode)
	}
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Identity{}, fmt.Errorf("decode user: %w", err)
	}
	return Identity{Provider: p.name, UID: fmt.Sprint(u.ID), Login: u.Login}, nil
}
