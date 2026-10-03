package delta

import "testing"

func TestEncoderDecoderInterningAndRecovery(t *testing.T) {
	encoder, decoder := NewEncoder(), NewDecoder()
	first := []Op{{"s", Path{"nested", "text"}, "a"}, {"a", Path{"nested", "text"}, "b"}}
	before := mustJSON(t, first)
	wire, err := encoder.Encode(first)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, wire, []any{[]any{"s", []any{"nested", "text"}, "a"}, []any{"a", "b"}})
	decoded, err := decoder.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, decoded, first)
	second := []Op{{"t", Path{"nested", "text"}, 1}}
	wire, err = encoder.Encode(second)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, wire, []any{[]any{"#", 0, []any{"nested", "text"}}, []any{"t", 0, 1}})
	decoded, err = decoder.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, decoded, second)
	base := []Op{{"r", map[string]any{"nested": map[string]any{"text": "new"}}}, {"a", Path{"nested", "text"}, "!"}}
	wire, err = encoder.Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = NewDecoder().Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, decoded, base)
	if !IsBase(base) || !IsReplace(base[0]) || IsBase(first) || IsReplace(first[0]) || IsBase(nil) {
		t.Fatal("replacement classification mismatch")
	}
	if mustJSON(t, first) != before {
		t.Fatal("codec mutated decoded inputs")
	}
}

func TestDecoderRejectsInvalidWireBatches(t *testing.T) {
	for _, bad := range [][]WireOp{
		{{"a", "omitted-first-path"}},
		{{"s", 77, true}},
		{{"#", 0, Path{"__proto__"}}, {"s", 0, true}},
		{{"unknown"}},
		{{"d"}},
		{{"m", []any{0, 0}}},
		{{"p", 0, 1}},
	} {
		if _, err := NewDecoder().Decode(bad); err == nil {
			t.Fatalf("invalid wire batch accepted: %v", bad)
		}
	}
}

func TestDecoderSecondUseOfPathDefinesID(t *testing.T) {
	encoder := NewEncoder()
	decoder := NewDecoder()
	encoder.Encode([]Op{{"s", Path{"x"}, 1}})
	wire, err := encoder.Encode([]Op{{"s", Path{"x"}, 2}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decoder.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, decoded, []Op{{"s", Path{"x"}, 2}})
}
