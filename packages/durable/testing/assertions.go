package durabletesting

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// NativeAssertions returns the default StorageConformanceAssertions
// implementation. It reproduces the source semantics of createExpectAssertions
// (strict/deep/partial equality, greater-than and rejection) as native helpers
// over normalized JSON values instead of a Jest/Vitest shim.
func NativeAssertions() StorageConformanceAssertions { return nativeAssertions{} }

type nativeAssertions struct{}

func (nativeAssertions) OK(value bool, message string) error {
	if !value {
		if message == "" {
			message = "expected truthy value"
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}

func (nativeAssertions) StrictEqual(actual, expected any) error {
	left, err := normalizeJSON(actual)
	if err != nil {
		return fmt.Errorf("strictEqual normalize actual: %w", err)
	}
	right, err := normalizeJSON(expected)
	if err != nil {
		return fmt.Errorf("strictEqual normalize expected: %w", err)
	}
	if !reflect.DeepEqual(left, right) {
		return fmt.Errorf("strictEqual mismatch: got %s want %s", compactJSON(left), compactJSON(right))
	}
	return nil
}

func (nativeAssertions) DeepEqual(actual, expected any) error {
	left, err := normalizeJSON(actual)
	if err != nil {
		return fmt.Errorf("deepEqual normalize actual: %w", err)
	}
	right, err := normalizeJSON(expected)
	if err != nil {
		return fmt.Errorf("deepEqual normalize expected: %w", err)
	}
	if !reflect.DeepEqual(left, right) {
		return fmt.Errorf("deepEqual mismatch: got %s want %s", compactJSON(left), compactJSON(right))
	}
	return nil
}

func (nativeAssertions) PartialDeepEqual(actual, expected any) error {
	left, err := normalizeJSON(actual)
	if err != nil {
		return fmt.Errorf("partialDeepEqual normalize actual: %w", err)
	}
	right, err := normalizeJSON(expected)
	if err != nil {
		return fmt.Errorf("partialDeepEqual normalize expected: %w", err)
	}
	if !partialMatch(left, right) {
		return fmt.Errorf("partialDeepEqual mismatch: got %s does not match %s", compactJSON(left), compactJSON(right))
	}
	return nil
}

func (nativeAssertions) GreaterThan(actual, expected int64) error {
	if actual <= expected {
		return fmt.Errorf("greaterThan: %d is not greater than %d", actual, expected)
	}
	return nil
}

func (nativeAssertions) Rejects(err error, messageIncludes string) error {
	if err == nil {
		return fmt.Errorf("expected an error containing %q but none was returned", messageIncludes)
	}
	if !strings.Contains(err.Error(), messageIncludes) {
		return fmt.Errorf("error %q does not contain %q", err.Error(), messageIncludes)
	}
	return nil
}

// normalizeJSON converts any Go value into the canonical JSON shape
// (map[string]any / []any / scalar) so that structural comparisons ignore Go
// numeric and pointer representation differences, matching the source
// toEqual/toMatchObject semantics.
func normalizeJSON(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// partialMatch reports whether want is a structural subset of got, mirroring the
// source toMatchObject semantics for objects and arrays.
func partialMatch(got, want any) bool {
	switch wanted := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range wanted {
			other, present := actual[key]
			if !present || !partialMatch(other, value) {
				return false
			}
		}
		return true
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(wanted) {
			return false
		}
		for index := range wanted {
			if !partialMatch(actual[index], wanted[index]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(got, want)
	}
}

func compactJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%#v", value)
	}
	return string(data)
}

// isNilValue reports whether value is a nil pointer, slice, map or interface,
// used for the source toBeUndefined adaptations on detached record getters.
func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface:
		return reflected.IsNil()
	default:
		return false
	}
}
