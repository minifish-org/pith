package delta

import "strings"

const defaultOverlapScan = 65536

// DiffRevisions computes an operation batch that replays from before to after.
// Equal revisions produce an empty batch. The generated batch is correct but not
// required to be minimal: replaying the returned operations with ApplyImmutable
// yields a value equal to after.
func DiffRevisions(before, after any) ([]Op, error) {
	if !IsJSONValue(before) {
		return nil, errNotJSON
	}
	if !IsJSONValue(after) {
		return nil, errNotJSON
	}
	ops := []Op{}
	if err := diffValue(before, after, nil, &ops); err != nil {
		return nil, err
	}
	return ops, nil
}

func appendSeg(path Path, key string) Path {
	out := make(Path, len(path)+1)
	copy(out, path)
	out[len(path)] = key
	return out
}

func emitSet(path Path, value any, ops *[]Op) {
	if len(path) == 0 {
		*ops = append(*ops, Op{"r", value})
		return
	}
	*ops = append(*ops, Op{"s", path, value})
}

func diffValue(before, after any, path Path, ops *[]Op) error {
	if jsonEqual(before, after) {
		return nil
	}
	if beforeString, ok := before.(string); ok {
		if afterString, ok := after.(string); ok && len(path) > 0 {
			return emitString(beforeString, afterString, path, ops)
		}
	}
	if beforeArray, ok := before.([]any); ok {
		if afterArray, ok := after.([]any); ok {
			return diffArray(beforeArray, afterArray, path, ops)
		}
	}
	if beforeObject, ok := before.(map[string]any); ok {
		if afterObject, ok := after.(map[string]any); ok {
			return diffObject(beforeObject, afterObject, path, ops)
		}
	}
	emitSet(path, after, ops)
	return nil
}

func diffObject(before, after map[string]any, path Path, ops *[]Op) error {
	for key := range before {
		if reservedSegments[key] {
			emitSet(path, after, ops)
			return nil
		}
	}
	for key := range after {
		if reservedSegments[key] {
			emitSet(path, after, ops)
			return nil
		}
	}
	for key, afterValue := range after {
		beforeValue, present := before[key]
		if present {
			if err := diffValue(beforeValue, afterValue, appendSeg(path, key), ops); err != nil {
				return err
			}
			continue
		}
		emitSet(appendSeg(path, key), afterValue, ops)
	}
	for key := range before {
		if _, present := after[key]; !present {
			*ops = append(*ops, Op{"d", appendSeg(path, key)})
		}
	}
	return nil
}

func diffArray(before, after []any, path Path, ops *[]Op) error {
	if len(before) == len(after) && len(before) > 1 {
		if permutation, ok := permutationOf(before, after); ok {
			*ops = append(*ops, Op{"m", path, permutation})
			return nil
		}
	}
	items := make([]any, len(after))
	copy(items, after)
	*ops = append(*ops, Op{"p", path, 0, len(before), items})
	return nil
}

func permutationOf(before, after []any) ([]any, bool) {
	if len(before) != len(after) {
		return nil, false
	}
	used := make([]bool, len(before))
	permutation := make([]any, len(after))
	for index := range after {
		found := false
		for candidate := range before {
			if !used[candidate] && jsonEqual(before[candidate], after[index]) {
				used[candidate] = true
				permutation[index] = candidate
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return permutation, true
}

func emitString(before, after string, path Path, ops *[]Op) error {
	if before == after {
		return nil
	}
	beforeUnits := utf16Length(before)
	afterUnits := utf16Length(after)
	if afterUnits > beforeUnits && strings.HasPrefix(after, before) {
		*ops = append(*ops, Op{"a", path, after[len(before):]})
		return nil
	}
	shared := Overlap(before, after, defaultOverlapScan)
	if shared == 0 {
		*ops = append(*ops, Op{"s", path, after})
		return nil
	}
	if beforeUnits > shared {
		*ops = append(*ops, Op{"t", path, beforeUnits - shared})
	}
	if afterUnits > shared {
		suffix, err := sliceFromUTF16(after, shared)
		if err != nil {
			return err
		}
		if suffix != "" {
			*ops = append(*ops, Op{"a", path, suffix})
		}
	}
	return nil
}
