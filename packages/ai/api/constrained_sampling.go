// This file is a Go port of packages/ai/src/api/constrained-sampling.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

// unsupportedStrictSchemaKeys are the JSON Schema keywords the strict
// constrained-sampling subset rejects.
var unsupportedStrictSchemaKeys = []string{
	"$ref", "$defs", "definitions", "allOf", "oneOf", "patternProperties",
	"dependentSchemas", "dependencies", "unevaluatedProperties", "propertyNames",
	"contains", "prefixItems", "not", "if", "then", "else",
}

// UnsupportedStrictJSONSchemaError reports a schema outside the strict subset.
type UnsupportedStrictJSONSchemaError struct{ Reason string }

// Error implements error.
func (e *UnsupportedStrictJSONSchemaError) Error() string { return e.Reason }

func asUnsupportedStrict(err error) (*UnsupportedStrictJSONSchemaError, bool) {
	var unsupported *UnsupportedStrictJSONSchemaError
	if errors.As(err, &unsupported) {
		return unsupported, true
	}
	return nil, false
}

type jsonSchemaObject = map[string]any

func isJSONSchemaObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func isStructuredSchema(schema any) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	schemaTypes := getSchemaTypes(object)
	if containsStr(schemaTypes, "object") || containsStr(schemaTypes, "array") {
		return true
	}
	return object["properties"] != nil || object["items"] != nil
}

func schemaAllowsNull(schema any) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if object["type"] == "null" {
		return true
	}
	if schemaTypes := getSchemaTypes(object); containsStr(schemaTypes, "null") {
		return true
	}
	if constant, ok := object["const"]; ok && constant == nil {
		return true
	}
	if enum, ok := object["enum"].([]any); ok {
		for _, entry := range enum {
			if entry == nil {
				return true
			}
		}
	}
	if anyOf, ok := object["anyOf"].([]any); ok {
		for _, variant := range anyOf {
			if schemaAllowsNull(variant) {
				return true
			}
		}
	}
	return false
}

func makeJSONSchemaNodeStrict(schema any) error {
	object, ok := schema.(map[string]any)
	if !ok {
		return &UnsupportedStrictJSONSchemaError{Reason: "boolean schemas are unsupported"}
	}
	for _, key := range unsupportedStrictSchemaKeys {
		if _, exists := object[key]; exists {
			return &UnsupportedStrictJSONSchemaError{Reason: key + " schemas are unsupported"}
		}
	}

	if anyOfValue, exists := object["anyOf"]; exists {
		anyOf, ok := anyOfValue.([]any)
		if !ok || len(anyOf) == 0 {
			return &UnsupportedStrictJSONSchemaError{Reason: "anyOf must contain at least one schema"}
		}
		for _, variant := range anyOf {
			if isStructuredSchema(variant) {
				return &UnsupportedStrictJSONSchemaError{Reason: "object and array unions are unsupported"}
			}
			if err := makeJSONSchemaNodeStrict(variant); err != nil {
				return err
			}
		}
	}

	if items, exists := object["items"]; exists {
		if _, ok := items.([]any); ok {
			return &UnsupportedStrictJSONSchemaError{Reason: "tuple schemas are unsupported"}
		}
		if err := makeJSONSchemaNodeStrict(items); err != nil {
			return err
		}
	}

	isObjectSchema := object["type"] == "object"
	if _, exists := object["properties"]; exists && !isObjectSchema {
		return &UnsupportedStrictJSONSchemaError{Reason: "properties require type object"}
	}
	if !isObjectSchema {
		return nil
	}
	if additional, exists := object["additionalProperties"]; exists && additional != false {
		return &UnsupportedStrictJSONSchemaError{Reason: "schema-valued or true additionalProperties is unsupported"}
	}
	properties, propertiesExist := object["properties"]
	if propertiesExist && !isJSONSchemaObject(properties) {
		return &UnsupportedStrictJSONSchemaError{Reason: "object properties must be a schema map"}
	}
	if required, exists := object["required"]; exists {
		requiredList, ok := required.([]any)
		if !ok {
			return &UnsupportedStrictJSONSchemaError{Reason: "object required must be a string array"}
		}
		for _, key := range requiredList {
			if _, ok := key.(string); !ok {
				return &UnsupportedStrictJSONSchemaError{Reason: "object required must be a string array"}
			}
		}
	}

	propertyMap, _ := properties.(map[string]any)
	propertyNames := []string{}
	for key := range propertyMap {
		propertyNames = append(propertyNames, key)
	}
	sort.Strings(propertyNames)

	requiredSet := map[string]bool{}
	if required, ok := object["required"].([]any); ok {
		for _, key := range required {
			if text, ok := key.(string); ok {
				requiredSet[text] = true
			}
		}
	}
	for name := range requiredSet {
		if !containsStr(propertyNames, name) {
			return &UnsupportedStrictJSONSchemaError{Reason: "required contains an unknown property"}
		}
	}
	for _, key := range propertyNames {
		property := propertyMap[key]
		if err := makeJSONSchemaNodeStrict(property); err != nil {
			return err
		}
		if !requiredSet[key] && !schemaAllowsNull(property) {
			propertyMap[key] = map[string]any{"anyOf": []any{property, map[string]any{"type": "null"}}}
		}
	}
	object["required"] = toAnySlice(propertyNames)
	object["additionalProperties"] = false
	return nil
}

// MakeStrictJSONSchema converts a tool schema to the strict subset expected by
// provider constrained sampling. The schema is a JSON Schema document.
func MakeStrictJSONSchema(schema json.RawMessage) (json.RawMessage, error) {
	decoded, err := decodeRaw(schema)
	if err != nil {
		return nil, err
	}
	if !isJSONSchemaObject(decoded) {
		return nil, &UnsupportedStrictJSONSchemaError{Reason: "root schema must have type object"}
	}
	if err := makeJSONSchemaNodeStrict(decoded); err != nil {
		return nil, err
	}
	if object, ok := decoded.(map[string]any); !ok || object["type"] != "object" {
		return nil, &UnsupportedStrictJSONSchemaError{Reason: "root schema must have type object"}
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// GetJSONSchemaToolParameters returns the tool parameters, converting them to
// the strict subset when strict is true.
func GetJSONSchemaToolParameters(tool types.Tool, strict *bool) (json.RawMessage, error) {
	if strict != nil && *strict {
		return MakeStrictJSONSchema(tool.Input.Schema)
	}
	return tool.Input.Schema, nil
}

// GrammarConstrainedSampling is a resolved grammar constraint for a tool.
type GrammarConstrainedSampling struct {
	Format        string `json:"format"`
	Definition    string `json:"definition"`
	InputProperty string `json:"inputProperty"`
}

// GrammarToolInputJSONBuffer accumulates a grammar tool's streamed input.
type GrammarToolInputJSONBuffer struct {
	Input   string `json:"input"`
	Started bool   `json:"started"`
	Closed  bool   `json:"closed"`
}

// GetGrammarToolInput extracts the required string input property of a grammar
// tool call.
func GetGrammarToolInput(toolName string, arguments map[string]any, inputProperty string) (string, error) {
	input, ok := arguments[inputProperty].(string)
	if !ok {
		return "", fmt.Errorf("Grammar tool call %q requires argument %q to be a string.", toolName, inputProperty)
	}
	return input, nil
}

// AppendGrammarToolInputJSONDelta appends a streamed grammar input delta to a
// JSON buffer, emitting the JSON fragment to send. A nil result means there is
// no new fragment.
func AppendGrammarToolInputJSONDelta(buffer *GrammarToolInputJSONBuffer, inputProperty, nextInput string, close bool) (*string, error) {
	if buffer.Closed {
		if close && nextInput == buffer.Input {
			return nil, nil
		}
		return nil, fmt.Errorf("grammar tool input for property %q changed after it was closed", inputProperty)
	}
	if !strings.HasPrefix(nextInput, buffer.Input) {
		return nil, fmt.Errorf("grammar tool input for property %q changed non-monotonically", inputProperty)
	}

	inputDelta := nextInput[len(buffer.Input):]
	if !close && len(inputDelta) == 0 {
		return nil, nil
	}

	delta := ""
	if !buffer.Started {
		encodedProperty, _ := json.Marshal(inputProperty)
		delta += "{" + string(encodedProperty) + ":\""
		buffer.Started = true
	}
	encodedDelta, _ := json.Marshal(inputDelta)
	encodedText := string(encodedDelta)
	if len(encodedText) >= 2 {
		delta += encodedText[1 : len(encodedText)-1]
	}
	buffer.Input = nextInput

	if close {
		delta += "\"}"
		buffer.Closed = true
	}
	return &delta, nil
}

func inferGrammarInputProperty(tool types.Tool) (string, error) {
	schema := decodeSchemaObject(tool.Input.Schema)
	if schema == nil || schema["type"] != "object" {
		return "", errors.New("grammar constrained sampling requires an object parameter schema")
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 {
		return "", errors.New("grammar constrained sampling requires exactly one required string property")
	}
	inputProperty, ok := required[0].(string)
	if !ok {
		return "", errors.New("grammar constrained sampling requires exactly one required string property")
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		propertySchema, exists := properties[inputProperty]
		if !exists {
			return "", fmt.Errorf("grammar constrained sampling requires a properties entry for %s", inputProperty)
		}
		if object, ok := propertySchema.(map[string]any); !ok || object["type"] != "string" {
			return "", fmt.Errorf("grammar constrained sampling property %s must have type string", inputProperty)
		}
	} else {
		return "", fmt.Errorf("grammar constrained sampling requires a properties entry for %s", inputProperty)
	}
	return inputProperty, nil
}

// ResolveJSONSchemaStrictSampling resolves the strict-sampling preference for a
// json_schema-constrained tool, returning nil when no strict mode applies.
func ResolveJSONSchemaStrictSampling(tool types.Tool, supportsStrictMode bool) (*bool, error) {
	config := tool.ConstrainedSampling
	if config == nil || config.Type != "json_schema" {
		return nil, nil
	}

	if supportsStrictMode {
		if _, err := MakeStrictJSONSchema(tool.Input.Schema); err == nil {
			value := true
			return &value, nil
		} else if _, ok := asUnsupportedStrict(err); !ok {
			return nil, err
		} else {
			if config.Strict == nil || *config.Strict != types.ConstrainedStrictRequire {
				return nil, nil
			}
			return nil, fmt.Errorf("Tool %q requires JSON-schema constrained sampling, but %s.", tool.Name, err.Error())
		}
	}
	if config.Strict != nil && *config.Strict == types.ConstrainedStrictRequire {
		return nil, fmt.Errorf("Tool %q requires JSON-schema constrained sampling, but strict tools are unsupported.", tool.Name)
	}
	return nil, nil
}

// ResolveGrammarConstrainedSampling resolves the grammar constraint for a
// grammar-constrained tool, returning nil when no grammar applies.
func ResolveGrammarConstrainedSampling(tool types.Tool, supportsOpenAIGrammarTools bool) (*GrammarConstrainedSampling, error) {
	config := tool.ConstrainedSampling
	if config == nil || config.Type != "grammar" {
		return nil, nil
	}
	if !supportsOpenAIGrammarTools {
		return nil, nil
	}

	larkDefinition := config.Variants[types.GrammarFormatOpenAILark]
	regexDefinition := config.Variants[types.GrammarFormatOpenAIRegex]
	hasLarkDefinition := strings.TrimSpace(larkDefinition) != ""
	hasRegexDefinition := strings.TrimSpace(regexDefinition) != ""

	if !hasLarkDefinition && !hasRegexDefinition {
		return nil, fmt.Errorf("Tool %q cannot use grammar constrained sampling: no supported grammar variant was provided.", tool.Name)
	}

	inputProperty, err := inferGrammarInputProperty(tool)
	if err != nil {
		return nil, fmt.Errorf("Tool %q cannot use grammar constrained sampling: %s.", tool.Name, err.Error())
	}

	format := "regex"
	definition := regexDefinition
	if hasLarkDefinition {
		format = "lark"
		definition = larkDefinition
	}
	return &GrammarConstrainedSampling{Format: format, Definition: definition, InputProperty: inputProperty}, nil
}

// CreateGrammarToolInputProperties returns the grammar input property per tool
// name, for the tools that resolve to a grammar constraint.
func CreateGrammarToolInputProperties(tools []types.Tool, supportsOpenAIGrammarTools bool) (map[string]string, error) {
	properties := map[string]string{}
	for _, tool := range tools {
		grammar, err := ResolveGrammarConstrainedSampling(tool, supportsOpenAIGrammarTools)
		if err != nil {
			return nil, err
		}
		if grammar != nil {
			properties[tool.Name] = grammar.InputProperty
		}
	}
	return properties, nil
}

func decodeRaw(schema json.RawMessage) (any, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	return decodeJSONValue(schema)
}

func decodeSchemaObject(schema json.RawMessage) map[string]any {
	value, err := decodeRaw(schema)
	if err != nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func containsStr(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func getSchemaTypes(schema map[string]any) []string {
	switch value := schema["type"].(type) {
	case string:
		return []string{value}
	case []any:
		typesList := []string{}
		for _, entry := range value {
			if text, ok := entry.(string); ok {
				typesList = append(typesList, text)
			}
		}
		return typesList
	default:
		return nil
	}
}
