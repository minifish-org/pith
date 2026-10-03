package chord

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func chordJSONEqual(t *testing.T, got, want any) {
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

func TestCopyJSONDetachesSharedContainers(t *testing.T) {
	shared := map[string]any{"value": 1}
	input := map[string]any{"left": shared, "right": shared}
	copied, err := CopyJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	root := copied.(map[string]any)
	if reflect.ValueOf(root["left"]).Pointer() == reflect.ValueOf(root["right"]).Pointer() {
		t.Fatal("shared child retained an alias in the copy")
	}
	root["left"].(map[string]any)["value"] = 9
	chordJSONEqual(t, shared, map[string]any{"value": 1})
	chordJSONEqual(t, root["right"], map[string]any{"value": 1})
}

func TestIsJSONValueRejectsUnsupportedValues(t *testing.T) {
	if !IsJSONValue(map[string]any{"text": "你好😀", "n": 1.5, "b": true, "z": nil}) {
		t.Fatal("valid strict JSON rejected")
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, invalid := range []any{math.NaN(), math.Inf(1), math.Inf(-1), func() {}, make(chan int), cycle, map[string]any{"bad": math.Inf(1)}} {
		if IsJSONValue(invalid) {
			t.Fatalf("non-JSON value accepted: %T", invalid)
		}
		if _, err := CopyJSON(invalid); err == nil {
			t.Fatalf("non-JSON copy accepted: %T", invalid)
		}
	}
}

func TestCopyJSONAcceptsIntegralNumericPrimitives(t *testing.T) {
	copied, err := CopyJSON(map[string]any{"i": 3, "f": 2.0, "s": "x"})
	if err != nil {
		t.Fatal(err)
	}
	chordJSONEqual(t, copied, map[string]any{"i": 3, "f": 2.0, "s": "x"})
}
