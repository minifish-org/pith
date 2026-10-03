package delta

import "testing"

func TestDiffRevisionsRoundTrip(t *testing.T) {
	fixtures := []struct{ before, after any }{
		{map[string]any{"value": 1}, map[string]any{"value": 1}},
		{map[string]any{"x": 1, "obsolete": true}, map[string]any{"x": 2, "nested": []any{true, nil}}},
		{[]any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}, []any{map[string]any{"id": "b"}, map[string]any{"id": "a"}}},
		{map[string]any{"text": "hello😀你好"}, map[string]any{"text": "😀你好🌍"}},
		{true, []any{false, nil, 3}},
		{map[string]any{"keep": map[string]any{"deep": 1}}, map[string]any{"keep": map[string]any{"deep": 2}}},
	}
	for index, fixture := range fixtures {
		before := mustJSON(t, fixture.before)
		ops, err := DiffRevisions(fixture.before, fixture.after)
		if err != nil {
			t.Fatalf("case %d diff: %v", index, err)
		}
		if index == 0 && len(ops) != 0 {
			t.Fatal("equal revisions emitted operations")
		}
		got, err := ApplyImmutable(fixture.before, ops)
		if err != nil {
			t.Fatalf("case %d replay: %v", index, err)
		}
		deltaJSONEqual(t, got, fixture.after)
		if mustJSON(t, fixture.before) != before {
			t.Fatalf("case %d changed the prior revision", index)
		}
	}
}

func TestDiffRevisionsAppendOptimization(t *testing.T) {
	ops, err := DiffRevisions(map[string]any{"text": "abc"}, map[string]any{"text": "abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0][0] != "a" {
		t.Fatalf("expected a single append operation, got %v", ops)
	}
}

func TestDiffRevisionsRejectsNonJSON(t *testing.T) {
	if _, err := DiffRevisions(func() {}, map[string]any{}); err == nil {
		t.Fatal("non-JSON before accepted")
	}
	if _, err := DiffRevisions(map[string]any{}, make(chan int)); err == nil {
		t.Fatal("non-JSON after accepted")
	}
}
