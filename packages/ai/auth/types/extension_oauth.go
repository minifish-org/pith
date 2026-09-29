// This file is a Go port of packages/ai/src/compat/extension-oauth-types.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// This is the legacy extension OAuth callback surface retained only for
// coding-agent extension compatibility. `OAuthCredentials` is the real token
// DTO declared in types.go and re-exported here by the same package; the
// remaining declarations are the callback and prompt shapes that extensions
// implemented against the old global API.
package authtypes

import "context"

// OAuthPrompt is the legacy extension OAuth text prompt.
//
// Ports `OAuthPrompt` from packages/ai/src/compat/extension-oauth-types.ts.
type OAuthPrompt struct {
	Message string
	// Placeholder is optional hint text for the prompt input.
	Placeholder *string
	// AllowEmpty permits an empty submission when true.
	AllowEmpty bool
}

// OAuthAuthInfo is the legacy extension OAuth authorization link.
//
// Ports `OAuthAuthInfo` from packages/ai/src/compat/extension-oauth-types.ts.
type OAuthAuthInfo struct {
	URL string
	// Instructions is optional text shown alongside the link.
	Instructions *string
}

// OAuthDeviceCodeInfo is the legacy extension OAuth device-code notification.
//
// Ports `OAuthDeviceCodeInfo` from
// packages/ai/src/compat/extension-oauth-types.ts.
type OAuthDeviceCodeInfo struct {
	UserCode         string
	VerificationURI  string
	IntervalSeconds  *float64
	ExpiresInSeconds *float64
}

// OAuthSelectOption is one option of a legacy extension OAuth select prompt.
//
// Ports `OAuthSelectOption` from
// packages/ai/src/compat/extension-oauth-types.ts.
type OAuthSelectOption struct {
	ID    string
	Label string
}

// OAuthSelectPrompt is a legacy extension OAuth selection prompt.
//
// Ports `OAuthSelectPrompt` from
// packages/ai/src/compat/extension-oauth-types.ts.
type OAuthSelectPrompt struct {
	Message string
	Options []OAuthSelectOption
}

// OAuthLoginCallbacks is the legacy extension OAuth callback surface. Optional
// upstream callbacks are function fields so an absent callback stays
// distinguishable from a present no-op; the Signal context replaces the
// upstream AbortSignal.
//
// Ports `OAuthLoginCallbacks` from
// packages/ai/src/compat/extension-oauth-types.ts.
type OAuthLoginCallbacks struct {
	// OnAuth receives the authorization link.
	OnAuth func(info OAuthAuthInfo)
	// OnDeviceCode receives the device-code notification.
	OnDeviceCode func(info OAuthDeviceCodeInfo)
	// OnPrompt asks the user for text and returns the entered value.
	OnPrompt func(ctx context.Context, prompt OAuthPrompt) (string, error)
	// OnProgress is the optional progress notification.
	OnProgress func(message string)
	// OnManualCodeInput is the optional manual authorization-code entry.
	OnManualCodeInput func(ctx context.Context) (string, error)
	// OnSelect is the optional selection prompt. ok is false when the user
	// dismisses the prompt, matching the upstream `string | undefined` result.
	OnSelect func(ctx context.Context, prompt OAuthSelectPrompt) (string, bool, error)
	// Signal cancels the flow.
	Signal context.Context
}
