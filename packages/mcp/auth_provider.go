package mcp

import (
	"context"
	"net/http"
	"net/url"
)

// McpFetch performs an HTTP request. It mirrors the platform `fetch` the
// upstream code injects so that transports and OAuth discovery can be driven
// by an arbitrary HTTP client.
type McpFetch func(req *http.Request) (*http.Response, error)

// FetchFromClient adapts an *http.Client to McpFetch. A nil client uses
// http.DefaultClient.
func FetchFromClient(client *http.Client) McpFetch {
	if client == nil {
		client = http.DefaultClient
	}
	return func(req *http.Request) (*http.Response, error) {
		return client.Do(req)
	}
}

// UnauthorizedContext describes a request rejected for authorization.
type UnauthorizedContext struct {
	// Response is the 401 response, or a 403 response whose challenge reports
	// `insufficient_scope`.
	Response *http.Response
	// ServerURL is the MCP server URL the request was sent to.
	ServerURL *url.URL
	// Fetch is the fetch used for the rejected request, for reuse by OAuth.
	Fetch McpFetch
	// Token is the access token the rejected request carried, if any.
	Token string
	// HasToken reports whether the rejected request carried a token.
	HasToken bool
}

// AuthProvider supplies bearer tokens to an MCP HTTP transport.
type AuthProvider interface {
	// Token returns the current access token, or "" when none is available.
	Token(ctx context.Context) (string, error)
}

// UnauthorizedHandler is implemented by auth providers that react to a 401 or
// an insufficient-scope 403.
type UnauthorizedHandler interface {
	OnUnauthorized(ctx context.Context, context UnauthorizedContext) error
}
