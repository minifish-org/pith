// This file is a Go port of packages/ai/src/legacy-api-aliases.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The deprecated global pi-ai API aliases. Each alias points at the same lazy
// API module the modern entry point uses, so an existing caller can switch from
// `streamAnthropic(...)` to `anthropicMessagesApi().stream` without a behavior
// change. Go exposes the same function values at package scope.
package ai

import (
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// Deprecated: use api.AnthropicMessagesApi().Stream.
var StreamAnthropic types.StreamFunction = api.AnthropicMessagesApi().Stream

// Deprecated: use api.AnthropicMessagesApi().StreamSimple.
var StreamSimpleAnthropic = api.AnthropicMessagesApi().StreamSimple

// Deprecated: use api.AzureOpenAIResponsesApi().Stream.
var StreamAzureOpenAIResponses types.StreamFunction = api.AzureOpenAIResponsesApi().Stream

// Deprecated: use api.AzureOpenAIResponsesApi().StreamSimple.
var StreamSimpleAzureOpenAIResponses = api.AzureOpenAIResponsesApi().StreamSimple

// Deprecated: use api.GoogleGenerativeAIApi().Stream.
var StreamGoogle types.StreamFunction = api.GoogleGenerativeAIApi().Stream

// Deprecated: use api.GoogleGenerativeAIApi().StreamSimple.
var StreamSimpleGoogle = api.GoogleGenerativeAIApi().StreamSimple

// Deprecated: use api.GoogleVertexApi().Stream.
var StreamGoogleVertex types.StreamFunction = api.GoogleVertexApi().Stream

// Deprecated: use api.GoogleVertexApi().StreamSimple.
var StreamSimpleGoogleVertex = api.GoogleVertexApi().StreamSimple

// Deprecated: use api.MistralConversationsApi().Stream.
var StreamMistral types.StreamFunction = api.MistralConversationsApi().Stream

// Deprecated: use api.MistralConversationsApi().StreamSimple.
var StreamSimpleMistral = api.MistralConversationsApi().StreamSimple

// Deprecated: use api.OpenAICodexResponsesApi().Stream.
var StreamOpenAICodexResponses types.StreamFunction = api.OpenAICodexResponsesApi().Stream

// Deprecated: use api.OpenAICodexResponsesApi().StreamSimple.
var StreamSimpleOpenAICodexResponses = api.OpenAICodexResponsesApi().StreamSimple

// Deprecated: use api.OpenAICompletionsApi().Stream.
var StreamOpenAICompletions types.StreamFunction = api.OpenAICompletionsApi().Stream

// Deprecated: use api.OpenAICompletionsApi().StreamSimple.
var StreamSimpleOpenAICompletions = api.OpenAICompletionsApi().StreamSimple

// Deprecated: use api.OpenAIResponsesApi().Stream.
var StreamOpenAIResponses types.StreamFunction = api.OpenAIResponsesApi().Stream

// Deprecated: use api.OpenAIResponsesApi().StreamSimple.
var StreamSimpleOpenAIResponses = api.OpenAIResponsesApi().StreamSimple
