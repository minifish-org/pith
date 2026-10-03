package delta_test

import (
	"encoding/json"
	"testing"

	"github.com/minifish-org/pith/packages/chord/delta"
)

func durableDeltaJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func durableDeltaEqual(t *testing.T, got, want any) {
	t.Helper()
	if g, w := durableDeltaJSON(t, got), durableDeltaJSON(t, want); g != w {
		t.Fatalf("JSON mismatch: got %s, want %s", g, w)
	}
}

func TestPortsmithJudgeDurableDeltaVocabulary(t *testing.T) {
	base := map[string]any{"text": "abcdefgh", "rows": []any{"a", "b", "c"}, "obsolete": true}
	ops := []delta.Op{
		{"s", delta.Path{"new"}, map[string]any{"ok": true}},
		{"d", delta.Path{"obsolete"}},
		{"t", delta.Path{"text"}, 3},
		{"a", delta.Path{"text"}, "xyz"},
		{"p", delta.Path{"rows"}, 1, 1, []any{"d", "e"}},
		{"m", delta.Path{"rows"}, []any{3, 2, 1, 0}},
	}
	got, err := delta.ApplyImmutable(base, ops)
	if err != nil {
		t.Fatal(err)
	}
	durableDeltaEqual(t, got, map[string]any{"text": "defghxyz", "rows": []any{"c", "e", "d", "a"}, "new": map[string]any{"ok": true}})
	durableDeltaEqual(t, base, map[string]any{"text": "abcdefgh", "rows": []any{"a", "b", "c"}, "obsolete": true})
	root, err := delta.Apply(nil, []delta.Op{{"r", []any{"a", "b"}}, {"p", delta.Path{}, 0, 1, []any{"x"}}, {"m", delta.Path{}, []any{1, 0}}})
	if err != nil {
		t.Fatal(err)
	}
	durableDeltaEqual(t, root, []any{"b", "x"})
	deleted, err := delta.Apply([]any{"a", "b", "c"}, []delta.Op{{"d", delta.Path{1}}})
	if err != nil {
		t.Fatal(err)
	}
	durableDeltaEqual(t, deleted, []any{"a", "c"})
}

func TestPortsmithJudgeDurableDeltaRejectsUnsafeOperations(t *testing.T) {
	fixtures := []struct {
		name string
		op   delta.Op
	}{
		{"unknown", delta.Op{"newer", delta.Path{"text"}, true}},
		{"bad_arity", delta.Op{"s", delta.Path{"text"}}},
		{"root_set", delta.Op{"s", delta.Path{}, true}},
		{"negative", delta.Op{"s", delta.Path{"rows", -1}, true}},
		{"fractional", delta.Op{"s", delta.Path{"rows", 0.5}, true}},
		{"sparse", delta.Op{"s", delta.Path{"rows", 9}, true}},
		{"array_string_key", delta.Op{"s", delta.Path{"rows", "0"}, true}},
		{"reserved_proto", delta.Op{"s", delta.Path{"__proto__", "polluted"}, true}},
		{"reserved_constructor", delta.Op{"s", delta.Path{"constructor"}, true}},
		{"reserved_prototype", delta.Op{"s", delta.Path{"prototype"}, true}},
		{"unresolved", delta.Op{"s", delta.Path{"missing", "child"}, true}},
		{"append_nonstring", delta.Op{"a", delta.Path{"rows"}, "x"}},
		{"truncate_negative", delta.Op{"t", delta.Path{"text"}, -1}},
		{"splice_nonarray", delta.Op{"p", delta.Path{"text"}, 0, 1, []any{}}},
		{"permutation_duplicate", delta.Op{"m", delta.Path{"rows"}, []any{0, 0}}},
		{"permutation_length", delta.Op{"m", delta.Path{"rows"}, []any{0}}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			base := map[string]any{"text": "hello", "rows": []any{1, 2}}
			before := durableDeltaJSON(t, base)
			if _, err := delta.ApplyImmutable(base, []delta.Op{fixture.op}); err == nil {
				t.Fatalf("invalid operation accepted: %v", fixture.op)
			}
			if durableDeltaJSON(t, base) != before {
				t.Fatal("failed immutable replay changed prior revision")
			}
		})
	}
}

func TestPortsmithJudgeDurableDeltaUnicodeAndBatches(t *testing.T) {
	base := map[string]any{"text": "😀你好", "nested": map[string]any{"value": 1}}
	got, err := delta.ApplyImmutableBatches(base, [][]delta.Op{
		{{"t", delta.Path{"text"}, 2}},
		{{"a", delta.Path{"text"}, "🌍"}, {"s", delta.Path{"nested", "value"}, 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	durableDeltaEqual(t, got, map[string]any{"text": "你好🌍", "nested": map[string]any{"value": 2}})
	durableDeltaEqual(t, base, map[string]any{"text": "😀你好", "nested": map[string]any{"value": 1}})
	if _, err := delta.ApplyImmutable(base, []delta.Op{{"t", delta.Path{"text"}, 1}}); err == nil {
		t.Fatal("UTF-16 truncation split a supplementary character without reporting unsupported boundary")
	}
	for _, fixture := range []struct {
		a, b       string
		scan, want int
	}{
		{"abcXYZ", "XYZmore", 6, 3}, {"abcXYZ", "XYZmore", 2, 0},
		{"tail😀你好", "😀你好world", 10, 4}, {"aaa", "aaa", 0, 0},
	} {
		if got := delta.Overlap(fixture.a, fixture.b, fixture.scan); got != fixture.want {
			t.Fatalf("overlap(%q,%q,%d)=%d want %d UTF-16 units", fixture.a, fixture.b, fixture.scan, got, fixture.want)
		}
	}
}

func TestPortsmithJudgeDurableDeltaDiffRoundTrip(t *testing.T) {
	fixtures := []struct{ before, after any }{
		{map[string]any{"value": 1}, map[string]any{"value": 1}},
		{map[string]any{"x": 1, "obsolete": true}, map[string]any{"x": 2, "nested": []any{true, nil}}},
		{[]any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}, []any{map[string]any{"id": "b"}, map[string]any{"id": "a"}}},
		{map[string]any{"text": "hello😀你好"}, map[string]any{"text": "😀你好🌍"}},
		{true, []any{false, nil, 3}},
	}
	for i, fixture := range fixtures {
		before := durableDeltaJSON(t, fixture.before)
		ops, err := delta.DiffRevisions(fixture.before, fixture.after)
		if err != nil {
			t.Fatalf("case %d diff: %v", i, err)
		}
		if i == 0 && len(ops) != 0 {
			t.Fatal("equal revisions emitted operations")
		}
		got, err := delta.ApplyImmutable(fixture.before, ops)
		if err != nil {
			t.Fatalf("case %d replay: %v", i, err)
		}
		durableDeltaEqual(t, got, fixture.after)
		if durableDeltaJSON(t, fixture.before) != before {
			t.Fatal("diff/replay changed prior revision")
		}
	}
}

func TestPortsmithJudgeDurableTrackerLifecycle(t *testing.T) {
	tracker, err := delta.Track(map[string]any{"nested": map[string]any{"value": 1}})
	if err != nil {
		t.Fatal(err)
	}
	change, err := tracker.BeginChange()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := change.Value()
	if err != nil {
		t.Fatal(err)
	}
	draft.(map[string]any)["nested"].(map[string]any)["value"] = 2
	prepared, err := change.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if prepared.BaseRevision() != 0 || tracker.Revision() != 0 {
		t.Fatal("preparation advanced authority")
	}
	durableDeltaEqual(t, prepared.Base(), map[string]any{"nested": map[string]any{"value": 1}})
	durableDeltaEqual(t, prepared.Value(), map[string]any{"nested": map[string]any{"value": 2}})
	durableDeltaEqual(t, tracker.Value(), map[string]any{"nested": map[string]any{"value": 1}})
	replayed, err := delta.ApplyImmutable(prepared.Base(), prepared.Ops())
	if err != nil {
		t.Fatal(err)
	}
	durableDeltaEqual(t, replayed, prepared.Value())
	draft.(map[string]any)["nested"].(map[string]any)["value"] = 999
	durableDeltaEqual(t, prepared.Value(), map[string]any{"nested": map[string]any{"value": 2}})
	if _, err := change.Value(); err == nil {
		t.Fatal("settled change handle remains usable")
	}
	if err := tracker.Adopt(prepared); err != nil {
		t.Fatal(err)
	}
	if tracker.Revision() != 1 {
		t.Fatal("adoption did not advance revision exactly once")
	}
	durableDeltaEqual(t, tracker.Value(), map[string]any{"nested": map[string]any{"value": 2}})
	if err := tracker.Adopt(prepared); err == nil {
		t.Fatal("prepared candidate adopted twice")
	}
	public := tracker.Value().(map[string]any)
	public["nested"].(map[string]any)["value"] = 500
	durableDeltaEqual(t, tracker.Value(), map[string]any{"nested": map[string]any{"value": 2}})
}

func TestPortsmithJudgeDurableTrackerRejectsForeignStaleAndAborted(t *testing.T) {
	newTracker := func() *delta.Tracker {
		t.Helper()
		v, e := delta.Track(map[string]any{"value": 0})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	a, b := newTracker(), newTracker()
	foreign, e := a.PrepareReplace(map[string]any{"value": 1})
	if e != nil {
		t.Fatal(e)
	}
	if e := b.Adopt(foreign); e == nil {
		t.Fatal("foreign owner accepted")
	}
	first, e := a.PrepareReplace(map[string]any{"value": 2})
	if e != nil {
		t.Fatal(e)
	}
	second, e := a.PrepareReplace(map[string]any{"value": 3})
	if e != nil {
		t.Fatal(e)
	}
	open, e := a.BeginChange()
	if e != nil {
		t.Fatal(e)
	}
	if e := a.Adopt(first); e != nil {
		t.Fatal(e)
	}
	if e := a.Adopt(second); e == nil {
		t.Fatal("stale prepared revision accepted")
	}
	if _, e := open.Prepare(); e == nil {
		t.Fatal("stale open draft prepared")
	}
	aborted, e := a.PrepareReplace(map[string]any{"value": 9})
	if e != nil {
		t.Fatal(e)
	}
	aborted.Abort()
	aborted.Abort()
	if e := a.Adopt(aborted); e == nil {
		t.Fatal("aborted candidate adopted")
	}
	change, e := a.BeginChange()
	if e != nil {
		t.Fatal(e)
	}
	change.Abort()
	change.Abort()
	if _, e := change.Prepare(); e == nil {
		t.Fatal("aborted draft prepared")
	}
	durableDeltaEqual(t, a.Value(), map[string]any{"value": 2})
	noop, e := a.BeginChange()
	if e != nil {
		t.Fatal(e)
	}
	p, e := noop.Prepare()
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Ops()) != 0 {
		t.Fatal("unchanged draft emitted operations")
	}
	if e := a.Adopt(p); e != nil {
		t.Fatal(e)
	}
	if a.Revision() != 2 {
		t.Fatal("accepted no-op candidate revision mismatch")
	}
	for _, root := range []any{nil, "scalar", true, 2} {
		if _, e := delta.Track(root); e == nil {
			t.Fatalf("non-container tracker root accepted: %v", root)
		}
	}
}

func TestPortsmithJudgeDurableDeltaCodecRecovery(t *testing.T) {
	encoder, decoder := delta.NewEncoder(), delta.NewDecoder()
	first := []delta.Op{{"s", delta.Path{"nested", "text"}, "a"}, {"a", delta.Path{"nested", "text"}, "b"}}
	beforeFirst := durableDeltaJSON(t, first)
	wire, e := encoder.Encode(first)
	if e != nil {
		t.Fatal(e)
	}
	durableDeltaEqual(t, wire, []any{[]any{"s", []any{"nested", "text"}, "a"}, []any{"a", "b"}})
	decoded, e := decoder.Decode(wire)
	if e != nil {
		t.Fatal(e)
	}
	durableDeltaEqual(t, decoded, first)
	second := []delta.Op{{"t", delta.Path{"nested", "text"}, 1}}
	wire, e = encoder.Encode(second)
	if e != nil {
		t.Fatal(e)
	}
	durableDeltaEqual(t, wire, []any{[]any{"#", 0, []any{"nested", "text"}}, []any{"t", 0, 1}})
	decoded, e = decoder.Decode(wire)
	if e != nil {
		t.Fatal(e)
	}
	durableDeltaEqual(t, decoded, second)
	base := []delta.Op{{"r", map[string]any{"nested": map[string]any{"text": "new"}}}, {"a", delta.Path{"nested", "text"}, "!"}}
	wire, e = encoder.Encode(base)
	if e != nil {
		t.Fatal(e)
	}
	fresh := delta.NewDecoder()
	decoded, e = fresh.Decode(wire)
	if e != nil {
		t.Fatal(e)
	}
	durableDeltaEqual(t, decoded, base)
	if !delta.IsBase(base) || !delta.IsReplace(base[0]) || delta.IsBase(first) || delta.IsReplace(first[0]) || delta.IsBase(nil) {
		t.Fatal("replacement classification mismatch")
	}
	for _, bad := range [][]delta.WireOp{
		{{"a", "omitted-first-path"}}, {{"s", 77, true}}, {{"#", 0, delta.Path{"__proto__"}}, {"s", 0, true}}, {{"unknown"}},
	} {
		if _, e := delta.NewDecoder().Decode(bad); e == nil {
			t.Fatalf("invalid wire batch accepted: %v", bad)
		}
	}
	if durableDeltaJSON(t, first) != beforeFirst {
		t.Fatal("codec mutated decoded inputs")
	}
}
