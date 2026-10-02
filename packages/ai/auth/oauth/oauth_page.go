// This file is a Go port of packages/ai/src/utils/oauth-page.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. The upstream page helpers
// moved from packages/ai/src/auth/oauth/oauth-page.ts to
// packages/ai/src/utils/oauth-page.ts; this file keeps the historical
// auth/oauth package surface as thin wrappers over the canonical
// packages/ai/utils implementation.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import "github.com/minifish-org/pith/packages/ai/utils"

// OAuthSuccessHTML renders the browser success page.
//
// Ports `oauthSuccessHtml` from packages/ai/src/utils/oauth-page.ts.
func OAuthSuccessHTML(message string) string {
	return utils.OAuthSuccessHTML(message)
}

// OAuthErrorHTML renders the browser error page with an optional details block.
//
// Ports `oauthErrorHtml` from packages/ai/src/utils/oauth-page.ts.
func OAuthErrorHTML(message string, details *string) string {
	return utils.OAuthErrorHTML(message, details)
}
