// This file is a Go port of packages/ai/src/api/cloudflare-ai-binding.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// AI Gateway transport over the Workers AI binding.
//
// Pi's Cloudflare AI Gateway support speaks HTTPS
// (`gateway.ai.cloudflare.com/v1/{account}/{gateway}/{provider}/...`, see
// api/cloudflare.go), which needs a Cloudflare API token even when the caller is
// a Worker in the gateway's own account.
//
// A Worker avoids that token by talking to the gateway through the AI binding's
// `fetch` passthrough (`env.AI.fetch()`), which serves the gateway's provider
// passthrough at
// `https://workers-binding.ai/ai-gateway/gateways/{gateway}/{provider}/{endpoint...}`
// — the same shape as the HTTPS URL, minus the account id (the binding channel
// carries identity). Binding calls are pre-authenticated in-account and return
// the provider's native wire format as a regular (streaming) response, so API
// implementations behave identically over either transport.
//
// A model whose `baseUrl` names that route therefore needs no translation: point
// it there and pass {@link CreateAiBindingFetch} as the request `fetch`.
package api

import (
	"errors"

	"github.com/minifish-org/pith/packages/ai/types"
)

// AiBindingFetch forwards one transport-neutral request to the Workers AI
// binding and returns the provider's native response.
type AiBindingFetch func(input *types.HTTPRequest) (*types.HTTPResponse, error)

// AiBinding is the Workers AI binding (`env.AI`), described structurally so this
// module does not depend on @cloudflare/workers-types.
//
// Fetch is optional only because the published Ai type does not declare it yet;
// every real binding has it at runtime. AiGatewayLogId pins the value to the AI
// binding: it is the one member unique to Ai, so without it this interface would
// also accept an AiGateway or any hand-rolled `{fetch}` object, which is exactly
// the mistake the runtime check reports late. A nil AiGatewayLogId models the
// upstream `string | null`.
type AiBinding struct {
	AiGatewayLogId *string
	Fetch          AiBindingFetch
}

// ErrAiBindingFetchMissing reports a binding without a fetch passthrough.
var ErrAiBindingFetchMissing = errors.New("createAiBindingFetch: the AI binding does not expose fetch()")

// CLOUDFLARE_GATEWAY_BINDING_AUTH_SENTINEL is the placeholder value for auth
// headers on binding-routed requests.
//
// API implementations require an API key or a recognized auth header
// (`authorization`, `x-api-key`, `cf-aig-authorization`) before dispatch;
// binding calls are pre-authenticated, so pass
// `cf-aig-authorization: Bearer <sentinel>` to satisfy the check. The gateway
// ignores (and strips) `cf-aig-authorization` on binding-routed requests. Pair it
// with `Authorization: null` / `x-api-key: null` so the SDKs' placeholder auth
// headers never reach the gateway, which would treat a request-supplied auth
// header as a BYOK provider key that overrides its stored keys — the same as it
// would over HTTPS.
const CloudflareGatewayBindingAuthSentinel = "cloudflare-gateway-binding"

// CreateAiBindingFetch creates a fetch backed by the AI binding, for models whose
// baseUrl already names a route the binding serves — including the gateway's
// provider passthrough,
// `https://workers-binding.ai/ai-gateway/gateways/{gateway}/{provider}/...`.
// Requests pass through untouched.
//
// The presence of the binding `fetch` is checked here — early, rather than as a
// confusing failure on the first inference request. The binding fetch is bound
// eagerly so that clearing the struct field later does not change the returned
// closure.
func CreateAiBindingFetch(binding AiBinding) (types.FetchFunction, error) {
	if binding.Fetch == nil {
		return nil, ErrAiBindingFetchMissing
	}
	bindingFetch := binding.Fetch
	return func(input *types.HTTPRequest) (*types.HTTPResponse, error) {
		return bindingFetch(input)
	}, nil
}
