// This file is a Go port of packages/ai/src/oauth.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// oauth.ts is a type-only compatibility entry point for coding-agent extension
// OAuth declarations. Go has no type-level re-export, so the SDK entry point
// exposes aliases to the real declarations in packages/ai/auth/types.
package sdk

import authtypes "github.com/minifish-org/pith/packages/ai/auth/types"

// OAuthAuthInfo is the legacy extension OAuth authorization link.
type OAuthAuthInfo = authtypes.OAuthAuthInfo

// OAuthCredentials is the extension OAuth token DTO.
type OAuthCredentials = authtypes.OAuthCredentials

// OAuthDeviceCodeInfo is the legacy extension OAuth device-code notification.
type OAuthDeviceCodeInfo = authtypes.OAuthDeviceCodeInfo

// OAuthLoginCallbacks is the legacy extension OAuth callback surface.
type OAuthLoginCallbacks = authtypes.OAuthLoginCallbacks

// OAuthPrompt is the legacy extension OAuth text prompt.
type OAuthPrompt = authtypes.OAuthPrompt

// OAuthSelectOption is one option of a legacy extension OAuth select prompt.
type OAuthSelectOption = authtypes.OAuthSelectOption

// OAuthSelectPrompt is a legacy extension OAuth selection prompt.
type OAuthSelectPrompt = authtypes.OAuthSelectPrompt
