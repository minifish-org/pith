// This file is a Go port of packages/ai/src/utils/overflow.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Go's regexp (RE2) has no non-capturing groups, so upstream's `(?:...)` groups
// are written as ordinary groups; the matching behavior is identical.
package utils

import (
	"regexp"

	"github.com/minifish-org/pith/packages/ai/types"
)

// overflowPatterns detects context overflow errors from different providers.
var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt (is )?too long`),                                                               // Anthropic and z.ai token overflow
	regexp.MustCompile(`(?i)prompt exceeds max length`),                                                           // z.ai CN endpoint token overflow
	regexp.MustCompile(`(?i)request_too_large`),                                                                   // Anthropic request byte-size overflow (HTTP 413)
	regexp.MustCompile(`(?i)input is too long for requested model`),                                               // Amazon Bedrock
	regexp.MustCompile(`(?i)exceeds the context window`),                                                          // OpenAI (Completions & Responses API)
	regexp.MustCompile(`(?i)exceeds (the )?(model'?s )?maximum context length( of [\d,]+ tokens?|\s*\([\d,]+\))`), // OpenAI-compatible proxies (LiteLLM)
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),                                              // Google (Gemini)
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),                                                        // xAI (Grok)
	regexp.MustCompile(`(?i)reduce the length of the messages`),                                                   // Groq
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),                                                // OpenRouter (most backends)
	regexp.MustCompile(`(?i)exceeds (the )?maximum allowed input length of [\d,]+ tokens?`),                       // OpenRouter/Poolside
	regexp.MustCompile(`(?i)input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)`),      // Together AI
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),                                                            // GitHub Copilot
	regexp.MustCompile(`(?i)exceeds the available context size`),                                                  // llama.cpp server
	regexp.MustCompile(`(?i)greater than the context length`),                                                     // LM Studio
	regexp.MustCompile(`(?i)context window exceeds limit`),                                                        // MiniMax
	regexp.MustCompile(`(?i)exceeded model token limit`),                                                          // Kimi For Coding
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),                                 // Mistral
	regexp.MustCompile(`(?i)prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?`),        // DS4 server
	regexp.MustCompile(`(?i)model_context_window_exceeded`),                                                       // z.ai non-standard finish_reason surfaced as error text
	regexp.MustCompile(`(?i)prompt too long; exceeded (max )?context length`),                                     // Ollama explicit overflow error
	regexp.MustCompile(`(?i)range of input length should be`),                                                     // DashScope / Qwen Token Plan
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),                                                       // Generic fallback
	regexp.MustCompile(`(?i)too many tokens`),                                                                     // Generic fallback
	regexp.MustCompile(`(?i)token limit exceeded`),                                                                // Generic fallback
}

var cerebrasBodylessOverflowPattern = regexp.MustCompile(`(?i)^4(00|13)\s*(status code)?\s*\(no body\)`)

// nonOverflowPatterns indicate non-overflow errors (e.g. rate limiting, server
// errors). Error messages matching any of these are excluded from overflow
// detection even if they also match an overflow pattern.
var nonOverflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(Throttling error|Service unavailable):`), // AWS Bedrock non-overflow errors
	regexp.MustCompile(`(?i)rate limit`),                               // Generic rate limiting
	regexp.MustCompile(`(?i)too many requests`),                        // Generic HTTP 429 style
}

// IsContextOverflow reports whether an assistant message represents a context
// overflow error. It handles error-based overflow, silent overflow when usage
// exceeds the context window, and the length-stop overflow some providers return
// when they truncate oversized input.
func IsContextOverflow(message types.AssistantMessage, contextWindow *float64) bool {
	// Case 1: error message patterns.
	if message.StopReason == types.StopReasonError && message.ErrorMessage != nil {
		errorMessage := *message.ErrorMessage
		if !matchesAny(nonOverflowPatterns, errorMessage) {
			if matchesAny(overflowPatterns, errorMessage) {
				return true
			}
			if string(message.Provider) == types.ProviderCerebras && cerebrasBodylessOverflowPattern.MatchString(errorMessage) {
				return true
			}
		}
	}

	// Case 2: silent overflow - successful but usage exceeds context.
	if contextWindow != nil && message.StopReason == types.StopReasonStop {
		inputTokens := message.Usage.Input + message.Usage.CacheRead
		if inputTokens > *contextWindow {
			return true
		}
	}

	// Case 3: length-stop overflow - the server truncates oversized input to fit
	// the context window, leaving no room for output.
	if contextWindow != nil && message.StopReason == types.StopReasonLength && message.Usage.Output == 0 {
		inputTokens := message.Usage.Input + message.Usage.CacheRead
		if inputTokens >= *contextWindow*0.99 {
			return true
		}
	}

	return false
}

// IsRecoverableLength reports whether a length stop ended below the caller or
// model's intended output limit. desiredMaxOutput must be the original limit
// before any context-based clamping.
func IsRecoverableLength(message types.AssistantMessage, desiredMaxOutput float64) bool {
	return message.StopReason == types.StopReasonLength && desiredMaxOutput > 0 && message.Usage.Output < desiredMaxOutput
}

// GetOverflowPatterns returns the overflow patterns, for testing purposes.
func GetOverflowPatterns() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(overflowPatterns))
	copy(out, overflowPatterns)
	return out
}

func matchesAny(patterns []*regexp.Regexp, text string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
