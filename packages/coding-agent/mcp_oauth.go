// MCP OAuth configuration for the embedded SDK.
//
// This file ports the OAuth-facing parts of
// packages/coding-agent/src/extensions/mcp/config.ts and oauth.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. It adapts the extension's
// interactive OAuth flow onto the accepted packages/mcp provider so an HTTP
// server can authenticate without a bundled Node OAuth helper.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"net/url"
	"strings"

	"github.com/minifish-org/pith/packages/mcp"
)

// MCPOAuthConfig configures OAuth for one HTTP MCP server. Callback serving and
// interactive prompts belong to the host: the SDK only builds the provider and
// surfaces the authorization URL through OnRedirect.
type MCPOAuthConfig struct {
	ClientID              string
	ClientSecret          string
	CallbackPort          int
	CallbackURL           string
	Scope                 string
	ClientName            string
	AuthServerMetadataURL string
}

// IsLoopbackRedirectURI reports whether a redirect URI is an http URI on a
// loopback host without a query or fragment, matching the upstream check.
func IsLoopbackRedirectURI(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1":
	default:
		return false
	}
	return parsed.RawQuery == "" && parsed.Fragment == ""
}

// MCPOAuthProviderOptions configures NewMCPOAuthProvider.
type MCPOAuthProviderOptions struct {
	ServerURL   string
	RedirectURL string
	Config      MCPOAuthConfig
	Store       mcp.McpOAuthStateStore
	OnRedirect  func(authorizationURL string) error
}

// NewMCPOAuthProvider builds an mcp.AuthProvider for one server. The provider
// supplies the current bearer token; a caller that needs to drive the
// interactive flow keeps the underlying *mcp.McpOAuthProvider via
// NewMCPOAuthFlowProvider.
func NewMCPOAuthProvider(options MCPOAuthProviderOptions) mcp.AuthProvider {
	return mcpOAuthAuthProvider{provider: NewMCPOAuthFlowProvider(options)}
}

// NewMCPOAuthFlowProvider builds the stateful OAuth client provider so a caller
// can run the authorization flow (AuthorizeMcp) itself.
func NewMCPOAuthFlowProvider(options MCPOAuthProviderOptions) *mcp.McpOAuthProvider {
	metadata := mcp.OAuthClientMetadata{}
	if options.Config.ClientName != "" {
		metadata.ClientName = options.Config.ClientName
	}
	if options.Config.Scope != "" {
		metadata.Scope = options.Config.Scope
	}
	if options.RedirectURL != "" {
		metadata.RedirectURIs = []string{options.RedirectURL}
	}
	return mcp.NewMcpOAuthProvider(mcp.McpOAuthProviderOptions{
		ServerURL:      options.ServerURL,
		RedirectURL:    options.RedirectURL,
		ClientMetadata: metadata,
		ClientID:       options.Config.ClientID,
		ClientSecret:   options.Config.ClientSecret,
		Store:          options.Store,
		OnRedirect:     options.OnRedirect,
	})
}

// mcpOAuthAuthProvider adapts the OAuth client provider to the transport's
// bearer-token interface.
type mcpOAuthAuthProvider struct {
	provider *mcp.McpOAuthProvider
}

// Token returns the current access token, or "" when none is stored.
func (p mcpOAuthAuthProvider) Token(ctx context.Context) (string, error) {
	tokens, err := p.provider.Tokens()
	if err != nil {
		return "", err
	}
	if tokens == nil {
		return "", nil
	}
	return tokens.AccessToken, nil
}

// validateMCPOAuth validates an OAuth config, returning a human-readable reason
// for the first invalid field.
func validateMCPOAuth(config *MCPOAuthConfig) string {
	if config == nil {
		return ""
	}
	if config.CallbackPort != 0 && (config.CallbackPort < 1 || config.CallbackPort > 65535) {
		return "oauth.callbackPort must be a port number"
	}
	if config.CallbackURL != "" && !IsLoopbackRedirectURI(config.CallbackURL) {
		return "oauth.callbackUrl must be an http URI on localhost, 127.0.0.1, or [::1] without query or fragment"
	}
	if config.AuthServerMetadataURL != "" && !strings.HasPrefix(config.AuthServerMetadataURL, "https://") && !IsLoopbackRedirectURI(config.AuthServerMetadataURL) {
		return "oauth.authServerMetadataUrl must use https"
	}
	return ""
}
