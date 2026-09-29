// This file is a Go port of packages/ai/src/utils/error-body.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Endpoints behind a proxy / gateway may return a non-2xx response whose body the
// provider SDK cannot fold into `error.message`. The SDK error object still
// carries the HTTP status and the raw/parsed body, but under SDK-specific field
// names. NormalizeProviderError probes the known SDK field shapes (Mistral,
// openai, @google/genai, AWS Bedrock) and returns a struct each provider
// composes into its display string. The MessageCarriesBody flag captures the
// Anthropic / @google/genai happy path where the SDK already folded the body
// into the message.
package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MaxProviderErrorBodyChars is the maximum length of a surfaced error body.
const MaxProviderErrorBodyChars = 4000

// NormalizedProviderError is the normalized view of a provider-thrown error.
type NormalizedProviderError struct {
	// Status is the HTTP status code, when one could be extracted.
	Status *int
	// Body is the raw HTTP body reason, already trimmed and truncated to the
	// cap.
	Body *string
	// Message is `error.message`, or a serialized value for a non-error throw.
	Message string
	// MessageCarriesBody is true when Message already contains the body (no
	// separate body to add).
	MessageCarriesBody bool
}

// providerErrorShape is the union of the SDK error fields probed below.
type providerErrorShape struct {
	StatusCode any
	Status     any
	Body       any
	Error      any
	Metadata   map[string]any
	Response   map[string]any
}

// NormalizeProviderError normalizes a thrown provider error value.
func NormalizeProviderError(err error) NormalizedProviderError {
	if err == nil {
		return NormalizedProviderError{Message: SafeJSONStringify(nil)}
	}
	shape := extractProviderShape(err)
	status := extractStatus(shape)
	body := extractBody(shape)
	message := err.Error()
	carriesBody := body == nil || strings.Contains(message, *body)

	return NormalizedProviderError{
		Status:             status,
		Body:               body,
		Message:            message,
		MessageCarriesBody: carriesBody,
	}
}

// NormalizeProviderErrorValue normalizes a thrown value that may not be an
// `error` (mirroring upstream's `unknown` parameter).
func NormalizeProviderErrorValue(value any) NormalizedProviderError {
	var err error
	if asError, ok := value.(error); ok {
		err = asError
	}
	if err == nil {
		return NormalizedProviderError{Message: SafeJSONStringify(value), MessageCarriesBody: false}
	}
	return NormalizeProviderError(err)
}

// extractProviderShape reads the SDK-specific fields through any interface.
func extractProviderShape(err error) providerErrorShape {
	var shape providerErrorShape
	accessor, ok := err.(interface {
		ProviderErrorFields() map[string]any
	})
	if ok {
		fields := accessor.ProviderErrorFields()
		shape.StatusCode = fields["statusCode"]
		shape.Status = fields["status"]
		shape.Body = fields["body"]
		shape.Error = fields["error"]
		if metadata, ok := fields["$metadata"].(map[string]any); ok {
			shape.Metadata = metadata
		}
		if response, ok := fields["$response"].(map[string]any); ok {
			shape.Response = response
		}
	}
	return shape
}

// extractStatus probes the HTTP status, first numeric hit wins, in SDK-field
// order: statusCode (Mistral) -> status (openai, @google/genai) ->
// $metadata.httpStatusCode (Bedrock) -> $response.statusCode (Bedrock).
func extractStatus(shape providerErrorShape) *int {
	if value, ok := numericValue(shape.StatusCode); ok {
		return &value
	}
	if value, ok := numericValue(shape.Status); ok {
		return &value
	}
	if shape.Metadata != nil {
		if value, ok := numericValue(shape.Metadata["httpStatusCode"]); ok {
			return &value
		}
	}
	if shape.Response != nil {
		if value, ok := numericValue(shape.Response["statusCode"]); ok {
			return &value
		}
	}
	return nil
}

func numericValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

// extractBody probes the raw body reason, first usable hit wins.
func extractBody(shape providerErrorShape) *string {
	bodyText := pickBodyText(shape)
	if bodyText == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*bodyText)
	if len(trimmed) == 0 {
		return nil
	}
	truncated := TruncateErrorText(trimmed, MaxProviderErrorBodyChars)
	return &truncated
}

func pickBodyText(shape providerErrorShape) *string {
	if body, ok := shape.Body.(string); ok {
		return &body
	}
	if isPlainNonEmptyObject(shape.Error) {
		serialized := SafeJSONStringify(shape.Error)
		return &serialized
	}
	if shape.Response != nil {
		responseBody := shape.Response["body"]
		if body, ok := responseBody.(string); ok {
			return &body
		}
		if isReadableStreamLike(responseBody) {
			return nil
		}
		if isPlainNonEmptyObject(responseBody) {
			serialized := SafeJSONStringify(responseBody)
			return &serialized
		}
	}
	return nil
}

// isReadableStreamLike reports whether a value is a node-style readable stream.
func isReadableStreamLike(value any) bool {
	stream, ok := value.(interface{ Pipe() })
	return ok && stream != nil
}

// isPlainNonEmptyObject reports whether a value is a non-empty plain JSON
// object. SDK error fields can hold class instances instead of parsed bodies
// (for example AWS SDK v3's `$response.body` is an HTTP response wrapper), and
// serializing those would replace the real `error.message` with noise. In Go
// the parsed-body analogue is `map[string]any`; other types are rejected.
func isPlainNonEmptyObject(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	return len(object) > 0
}

// FormatProviderError composes a display string from a normalized error. When
// the message already carries the body or no body/status was extracted, the
// message is returned unchanged. Otherwise the status and body are surfaced,
// with an optional provider prefix.
//
// - no prefix: "<status>: <body>"
// - prefix:    "<prefix> (<status>): <body>"
func FormatProviderError(norm NormalizedProviderError, prefix *string) string {
	if norm.MessageCarriesBody || norm.Status == nil || norm.Body == nil {
		if prefix != nil && norm.Status != nil {
			return fmt.Sprintf("%s (%d): %s", *prefix, *norm.Status, norm.Message)
		}
		return norm.Message
	}
	if prefix != nil {
		return fmt.Sprintf("%s (%d): %s", *prefix, *norm.Status, *norm.Body)
	}
	return fmt.Sprintf("%d: %s", *norm.Status, *norm.Body)
}

// TruncateErrorText truncates text to maxChars, appending a note with the number
// of dropped characters. Length is measured in UTF-16 code units, matching the
// JavaScript `.length` upstream.
func TruncateErrorText(text string, maxChars int) string {
	if maxChars < 0 {
		maxChars = 0
	}
	units := utf16Units(text)
	if len(units) <= maxChars {
		return text
	}
	return string(unitsToUTF8(units[:maxChars])) + fmt.Sprintf("... [truncated %d chars]", len(units)-maxChars)
}

// unitsToUTF8 re-encodes UTF-16 code units, keeping unpaired surrogates as their
// literal encoding so a truncation in the middle of an astral character cannot
// silently alter the kept prefix length.
func unitsToUTF8(units []uint16) string {
	var builder strings.Builder
	for i := 0; i < len(units); i++ {
		unit := units[i]
		switch {
		case unit >= 0xD800 && unit <= 0xDBFF && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] <= 0xDFFF:
			builder.WriteRune(utf16Decode(unit, units[i+1]))
			i++
		case unit >= 0xD800 && unit <= 0xDFFF:
			// Unpaired surrogate: emit its WTF-8 encoding.
			builder.WriteByte(byte(0xE0 | (unit >> 12)))
			builder.WriteByte(byte(0x80 | ((unit >> 6) & 0x3F)))
			builder.WriteByte(byte(0x80 | (unit & 0x3F)))
		default:
			builder.WriteRune(rune(unit))
		}
	}
	return builder.String()
}

// SafeJSONStringify serializes a value to JSON, falling back to a plain string
// form when serialization fails.
func SafeJSONStringify(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err == nil {
		return string(encoded)
	}
	return fmt.Sprint(value)
}

// ErrAbortError is the Go stand-in for the AbortError name. Callers can test
// with errors.Is.
var ErrAbortError = errors.New("AbortError")
