package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth flow results.
const (
	OAuthFlowAuthorized = "AUTHORIZED"
	OAuthFlowRedirect   = "REDIRECT"
)

// OAuthClientProvider supplies OAuth credentials and state to the flow.
//
// Optional capabilities are detected through the additional interfaces below,
// mirroring the optional methods of the upstream provider type.
type OAuthClientProvider interface {
	RedirectURL() string
	ClientMetadata() OAuthClientMetadata
	ClientInformation() (*OAuthClientInformation, error)
	Tokens() (*OAuthTokens, error)
	SaveTokens(tokens *OAuthTokens) error
	RedirectToAuthorization(authorizationURL string) error
	SaveCodeVerifier(verifier string) error
	CodeVerifier() (string, error)
}

type oauthStateProvider interface{ State() (string, error) }

type oauthClientMetadataURLProvider interface{ ClientMetadataURL() string }

type oauthSaveClientInformationProvider interface {
	SaveClientInformation(information *OAuthClientInformation) error
}

type oauthAddClientAuthenticationProvider interface {
	AddClientAuthentication(headers http.Header, params url.Values, endpoint string, metadata *AuthorizationServerMetadata) error
}

type oauthInvalidateCredentialsProvider interface{ InvalidateCredentials(kind string) error }

type oauthSaveDiscoveryStateProvider interface {
	SaveDiscoveryState(state OAuthDiscoveryState) error
}

type oauthDiscoveryStateProvider interface {
	DiscoveryState() (*OAuthDiscoveryState, error)
}

// OAuthFlowOptions configures AuthorizeMcp.
type OAuthFlowOptions struct {
	ServerURL                      string
	AuthorizationCode              string
	ISS                            string
	HasISS                         bool
	Scope                          string
	ResourceMetadataURL            *url.URL
	AuthorizationServerMetadataURL *url.URL
	Fetch                          McpFetch
	SkipIssuerValidation           bool
	SkipRefresh                    bool
}

// TokenRequestOptions are the shared inputs of a token request.
type TokenRequestOptions struct {
	Metadata                *AuthorizationServerMetadata
	ClientInformation       OAuthClientInformation
	Resource                string
	AddClientAuthentication func(headers http.Header, params url.Values, endpoint string, metadata *AuthorizationServerMetadata) error
	Fetch                   McpFetch
}

// ExchangeOptions are the inputs of an authorization-code exchange.
type ExchangeOptions struct {
	TokenRequestOptions
	Code         string
	CodeVerifier string
	RedirectURL  string
}

// RefreshOptions are the inputs of a refresh-token request.
type RefreshOptions struct {
	TokenRequestOptions
	RefreshToken string
}

// RegisterClientOptions configures dynamic client registration.
type RegisterClientOptions struct {
	Metadata       *AuthorizationServerMetadata
	ClientMetadata OAuthClientMetadata
	Scope          string
	Fetch          McpFetch
}

// StartAuthorizationOptions configures the authorization redirect.
type StartAuthorizationOptions struct {
	Metadata          *AuthorizationServerMetadata
	ClientInformation OAuthClientInformation
	RedirectURL       string
	Scope             string
	State             string
	Resource          string
}

// AuthorizationResult is a prepared authorization redirect.
type AuthorizationResult struct {
	AuthorizationURL string
	CodeVerifier     string
}

func loopbackHost(hostname string) bool {
	switch hostname {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	default:
		return false
	}
}

func secureEndpoint(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" && !loopbackHost(parsed.Hostname()) {
		return nil, &OAuthInsecureEndpointError{Endpoint: parsed.String()}
	}
	return parsed, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func selectClientAuthMethod(information OAuthClientInformation, supported []string) string {
	hinted := information.TokenEndpointAuthMethod
	validHint := hinted == "client_secret_basic" || hinted == "client_secret_post" || hinted == "none"
	if validHint && (len(supported) == 0 || containsString(supported, hinted)) {
		return hinted
	}
	if len(supported) == 0 {
		if information.ClientSecret != "" {
			return "client_secret_basic"
		}
		return "none"
	}
	if information.ClientSecret != "" && containsString(supported, "client_secret_basic") {
		return "client_secret_basic"
	}
	if information.ClientSecret != "" && containsString(supported, "client_secret_post") {
		return "client_secret_post"
	}
	if containsString(supported, "none") {
		return "none"
	}
	if information.ClientSecret != "" {
		return "client_secret_post"
	}
	return "none"
}

func applyClientAuthentication(method string, information OAuthClientInformation, headers http.Header, params url.Values) error {
	switch method {
	case "client_secret_basic":
		if information.ClientSecret == "" {
			return errors.New("client_secret_basic requires a client secret")
		}
		credentials := base64.StdEncoding.EncodeToString([]byte(information.ClientID + ":" + information.ClientSecret))
		headers.Set("Authorization", "Basic "+credentials)
	default:
		params.Set("client_id", information.ClientID)
		if method == "client_secret_post" && information.ClientSecret != "" {
			params.Set("client_secret", information.ClientSecret)
		}
	}
	return nil
}

func generatePKCE() (string, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(bytes)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	return verifier, challenge, nil
}

func serverOrigin(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	return parsed.Scheme + "://" + parsed.Host
}

// StartAuthorization builds the authorization URL and PKCE verifier.
func StartAuthorization(ctx context.Context, authorizationServerURL string, options StartAuthorizationOptions) (AuthorizationResult, error) {
	metadata := options.Metadata
	if metadata != nil {
		if !containsString(metadata.ResponseTypesSupported, "code") {
			return AuthorizationResult{}, errors.New("Authorization server does not support authorization codes")
		}
		if len(metadata.CodeChallengeMethodsSupported) > 0 && !containsString(metadata.CodeChallengeMethodsSupported, "S256") {
			return AuthorizationResult{}, errors.New("Authorization server does not support PKCE S256")
		}
	}
	endpoint := ""
	if metadata != nil && metadata.AuthorizationEndpoint != "" {
		endpoint = metadata.AuthorizationEndpoint
	} else {
		endpoint = serverOrigin(authorizationServerURL) + "/authorize"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return AuthorizationResult{}, err
	}
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return AuthorizationResult{}, err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", options.ClientInformation.ClientID)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("redirect_uri", options.RedirectURL)
	if options.State != "" {
		query.Set("state", options.State)
	}
	if options.Scope != "" {
		query.Set("scope", options.Scope)
	}
	if options.Scope != "" && containsString(strings.Fields(options.Scope), "offline_access") {
		query.Set("prompt", "consent")
	}
	if options.Resource != "" {
		query.Set("resource", options.Resource)
	}
	parsed.RawQuery = query.Encode()
	return AuthorizationResult{AuthorizationURL: parsed.String(), CodeVerifier: verifier}, nil
}

func doTokenRequest(ctx context.Context, authorizationServerURL string, options TokenRequestOptions, params url.Values) (OAuthTokens, error) {
	endpoint := ""
	if options.Metadata != nil {
		endpoint = options.Metadata.TokenEndpoint
	}
	if endpoint == "" {
		endpoint = serverOrigin(authorizationServerURL) + "/token"
	}
	secure, err := secureEndpoint(endpoint)
	if err != nil {
		return OAuthTokens{}, err
	}
	if options.Resource != "" {
		params.Set("resource", options.Resource)
	}
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	if options.AddClientAuthentication != nil {
		if err := options.AddClientAuthentication(headers, params, secure.String(), options.Metadata); err != nil {
			return OAuthTokens{}, err
		}
	} else {
		methods := []string{}
		if options.Metadata != nil {
			methods = options.Metadata.TokenEndpointAuthMethodsSupported
		}
		method := selectClientAuthMethod(options.ClientInformation, methods)
		if err := applyClientAuthentication(method, options.ClientInformation, headers, params); err != nil {
			return OAuthTokens{}, err
		}
	}
	fetch := options.Fetch
	if fetch == nil {
		fetch = FetchFromClient(nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, secure.String(), strings.NewReader(params.Encode()))
	if err != nil {
		return OAuthTokens{}, err
	}
	request.Header = headers
	response, err := fetch(request)
	if err != nil {
		return OAuthTokens{}, err
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return OAuthTokens{}, err
	}
	var value any
	_ = json.Unmarshal(body, &value)
	if object, ok := value.(map[string]any); ok {
		if errorCode, ok := object["error"].(string); ok {
			message := errorCode
			if description, ok := object["error_description"].(string); ok {
				message = description
			}
			errorURI := ""
			if uri, ok := object["error_uri"].(string); ok {
				errorURI = uri
			}
			return OAuthTokens{}, &OAuthError{Code: errorCode, Message: message, ErrorURI: errorURI}
		}
	}
	if !isValidHTTPStatus(response.StatusCode) {
		return OAuthTokens{}, &OAuthError{Code: "server_error", Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, string(body))}
	}
	return ParseOAuthTokens(value)
}

// ExchangeAuthorizationCode exchanges an authorization code for tokens.
func ExchangeAuthorizationCode(ctx context.Context, authorizationServerURL string, options ExchangeOptions) (OAuthTokens, error) {
	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", options.Code)
	params.Set("code_verifier", options.CodeVerifier)
	params.Set("redirect_uri", options.RedirectURL)
	return doTokenRequest(ctx, authorizationServerURL, options.TokenRequestOptions, params)
}

// RefreshAuthorization exchanges a refresh token for tokens.
func RefreshAuthorization(ctx context.Context, authorizationServerURL string, options RefreshOptions) (OAuthTokens, error) {
	params := url.Values{}
	params.Set("grant_type", "refresh_token")
	params.Set("refresh_token", options.RefreshToken)
	tokens, err := doTokenRequest(ctx, authorizationServerURL, options.TokenRequestOptions, params)
	if err != nil {
		return OAuthTokens{}, err
	}
	// Keep the returned refresh token when the server rotates it; otherwise the
	// request's refresh token remains the grant (RFC 6749 §6).
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = options.RefreshToken
	}
	return tokens, nil
}

// RegisterClient performs dynamic client registration.
func RegisterClient(ctx context.Context, authorizationServerURL string, options RegisterClientOptions) (OAuthClientInformation, error) {
	endpoint := ""
	if options.Metadata != nil {
		endpoint = options.Metadata.RegistrationEndpoint
		if endpoint == "" {
			return OAuthClientInformation{}, errors.New("Authorization server does not support dynamic client registration")
		}
	}
	if endpoint == "" {
		endpoint = serverOrigin(authorizationServerURL) + "/register"
	}
	payload := map[string]any{}
	data, err := json.Marshal(options.ClientMetadata)
	if err != nil {
		return OAuthClientInformation{}, err
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return OAuthClientInformation{}, err
	}
	if options.Scope != "" {
		payload["scope"] = options.Scope
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return OAuthClientInformation{}, err
	}
	fetch := options.Fetch
	if fetch == nil {
		fetch = FetchFromClient(nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return OAuthClientInformation{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := fetch(request)
	if err != nil {
		return OAuthClientInformation{}, err
	}
	if !isValidHTTPStatus(response.StatusCode) {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return OAuthClientInformation{}, &OAuthRegistrationError{Status: response.StatusCode, Body: string(body)}
	}
	value, err := decodeJSONBody(response)
	if err != nil {
		return OAuthClientInformation{}, err
	}
	return ParseClientInformation(value)
}

// StepUpScope merges the granted and challenged scopes for step-up.
func StepUpScope(granted, challenged string) string {
	if challenged == "" {
		return ""
	}
	seen := map[string]bool{}
	var scopes []string
	for _, group := range []string{granted, challenged} {
		for _, scope := range strings.Fields(group) {
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}
	return strings.Join(scopes, " ")
}

func withScope(tokens OAuthTokens, scope string) OAuthTokens {
	if tokens.Scope == "" && scope != "" {
		tokens.Scope = scope
	}
	return tokens
}

// AuthorizeMcp runs the OAuth flow, retrying once after a recoverable error.
func AuthorizeMcp(ctx context.Context, provider OAuthClientProvider, options OAuthFlowOptions) (string, error) {
	result, err := runFlow(ctx, provider, options)
	if err != nil {
		var oauthErr *OAuthError
		if errors.As(err, &oauthErr) {
			switch oauthErr.Code {
			case "invalid_client", "unauthorized_client":
				if p, ok := provider.(oauthInvalidateCredentialsProvider); ok {
					_ = p.InvalidateCredentials("all")
				}
				return runFlow(ctx, provider, options)
			case "invalid_grant":
				if p, ok := provider.(oauthInvalidateCredentialsProvider); ok {
					_ = p.InvalidateCredentials("tokens")
				}
				return runFlow(ctx, provider, options)
			}
		}
	}
	return result, err
}

func runFlow(ctx context.Context, provider OAuthClientProvider, options OAuthFlowOptions) (string, error) {
	var metadataURL *url.URL
	if options.AuthorizationServerMetadataURL != nil {
		secure, err := secureEndpoint(options.AuthorizationServerMetadataURL.String())
		if err != nil {
			return "", err
		}
		metadataURL = secure
	}

	var discovered OAuthServerInfo
	if metadataURL == nil {
		if p, ok := provider.(oauthDiscoveryStateProvider); ok {
			cached, err := p.DiscoveryState()
			if err != nil {
				return "", err
			}
			if cached != nil && cached.AuthorizationServerURL != "" {
				metadata := cached.AuthorizationServerMetadata
				if metadata == nil {
					metadata, err = DiscoverAuthorizationServerMetadata(ctx, cached.AuthorizationServerURL, DiscoverAuthorizationServerOptions{
						Fetch:                options.Fetch,
						SkipIssuerValidation: options.SkipIssuerValidation,
					})
					if err != nil {
						return "", err
					}
				}
				discovered = OAuthServerInfo{
					AuthorizationServerURL:      cached.AuthorizationServerURL,
					AuthorizationServerMetadata: metadata,
					ResourceMetadata:            cached.ResourceMetadata,
				}
			}
		}
	}
	if discovered.AuthorizationServerURL == "" {
		info, err := DiscoverOAuthServerInfo(ctx, options.ServerURL, DiscoverOAuthServerInfoOptions{
			ResourceMetadataURL:            options.ResourceMetadataURL,
			AuthorizationServerMetadataURL: metadataURL,
			Fetch:                          options.Fetch,
			SkipIssuerValidation:           options.SkipIssuerValidation,
		})
		if err != nil {
			return "", err
		}
		discovered = info
	}
	if metadataURL == nil {
		if p, ok := provider.(oauthSaveDiscoveryStateProvider); ok {
			state := OAuthDiscoveryState{
				AuthorizationServerURL:      discovered.AuthorizationServerURL,
				AuthorizationServerMetadata: discovered.AuthorizationServerMetadata,
				ResourceMetadata:            discovered.ResourceMetadata,
			}
			if options.ResourceMetadataURL != nil {
				state.ResourceMetadataURL = options.ResourceMetadataURL.String()
			}
			_ = p.SaveDiscoveryState(state)
		}
	}

	metadata := discovered.AuthorizationServerMetadata
	resource, err := SelectResource(options.ServerURL, discovered.ResourceMetadata)
	if err != nil {
		return "", err
	}
	scope := options.Scope
	if scope == "" && discovered.ResourceMetadata != nil && len(discovered.ResourceMetadata.ScopesSupported) > 0 {
		scope = strings.Join(discovered.ResourceMetadata.ScopesSupported, " ")
	}
	if scope == "" {
		scope = provider.ClientMetadata().Scope
	}

	client, err := provider.ClientInformation()
	if err != nil {
		return "", err
	}
	if client == nil {
		if options.AuthorizationCode != "" {
			return "", errors.New("OAuth client information is missing during code exchange")
		}
		metadataDocumentSupported := metadata != nil && metadata.ClientIDMetadataDocumentSupported
		clientMetadataURL := ""
		if p, ok := provider.(oauthClientMetadataURLProvider); ok {
			clientMetadataURL = p.ClientMetadataURL()
		}
		if metadataDocumentSupported && clientMetadataURL != "" {
			parsed, parseErr := url.Parse(clientMetadataURL)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Path == "/" || parsed.Path == "" {
				return "", errors.New("Invalid OAuth client metadata URL")
			}
			client = &OAuthClientInformation{ClientID: clientMetadataURL}
			if p, ok := provider.(oauthSaveClientInformationProvider); ok {
				_ = p.SaveClientInformation(client)
			}
		} else {
			saver, ok := provider.(oauthSaveClientInformationProvider)
			if !ok {
				return "", errors.New("OAuth client information cannot be persisted")
			}
			registered, registerErr := RegisterClient(ctx, discovered.AuthorizationServerURL, RegisterClientOptions{
				Metadata:       metadata,
				ClientMetadata: provider.ClientMetadata(),
				Scope:          scope,
				Fetch:          options.Fetch,
			})
			if registerErr != nil {
				return "", registerErr
			}
			client = &registered
			if err := saver.SaveClientInformation(client); err != nil {
				return "", err
			}
		}
	}

	tokenOptions := TokenRequestOptions{
		Metadata:          metadata,
		ClientInformation: *client,
		Fetch:             options.Fetch,
	}
	if resource != nil {
		tokenOptions.Resource = *resource
	}
	if p, ok := provider.(oauthAddClientAuthenticationProvider); ok {
		tokenOptions.AddClientAuthentication = p.AddClientAuthentication
	}

	if options.AuthorizationCode != "" {
		if metadata != nil && (options.HasISS || metadata.AuthorizationResponseISSParameterSupported) {
			if !options.HasISS || options.ISS != metadata.Issuer {
				return "", &OAuthIssuerMismatchError{Expected: metadata.Issuer, Received: options.ISS, HasValue: options.HasISS}
			}
		}
		verifier, err := provider.CodeVerifier()
		if err != nil {
			return "", err
		}
		tokens, err := ExchangeAuthorizationCode(ctx, discovered.AuthorizationServerURL, ExchangeOptions{
			TokenRequestOptions: tokenOptions,
			Code:                options.AuthorizationCode,
			CodeVerifier:        verifier,
			RedirectURL:         provider.RedirectURL(),
		})
		if err != nil {
			return "", err
		}
		saved := withScope(tokens, scope)
		if err := provider.SaveTokens(&saved); err != nil {
			return "", err
		}
		return OAuthFlowAuthorized, nil
	}

	var existing *OAuthTokens
	if !options.SkipRefresh {
		existing, err = provider.Tokens()
		if err != nil {
			return "", err
		}
	}
	if existing != nil && existing.RefreshToken != "" {
		tokens, err := RefreshAuthorization(ctx, discovered.AuthorizationServerURL, RefreshOptions{
			TokenRequestOptions: tokenOptions,
			RefreshToken:        existing.RefreshToken,
		})
		if err != nil {
			var insecure *OAuthInsecureEndpointError
			if errors.As(err, &insecure) {
				return "", err
			}
			var oauthErr *OAuthError
			if errors.As(err, &oauthErr) && oauthErr.Code != "server_error" {
				return "", err
			}
		} else {
			saved := withScope(tokens, existing.Scope)
			if err := provider.SaveTokens(&saved); err != nil {
				return "", err
			}
			return OAuthFlowAuthorized, nil
		}
	}

	state := ""
	if p, ok := provider.(oauthStateProvider); ok {
		state, err = p.State()
		if err != nil {
			return "", err
		}
	}
	startOptions := StartAuthorizationOptions{
		Metadata:          metadata,
		ClientInformation: *client,
		RedirectURL:       provider.RedirectURL(),
		Scope:             scope,
		State:             state,
	}
	if resource != nil {
		startOptions.Resource = *resource
	}
	authorization, err := StartAuthorization(ctx, discovered.AuthorizationServerURL, startOptions)
	if err != nil {
		return "", err
	}
	if err := provider.SaveCodeVerifier(authorization.CodeVerifier); err != nil {
		return "", err
	}
	if err := provider.RedirectToAuthorization(authorization.AuthorizationURL); err != nil {
		return "", err
	}
	return OAuthFlowRedirect, nil
}

// McpOAuthState is persisted OAuth state for one MCP server.
type McpOAuthState struct {
	ServerURL         string                  `json:"serverUrl"`
	ClientInformation *OAuthClientInformation `json:"clientInformation,omitempty"`
	Tokens            *OAuthTokens            `json:"tokens,omitempty"`
	TokensExpireAt    *int64                  `json:"tokensExpireAt,omitempty"`
	CodeVerifier      string                  `json:"codeVerifier,omitempty"`
	OAuthState        string                  `json:"oauthState,omitempty"`
	Discovery         *OAuthDiscoveryState    `json:"discovery,omitempty"`
}

// McpOAuthStateStore persists OAuth state.
type McpOAuthStateStore interface {
	Load() (*McpOAuthState, error)
	Save(state McpOAuthState) error
}

// MemoryOAuthStateStore keeps OAuth state in memory.
type MemoryOAuthStateStore struct {
	mu    sync.Mutex
	value *McpOAuthState
}

// Load returns a copy of the stored state.
func (s *MemoryOAuthStateStore) Load() (*McpOAuthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value == nil {
		return nil, nil
	}
	copy := *s.value
	return &copy, nil
}

// Save stores a copy of the state.
func (s *MemoryOAuthStateStore) Save(state McpOAuthState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := state
	s.value = &copy
	return nil
}

// McpOAuthProviderOptions configures an McpOAuthProvider.
type McpOAuthProviderOptions struct {
	ServerURL      string
	RedirectURL    string
	ClientMetadata OAuthClientMetadata
	ClientID       string
	ClientSecret   string
	Store          McpOAuthStateStore
	OnRedirect     func(authorizationURL string) error
}

// McpOAuthProvider is the default stateful provider for one MCP server URL.
type McpOAuthProvider struct {
	mu               sync.Mutex
	serverURL        string
	redirectURL      string
	clientMetadata   OAuthClientMetadata
	configuredClient *OAuthClientInformation
	store            McpOAuthStateStore
	onRedirect       func(string) error
}

// NewMcpOAuthProvider builds an OAuth provider for one server.
func NewMcpOAuthProvider(options McpOAuthProviderOptions) *McpOAuthProvider {
	metadata := options.ClientMetadata
	if len(metadata.RedirectURIs) == 0 {
		metadata.RedirectURIs = []string{options.RedirectURL}
	}
	if len(metadata.GrantTypes) == 0 {
		metadata.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	if len(metadata.ResponseTypes) == 0 {
		metadata.ResponseTypes = []string{"code"}
	}
	if metadata.TokenEndpointAuthMethod == "" {
		if options.ClientSecret != "" {
			metadata.TokenEndpointAuthMethod = "client_secret_post"
		} else {
			metadata.TokenEndpointAuthMethod = "none"
		}
	}
	provider := &McpOAuthProvider{
		serverURL:      options.ServerURL,
		redirectURL:    options.RedirectURL,
		clientMetadata: metadata,
		store:          options.Store,
		onRedirect:     options.OnRedirect,
	}
	if provider.store == nil {
		provider.store = &MemoryOAuthStateStore{}
	}
	if options.ClientID != "" {
		client := &OAuthClientInformation{ClientID: options.ClientID}
		if options.ClientSecret != "" {
			client.ClientSecret = options.ClientSecret
		}
		provider.configuredClient = client
	}
	return provider
}

// RedirectURL returns the configured redirect URL.
func (p *McpOAuthProvider) RedirectURL() string { return p.redirectURL }

// ClientMetadata returns the configured client metadata.
func (p *McpOAuthProvider) ClientMetadata() OAuthClientMetadata { return p.clientMetadata }

// ClientMetadataURL is empty for the default provider.
func (p *McpOAuthProvider) ClientMetadataURL() string { return "" }

func (p *McpOAuthProvider) own(state *McpOAuthState) McpOAuthState {
	if state != nil && state.ServerURL == p.serverURL {
		return *state
	}
	return McpOAuthState{ServerURL: p.serverURL}
}

// ClientInformation returns configured or registered client information.
func (p *McpOAuthProvider) ClientInformation() (*OAuthClientInformation, error) {
	if p.configuredClient != nil {
		return p.configuredClient, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	return p.own(state).ClientInformation, nil
}

// SaveClientInformation persists registered client information.
func (p *McpOAuthProvider) SaveClientInformation(information *OAuthClientInformation) error {
	if p.configuredClient != nil {
		return nil
	}
	return p.update(func(state *McpOAuthState) { state.ClientInformation = information })
}

// Tokens returns the stored tokens.
func (p *McpOAuthProvider) Tokens() (*OAuthTokens, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	return p.own(state).Tokens, nil
}

// SaveTokens stores tokens and their expiry.
func (p *McpOAuthProvider) SaveTokens(tokens *OAuthTokens) error {
	return p.update(func(state *McpOAuthState) {
		state.Tokens = tokens
		if tokens != nil && tokens.ExpiresIn != nil {
			expiresAt := time.Now().UnixMilli() + int64(*tokens.ExpiresIn*1000)
			state.TokensExpireAt = &expiresAt
		} else {
			state.TokensExpireAt = nil
		}
	})
}

// RedirectToAuthorization hands the authorization URL to the application.
func (p *McpOAuthProvider) RedirectToAuthorization(authorizationURL string) error {
	if p.onRedirect == nil {
		return nil
	}
	return p.onRedirect(authorizationURL)
}

// SaveCodeVerifier stores the PKCE verifier.
func (p *McpOAuthProvider) SaveCodeVerifier(verifier string) error {
	return p.update(func(state *McpOAuthState) { state.CodeVerifier = verifier })
}

// CodeVerifier returns the stored PKCE verifier.
func (p *McpOAuthProvider) CodeVerifier() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.store.Load()
	if err != nil {
		return "", err
	}
	verifier := p.own(state).CodeVerifier
	if verifier == "" {
		return "", errors.New("No OAuth PKCE code verifier is stored")
	}
	return verifier, nil
}

// State returns the CSRF state, creating one if needed.
func (p *McpOAuthProvider) State() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.store.Load()
	if err != nil {
		return "", err
	}
	current := p.own(state)
	if current.OAuthState != "" {
		return current.OAuthState, nil
	}
	value, err := randomHex(32)
	if err != nil {
		return "", err
	}
	current.OAuthState = value
	if err := p.store.Save(current); err != nil {
		return "", err
	}
	return value, nil
}

// InvalidateCredentials drops stored credentials of the given kind.
func (p *McpOAuthProvider) InvalidateCredentials(kind string) error {
	return p.update(func(state *McpOAuthState) {
		if kind == "all" || kind == "client" {
			state.ClientInformation = nil
		}
		if kind == "all" || kind == "tokens" {
			state.Tokens = nil
			state.TokensExpireAt = nil
		}
		if kind == "all" || kind == "verifier" {
			state.CodeVerifier = ""
		}
		if kind == "all" || kind == "discovery" {
			state.Discovery = nil
		}
		if kind == "all" {
			state.OAuthState = ""
		}
	})
}

// SaveDiscoveryState stores cached discovery state.
func (p *McpOAuthProvider) SaveDiscoveryState(discovery OAuthDiscoveryState) error {
	return p.update(func(state *McpOAuthState) { state.Discovery = &discovery })
}

// DiscoveryState returns cached discovery state.
func (p *McpOAuthProvider) DiscoveryState() (*OAuthDiscoveryState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	return p.own(state).Discovery, nil
}

func (p *McpOAuthProvider) update(apply func(state *McpOAuthState)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.store.Load()
	if err != nil {
		return err
	}
	current := p.own(state)
	apply(&current)
	return p.store.Save(current)
}

func randomHex(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// adaptedOAuthProvider adapts an OAuthClientProvider to the transport's
// AuthProvider, coalescing concurrent 401 refreshes.
type adaptedOAuthProvider struct {
	provider OAuthClientProvider
	mu       sync.Mutex
	inFlight chan struct{}
	err      error
}

// AdaptOAuthProvider turns an OAuth provider into a transport auth provider.
func AdaptOAuthProvider(provider OAuthClientProvider) AuthProvider {
	return &adaptedOAuthProvider{provider: provider}
}

// Token returns the current access token.
func (a *adaptedOAuthProvider) Token(ctx context.Context) (string, error) {
	tokens, err := a.provider.Tokens()
	if err != nil {
		return "", err
	}
	if tokens == nil {
		return "", nil
	}
	return tokens.AccessToken, nil
}

// OnUnauthorized refreshes, or requests interactive authorization.
func (a *adaptedOAuthProvider) OnUnauthorized(ctx context.Context, unauthorized UnauthorizedContext) error {
	challenge := ParseWwwAuthenticate(unauthorized.Response.Header.Get("Www-Authenticate"))
	insufficientScope := challenge.Error == "insufficient_scope"
	if !insufficientScope && unauthorized.HasToken {
		a.mu.Lock()
		busy := a.inFlight != nil
		a.mu.Unlock()
		if !busy {
			current, err := a.provider.Tokens()
			if err != nil {
				return err
			}
			if current != nil && current.AccessToken != unauthorized.Token {
				return nil
			}
		}
	}
	a.mu.Lock()
	if a.inFlight == nil {
		done := make(chan struct{})
		a.inFlight = done
		a.mu.Unlock()
		err := a.run(ctx, unauthorized, challenge, insufficientScope)
		a.mu.Lock()
		a.err = err
		a.inFlight = nil
		a.mu.Unlock()
		close(done)
	} else {
		done := a.inFlight
		a.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.mu.Lock()
	err := a.err
	a.mu.Unlock()
	return err
}

func (a *adaptedOAuthProvider) run(ctx context.Context, unauthorized UnauthorizedContext, challenge OAuthChallenge, insufficientScope bool) error {
	scope := challenge.Scope
	if insufficientScope {
		var granted string
		if tokens, err := a.provider.Tokens(); err == nil && tokens != nil {
			granted = tokens.Scope
		}
		scope = StepUpScope(granted, challenge.Scope)
	}
	options := OAuthFlowOptions{
		ServerURL:           unauthorized.ServerURL.String(),
		ResourceMetadataURL: challenge.ResourceMetadataURL,
		Scope:               scope,
		Fetch:               unauthorized.Fetch,
		SkipRefresh:         insufficientScope,
	}
	result, err := AuthorizeMcp(ctx, a.provider, options)
	if err != nil {
		return err
	}
	if result == OAuthFlowRedirect {
		return &McpOAuthAuthorizationRequiredError{}
	}
	return nil
}

var _ AuthProvider = (*adaptedOAuthProvider)(nil)
var _ UnauthorizedHandler = (*adaptedOAuthProvider)(nil)
