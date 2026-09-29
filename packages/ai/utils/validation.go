// This file is a Go port of packages/ai/src/utils/validation.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream validates with TypeBox Compile, coerces with Value.Convert plus a
// hand-written JSON-schema coercion pass, and normalizes optional nulls before
// validation. The Go port keeps the same three stages: tool arguments are
// cloned, optional nulls are dropped for non-required properties, primitives are
// coerced by their declared type, and the result is validated against the
// supported JSON Schema subset. Error wording differs from TypeBox; the
// success/error classification is preserved.
package utils

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

// ValidateToolCall finds a tool by name and validates the tool call arguments
// against its schema.
func ValidateToolCall(tools []types.Tool, toolCall types.ToolCall) (any, error) {
	for _, tool := range tools {
		if tool.Name == toolCall.Name {
			return ValidateToolArguments(tool, toolCall)
		}
	}
	return nil, fmt.Errorf("Tool %q not found", toolCall.Name)
}

// ValidateToolArguments validates tool call arguments against the tool's JSON
// schema and returns the validated (and potentially coerced) arguments.
func ValidateToolArguments(tool types.Tool, toolCall types.ToolCall) (any, error) {
	args, err := decodeArgs(toolCall.Arguments)
	if err != nil {
		return nil, err
	}
	schema := decodeSchema(tool.Input.Schema)

	normalizeOptionalNulls(args, schema)
	coerced := coerceWithJSONSchema(args, schema)
	args = coerced

	if checkSchema(schema, args) {
		return args, nil
	}

	errors := collectValidationErrors(schema, args, "")
	lines := make([]string, 0, len(errors))
	for _, validationError := range errors {
		lines = append(lines, "  - "+validationError)
	}
	errorList := strings.Join(lines, "\n")
	if errorList == "" {
		errorList = "Unknown validation error"
	}
	received, _ := json.MarshalIndent(json.RawMessage(orEmptyObject(toolCall.Arguments)), "", "  ")
	return nil, fmt.Errorf("Validation failed for tool %q:\n%s\n\nReceived arguments:\n%s", toolCall.Name, errorList, string(received))
}

func orEmptyObject(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	return args
}

func decodeArgs(args json.RawMessage) (any, error) {
	value, err := ParseJSONWithRepair(string(orEmptyObject(args)))
	if err != nil {
		return nil, err
	}
	return value, nil
}

func decodeSchema(schema json.RawMessage) map[string]any {
	if len(schema) == 0 {
		return map[string]any{}
	}
	value, err := ParseJSONWithRepair(string(schema))
	if err != nil {
		return map[string]any{}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return object
}

func getSchemaTypes(schema map[string]any) []string {
	switch value := schema["type"].(type) {
	case string:
		return []string{value}
	case []any:
		types := []string{}
		for _, entry := range value {
			if text, ok := entry.(string); ok {
				types = append(types, text)
			}
		}
		return types
	default:
		return []string{}
	}
}

func schemaSubObject(schema map[string]any, key string) map[string]any {
	if value, ok := schema[key].(map[string]any); ok {
		return value
	}
	return nil
}

func schemaSubArray(schema map[string]any, key string) []any {
	if value, ok := schema[key].([]any); ok {
		return value
	}
	return nil
}

func matchesJSONType(value any, schemaType string) bool {
	switch schemaType {
	case "number":
		_, ok := toFloat(value)
		return ok
	case "integer":
		number, ok := toFloat(value)
		return ok && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "null":
		return value == nil
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func coercePrimitiveByType(value any, schemaType string) any {
	switch schemaType {
	case "number":
		if value == nil {
			return float64(0)
		}
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) != "" {
				if parsed, ok := parseNumber(text); ok {
					return parsed
				}
			}
		}
		if boolean, ok := value.(bool); ok {
			if boolean {
				return float64(1)
			}
			return float64(0)
		}
		return value
	case "integer":
		if value == nil {
			return float64(0)
		}
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) != "" {
				if parsed, ok := parseNumber(text); ok && parsed == float64(int64(parsed)) {
					return parsed
				}
			}
		}
		if boolean, ok := value.(bool); ok {
			if boolean {
				return float64(1)
			}
			return float64(0)
		}
		return value
	case "boolean":
		if value == nil {
			return false
		}
		if text, ok := value.(string); ok {
			if text == "true" {
				return true
			}
			if text == "false" {
				return false
			}
		}
		if number, ok := toFloat(value); ok {
			if number == 1 {
				return true
			}
			if number == 0 {
				return false
			}
		}
		return value
	case "string":
		if value == nil {
			return ""
		}
		switch v := value.(type) {
		case float64:
			return trimNumber(v)
		case int:
			return fmt.Sprintf("%d", v)
		case int64:
			return fmt.Sprintf("%d", v)
		case json.Number:
			return v.String()
		case bool:
			if v {
				return "true"
			}
			return "false"
		default:
			return value
		}
	case "null":
		if text, ok := value.(string); ok && text == "" {
			return nil
		}
		if number, ok := toFloat(value); ok && number == 0 {
			return nil
		}
		if boolean, ok := value.(bool); ok && !boolean {
			return nil
		}
		return value
	default:
		return value
	}
}

func trimNumber(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func parseNumber(text string) (float64, bool) {
	var number json.Number
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&number); err != nil {
		return 0, false
	}
	parsed, err := number.Float64()
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func applySchemaObjectCoercion(value map[string]any, schema map[string]any) {
	properties := schemaSubObject(schema, "properties")
	definedKeys := map[string]bool{}
	if properties != nil {
		for key, propertySchema := range properties {
			definedKeys[key] = true
			if _, ok := value[key]; !ok {
				continue
			}
			if objectSchema, ok := propertySchema.(map[string]any); ok {
				value[key] = coerceWithJSONSchema(value[key], objectSchema)
			}
		}
	}

	if additional, ok := schema["additionalProperties"].(map[string]any); ok {
		for key, propertyValue := range value {
			if definedKeys[key] {
				continue
			}
			value[key] = coerceWithJSONSchema(propertyValue, additional)
		}
	}
}

func applySchemaArrayCoercion(value []any, schema map[string]any) {
	if items := schemaSubArray(schema, "items"); items != nil {
		for index := range value {
			if index >= len(items) {
				break
			}
			if itemSchema, ok := items[index].(map[string]any); ok {
				value[index] = coerceWithJSONSchema(value[index], itemSchema)
			}
		}
		return
	}
	if itemSchema := schemaSubObject(schema, "items"); itemSchema != nil {
		for index := range value {
			value[index] = coerceWithJSONSchema(value[index], itemSchema)
		}
	}
}

func coerceWithUnionSchema(value any, schemas []any) any {
	for _, schema := range schemas {
		if objectSchema, ok := schema.(map[string]any); ok {
			if checkSchema(objectSchema, value) {
				return value
			}
		}
	}
	for _, schema := range schemas {
		objectSchema, ok := schema.(map[string]any)
		if !ok {
			continue
		}
		candidate := deepClone(value)
		coerced := coerceWithJSONSchema(candidate, objectSchema)
		if checkSchema(objectSchema, coerced) {
			return coerced
		}
	}
	return value
}

func coerceWithJSONSchema(value any, schema map[string]any) any {
	nextValue := value

	if allOf := schemaSubArray(schema, "allOf"); allOf != nil {
		for _, nested := range allOf {
			if objectSchema, ok := nested.(map[string]any); ok {
				nextValue = coerceWithJSONSchema(nextValue, objectSchema)
			}
		}
	}

	if anyOf := schemaSubArray(schema, "anyOf"); anyOf != nil {
		nextValue = coerceWithUnionSchema(nextValue, anyOf)
	}

	if oneOf := schemaSubArray(schema, "oneOf"); oneOf != nil {
		nextValue = coerceWithUnionSchema(nextValue, oneOf)
	}

	schemaTypes := getSchemaTypes(schema)
	matchesUnionMember := len(schemaTypes) > 1 && anyTypeMatches(nextValue, schemaTypes)
	if len(schemaTypes) > 0 && !matchesUnionMember {
		for _, schemaType := range schemaTypes {
			candidate := coercePrimitiveByType(nextValue, schemaType)
			if !reflectEqual(candidate, nextValue) {
				nextValue = candidate
				break
			}
		}
	}

	if containsString(schemaTypes, "object") {
		if object, ok := nextValue.(map[string]any); ok {
			applySchemaObjectCoercion(object, schema)
		}
	}

	if containsString(schemaTypes, "array") {
		if array, ok := nextValue.([]any); ok {
			applySchemaArrayCoercion(array, schema)
		}
	}

	return nextValue
}

func anyTypeMatches(value any, schemaTypes []string) bool {
	for _, schemaType := range schemaTypes {
		if matchesJSONType(value, schemaType) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeOptionalNulls(value any, schema map[string]any) {
	if array, ok := value.([]any); ok {
		if items := schemaSubArray(schema, "items"); items != nil {
			for index := range array {
				if index >= len(items) {
					break
				}
				if itemSchema, ok := items[index].(map[string]any); ok {
					normalizeOptionalNulls(array[index], itemSchema)
				}
			}
			return
		}
		if itemSchema := schemaSubObject(schema, "items"); itemSchema != nil {
			for _, item := range array {
				normalizeOptionalNulls(item, itemSchema)
			}
		}
		return
	}
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	properties := schemaSubObject(schema, "properties")
	if properties == nil {
		return
	}
	required := map[string]bool{}
	for _, name := range schemaSubArray(schema, "required") {
		if text, ok := name.(string); ok {
			required[text] = true
		}
	}
	for key, propertySchema := range properties {
		current, exists := object[key]
		if !exists {
			continue
		}
		objectSchema, _ := propertySchema.(map[string]any)
		if current == nil && !required[key] && !hasRef(objectSchema) && objectSchema != nil && !checkSchema(objectSchema, nil) {
			delete(object, key)
		} else if objectSchema != nil {
			normalizeOptionalNulls(object[key], objectSchema)
		}
	}
}

func hasRef(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	_, ok := schema["$ref"].(string)
	return ok
}

// checkSchema validates a value against the supported JSON Schema subset.
func checkSchema(schema map[string]any, value any) bool {
	if schema == nil {
		return true
	}

	if allOf := schemaSubArray(schema, "allOf"); allOf != nil {
		for _, nested := range allOf {
			if objectSchema, ok := nested.(map[string]any); ok && !checkSchema(objectSchema, value) {
				return false
			}
		}
	}
	if anyOf := schemaSubArray(schema, "anyOf"); anyOf != nil {
		matched := false
		for _, nested := range anyOf {
			if objectSchema, ok := nested.(map[string]any); ok && checkSchema(objectSchema, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if oneOf := schemaSubArray(schema, "oneOf"); oneOf != nil {
		matches := 0
		for _, nested := range oneOf {
			if objectSchema, ok := nested.(map[string]any); ok && checkSchema(objectSchema, value) {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}

	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflectEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if constant, ok := schema["const"]; ok {
		if !reflectEqual(constant, value) {
			return false
		}
	}

	schemaTypes := getSchemaTypes(schema)
	if len(schemaTypes) > 0 && !anyTypeMatches(value, schemaTypes) {
		return false
	}

	if object, ok := value.(map[string]any); ok {
		if required := schemaSubArray(schema, "required"); required != nil {
			for _, name := range required {
				text, ok := name.(string)
				if !ok {
					continue
				}
				if _, exists := object[text]; !exists {
					return false
				}
			}
		}
		if properties := schemaSubObject(schema, "properties"); properties != nil {
			for key, propertySchema := range properties {
				current, exists := object[key]
				if !exists {
					continue
				}
				if objectSchema, ok := propertySchema.(map[string]any); ok && !checkSchema(objectSchema, current) {
					return false
				}
			}
		}
		if additional, exists := schema["additionalProperties"]; exists {
			switch allowed := additional.(type) {
			case bool:
				if !allowed {
					properties := schemaSubObject(schema, "properties")
					for key := range object {
						if properties == nil {
							return false
						}
						if _, ok := properties[key]; !ok {
							return false
						}
					}
				}
			case map[string]any:
				properties := schemaSubObject(schema, "properties")
				for key, current := range object {
					if properties != nil {
						if _, ok := properties[key]; ok {
							continue
						}
					}
					if !checkSchema(allowed, current) {
						return false
					}
				}
			}
		}
	}

	if array, ok := value.([]any); ok {
		if items := schemaSubArray(schema, "items"); items != nil {
			for index, item := range array {
				if index >= len(items) {
					return false
				}
				if objectSchema, ok := items[index].(map[string]any); ok && !checkSchema(objectSchema, item) {
					return false
				}
			}
		} else if itemSchema := schemaSubObject(schema, "items"); itemSchema != nil {
			for _, item := range array {
				if !checkSchema(itemSchema, item) {
					return false
				}
			}
		}
	}

	return true
}

// collectValidationErrors produces a small set of path-qualified messages.
func collectValidationErrors(schema map[string]any, value any, path string) []string {
	if schema == nil {
		return nil
	}
	errors := []string{}
	if enum, ok := schema["enum"].([]any); ok {
		if !checkSchema(schema, value) {
			return []string{formatErrorPath(path) + ": must be equal to one of the allowed values"}
		}
		_ = enum
	}

	schemaTypes := getSchemaTypes(schema)
	if len(schemaTypes) > 0 && !anyTypeMatches(value, schemaTypes) {
		return []string{formatErrorPath(path) + ": must be " + schemaTypes[0]}
	}

	if object, ok := value.(map[string]any); ok {
		if required := schemaSubArray(schema, "required"); required != nil {
			for _, name := range required {
				text, ok := name.(string)
				if !ok {
					continue
				}
				if _, exists := object[text]; !exists {
					errors = append(errors, formatErrorPath(joinPath(path, text))+": must have required property")
				}
			}
		}
		if properties := schemaSubObject(schema, "properties"); properties != nil {
			keys := make([]string, 0, len(properties))
			for key := range properties {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				current, exists := object[key]
				if !exists {
					continue
				}
				if objectSchema, ok := properties[key].(map[string]any); ok {
					errors = append(errors, collectValidationErrors(objectSchema, current, joinPath(path, key))...)
				}
			}
		}
	}
	return errors
}

func joinPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}

func formatErrorPath(path string) string {
	if path == "" {
		return "root"
	}
	return path
}

func reflectEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

func deepClone(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoded, err := ParseJSONWithRepair(string(encoded))
	if err != nil {
		return value
	}
	return decoded
}
