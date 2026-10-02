# Pi 1.0 credential and OAuth flows

Port the actual source changes under auth, including OpenAI ChatGPT auth,
Anthropic manual code/copy flow and WIF, shared OAuth callback server/PKCE/page
helpers, OAuth metadata, token refresh and provider-specific available-model
filtering. Keep caller-owned stores and per-instance state. Do not import UI/CLI
prompts or require an ambient home-directory store. Browser launch/callback input
are optional injected functions suitable for an embedded SDK.

PKCE uses cryptographic random bytes and S256. Preserve the existing native Go
API in package `auth/oauth`: `GeneratePKCE() (PKCE, error)`, where `PKCE` exposes
`Verifier` and `Challenge` string fields. Do not change its return arity: existing
provider flows, SDK callers and frozen historical tests depend on this signature.
Add
`ParseCallback(rawURL, expectedState, expectedIssuer string) (code string, err error)`.
Reject state mismatch, provider error, missing/duplicate code/state, and issuer
mismatch when expectedIssuer is set. Accept a valid code exactly once per flow.
The shared server binds loopback, handles browser-open failure without losing the
manual flow, closes on cancellation, and rejects stale callbacks. Preserve source
credential keys/migration and never persist provider-event or request secrets.

Independent judges cover PKCE/state/issuer boundary. Candidate tests must cover
all changed provider flows using fake token/metadata servers, refresh coalescing,
cancellation, denial callbacks, WIF exchange and credential-store round trips.
Freeze secrets as fake literal tokens in tests only. Existing auth judges remain.
