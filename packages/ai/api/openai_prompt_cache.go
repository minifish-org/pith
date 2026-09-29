// This file is a Go port of packages/ai/src/api/openai-prompt-cache.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

// OpenAIPromptCacheKeyMaxLength is the maximum provider prompt-cache key length.
const OpenAIPromptCacheKeyMaxLength = 64

// ClampOpenAIPromptCacheKey truncates a prompt-cache key to the provider limit.
// It counts Unicode code points (upstream Array.from), so a key of astral
// characters is not split mid-character. A nil key stays nil.
func ClampOpenAIPromptCacheKey(key *string) *string {
	if key == nil {
		return nil
	}
	chars := []rune(*key)
	if len(chars) <= OpenAIPromptCacheKeyMaxLength {
		return key
	}
	clamped := string(chars[:OpenAIPromptCacheKeyMaxLength])
	return &clamped
}
