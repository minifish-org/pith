package delta

import (
	"encoding/json"
	"testing"
)

func deltaJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != string(w) {
		t.Fatalf("JSON mismatch: got %s, want %s", g, w)
	}
}

func TestApplyVocabulary(t *testing.T) {
	base := map[string]any{"text": "abcdefgh", "rows": []any{"a", "b", "c"}, "obsolete": true}
	ops := []Op{
		{"s", Path{"new"}, map[string]any{"ok": true}},
		{"d", Path{"obsolete"}},
		{"t", Path{"text"}, 3},
		{"a", Path{"text"}, "xyz"},
		{"p", Path{"rows"}, 1, 1, []any{"d", "e"}},
		{"m", Path{"rows"}, []any{3, 2, 1, 0}},
	}
	got, err := ApplyImmutable(base, ops)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, got, map[string]any{"text": "defghxyz", "rows": []any{"c", "e", "d", "a"}, "new": map[string]any{"ok": true}})
	deltaJSONEqual(t, base, map[string]any{"text": "abcdefgh", "rows": []any{"a", "b", "c"}, "obsolete": true})
}

func TestApplyRootReplacementAndSplice(t *testing.T) {
	root, err := Apply(nil, []Op{{"r", []any{"a", "b"}}, {"p", Path{}, 0, 1, []any{"x"}}, {"m", Path{}, []any{1, 0}}})
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, root, []any{"b", "x"})
	deleted, err := Apply([]any{"a", "b", "c"}, []Op{{"d", Path{1}}})
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, deleted, []any{"a", "c"})
}

func TestApplyRejectsUnsafeOperations(t *testing.T) {
	fixtures := []struct {
		name string
		op   Op
	}{
		{"unknown", Op{"newer", Path{"text"}, true}},
		{"bad_arity", Op{"s", Path{"text"}}},
		{"root_set", Op{"s", Path{}, true}},
		{"root_delete", Op{"d", Path{}}},
		{"root_append", Op{"a", Path{}, "x"}},
		{"negative", Op{"s", Path{"rows", -1}, true}},
		{"fractional", Op{"s", Path{"rows", 0.5}, true}},
		{"sparse", Op{"s", Path{"rows", 9}, true}},
		{"array_string_key", Op{"s", Path{"rows", "0"}, true}},
		{"reserved_proto", Op{"s", Path{"__proto__", "polluted"}, true}},
		{"reserved_constructor", Op{"s", Path{"constructor"}, true}},
		{"reserved_prototype", Op{"s", Path{"prototype"}, true}},
		{"unresolved", Op{"s", Path{"missing", "child"}, true}},
		{"append_nonstring", Op{"a", Path{"rows"}, "x"}},
		{"truncate_negative", Op{"t", Path{"text"}, -1}},
		{"splice_nonarray", Op{"p", Path{"text"}, 0, 1, []any{}}},
		{"permutation_duplicate", Op{"m", Path{"rows"}, []any{0, 0}}},
		{"permutation_length", Op{"m", Path{"rows"}, []any{0}}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			base := map[string]any{"text": "hello", "rows": []any{1, 2}}
			before := mustJSON(t, base)
			if _, err := ApplyImmutable(base, []Op{fixture.op}); err == nil {
				t.Fatalf("invalid operation accepted: %v", fixture.op)
			}
			if mustJSON(t, base) != before {
				t.Fatal("failed immutable replay changed the prior revision")
			}
		})
	}
}

func TestApplyUnicodeTruncationAndBatches(t *testing.T) {
	base := map[string]any{"text": "😀你好", "nested": map[string]any{"value": 1}}
	got, err := ApplyImmutableBatches(base, [][]Op{
		{{"t", Path{"text"}, 2}},
		{{"a", Path{"text"}, "🌍"}, {"s", Path{"nested", "value"}, 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, got, map[string]any{"text": "你好🌍", "nested": map[string]any{"value": 2}})
	deltaJSONEqual(t, base, map[string]any{"text": "😀你好", "nested": map[string]any{"value": 1}})
	if _, err := ApplyImmutable(base, []Op{{"t", Path{"text"}, 1}}); err == nil {
		t.Fatal("truncation bisecting a supplementary character was accepted")
	}
	if _, err := ApplyImmutable(base, []Op{{"t", Path{"text"}, 99}}); err != nil {
		t.Fatalf("truncation past the end should yield the empty string: %v", err)
	}
}

func TestOverlapUsesUTF16UnitsAndScanWindow(t *testing.T) {
	fixtures := []struct {
		a, b       string
		scan, want int
	}{
		{"abcXYZ", "XYZmore", 6, 3},
		{"abcXYZ", "XYZmore", 2, 0},
		{"tail😀你好", "😀你好world", 10, 4},
		{"aaa", "aaa", 0, 0},
		{"", "anything", 10, 0},
		{"anything", "", 10, 0},
	}
	for _, fixture := range fixtures {
		if got := Overlap(fixture.a, fixture.b, fixture.scan); got != fixture.want {
			t.Fatalf("Overlap(%q,%q,%d)=%d want %d", fixture.a, fixture.b, fixture.scan, got, fixture.want)
		}
	}
}

func TestApplyMutableDoesNotAliasPriorRevision(t *testing.T) {
	base := map[string]any{"rows": []any{1, 2, 3}}
	before := mustJSON(t, base)
	if _, err := Apply(base, []Op{{"p", Path{"rows"}, 0, 1, []any{9}}}); err != nil {
		t.Fatal(err)
	}
	// Apply is allowed to mutate the caller-owned target; the immutable path must
	// still leave the original untouched.
	base2 := map[string]any{"rows": []any{1, 2, 3}}
	if _, err := ApplyImmutable(base2, []Op{{"p", Path{"rows"}, 0, 1, []any{9}}}); err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, base2) != before {
		t.Fatal("ApplyImmutable mutated the input")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	bytes, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes)
}
