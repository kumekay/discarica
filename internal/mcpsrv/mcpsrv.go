// Package mcpsrv exposes discarica over the Model Context Protocol
// (streamable HTTP). Bearer tokens — dashboard upload tokens or tokens issued
// by the built-in OAuth server — authenticate every request.
package mcpsrv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kumekay/discarica/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type service struct {
	store   *store.Store
	baseURL string
}

// Handler returns the authenticated /mcp handler. baseURL is the external
// base URL used for sharable links and OAuth discovery.
func Handler(st *store.Store, baseURL string) http.Handler {
	svc := &service{store: st, baseURL: baseURL}

	server := mcp.NewServer(&mcp.Implementation{Name: "discarica", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "upload_page",
		Description: "Upload an HTML or Markdown page and get a sharable link.",
	}, svc.uploadPage)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_pages",
		Description: "List your uploaded pages.",
	}, svc.listPages)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Description: "Delete an uploaded page.",
	}, svc.deletePage)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_visibility",
		Description: "Make a page public (link-only access) or private (viewing requires login).",
	}, svc.setVisibility)

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		u, err := st.UserByToken(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: unknown bearer token", auth.ErrInvalidToken)
		}
		return &auth.TokenInfo{
			UserID: strconv.FormatInt(u.ID, 10),
			// Tokens do not expire; the middleware requires a deadline.
			Expiration: time.Now().Add(time.Hour),
		}, nil
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: baseURL + "/.well-known/oauth-protected-resource",
	})(h)
}

func userID(ctx context.Context) (int64, error) {
	info := auth.TokenInfoFromContext(ctx)
	if info == nil {
		return 0, errors.New("no authenticated user in context")
	}
	return strconv.ParseInt(info.UserID, 10, 64)
}

type pageOut struct {
	ID     string `json:"id" jsonschema:"page id"`
	Name   string `json:"name" jsonschema:"display name"`
	Public bool   `json:"public" jsonschema:"true if anyone with the link can view"`
	URL    string `json:"url" jsonschema:"sharable viewer URL"`
	RawURL string `json:"raw_url,omitempty" jsonschema:"raw markdown URL (md pages only)"`
}

func (s *service) pageOut(p store.Page) pageOut {
	out := pageOut{ID: p.ID, Name: p.Name, Public: p.Public}
	if p.Public {
		out.URL = s.baseURL + "/pub/" + p.ID
	} else {
		out.URL = s.baseURL + "/p/" + p.ID
	}
	if p.Kind == "md" {
		out.RawURL = out.URL + "/raw"
	}
	return out
}

type uploadIn struct {
	Content string `json:"content" jsonschema:"page content (HTML or Markdown text)"`
	Name    string `json:"name,omitempty" jsonschema:"display name; defaults to Untitled"`
	Format  string `json:"format,omitempty" jsonschema:"html or md; defaults to html"`
	Public  bool   `json:"public,omitempty" jsonschema:"true: anyone with the link can view; false (default): viewing requires GitHub/Gitea login"`
}

func (s *service) uploadPage(ctx context.Context, _ *mcp.CallToolRequest, in uploadIn) (*mcp.CallToolResult, pageOut, error) {
	uid, err := userID(ctx)
	if err != nil {
		return nil, pageOut{}, err
	}
	if in.Content == "" {
		return nil, pageOut{}, errors.New("content is required")
	}
	kind := "html"
	if in.Format == "md" {
		kind = "md"
	}
	name := in.Name
	if name == "" {
		name = "Untitled"
	}
	p, err := s.store.CreatePage(ctx, uid, name, kind, []byte(in.Content), in.Public)
	if err != nil {
		return nil, pageOut{}, fmt.Errorf("create page: %w", err)
	}
	return nil, s.pageOut(p), nil
}

type listOut struct {
	Pages []pageOut `json:"pages"`
}

func (s *service) listPages(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, listOut, error) {
	uid, err := userID(ctx)
	if err != nil {
		return nil, listOut{}, err
	}
	pages, err := s.store.ListPages(ctx, uid)
	if err != nil {
		return nil, listOut{}, fmt.Errorf("list pages: %w", err)
	}
	out := listOut{Pages: make([]pageOut, 0, len(pages))}
	for _, p := range pages {
		out.Pages = append(out.Pages, s.pageOut(p))
	}
	return nil, out, nil
}

type idIn struct {
	ID string `json:"id" jsonschema:"page id"`
}

type okOut struct {
	OK bool `json:"ok"`
}

func (s *service) deletePage(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, okOut, error) {
	uid, err := userID(ctx)
	if err != nil {
		return nil, okOut{}, err
	}
	if err := s.store.DeletePage(ctx, in.ID, uid); err != nil {
		return nil, okOut{}, fmt.Errorf("delete page: %w", err)
	}
	return nil, okOut{OK: true}, nil
}

type visibilityIn struct {
	ID     string `json:"id" jsonschema:"page id"`
	Public bool   `json:"public" jsonschema:"desired visibility"`
}

func (s *service) setVisibility(ctx context.Context, _ *mcp.CallToolRequest, in visibilityIn) (*mcp.CallToolResult, pageOut, error) {
	uid, err := userID(ctx)
	if err != nil {
		return nil, pageOut{}, err
	}
	if err := s.store.SetPageVisibility(ctx, in.ID, uid, in.Public); err != nil {
		return nil, pageOut{}, fmt.Errorf("set visibility: %w", err)
	}
	p, err := s.store.PageByID(ctx, in.ID)
	if err != nil {
		return nil, pageOut{}, fmt.Errorf("reload page: %w", err)
	}
	return nil, s.pageOut(p), nil
}
