// This file is a Go port of packages/ai/src/auth/types.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package is named authtypes so importing code can alias the two `types`
// packages of the module unambiguously. The directory still maps one-to-one to
// the upstream `packages/ai/src/auth/types.ts` source.
package authtypes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/minifish-org/pith/packages/ai/types"
)

// ModelAuth is the request auth for a single model request. If a value cannot
// be expressed as apiKey, headers or baseUrl, it is provider config, not auth.
type ModelAuth struct {
	APIKey  *string               `json:"apiKey,omitempty"`
	Headers types.ProviderHeaders `json:"headers,omitempty"`
	BaseURL *string               `json:"baseUrl,omitempty"`
}

// Credential type discriminators.
const (
	CredentialTypeAPIKey = "api_key"
	CredentialTypeOAuth  = "oauth"
)

// ApiKeyCredential is a stored api-key credential. Env holds provider-scoped
// environment/config values such as Cloudflare account/gateway ids.
type ApiKeyCredential struct {
	Type string            `json:"type"`
	Key  *string           `json:"key,omitempty"`
	Env  types.ProviderEnv `json:"env,omitempty"`
}

// NewApiKeyCredential builds an api_key credential.
func NewApiKeyCredential(key string) *ApiKeyCredential {
	return &ApiKeyCredential{Type: CredentialTypeAPIKey, Key: &key}
}

// CredentialType returns the credential discriminator.
func (c *ApiKeyCredential) CredentialType() string { return CredentialTypeAPIKey }

// OAuthCredentials is the token data returned by extension compatibility
// flows: refresh, access, expires plus arbitrary extension fields.
type OAuthCredentials struct {
	Refresh string
	Access  string
	Expires float64
	// Extra preserves extension fields such as enterpriseUrl, accountId,
	// availableModelIds and scope. It is stored verbatim.
	Extra map[string]json.RawMessage
}

// UnmarshalJSON decodes the known token fields and captures the rest.
func (c *OAuthCredentials) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = OAuthCredentials{}
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return err
	}
	if raw, ok := object["refresh"]; ok {
		if err := json.Unmarshal(raw, &c.Refresh); err != nil {
			return err
		}
	}
	if raw, ok := object["access"]; ok {
		if err := json.Unmarshal(raw, &c.Access); err != nil {
			return err
		}
	}
	if raw, ok := object["expires"]; ok {
		if err := json.Unmarshal(raw, &c.Expires); err != nil {
			return err
		}
	}
	delete(object, "refresh")
	delete(object, "access")
	delete(object, "expires")
	if len(object) == 0 {
		c.Extra = nil
	} else {
		c.Extra = object
	}
	return nil
}

// MarshalJSON emits refresh, access, expires and every extension field.
func (c OAuthCredentials) MarshalJSON() ([]byte, error) {
	object := make(map[string]json.RawMessage, len(c.Extra)+3)
	refresh, err := json.Marshal(c.Refresh)
	if err != nil {
		return nil, err
	}
	access, err := json.Marshal(c.Access)
	if err != nil {
		return nil, err
	}
	expires, err := json.Marshal(c.Expires)
	if err != nil {
		return nil, err
	}
	object["refresh"] = refresh
	object["access"] = access
	object["expires"] = expires
	for key, value := range c.Extra {
		if _, exists := object[key]; !exists {
			object[key] = value
		}
	}
	return json.Marshal(object)
}

// OAuthCredential is the stored canonical OAuth credential, tagged with
// type "oauth".
type OAuthCredential struct {
	OAuthCredentials
	Type string
}

// NewOAuthCredential builds an oauth credential from token data.
func NewOAuthCredential(refresh, access string, expires float64) *OAuthCredential {
	return &OAuthCredential{
		OAuthCredentials: OAuthCredentials{Refresh: refresh, Access: access, Expires: expires},
		Type:             CredentialTypeOAuth,
	}
}

// UnmarshalJSON decodes the credential object, routing the token fields to the
// embedded OAuthCredentials and preserving every other field.
func (c *OAuthCredential) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = OAuthCredential{}
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return err
	}
	if raw, ok := object["type"]; ok {
		if err := json.Unmarshal(raw, &c.Type); err != nil {
			return err
		}
	}
	delete(object, "type")
	remainder, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return c.OAuthCredentials.UnmarshalJSON(remainder)
}

// MarshalJSON emits the credential object including type "oauth".
func (c OAuthCredential) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(c.OAuthCredentials)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(base, &object); err != nil {
		return nil, err
	}
	credentialType := c.Type
	if credentialType == "" {
		credentialType = CredentialTypeOAuth
	}
	encodedType, err := json.Marshal(credentialType)
	if err != nil {
		return nil, err
	}
	object["type"] = encodedType
	return json.Marshal(object)
}

// CredentialType returns the credential discriminator.
func (c *OAuthCredential) CredentialType() string { return CredentialTypeOAuth }

// SetExtra stores an extension field on the credential.
func (c *OAuthCredential) SetExtra(key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if c.Extra == nil {
		c.Extra = map[string]json.RawMessage{}
	}
	c.Extra[key] = json.RawMessage(encoded)
	return nil
}

// ExtraRaw returns an extension field verbatim.
func (c *OAuthCredential) ExtraRaw(key string) (json.RawMessage, bool) {
	if c == nil || c.Extra == nil {
		return nil, false
	}
	value, ok := c.Extra[key]
	return value, ok
}

// ExtraString reads a string extension field.
func (c *OAuthCredential) ExtraString(key string) (string, bool) {
	raw, ok := c.ExtraRaw(key)
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// ExtraStringSlice reads a string array extension field.
func (c *OAuthCredential) ExtraStringSlice(key string) ([]string, bool) {
	raw, ok := c.ExtraRaw(key)
	if !ok {
		return nil, false
	}
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// Credential is one type-tagged credential per provider.
type Credential interface {
	CredentialType() string
}

// CredentialInfo is non-secret credential metadata for account/status
// enumeration.
type CredentialInfo struct {
	ProviderID string `json:"providerId"`
	Type       string `json:"type"`
}

// AuthOperationOptions carries optional cancellation for public auth and
// credential operations. The context passed alongside an operation is
// authoritative; Signal is retained for API parity with upstream.
type AuthOperationOptions struct {
	Signal context.Context
}

// CredentialStore is the app-owned credential storage keyed by Provider id,
// one credential per provider. Modify is the only write path.
type CredentialStore interface {
	// Read returns the stored credential, possibly expired, or nil for a
	// missing entry.
	Read(ctx context.Context, providerID string, options *AuthOperationOptions) (Credential, error)

	// List returns stored credential metadata without resolving or exposing
	// secrets.
	List(ctx context.Context, options *AuthOperationOptions) ([]CredentialInfo, error)

	// Modify serializes a read-modify-write per provider. fn sees the current
	// credential and returns the new credential, or nil to leave the entry
	// unchanged. It resolves with the post-write credential.
	Modify(ctx context.Context, providerID string, fn func(current Credential) (Credential, error), options *AuthOperationOptions) (Credential, error)

	// Delete removes a credential (logout), serialized against Modify.
	Delete(ctx context.Context, providerID string, options *AuthOperationOptions) error
}

// AuthContext is the environment access used for auth resolution. It is
// injectable for tests and browsers.
type AuthContext interface {
	Env(name string) (*string, error)
	// FileExists reports whether a file exists. It supports a leading `~`.
	FileExists(path string) (bool, error)
}

// AuthResult is the result of resolving auth for a model.
type AuthResult struct {
	Auth ModelAuth
	// Env holds provider-scoped environment/config values resolved from
	// credentials and ambient context.
	Env types.ProviderEnv
	// Source is a human-readable label for status UI.
	Source *string
}

// AuthCheck is the result of a side-effect-free availability check.
type AuthCheck struct {
	Source *string
	Type   AuthType
}

// AuthType is an auth method discriminator.
type AuthType string

// Auth type values.
const (
	AuthTypeAPIKey AuthType = CredentialTypeAPIKey
	AuthTypeOAuth  AuthType = CredentialTypeOAuth
)

// AuthPrompt is the prompt shown to the user during login. The context passed
// to Prompt lets a flow cancel a pending prompt when an out-of-band event
// resolves the step.
type AuthPrompt struct {
	Type string
	// Message is the prompt text.
	Message string
	// Placeholder is optional prompt placeholder text.
	Placeholder *string
	// Options is populated for Type "select".
	Options []AuthPromptOption
}

// AuthPromptOption is one selectable option of a select prompt.
type AuthPromptOption struct {
	ID          string
	Label       string
	Description *string
}

// AuthInfoLink is an optional link attached to an info event.
type AuthInfoLink struct {
	URL   string
	Label *string
}

// AuthEvent is a login progress notification. Fields are grouped by event type.
type AuthEvent struct {
	Type string
	// info.
	Message *string
	Links   []AuthInfoLink
	// auth_url.
	URL          *string
	Instructions *string
	// device_code.
	UserCode         *string
	VerificationURI  *string
	IntervalSeconds  *float64
	ExpiresInSeconds *float64
}

// Auth event types.
const (
	AuthEventInfo       = "info"
	AuthEventAuthURL    = "auth_url"
	AuthEventDeviceCode = "device_code"
	AuthEventProgress   = "progress"
)

// Auth prompt types.
const (
	AuthPromptText       = "text"
	AuthPromptSecret     = "secret"
	AuthPromptSelect     = "select"
	AuthPromptManualCode = "manual_code"
)

// AuthInteraction is the login interaction callback used by both api-key and
// OAuth flows.
type AuthInteraction interface {
	// Prompt returns the entered/selected string (select returns the option id).
	Prompt(ctx context.Context, prompt AuthPrompt) (string, error)
	// Notify emits a progress/info event.
	Notify(event AuthEvent)
}

// ProviderAuthInteraction is the normalized interaction passed to provider
// login implementations: an AuthInteraction plus the flow-wide signal.
type ProviderAuthInteraction interface {
	AuthInteraction
	// Signal returns the live login-flow cancellation context.
	Signal() context.Context
}

// Interaction is a concrete AuthInteraction/ProviderAuthInteraction backed by
// callbacks. It is the injectable implementation used by CLI and tests.
type Interaction struct {
	SignalCtx  context.Context
	PromptFunc func(ctx context.Context, prompt AuthPrompt) (string, error)
	NotifyFunc func(event AuthEvent)
}

// NewInteraction builds an interaction. A nil signal becomes
// context.Background().
func NewInteraction(signal context.Context, prompt func(ctx context.Context, prompt AuthPrompt) (string, error), notify func(event AuthEvent)) *Interaction {
	return &Interaction{SignalCtx: signal, PromptFunc: prompt, NotifyFunc: notify}
}

// Prompt dispatches to the configured prompt callback.
func (i *Interaction) Prompt(ctx context.Context, prompt AuthPrompt) (string, error) {
	if i == nil || i.PromptFunc == nil {
		return "", fmt.Errorf("auth: interaction has no prompt handler")
	}
	return i.PromptFunc(ctx, prompt)
}

// Notify dispatches to the configured notification callback.
func (i *Interaction) Notify(event AuthEvent) {
	if i == nil || i.NotifyFunc == nil {
		return
	}
	i.NotifyFunc(event)
}

// Signal returns the flow-wide cancellation context.
func (i *Interaction) Signal() context.Context {
	if i == nil || i.SignalCtx == nil {
		return context.Background()
	}
	return i.SignalCtx
}

// ApiKeyAuth is api-key auth: a stored key/provider env plus ambient sources.
type ApiKeyAuth struct {
	// Name is the display name, e.g. "Anthropic API key".
	Name string
	// Login performs interactive setup. A nil Login means ambient-only.
	Login func(ctx context.Context, interaction ProviderAuthInteraction) (*ApiKeyCredential, error)
	// Check is an optional side-effect-free availability check.
	Check func(ctx context.Context, input ApiKeyCheckInput) (*AuthCheck, error)
	// Resolve resolves auth from the stored credential and/or ambient sources.
	Resolve func(ctx context.Context, input ApiKeyResolveInput) (*AuthResult, error)
}

// ApiKeyCheckInput is the input to ApiKeyAuth.Check.
type ApiKeyCheckInput struct {
	Ctx        AuthContext
	Credential *ApiKeyCredential
	Signal     context.Context
}

// ApiKeyResolveInput is the input to ApiKeyAuth.Resolve.
type ApiKeyResolveInput struct {
	Ctx        AuthContext
	Credential *ApiKeyCredential
	Signal     context.Context
}

// LoginOptions is app-supplied context for an OAuth login. It is optional;
// flows that do not need installation state ignore it.
type LoginOptions struct {
	// GetDeviceID returns the stable ID of this app installation. It is called
	// only by login flows that need it, so apps can create the ID on first use
	// and must return the same ID on every later call.
	GetDeviceID func() string
}

// OAuthAuth is OAuth auth. The Refresh/ToAuth split lets callers own the
// locked refresh pattern.
type OAuthAuth struct {
	// Name is the display name, e.g. "Anthropic (Claude Pro/Max)".
	Name string
	// IsSubscription reports whether access is backed by a provider
	// subscription.
	IsSubscription bool
	// LoginLabel is the selector label for the OAuth login option.
	LoginLabel string
	// Login runs the interactive login.
	Login func(ctx context.Context, interaction ProviderAuthInteraction) (*OAuthCredential, error)
	// LoginWithOptions is an optional login variant that also receives
	// app-supplied installation state. When set, callers may prefer it over
	// Login.
	LoginWithOptions func(ctx context.Context, interaction ProviderAuthInteraction, options *LoginOptions) (*OAuthCredential, error)
	// Refresh exchanges the refresh token. Network call; returns an error on
	// failure. Callers run it under the store lock.
	Refresh func(ctx context.Context, credential *OAuthCredential) (*OAuthCredential, error)
	// ToAuth derives request auth from a valid credential. It is
	// side-effect-free.
	ToAuth func(ctx context.Context, credential *OAuthCredential) (ModelAuth, error)
}

// ProviderAuth is the auth configuration of a provider. At least one of
// APIKey/OAuth must be present.
type ProviderAuth struct {
	APIKey *ApiKeyAuth
	OAuth  *OAuthAuth
}

// SortedExtraKeys returns the extension field names in stable order. It exists
// so callers can enumerate preserved unknown fields deterministically.
func (c *OAuthCredential) SortedExtraKeys() []string {
	if c == nil || len(c.Extra) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.Extra))
	for key := range c.Extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
