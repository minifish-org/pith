// This file is a Go port of packages/ai/src/utils/headers.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"net/http"

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

// ProviderHeadersToRecord converts provider headers to a plain string map,
// dropping nil (suppressed) values. It returns nil when the result would be
// empty, matching the upstream `undefined`.
func ProviderHeadersToRecord(headers types.ProviderHeaders) map[string]string {
	if headers == nil {
		return nil
	}
	result := map[string]string{}
	for key, value := range headers {
		if value != nil {
			result[key] = *value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
