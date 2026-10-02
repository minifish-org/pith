package codemode

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// CodemodeOptionsPrefix starts the optional first-line options directive.
const CodemodeOptionsPrefix = "// @options:"

// CodemodeSourceGrammar is the Lark grammar for grammar-constrained tool input.
const CodemodeSourceGrammar = `
start: options_source | plain_source
options_source: OPTIONS_LINE NEWLINE SOURCE
plain_source: SOURCE

OPTIONS_LINE: /[ \t]*\/\/ @options:[^\r\n]*/
NEWLINE: /\r?\n/
SOURCE: /[\s\S]+/
`

// maxTimeoutMs is the largest delay setTimeout supports, bounding timeout_ms.
const maxTimeoutMs = 2147483647

const supportedFieldsText = "`max_output_tokens` and `timeout_ms`"

var supportedFields = []string{"max_output_tokens", "timeout_ms"}

// CodemodeSourceError is returned for empty input and invalid options.
type CodemodeSourceError struct {
	Message string
}

func (e *CodemodeSourceError) Error() string { return e.Message }

func newSourceError(format string, args ...any) error {
	return &CodemodeSourceError{Message: fmt.Sprintf(format, args...)}
}

// CodemodeSourceOptions holds parsed `// @options` fields.
type CodemodeSourceOptions struct {
	// MaxOutputTokens is the token budget for the script's output.
	MaxOutputTokens *int64
	// TimeoutMs is the hard deadline for the whole script in milliseconds.
	TimeoutMs *int64
}

// ParsedCodemodeSource is the result of ParseCodemodeSource.
type ParsedCodemodeSource struct {
	// Code is the script with the options line replaced by an empty line, so
	// line numbers are unchanged.
	Code    string
	Options CodemodeSourceOptions
}

func isSafeIntegerValue(v float64) bool {
	return !math.IsInf(v, 0) && !math.IsNaN(v) && v == math.Trunc(v) && math.Abs(v) <= 9007199254740991
}

func parseSourceOptions(directive string) (CodemodeSourceOptions, error) {
	var options CodemodeSourceOptions
	if directive == "" {
		return options, newSourceError("@options must be a JSON object with supported fields %s", supportedFieldsText)
	}
	var value any
	if err := json.Unmarshal([]byte(directive), &value); err != nil {
		return options, newSourceError("@options must be valid JSON with supported fields %s: %s", supportedFieldsText, err.Error())
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return options, newSourceError("@options must be a JSON object with supported fields %s", supportedFieldsText)
	}
	for key := range fields {
		supported := false
		for _, f := range supportedFields {
			if f == key {
				supported = true
				break
			}
		}
		if !supported {
			return options, newSourceError("@options only supports %s; got `%s`", supportedFieldsText, key)
		}
	}
	if raw, ok := fields["max_output_tokens"]; ok {
		v, ok := raw.(float64)
		if !ok || !isSafeIntegerValue(v) || v < 0 {
			return options, newSourceError("@options field `max_output_tokens` must be a non-negative safe integer")
		}
		n := int64(v)
		options.MaxOutputTokens = &n
	}
	if raw, ok := fields["timeout_ms"]; ok {
		v, ok := raw.(float64)
		if !ok || !isSafeIntegerValue(v) || v == 0 || v > maxTimeoutMs {
			return options, newSourceError("@options field `timeout_ms` must be a positive integer up to %d", maxTimeoutMs)
		}
		n := int64(v)
		options.TimeoutMs = &n
	}
	return options, nil
}

// ParseCodemodeSource splits an optional first-line `// @options: {...}` from
// the script. It returns a CodemodeSourceError for empty input and invalid
// options.
func ParseCodemodeSource(input string) (ParsedCodemodeSource, error) {
	if strings.TrimSpace(input) == "" {
		return ParsedCodemodeSource{}, newSourceError("Expected JavaScript source text (non-empty). Provide JS only, optionally with a first line `// @options: {\"max_output_tokens\": 1000}`.")
	}
	newline := strings.IndexByte(input, '\n')
	firstLine := input
	if newline != -1 {
		firstLine = input[:newline]
	}
	firstLine = strings.TrimSuffix(firstLine, "\r")
	trimmed := strings.TrimLeft(firstLine, " \t\n\r\v\f")
	if !strings.HasPrefix(trimmed, CodemodeOptionsPrefix) {
		return ParsedCodemodeSource{Code: input, Options: CodemodeSourceOptions{}}, nil
	}
	code := ""
	if newline != -1 {
		code = input[newline:]
	}
	if strings.TrimSpace(code) == "" {
		return ParsedCodemodeSource{}, newSourceError("The @options line must be followed by JavaScript source on subsequent lines")
	}
	directive := strings.TrimSpace(trimmed[len(CodemodeOptionsPrefix):])
	options, err := parseSourceOptions(directive)
	if err != nil {
		return ParsedCodemodeSource{}, err
	}
	return ParsedCodemodeSource{Code: code, Options: options}, nil
}

// ensure the sentinel error type satisfies error.
var _ error = (*CodemodeSourceError)(nil)
