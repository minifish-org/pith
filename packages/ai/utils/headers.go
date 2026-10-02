// This file is a Go port of packages/ai/src/utils/headers.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"net/http"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

// HeadersToRecord converts http.Header to a plain string map, keeping the last
// value for a repeated header, mirroring `Headers.entries()`.
func HeadersToRecord(headers http.Header) map[string]string {
	result := map[string]string{}
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		result[key] = values[len(values)-1]
	}
	return result
}

// ProviderHeadersToRecord merges provider headers case-insensitively into a
// plain string map. Sources are applied left to right: a later spelling wins
// over an earlier one, a nil value removes any earlier value for the same
// normalized name, and an empty result returns nil (matching upstream
// `undefined`). The returned key keeps the spelling from the source that last
// set the value.
func ProviderHeadersToRecord(headerSources ...types.ProviderHeaders) map[string]string {
	type entry struct {
		name  string
		value string
	}
	merged := map[string]entry{}
	for _, source := range headerSources {
		for name, value := range source {
			normalized := strings.ToLower(name)
			delete(merged, normalized)
			if value != nil {
				merged[normalized] = entry{name: name, value: *value}
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	result := make(map[string]string, len(merged))
	for _, item := range merged {
		result[item.name] = item.value
	}
	return result
}
