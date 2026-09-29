// This file carries the JSON value contract of packages/chord/src/types.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package chordcontext

// JsonValue is the strict-JSON value union. In Go the union is represented by
// any and enforced at the encoding boundaries.
type JsonValue = any

// JsonRepresentation is the strict-JSON representation of an application data
// type. The upstream conditional type only erases compile-time detail, so the
// Go representation is the same as JsonValue.
type JsonRepresentation = any
