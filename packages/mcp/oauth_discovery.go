package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// OAuthProtectedResourceMetadata is the RFC 9728 protected-resource document.
type OAuthProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers,omitempty"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

// AuthorizationServerMetadata is the RFC 8414 authorization-server document.
type AuthorizationServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                            []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported,omitempty"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported,omitempty"`
	AuthorizationResponseISSParameterSupported bool     `json:"authorization_response_iss_parameter_supported,omitempty"`
}

// OAuthTokens is an OAuth token response.
type OAuthTokens struct {
	AccessToken  string   `json:"access_token"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    *float64 `json:"expires_in,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	IDToken      string   `json:"id_token,omitempty"`
}

// OAuthClientMetadata is the dynamic client registration payload.
type OAuthClientMetadata struct {
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
	ClientURI               string   `json:"client_uri,omitempty"`
	LogoURI                 string   `json:"logo_uri,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	Contacts                []string `json:"contacts,omitempty"`
	TOSURI                  string   `json:"tos_uri,omitempty"`
	PolicyURI               string   `json:"policy_uri,omitempty"`
	JWKSURI                 string   `json:"jwks_uri,omitempty"`
	JWKS                    any      `json:"jwks,omitempty"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
	SoftwareStatement       string   `json:"software_statement,omitempty"`
}

// OAuthClientInformation is registered client information, optionally merged
// with the metadata returned by registration.
type OAuthClientInformation struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        *float64 `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   *float64 `json:"client_secret_expires_at,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
}

// OAuthDiscoveryState is cached discovery state.
type OAuthDiscoveryState struct {
	AuthorizationServerURL      string                          `json:"authorizationServerUrl"`
	AuthorizationServerMetadata *AuthorizationServerMetadata    `json:"authorizationServerMetadata,omitempty"`
	ResourceMetadata            *OAuthProtectedResourceMetadata `json:"resourceMetadata,omitempty"`
	ResourceMetadataURL         string                          `json:"resourceMetadataUrl,omitempty"`
}

// OAuthServerInfo is the result of OAuth discovery.
type OAuthServerInfo struct {
	AuthorizationServerURL      string
	AuthorizationServerMetadata *AuthorizationServerMetadata
	ResourceMetadata            *OAuthProtectedResourceMetadata
}

// OAuthChallenge is the parsed WWW-Authenticate bearer challenge.
type OAuthChallenge struct {
	ResourceMetadataURL *url.URL
	Scope               string
	Error               string
	ErrorDescription    string
}

// OAuth error types.

// OAuthError is an RFC 6749 token or authorization error.
type OAuthError struct {
	Code     string
	Message  string
	ErrorURI string
}

// Error implements the error interface.
func (e *OAuthError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// OAuthIssuerMismatchError reports a discovery issuer mismatch (RFC 9207).
type OAuthIssuerMismatchError struct {
	Expected string
	Received string
	HasValue bool
}

// Error implements the error interface.
func (e *OAuthIssuerMismatchError) Error() string {
	received := "none"
	if e.HasValue {
		received = fmt.Sprintf("%q", e.Received)
	}
	return fmt.Sprintf("OAuth issuer mismatch: expected %q, received %s", e.Expected, received)
}

// OAuthInsecureEndpointError reports a non-HTTPS endpoint outside loopback.
type OAuthInsecureEndpointError struct {
	Endpoint string
}

// Error implements the error interface.
func (e *OAuthInsecureEndpointError) Error() string {
	return fmt.Sprintf("Refusing to send OAuth credentials to non-HTTPS endpoint %s", e.Endpoint)
}

// OAuthRegistrationError reports a failed dynamic client registration.
type OAuthRegistrationError struct {
	Status int
	Body   string
}

// Error implements the error interface.
func (e *OAuthRegistrationError) Error() string {
	return fmt.Sprintf("OAuth dynamic client registration failed with status %d: %s", e.Status, e.Body)
}

// McpOAuthAuthorizationRequiredError reports that interactive authorization is
// required before the request can succeed.
type McpOAuthAuthorizationRequiredError struct{}

// Error implements the error interface.
func (e *McpOAuthAuthorizationRequiredError) Error() string {
	return "MCP OAuth authorization requires user interaction"
}

// parse helpers

func parseObject(value any, name string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("Invalid %s", name)
	}
	return object, nil
}

func requiredString(value any, name string) (string, error) {
	text, ok := value.(string)
	if !ok || text == "" {
		return "", fmt.Errorf("Invalid %s", name)
	}
	return text, nil
}

func isAbsent(value any) bool {
	return value == nil || value == ""
}

func optionalString(value any, name string) (*string, error) {
	if isAbsent(value) {
		return nil, nil
	}
	text, err := requiredString(value, name)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

func optionalStrings(value any, name string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid %s", name)
	}
	result := make([]string, 0, len(array))
	for _, item := range array {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("Invalid %s", name)
		}
		result = append(result, text)
	}
	return result, nil
}

func safeURL(value any, name string) (string, error) {
	text, err := requiredString(value, name)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("Invalid %s", name)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "javascript", "data", "vbscript":
		return "", fmt.Errorf("Invalid %s", name)
	}
	return text, nil
}

func optionalURL(value any, name string) (*string, error) {
	if isAbsent(value) {
		return nil, nil
	}
	text, err := safeURL(value, name)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

// ParseProtectedResourceMetadata validates a protected-resource document.
func ParseProtectedResourceMetadata(value any) (OAuthProtectedResourceMetadata, error) {
	input, err := parseObject(value, "OAuth protected resource metadata")
	if err != nil {
		return OAuthProtectedResourceMetadata{}, err
	}
	resource, err := safeURL(input["resource"], "OAuth protected resource metadata resource")
	if err != nil {
		return OAuthProtectedResourceMetadata{}, err
	}
	servers, err := optionalStrings(input["authorization_servers"], "authorization_servers")
	if err != nil {
		return OAuthProtectedResourceMetadata{}, err
	}
	for i, server := range servers {
		validated, err := safeURL(server, "authorization server URL")
		if err != nil {
			return OAuthProtectedResourceMetadata{}, err
		}
		servers[i] = validated
	}
	scopes, err := optionalStrings(input["scopes_supported"], "scopes_supported")
	if err != nil {
		return OAuthProtectedResourceMetadata{}, err
	}
	return OAuthProtectedResourceMetadata{Resource: resource, AuthorizationServers: servers, ScopesSupported: scopes}, nil
}

// ParseAuthorizationServerMetadata validates an authorization-server document.
func ParseAuthorizationServerMetadata(value any) (AuthorizationServerMetadata, error) {
	input, err := parseObject(value, "authorization server metadata")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	responseTypes, err := optionalStrings(input["response_types_supported"], "response_types_supported")
	if err != nil || responseTypes == nil {
		return AuthorizationServerMetadata{}, errors.New("Invalid response_types_supported")
	}
	issuer, err := safeURL(input["issuer"], "authorization server issuer")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	authorizationEndpoint, err := safeURL(input["authorization_endpoint"], "authorization endpoint")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	tokenEndpoint, err := safeURL(input["token_endpoint"], "token endpoint")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	registrationEndpoint, err := optionalURL(input["registration_endpoint"], "registration endpoint")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	scopes, err := optionalStrings(input["scopes_supported"], "scopes_supported")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	grantTypes, err := optionalStrings(input["grant_types_supported"], "grant_types_supported")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	authMethods, err := optionalStrings(input["token_endpoint_auth_methods_supported"], "token_endpoint_auth_methods_supported")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	challengeMethods, err := optionalStrings(input["code_challenge_methods_supported"], "code_challenge_methods_supported")
	if err != nil {
		return AuthorizationServerMetadata{}, err
	}
	metadata := AuthorizationServerMetadata{
		Issuer:                            issuer,
		AuthorizationEndpoint:             authorizationEndpoint,
		TokenEndpoint:                     tokenEndpoint,
		ScopesSupported:                   scopes,
		ResponseTypesSupported:            responseTypes,
		GrantTypesSupported:               grantTypes,
		TokenEndpointAuthMethodsSupported: authMethods,
		CodeChallengeMethodsSupported:     challengeMethods,
	}
	if registrationEndpoint != nil {
		metadata.RegistrationEndpoint = *registrationEndpoint
	}
	if flag, ok := input["client_id_metadata_document_supported"].(bool); ok {
		metadata.ClientIDMetadataDocumentSupported = flag
	}
	if flag, ok := input["authorization_response_iss_parameter_supported"].(bool); ok {
		metadata.AuthorizationResponseISSParameterSupported = flag
	}
	return metadata, nil
}

// ParseOAuthTokens validates a token response.
func ParseOAuthTokens(value any) (OAuthTokens, error) {
	input, err := parseObject(value, "OAuth token response")
	if err != nil {
		return OAuthTokens{}, err
	}
	accessToken, err := requiredString(input["access_token"], "access_token")
	if err != nil {
		return OAuthTokens{}, err
	}
	tokenType, err := requiredString(input["token_type"], "token_type")
	if err != nil {
		return OAuthTokens{}, err
	}
	var expiresIn *float64
	if !isAbsent(input["expires_in"]) {
		number, ok := toFloat(input["expires_in"])
		if !ok {
			return OAuthTokens{}, errors.New("Invalid expires_in")
		}
		expiresIn = &number
	}
	scope, err := optionalString(input["scope"], "scope")
	if err != nil {
		return OAuthTokens{}, err
	}
	refreshToken, err := optionalString(input["refresh_token"], "refresh_token")
	if err != nil {
		return OAuthTokens{}, err
	}
	idToken, err := optionalString(input["id_token"], "id_token")
	if err != nil {
		return OAuthTokens{}, err
	}
	tokens := OAuthTokens{AccessToken: accessToken, TokenType: tokenType, ExpiresIn: expiresIn}
	if scope != nil {
		tokens.Scope = *scope
	}
	if refreshToken != nil {
		tokens.RefreshToken = *refreshToken
	}
	if idToken != nil {
		tokens.IDToken = *idToken
	}
	return tokens, nil
}

// ParseClientInformation validates a dynamic client registration response.
func ParseClientInformation(value any) (OAuthClientInformation, error) {
	input, err := parseObject(value, "OAuth client registration response")
	if err != nil {
		return OAuthClientInformation{}, err
	}
	clientID, err := requiredString(input["client_id"], "client_id")
	if err != nil {
		return OAuthClientInformation{}, err
	}
	information := OAuthClientInformation{ClientID: clientID}
	if secret, err := optionalString(input["client_secret"], "client_secret"); err != nil {
		return OAuthClientInformation{}, err
	} else if secret != nil {
		information.ClientSecret = *secret
	}
	if issuedAt, ok := toFloat(input["client_id_issued_at"]); ok {
		information.ClientIDIssuedAt = &issuedAt
	}
	if expiresAt, ok := toFloat(input["client_secret_expires_at"]); ok {
		information.ClientSecretExpiresAt = &expiresAt
	}
	if method, err := optionalString(input["token_endpoint_auth_method"], "token_endpoint_auth_method"); err != nil {
		return OAuthClientInformation{}, err
	} else if method != nil {
		information.TokenEndpointAuthMethod = *method
	}
	if redirects, err := optionalStrings(input["redirect_uris"], "redirect_uris"); err != nil {
		return OAuthClientInformation{}, err
	} else {
		information.RedirectURIs = redirects
	}
	if name, err := optionalString(input["client_name"], "client_name"); err != nil {
		return OAuthClientInformation{}, err
	} else if name != nil {
		information.ClientName = *name
	}
	return information, nil
}

func toFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// isValidHTTPStatus reports a 2xx response.
func isValidHTTPStatus(status int) bool { return status >= 200 && status < 300 }

// isDiscoveryMiss reports statuses meaning "no metadata here, try the next URL".
func isDiscoveryMiss(status int) bool {
	return (status >= 400 && status < 500) || status == 502
}

func pathSuffix(pathname string) string {
	if strings.HasSuffix(pathname, "/") {
		return pathname[:len(pathname)-1]
	}
	return pathname
}

var fieldPattern = regexp.MustCompile(`(?i)(?:^|[,\s])([a-z_]+)=(?:"([^"]*)"|([^\s,]+))`)

func authField(header, name string) string {
	matches := fieldPattern.FindAllStringSubmatch(header, -1)
	for _, match := range matches {
		if !strings.EqualFold(match[1], name) {
			continue
		}
		if match[2] != "" {
			return match[2]
		}
		if match[3] != "" {
			return match[3]
		}
	}
	return ""
}

// ParseWwwAuthenticate parses a bearer/dpop challenge.
func ParseWwwAuthenticate(header string) OAuthChallenge {
	if header == "" {
		return OAuthChallenge{}
	}
	trimmed := strings.TrimLeft(header, " \t")
	scheme := strings.ToLower(strings.SplitN(trimmed, " ", 2)[0])
	if scheme != "bearer" && scheme != "dpop" {
		return OAuthChallenge{}
	}
	challenge := OAuthChallenge{
		Scope:            authField(header, "scope"),
		Error:            authField(header, "error"),
		ErrorDescription: authField(header, "error_description"),
	}
	if resourceMetadata := authField(header, "resource_metadata"); resourceMetadata != "" {
		if parsed, err := url.Parse(resourceMetadata); err == nil {
			challenge.ResourceMetadataURL = parsed
		}
	}
	return challenge
}

func fetchMetadata(ctx context.Context, target string, fetch McpFetch, protocolVersion string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	return fetch(request)
}

func discardHTTPResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
}

func decodeJSONBody(response *http.Response) (any, error) {
	defer response.Body.Close()
	decoder := json.NewDecoder(response.Body)
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// DiscoverProtectedResourceOptions configures protected-resource discovery.
type DiscoverProtectedResourceOptions struct {
	ResourceMetadataURL string
	ProtocolVersion     string
	Fetch               McpFetch
}

// DiscoverProtectedResourceMetadata fetches the RFC 9728 document.
func DiscoverProtectedResourceMetadata(ctx context.Context, serverURL string, options DiscoverProtectedResourceOptions) (*OAuthProtectedResourceMetadata, error) {
	server, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	fetch := options.Fetch
	if fetch == nil {
		fetch = FetchFromClient(nil)
	}
	version := options.ProtocolVersion
	if version == "" {
		version = LatestProtocolVersion
	}
	target := options.ResourceMetadataURL
	if target == "" {
		origin := server.Scheme + "://" + server.Host
		target = origin + "/.well-known/oauth-protected-resource" + pathSuffix(server.Path)
	}
	response, err := fetchMetadata(ctx, target, fetch, version)
	if err != nil {
		return nil, err
	}
	if options.ResourceMetadataURL == "" && server.Path != "" && server.Path != "/" && isDiscoveryMiss(response.StatusCode) {
		discardHTTPResponse(response)
		origin := server.Scheme + "://" + server.Host
		response, err = fetchMetadata(ctx, origin+"/.well-known/oauth-protected-resource", fetch, version)
		if err != nil {
			return nil, err
		}
	}
	if !isValidHTTPStatus(response.StatusCode) {
		status := response.StatusCode
		discardHTTPResponse(response)
		return nil, fmt.Errorf("HTTP %d loading OAuth protected resource metadata", status)
	}
	value, err := decodeJSONBody(response)
	if err != nil {
		return nil, err
	}
	metadata, err := ParseProtectedResourceMetadata(value)
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

// AuthorizationServerDiscoveryURL is one candidate metadata document.
type AuthorizationServerDiscoveryURL struct {
	URL  string
	Type string
}

// BuildAuthorizationServerDiscoveryURLs lists the metadata URLs to try.
func BuildAuthorizationServerDiscoveryURLs(authorizationServerURL string) []AuthorizationServerDiscoveryURL {
	issuer, err := url.Parse(authorizationServerURL)
	if err != nil {
		return nil
	}
	origin := issuer.Scheme + "://" + issuer.Host
	path := pathSuffix(issuer.Path)
	urls := []AuthorizationServerDiscoveryURL{
		{URL: origin + "/.well-known/oauth-authorization-server" + path, Type: "oauth"},
		{URL: origin + "/.well-known/openid-configuration" + path, Type: "oidc"},
	}
	if path != "" {
		urls = append(urls, AuthorizationServerDiscoveryURL{URL: origin + path + "/.well-known/openid-configuration", Type: "oidc"})
	}
	return urls
}

// DiscoverAuthorizationServerOptions configures server metadata discovery.
type DiscoverAuthorizationServerOptions struct {
	Fetch                McpFetch
	ProtocolVersion      string
	SkipIssuerValidation bool
}

// DiscoverAuthorizationServerMetadata fetches metadata, validating the issuer.
func DiscoverAuthorizationServerMetadata(ctx context.Context, authorizationServerURL string, options DiscoverAuthorizationServerOptions) (*AuthorizationServerMetadata, error) {
	fetch := options.Fetch
	if fetch == nil {
		fetch = FetchFromClient(nil)
	}
	version := options.ProtocolVersion
	if version == "" {
		version = LatestProtocolVersion
	}
	for _, candidate := range BuildAuthorizationServerDiscoveryURLs(authorizationServerURL) {
		response, err := fetchMetadata(ctx, candidate.URL, fetch, version)
		if err != nil {
			return nil, err
		}
		if !isValidHTTPStatus(response.StatusCode) {
			status := response.StatusCode
			discardHTTPResponse(response)
			if isDiscoveryMiss(status) {
				continue
			}
			return nil, fmt.Errorf("HTTP %d loading authorization server metadata from %s", status, candidate.URL)
		}
		value, err := decodeJSONBody(response)
		if err != nil {
			return nil, err
		}
		metadata, err := ParseAuthorizationServerMetadata(value)
		if err != nil {
			return nil, err
		}
		if !options.SkipIssuerValidation {
			expected := authorizationServerURL
			if trimTrailingSlash(metadata.Issuer) != trimTrailingSlash(expected) {
				return nil, &OAuthIssuerMismatchError{Expected: expected, Received: metadata.Issuer, HasValue: true}
			}
		}
		return &metadata, nil
	}
	return nil, nil
}

func trimTrailingSlash(value string) string {
	if strings.HasSuffix(value, "/") {
		return value[:len(value)-1]
	}
	return value
}

// DiscoverOAuthServerInfoOptions configures combined discovery.
type DiscoverOAuthServerInfoOptions struct {
	ResourceMetadataURL            *url.URL
	AuthorizationServerMetadataURL *url.URL
	Fetch                          McpFetch
	SkipIssuerValidation           bool
}

// DiscoverOAuthServerInfo discovers the resource and authorization server.
func DiscoverOAuthServerInfo(ctx context.Context, serverURL string, options DiscoverOAuthServerInfoOptions) (OAuthServerInfo, error) {
	fetch := options.Fetch
	if fetch == nil {
		fetch = FetchFromClient(nil)
	}
	var resourceMetadata *OAuthProtectedResourceMetadata
	resourceMetadataURL := ""
	if options.ResourceMetadataURL != nil {
		resourceMetadataURL = options.ResourceMetadataURL.String()
	}
	metadata, err := DiscoverProtectedResourceMetadata(ctx, serverURL, DiscoverProtectedResourceOptions{
		ResourceMetadataURL: resourceMetadataURL,
		Fetch:               fetch,
	})
	if err != nil {
		// A network failure is fatal; a malformed document falls back.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return OAuthServerInfo{}, err
		}
	} else {
		resourceMetadata = metadata
	}
	if options.AuthorizationServerMetadataURL != nil {
		target := options.AuthorizationServerMetadataURL.String()
		response, err := fetchMetadata(ctx, target, fetch, LatestProtocolVersion)
		if err != nil {
			return OAuthServerInfo{}, err
		}
		if !isValidHTTPStatus(response.StatusCode) {
			status := response.StatusCode
			discardHTTPResponse(response)
			return OAuthServerInfo{}, fmt.Errorf("HTTP %d loading authorization server metadata from %s", status, target)
		}
		value, err := decodeJSONBody(response)
		if err != nil {
			return OAuthServerInfo{}, err
		}
		serverMetadata, err := ParseAuthorizationServerMetadata(value)
		if err != nil {
			return OAuthServerInfo{}, err
		}
		return OAuthServerInfo{
			AuthorizationServerURL:      serverMetadata.Issuer,
			AuthorizationServerMetadata: &serverMetadata,
			ResourceMetadata:            resourceMetadata,
		}, nil
	}
	authorizationServerURL := ""
	if resourceMetadata != nil && len(resourceMetadata.AuthorizationServers) > 0 {
		authorizationServerURL = resourceMetadata.AuthorizationServers[0]
	} else {
		parsed, err := url.Parse(serverURL)
		if err != nil {
			return OAuthServerInfo{}, err
		}
		authorizationServerURL = parsed.Scheme + "://" + parsed.Host + "/"
	}
	serverMetadata, err := DiscoverAuthorizationServerMetadata(ctx, authorizationServerURL, DiscoverAuthorizationServerOptions{
		Fetch:                fetch,
		SkipIssuerValidation: options.SkipIssuerValidation,
	})
	if err != nil {
		return OAuthServerInfo{}, err
	}
	return OAuthServerInfo{
		AuthorizationServerURL:      authorizationServerURL,
		AuthorizationServerMetadata: serverMetadata,
		ResourceMetadata:            resourceMetadata,
	}, nil
}

// ResourceURLFromServerURL strips the fragment from a server URL.
func ResourceURLFromServerURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	parsed.Fragment = ""
	return parsed, nil
}

// SelectResource validates the protected resource against the server URL.
func SelectResource(serverURL string, metadata *OAuthProtectedResourceMetadata) (*string, error) {
	if metadata == nil {
		return nil, nil
	}
	requested, err := ResourceURLFromServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	configured, err := url.Parse(metadata.Resource)
	if err != nil {
		return nil, err
	}
	if requested.Scheme+"://"+requested.Host != configured.Scheme+"://"+configured.Host {
		return nil, fmt.Errorf("Protected resource %s does not match MCP server %s", metadata.Resource, requested.String())
	}
	requestedPath := requested.Path
	if !strings.HasSuffix(requestedPath, "/") {
		requestedPath += "/"
	}
	configuredPath := configured.Path
	if !strings.HasSuffix(configuredPath, "/") {
		configuredPath += "/"
	}
	if !strings.HasPrefix(requestedPath, configuredPath) {
		return nil, fmt.Errorf("Protected resource %s does not match MCP server %s", metadata.Resource, requested.String())
	}
	resource := metadata.Resource
	return &resource, nil
}
