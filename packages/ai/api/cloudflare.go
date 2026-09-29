// This file is a Go port of packages/ai/src/api/cloudflare.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

// CloudflareWorkersAIBaseURL is the Workers AI direct endpoint. The
// {CLOUDFLARE_ACCOUNT_ID} placeholder is filled in by catalog data.
const CloudflareWorkersAIBaseURL = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"

// CloudflareAIGatewayCompatBaseURL is the AI Gateway Unified API endpoint.
// https://developers.cloudflare.com/ai-gateway/usage/unified-api/
const CloudflareAIGatewayCompatBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat"

// CloudflareAIGatewayOpenAIBaseURL is the AI Gateway OpenAI passthrough. It is
// used until /compat supports /v1/responses.
const CloudflareAIGatewayOpenAIBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai"

// CloudflareAIGatewayAnthropicBaseURL is the AI Gateway Anthropic passthrough.
const CloudflareAIGatewayAnthropicBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic"
