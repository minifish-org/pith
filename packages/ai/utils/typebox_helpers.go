// This file is a Go port of packages/ai/src/utils/typebox-helpers.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream builds a TypeBox `Type.Unsafe` schema. The Go port represents the
// same JSON Schema document as an ordered map written through json.RawMessage,
// which is what the provider request bodies consume; the TypeBox kind symbol
// has no Go counterpart and is not part of the wire contract.
package utils

import (
	"encoding/json"
)

// StringEnumOptions are the optional StringEnum schema decorations.
type StringEnumOptions struct {
	Description *string
	Default     *string
}

// StringEnum creates a string enum schema compatible with Google's API and other
// providers that do not support anyOf/const patterns. Only a non-empty
// description or a non-nil default is emitted, matching upstream's truthiness
// checks.
func StringEnum(values []string, options *StringEnumOptions) json.RawMessage {
	enumValues := make([]any, len(values))
	for i, value := range values {
		enumValues[i] = value
	}
	schema := orderedSchema{}
	schema.set("type", "string")
	schema.set("enum", enumValues)
	if options != nil {
		if options.Description != nil && *options.Description != "" {
			schema.set("description", *options.Description)
		}
		if options.Default != nil && *options.Default != "" {
			schema.set("default", *options.Default)
		}
	}
	encoded, _ := json.Marshal(schema.entries)
	return encoded
}

// orderedSchema preserves insertion order for the emitted JSON object keys,
// since Go maps would reorder them.
type orderedSchema struct {
	entries []schemaEntry
}

type schemaEntry struct {
	key   string
	value any
}

func (s *orderedSchema) set(key string, value any) {
	s.entries = append(s.entries, schemaEntry{key: key, value: value})
}

// MarshalJSON writes the entries as a JSON object in insertion order.
func (s orderedSchema) MarshalJSON() ([]byte, error) {
	var out []byte
	out = append(out, '{')
	for i, entry := range s.entries {
		if i > 0 {
			out = append(out, ',')
		}
		key, err := json.Marshal(entry.key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(entry.value)
		if err != nil {
			return nil, err
		}
		out = append(out, key...)
		out = append(out, ':')
		out = append(out, value...)
	}
	out = append(out, '}')
	return out, nil
}
