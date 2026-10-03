// Package delta is the Go port of Chord's immutable revision engine
// (packages/chord/src/delta/index.ts and its siblings) at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// # Host/runtime format differences
//
// JavaScript values are dynamic; Go values are typed. The canonical strict-JSON
// runtime containers are map[string]any (object), []any (array), and the
// primitive Go values nil, bool, string and finite numbers. Every operation is
// persisted and exposed as the exact decoded Pi tuple vocabulary:
//
//	["r", value]
//	["s", path, value]
//	["d", path]
//	["a", path, string]
//	["t", path, count]
//	["p", path, index, remove, items]
//	["m", path, permutation]
//
// String offsets are counted in UTF-16 code units, matching the JavaScript
// implementation and the on-disk tuple format. Go strings are UTF-8 and cannot
// represent an isolated UTF-16 surrogate, so a truncation that would bisect a
// supplementary character is rejected with an error rather than corrupting the
// persisted value. This is an explicit and documented adaptation.
package delta

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"unicode/utf16"
)

// Path is one decoded tuple path: an ordered list of string object keys and
// non-negative numeric array indices.
type Path []any

// Op is one decoded delta operation tuple.
type Op []any

// WireOp is one encoded (compressed) delta operation tuple. It is only produced
// and consumed by the Encoder/Decoder pair.
type WireOp []any

var reservedSegments = map[string]bool{
	"__proto__":   true,
	"constructor": true,
	"prototype":   true,
}

var (
	errNotTuple   = errors.New("delta: op is not a tuple")
	errEmptyPath  = errors.New("delta: path is empty")
	errBadPath    = errors.New("delta: path is not an array")
	errUnsafePath = errors.New("delta: unsafe path segment")
	errCycle      = errors.New("delta: value contains cycles and is not strict JSON")
	errNotJSON    = errors.New("delta: value is not strict JSON")
)

func arityErr(op Op) error {
	return fmt.Errorf("delta: invalid arity for %v", op[0])
}

// IsReplace reports whether the operation replaces the whole value.
func IsReplace(op Op) bool {
	return len(op) > 0 && op[0] == "r"
}

// IsBase reports whether the batch begins with a replacement, making it a valid
// recovery point for a fresh decoder.
func IsBase(ops []Op) bool {
	return len(ops) > 0 && IsReplace(ops[0])
}

// asPath coerces a Go value carrying a path into a Path. It accepts the named
// Path type and a raw []any.
func asPath(value any) (Path, bool) {
	switch path := value.(type) {
	case Path:
		return path, true
	case []any:
		return Path(path), true
	case []string:
		out := make(Path, len(path))
		for i, seg := range path {
			out[i] = seg
		}
		return out, true
	default:
		return nil, false
	}
}

// asSlice coerces a Go value carrying a JSON array into []any.
func asSlice(value any) ([]any, bool) {
	switch items := value.(type) {
	case []any:
		return items, true
	case Op:
		return []any(items), true
	case Path:
		return []any(items), true
	default:
		return nil, false
	}
}

// toInt reports whether value is an integral number and returns it as an int.
func toInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		if n > math.MaxInt || n < math.MinInt {
			return 0, false
		}
		return int(n), true
	case uint:
		if uint64(n) > math.MaxInt {
			return 0, false
		}
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		if n > math.MaxInt {
			return 0, false
		}
		return int(n), true
	case float32:
		return floatToInt(float64(n))
	case float64:
		return floatToInt(n)
	case json.Number:
		if parsed, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			if parsed > math.MaxInt || parsed < math.MinInt {
				return 0, false
			}
			return int(parsed), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func floatToInt(value float64) (int, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	if value < math.MinInt || value > math.MaxInt {
		return 0, false
	}
	return int(value), true
}

// numberValue reports whether value is a finite numeric primitive and returns
// it as a JSON number (float64 or int64).
func numberValue(value any) (any, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return nil, false
		}
		return int64(n), true
	case float32:
		return finiteFloat(float64(n))
	case float64:
		return finiteFloat(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			if i, ierr := strconv.ParseInt(string(n), 10, 64); ierr == nil {
				return i, true
			}
			return nil, false
		}
		return finiteFloat(f)
	default:
		return nil, false
	}
}

func finiteFloat(value float64) (any, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, false
	}
	return value, true
}

// IsJSONValue reports whether value is finite strict JSON with map/array
// containers and no cycles.
func IsJSONValue(value any) bool {
	return isJSONValue(value, nil, nil)
}

func isJSONValue(value any, objectAncestors map[uintptr]bool, sliceAncestors map[[2]uintptr]bool) bool {
	switch typed := value.(type) {
	case nil, bool, string:
		return true
	case map[string]any:
		ptr := reflect.ValueOf(typed).Pointer()
		if objectAncestors == nil {
			objectAncestors = map[uintptr]bool{}
		}
		if objectAncestors[ptr] {
			return false
		}
		objectAncestors[ptr] = true
		for _, child := range typed {
			if !isJSONValue(child, objectAncestors, sliceAncestors) {
				delete(objectAncestors, ptr)
				return false
			}
		}
		delete(objectAncestors, ptr)
		return true
	case []any:
		ptr := reflect.ValueOf(typed).Pointer()
		key := [2]uintptr{ptr, uintptr(len(typed))}
		if sliceAncestors == nil {
			sliceAncestors = map[[2]uintptr]bool{}
		}
		if sliceAncestors[key] {
			return false
		}
		sliceAncestors[key] = true
		for _, child := range typed {
			if !isJSONValue(child, objectAncestors, sliceAncestors) {
				delete(sliceAncestors, key)
				return false
			}
		}
		delete(sliceAncestors, key)
		return true
	case Path:
		// A path is a JSON array of string keys and numeric indices; the named
		// type exists only to document intent.
		return isJSONValue([]any(typed), objectAncestors, sliceAncestors)
	case Op:
		return isJSONValue([]any(typed), objectAncestors, sliceAncestors)
	case WireOp:
		return isJSONValue([]any(typed), objectAncestors, sliceAncestors)
	default:
		_, ok := numberValue(value)
		return ok
	}
}

// CopyJSON copies a value into an alias-free strict-JSON tree owned by the
// caller. Each container occurrence is duplicated: a shared input child becomes
// independent output children.
func CopyJSON(value any) (any, error) {
	return copyJSON(value, nil, nil)
}

func copyJSON(value any, objectAncestors map[uintptr]bool, sliceAncestors map[[2]uintptr]bool) (any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case bool:
		return typed, nil
	case string:
		return typed, nil
	case map[string]any:
		ptr := reflect.ValueOf(typed).Pointer()
		if objectAncestors == nil {
			objectAncestors = map[uintptr]bool{}
		}
		if objectAncestors[ptr] {
			return nil, errCycle
		}
		objectAncestors[ptr] = true
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			copied, err := copyJSON(child, objectAncestors, sliceAncestors)
			if err != nil {
				return nil, err
			}
			out[key] = copied
		}
		delete(objectAncestors, ptr)
		return out, nil
	case []any:
		ptr := reflect.ValueOf(typed).Pointer()
		key := [2]uintptr{ptr, uintptr(len(typed))}
		if sliceAncestors == nil {
			sliceAncestors = map[[2]uintptr]bool{}
		}
		if sliceAncestors[key] {
			return nil, errCycle
		}
		sliceAncestors[key] = true
		out := make([]any, len(typed))
		for index, child := range typed {
			copied, err := copyJSON(child, objectAncestors, sliceAncestors)
			if err != nil {
				return nil, err
			}
			out[index] = copied
		}
		delete(sliceAncestors, key)
		return out, nil
	case Path:
		return copyJSON([]any(typed), objectAncestors, sliceAncestors)
	case Op:
		return copyJSON([]any(typed), objectAncestors, sliceAncestors)
	case WireOp:
		return copyJSON([]any(typed), objectAncestors, sliceAncestors)
	default:
		if number, ok := numberValue(value); ok {
			return number, nil
		}
		return nil, fmt.Errorf("%w: %T", errNotJSON, value)
	}
}

// cloneJSON is the internal alias-free deep copy used by the tracker and the
// immutable applier. It rejects non-strict-JSON values.
func cloneJSON(value any) (any, error) {
	return copyJSON(value, nil, nil)
}

// jsonEqual compares two strict-JSON values by value.
func jsonEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	switch l := left.(type) {
	case bool:
		r, ok := right.(bool)
		return ok && l == r
	case string:
		r, ok := right.(string)
		return ok && l == r
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for key, value := range l {
			other, present := r[key]
			if !present || !jsonEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for index := range l {
			if !jsonEqual(l[index], r[index]) {
				return false
			}
		}
		return true
	default:
		ln, lok := numberValue(left)
		rn, rok := numberValue(right)
		if !lok || !rok {
			return false
		}
		return numericEqual(ln, rn)
	}
}

func numericEqual(left, right any) bool {
	lf, ok := toFloat(left)
	if !ok {
		return false
	}
	rf, ok := toFloat(right)
	if !ok {
		return false
	}
	return lf == rf
}

func toFloat(value any) (float64, bool) {
	switch n := value.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// Overlap returns the length in UTF-16 code units of the longest suffix of a
// that is a prefix of b, limited to the requested scan window.
func Overlap(a, b string, scan int) int {
	if a == "" || b == "" || scan <= 0 {
		return 0
	}
	unitsA := utf16.Encode([]rune(a))
	unitsB := utf16.Encode([]rune(b))
	limit := len(unitsA)
	if scan < limit {
		limit = scan
	}
	if len(unitsB) < limit {
		limit = len(unitsB)
	}
	for n := limit; n > 0; n-- {
		match := true
		for i := 0; i < n; i++ {
			if unitsA[len(unitsA)-n+i] != unitsB[i] {
				match = false
				break
			}
		}
		if match {
			return n
		}
	}
	return 0
}

// utf16Length returns the number of UTF-16 code units in s.
func utf16Length(s string) int {
	units := 0
	for _, r := range s {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// truncateUTF16 removes count UTF-16 code units from the start of s. It rejects
// a count that would bisect a supplementary character because Go cannot
// represent the resulting isolated surrogate as valid UTF-8.
func truncateUTF16(s string, count int) (string, error) {
	if count <= 0 {
		return s, nil
	}
	units := 0
	for index, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > count {
			return "", fmt.Errorf("delta: truncation splits a supplementary character at UTF-16 offset %d", count)
		}
		units += width
		if units == count {
			return s[index+len(string(r)):], nil
		}
	}
	return "", nil
}

// sliceFromUTF16 returns the suffix of s beginning at the rune boundary
// corresponding to the given UTF-16 offset. The offset must already fall on a
// valid rune boundary in s.
func sliceFromUTF16(s string, offset int) (string, error) {
	if offset <= 0 {
		return s, nil
	}
	units := 0
	for index, r := range s {
		if units == offset {
			return s[index:], nil
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		if units > offset {
			return "", fmt.Errorf("delta: offset %d splits a supplementary character", offset)
		}
	}
	if units == offset {
		return "", nil
	}
	return "", fmt.Errorf("delta: offset %d is past the end of the string", offset)
}

// validatePathArg checks a decoded path argument.
func validatePathArg(value any, nonEmpty bool) error {
	path, ok := asPath(value)
	if !ok {
		return errBadPath
	}
	if nonEmpty && len(path) == 0 {
		return errEmptyPath
	}
	for _, segment := range path {
		if key, ok := segment.(string); ok {
			if reservedSegments[key] {
				return fmt.Errorf("%w: %s", errUnsafePath, key)
			}
			continue
		}
		index, ok := toInt(segment)
		if !ok || index < 0 {
			return fmt.Errorf("%w: %v", errUnsafePath, segment)
		}
	}
	return nil
}

func validatePermutation(value any) error {
	items, ok := asSlice(value)
	if !ok {
		return errors.New("delta: m permutation is not an array")
	}
	seen := make([]bool, len(items))
	for _, entry := range items {
		index, ok := toInt(entry)
		if !ok || index < 0 || index >= len(items) || seen[index] {
			return errors.New("delta: m permutation is not a bijection")
		}
		seen[index] = true
	}
	return nil
}

// validateOp validates one decoded operation against the verb, arity and payload
// shape of the Op vocabulary.
func validateOp(op Op) error {
	if len(op) == 0 {
		return errNotTuple
	}
	verb, ok := op[0].(string)
	if !ok {
		return errNotTuple
	}
	switch verb {
	case "r":
		if len(op) != 2 {
			return arityErr(op)
		}
		if !IsJSONValue(op[1]) {
			return fmt.Errorf("%w: replacement", errNotJSON)
		}
	case "s":
		if len(op) != 3 {
			return arityErr(op)
		}
		if err := validatePathArg(op[1], true); err != nil {
			return err
		}
		if !IsJSONValue(op[2]) {
			return fmt.Errorf("%w: set value", errNotJSON)
		}
	case "d":
		if len(op) != 2 {
			return arityErr(op)
		}
		if err := validatePathArg(op[1], true); err != nil {
			return err
		}
	case "a":
		if len(op) != 3 {
			return arityErr(op)
		}
		if err := validatePathArg(op[1], true); err != nil {
			return err
		}
		if _, ok := op[2].(string); !ok {
			return errors.New("delta: a value is not a string")
		}
	case "t":
		if len(op) != 3 {
			return arityErr(op)
		}
		if err := validatePathArg(op[1], true); err != nil {
			return err
		}
		if count, ok := toInt(op[2]); !ok || count < 0 {
			return errors.New("delta: t count is not a non-negative integer")
		}
	case "p":
		if len(op) != 5 {
			return arityErr(op)
		}
		if err := validatePathArg(op[1], false); err != nil {
			return err
		}
		if index, ok := toInt(op[2]); !ok || index < 0 {
			return errors.New("delta: p index is not a non-negative integer")
		}
		if remove, ok := toInt(op[3]); !ok || remove < 0 {
			return errors.New("delta: p remove is not a non-negative integer")
		}
		items, ok := asSlice(op[4])
		if !ok {
			return errors.New("delta: p items is not an array")
		}
		if !IsJSONValue(items) {
			return fmt.Errorf("%w: p items", errNotJSON)
		}
	case "m":
		if len(op) != 3 {
			return arityErr(op)
		}
		if err := validatePathArg(op[1], false); err != nil {
			return err
		}
		if err := validatePermutation(op[2]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("delta: unknown op verb: %v", op[0])
	}
	return nil
}

// resolveNode walks path from root, requiring every intermediate container and
// own property to exist.
func resolveNode(root any, path Path) (any, error) {
	node := root
	for _, segment := range path {
		switch container := node.(type) {
		case map[string]any:
			key, ok := segment.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %v", errUnsafePath, segment)
			}
			child, present := container[key]
			if !present {
				return nil, fmt.Errorf("delta: unresolvable path: %v", path)
			}
			node = child
		case []any:
			index, ok := toInt(segment)
			if !ok {
				return nil, fmt.Errorf("%w: %v", errUnsafePath, segment)
			}
			if index < 0 || index >= len(container) {
				return nil, fmt.Errorf("delta: unresolvable path: %v", path)
			}
			node = container[index]
		default:
			return nil, fmt.Errorf("delta: unresolvable path: %v", path)
		}
	}
	return node, nil
}

// replaceAt writes value at path, rebuilding slice headers along the way.
func replaceAt(node any, path Path, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	segment := path[0]
	switch container := node.(type) {
	case map[string]any:
		key, ok := segment.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %v", errUnsafePath, segment)
		}
		child, present := container[key]
		if !present {
			return nil, fmt.Errorf("delta: unresolvable path: %v", path)
		}
		updated, err := replaceAt(child, path[1:], value)
		if err != nil {
			return nil, err
		}
		container[key] = updated
		return container, nil
	case []any:
		index, ok := toInt(segment)
		if !ok || index < 0 || index >= len(container) {
			return nil, fmt.Errorf("delta: unresolvable path: %v", path)
		}
		updated, err := replaceAt(container[index], path[1:], value)
		if err != nil {
			return nil, err
		}
		container[index] = updated
		return container, nil
	default:
		return nil, fmt.Errorf("delta: unresolvable path: %v", path)
	}
}

// mutateKey descends to the parent of the final path segment and applies fn to
// it, returning the possibly-rebuilt container chain.
func mutateKey(node any, path Path, fn func(parent any, key any) (any, error)) (any, error) {
	if len(path) == 0 {
		return nil, errEmptyPath
	}
	if len(path) == 1 {
		return fn(node, path[0])
	}
	segment := path[0]
	switch container := node.(type) {
	case map[string]any:
		key, ok := segment.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %v", errUnsafePath, segment)
		}
		child, present := container[key]
		if !present {
			return nil, fmt.Errorf("delta: unresolvable path: %v", path)
		}
		updated, err := mutateKey(child, path[1:], fn)
		if err != nil {
			return nil, err
		}
		container[key] = updated
		return container, nil
	case []any:
		index, ok := toInt(segment)
		if !ok || index < 0 || index >= len(container) {
			return nil, fmt.Errorf("delta: unresolvable path: %v", path)
		}
		updated, err := mutateKey(container[index], path[1:], fn)
		if err != nil {
			return nil, err
		}
		container[index] = updated
		return container, nil
	default:
		return nil, fmt.Errorf("delta: unresolvable path: %v", path)
	}
}

func spliceArray(target []any, start, remove int, items []any) ([]any, error) {
	if start < 0 || remove < 0 || start > len(target) {
		return nil, fmt.Errorf("delta: invalid splice: %d/%d", start, remove)
	}
	if remove > len(target)-start {
		remove = len(target) - start
	}
	out := make([]any, 0, len(target)-remove+len(items))
	out = append(out, target[:start]...)
	out = append(out, items...)
	out = append(out, target[start+remove:]...)
	return out, nil
}

func permuteArray(target []any, permutation []any) ([]any, error) {
	if len(target) != len(permutation) {
		return nil, errors.New("delta: m permutation length mismatch")
	}
	previous := make([]any, len(target))
	copy(previous, target)
	out := make([]any, len(target))
	for index, entry := range permutation {
		position, ok := toInt(entry)
		if !ok || position < 0 || position >= len(target) {
			return nil, errors.New("delta: m permutation is not a bijection")
		}
		out[index] = previous[position]
	}
	return out, nil
}

// applyOne applies a single validated operation to root.
func applyOne(root any, op Op) (any, error) {
	verb := op[0].(string)
	if verb == "r" {
		return op[1], nil
	}
	path, _ := asPath(op[1])
	switch verb {
	case "p":
		node, err := resolveNode(root, path)
		if err != nil {
			return nil, err
		}
		target, ok := node.([]any)
		if !ok {
			return nil, fmt.Errorf("delta: splice target is not an array: %v", path)
		}
		start, _ := toInt(op[2])
		remove, _ := toInt(op[3])
		items, _ := asSlice(op[4])
		updated, err := spliceArray(target, start, remove, items)
		if err != nil {
			return nil, err
		}
		return replaceAt(root, path, updated)
	case "m":
		node, err := resolveNode(root, path)
		if err != nil {
			return nil, err
		}
		target, ok := node.([]any)
		if !ok {
			return nil, fmt.Errorf("delta: permutation target is not an array: %v", path)
		}
		permutation, _ := asSlice(op[2])
		updated, err := permuteArray(target, permutation)
		if err != nil {
			return nil, err
		}
		return replaceAt(root, path, updated)
	case "s":
		return mutateKey(root, path, func(parent any, key any) (any, error) {
			switch container := parent.(type) {
			case map[string]any:
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("%w: %v", errUnsafePath, key)
				}
				container[name] = op[2]
				return container, nil
			case []any:
				index, ok := toInt(key)
				if !ok {
					return nil, fmt.Errorf("%w: %v", errUnsafePath, key)
				}
				if index > len(container) {
					return nil, fmt.Errorf("delta: sparse array write at index %d", index)
				}
				if index == len(container) {
					container = append(container, op[2])
				} else {
					container[index] = op[2]
				}
				return container, nil
			default:
				return nil, fmt.Errorf("delta: unresolvable path: %v", path)
			}
		})
	case "d":
		return mutateKey(root, path, func(parent any, key any) (any, error) {
			switch container := parent.(type) {
			case map[string]any:
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("%w: %v", errUnsafePath, key)
				}
				delete(container, name)
				return container, nil
			case []any:
				index, ok := toInt(key)
				if !ok {
					return nil, fmt.Errorf("%w: %v", errUnsafePath, key)
				}
				if index < 0 || index >= len(container) {
					return nil, fmt.Errorf("delta: unresolvable path: %v", path)
				}
				container = append(container[:index], container[index+1:]...)
				return container, nil
			default:
				return nil, fmt.Errorf("delta: unresolvable path: %v", path)
			}
		})
	case "a":
		return mutateKey(root, path, func(parent any, key any) (any, error) {
			current, err := readStringChild(parent, key, path)
			if err != nil {
				return nil, err
			}
			return writeChild(parent, key, current+op[2].(string), path)
		})
	case "t":
		return mutateKey(root, path, func(parent any, key any) (any, error) {
			current, err := readStringChild(parent, key, path)
			if err != nil {
				return nil, err
			}
			count, _ := toInt(op[2])
			truncated, err := truncateUTF16(current, count)
			if err != nil {
				return nil, err
			}
			return writeChild(parent, key, truncated, path)
		})
	default:
		return nil, fmt.Errorf("delta: unknown op verb: %v", op[0])
	}
}

func readStringChild(parent any, key any, path Path) (string, error) {
	switch container := parent.(type) {
	case map[string]any:
		name, ok := key.(string)
		if !ok {
			return "", fmt.Errorf("%w: %v", errUnsafePath, key)
		}
		value, present := container[name]
		if !present {
			return "", fmt.Errorf("delta: unresolvable path: %v", path)
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("delta: string operation target is not a string: %v", path)
		}
		return text, nil
	case []any:
		index, ok := toInt(key)
		if !ok {
			return "", fmt.Errorf("%w: %v", errUnsafePath, key)
		}
		if index < 0 || index >= len(container) {
			return "", fmt.Errorf("delta: unresolvable path: %v", path)
		}
		text, ok := container[index].(string)
		if !ok {
			return "", fmt.Errorf("delta: string operation target is not a string: %v", path)
		}
		return text, nil
	default:
		return "", fmt.Errorf("delta: unresolvable path: %v", path)
	}
}

func writeChild(parent any, key any, value string, path Path) (any, error) {
	switch container := parent.(type) {
	case map[string]any:
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %v", errUnsafePath, key)
		}
		container[name] = value
		return container, nil
	case []any:
		index, ok := toInt(key)
		if !ok || index < 0 || index >= len(container) {
			return nil, fmt.Errorf("delta: unresolvable path: %v", path)
		}
		container[index] = value
		return container, nil
	default:
		return nil, fmt.Errorf("delta: unresolvable path: %v", path)
	}
}

// Apply applies decoded operations to a caller-owned value. It may mutate the
// supplied value and adopts tuple payload ownership.
func Apply(target any, ops []Op) (any, error) {
	root := target
	for _, op := range ops {
		if err := validateOp(op); err != nil {
			return nil, err
		}
		updated, err := applyOne(root, op)
		if err != nil {
			return nil, err
		}
		root = updated
	}
	return root, nil
}

// ApplyImmutable applies decoded operations without mutating the previous
// revision. The returned value is independent of the input.
func ApplyImmutable(target any, ops []Op) (any, error) {
	return ApplyImmutableBatches(target, [][]Op{ops})
}

// ApplyImmutableBatches applies decoded operation batches as one
// final-result-only replay. Earlier revisions are left unchanged.
func ApplyImmutableBatches(target any, batches [][]Op) (any, error) {
	root, err := cloneJSON(target)
	if err != nil {
		return nil, err
	}
	for _, ops := range batches {
		for _, op := range ops {
			if err := validateOp(op); err != nil {
				return nil, err
			}
			if IsReplace(op) {
				detached, err := cloneJSON(op[1])
				if err != nil {
					return nil, err
				}
				root = detached
				continue
			}
			updated, err := applyOne(root, op)
			if err != nil {
				return nil, err
			}
			root = updated
		}
	}
	return root, nil
}
