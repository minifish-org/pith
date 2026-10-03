// Package chord is the Go port of the Chord replicated-state runtime used by
// the optional Pith Durable SDK at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// This is a native Go adaptation, not a line-for-line or binary-compatible
// TypeScript API. Strict JSON is a runtime boundary: the canonical containers
// are map[string]any and []any, and every value handed to the runtime is
// validated and detached. Numeric primitives normalize to JSON numbers and
// comparisons are by JSON value rather than a particular Go representation.
package chord

import "github.com/minifish-org/pith/packages/chord/delta"

// CopyJSON copies a value into an alias-free strict-JSON tree owned by the
// caller. Each container occurrence is duplicated so a shared input child
// becomes independent output children. Cycles, non-finite numbers, functions,
// channels and other unsupported objects are rejected.
func CopyJSON(value any) (any, error) {
	return delta.CopyJSON(value)
}

// IsJSONValue reports whether value is finite strict JSON with map/array
// containers and no cycles.
func IsJSONValue(value any) bool {
	return delta.IsJSONValue(value)
}
