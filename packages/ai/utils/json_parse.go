// This file is a Go port of packages/ai/src/utils/json-parse.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream module layers three behaviors: repairJson rewrites malformed
// string literals, parseJsonWithRepair is the strict-then-repaired parser, and
// parseStreamingJson adds the lenient `partial-json` fallback used while a tool
// call is still streaming. Go's encoding/json has no partial parser, so the
// incompleteness-tolerant path is implemented here: an unclosed string is
// terminated and open containers are closed, which is the observable contract
// of the pinned partial-json behavior for the shapes the SDK feeds it
// (tool-call JSON deltas), not a general JSON-Patch parser.
package utils

import (
	"encoding/json"
	"fmt"
	"strings"
)

// validJSONEscapes mirrors the upstream VALID_JSON_ESCAPES set.
var validJSONEscapes = map[byte]bool{
	'"': true, '\\': true, '/': true, 'b': true, 'f': true,
	'n': true, 'r': true, 't': true, 'u': true,
}

func escapeControlCharacter(b byte) string {
	switch b {
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	default:
		return fmt.Sprintf(`\u%04x`, b)
	}
}

func isHexDigit4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// RepairJSON repairs malformed JSON string literals by escaping raw control
// characters inside strings and doubling backslashes before invalid escape
// characters.
func RepairJSON(input string) string {
	var repaired strings.Builder
	inString := false

	for index := 0; index < len(input); index++ {
		char := input[index]

		if !inString {
			repaired.WriteByte(char)
			if char == '"' {
				inString = true
			}
			continue
		}

		if char == '"' {
			repaired.WriteByte(char)
			inString = false
			continue
		}

		if char == '\\' {
			if index+1 >= len(input) {
				repaired.WriteString(`\\`)
				continue
			}
			nextChar := input[index+1]

			if nextChar == 'u' && index+6 <= len(input) && isHexDigit4(input[index+2:index+6]) {
				repaired.WriteString(`\u`)
				repaired.WriteString(input[index+2 : index+6])
				index += 5
				continue
			}

			if validJSONEscapes[nextChar] {
				repaired.WriteByte('\\')
				repaired.WriteByte(nextChar)
				index++
				continue
			}

			repaired.WriteString(`\\`)
			continue
		}

		if char <= 0x1f {
			repaired.WriteString(escapeControlCharacter(char))
		} else {
			repaired.WriteByte(char)
		}
	}

	return repaired.String()
}

// ParseJSONWithRepair parses JSON, falling back to the repaired text when the
// strict parse fails. The repaired text is only retried when it differs from the
// input, and the original parse error is returned when repair did not help.
func ParseJSONWithRepair(input string) (any, error) {
	value, err := decodeJSON(input)
	if err == nil {
		return value, nil
	}
	repaired := RepairJSON(input)
	if repaired != input {
		if repairedValue, rerr := decodeJSON(repaired); rerr == nil {
			return repairedValue, nil
		}
	}
	return nil, err
}

func decodeJSON(input string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// ParseStreamingJSON attempts to parse potentially incomplete JSON during
// streaming and always returns a value. The fallback is the upstream `{}`
// default, so a value that cannot be recovered at all returns an empty object.
func ParseStreamingJSON(partialJSON string) any {
	if value, err := ParseJSONWithRepair(partialJSON); err == nil {
		return value
	}
	if value, ok := parsePartialJSON(partialJSON); ok {
		return value
	}
	if value, ok := parsePartialJSON(RepairJSON(partialJSON)); ok {
		return value
	}
	return map[string]any{}
}

// ParseStreamingJSONObject parses partial JSON that is expected to be an object.
// Non-object results and failures yield an empty (non-nil) map, matching the
// upstream `{} as T` default for `T = Record<string, unknown>`.
func ParseStreamingJSONObject(partialJSON string) map[string]any {
	value := ParseStreamingJSON(partialJSON)
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

// parsePartialJSON is the lenient reader for incomplete JSON. It reports ok when
// a value could be recovered from the prefix.
func parsePartialJSON(input string) (any, bool) {
	if strings.TrimSpace(input) == "" {
		return nil, false
	}
	repaired := closeIncompleteJSON(input)
	if !json.Valid([]byte(repaired)) {
		return nil, false
	}
	value, err := decodeJSON(repaired)
	if err != nil {
		return nil, false
	}
	return value, true
}

// closeIncompleteJSON appends the minimal closing tokens needed to make an
// incomplete JSON prefix parseable: a terminating quote for an open string and
// the matching close for every open object/array.
func closeIncompleteJSON(input string) string {
	var out strings.Builder
	var stack []byte
	inString := false
	escaped := false

	for index := 0; index < len(input); index++ {
		char := input[index]
		out.WriteByte(char)
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch char {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, char)
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
	}

	if inString {
		out.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out.WriteByte('}')
		} else {
			out.WriteByte(']')
		}
	}
	return out.String()
}
