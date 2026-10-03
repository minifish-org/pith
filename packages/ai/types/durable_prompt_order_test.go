package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeSystem decodes a system message and fails the test on error.
func decodeSystem(t *testing.T, text string) SystemMessage {
	t.Helper()
	var message SystemMessage
	if err := json.Unmarshal([]byte(text), &message); err != nil {
		t.Fatalf("Unmarshal(%s): %v", text, err)
	}
	return message
}

func assertOrder(t *testing.T, got, first, second string) {
	t.Helper()
	a, b := strings.Index(got, first), strings.Index(got, second)
	if a < 0 || b < 0 {
		t.Fatalf("missing %q or %q in %q", first, second, got)
	}
	if b <= a {
		t.Fatalf("expected %q before %q in %q", first, second, got)
	}
}

// Verifies the decoder captures section insertion order, including explicit
// null removals, rather than dropping it into an unordered map.
func TestDurablePromptOrderDecodeCapturesSectionOrder(t *testing.T) {
	message := decodeSystem(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	want := []string{"zeta", "alpha"}
	if len(message.SectionOrder) != len(want) {
		t.Fatalf("SectionOrder = %v", message.SectionOrder)
	}
	for i := range want {
		if message.SectionOrder[i] != want[i] {
			t.Fatalf("SectionOrder = %v, want %v", message.SectionOrder, want)
		}
	}

	withNull := decodeSystem(t, `{"role":"system","content":"","sections":{"a":null,"b":"B"},"timestamp":2}`)
	if len(withNull.SectionOrder) != 2 || withNull.SectionOrder[0] != "a" || withNull.SectionOrder[1] != "b" {
		t.Fatalf("null removal not recorded in order: %v", withNull.SectionOrder)
	}
	if value, ok := withNull.Sections["a"]; !ok || value != nil {
		t.Fatalf("null removal not preserved: %+v", withNull.Sections)
	}
}

// Verifies the encoder writes section properties in the recorded order and never
// leaks native metadata onto the wire.
func TestDurablePromptOrderEncode(t *testing.T) {
	message := decodeSystem(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(t, string(encoded), `"zeta"`, `"alpha"`)
	if strings.Contains(string(encoded), "SectionOrder") || strings.Contains(string(encoded), "sectionOrder") {
		t.Fatalf("native metadata leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"sections":{"zeta":"Z","alpha":"A"}`) {
		t.Fatalf("unexpected sections encoding: %s", encoded)
	}
}

// Verifies the encoder deduplicates and drops stale SectionOrder entries and
// falls back to sorted map keys for map-only callers.
func TestDurablePromptOrderEncodeDedupAndFallback(t *testing.T) {
	a, z := "A", "Z"
	message := SystemMessage{
		Role:         SystemMessageRole,
		Content:      SystemContentText("base"),
		Sections:     SystemSections{"alpha": &a, "zeta": &z},
		SectionOrder: []string{"zeta", "zeta", "missing", "alpha"},
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(encoded), `"zeta"`) != 1 {
		t.Fatalf("duplicate section name emitted: %s", encoded)
	}
	if strings.Contains(string(encoded), "missing") {
		t.Fatalf("stale section name emitted: %s", encoded)
	}
	assertOrder(t, string(encoded), `"zeta"`, `"alpha"`)

	mapOnly := SystemMessage{Role: SystemMessageRole, Content: SystemContentText("base"), Sections: SystemSections{"zeta": &z, "alpha": &a}}
	encoded, err = json.Marshal(mapOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(t, string(encoded), `"alpha"`, `"zeta"`)
}

// Verifies decoding twice replaces stale section metadata instead of merging it.
func TestDurablePromptOrderDecodeClearsStale(t *testing.T) {
	message := decodeSystem(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	if err := json.Unmarshal([]byte(`{"role":"system","content":"","sections":{"beta":"B"},"timestamp":4}`), &message); err != nil {
		t.Fatal(err)
	}
	if len(message.SectionOrder) != 1 || message.SectionOrder[0] != "beta" {
		t.Fatalf("stale SectionOrder retained: %v", message.SectionOrder)
	}
	if len(message.Sections) != 1 {
		t.Fatalf("stale sections retained: %+v", message.Sections)
	}
	if _, ok := message.Sections["zeta"]; ok {
		t.Fatalf("stale section zeta retained: %+v", message.Sections)
	}
}

// Verifies the Message union preserves section order across a round trip.
func TestDurablePromptOrderMessageUnionRoundTrip(t *testing.T) {
	var union Message
	if err := json.Unmarshal([]byte(`{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`), &union); err != nil {
		t.Fatal(err)
	}
	if union.System == nil {
		t.Fatal("system variant not decoded")
	}
	if len(union.System.SectionOrder) != 2 || union.System.SectionOrder[0] != "zeta" {
		t.Fatalf("union dropped section order: %v", union.System.SectionOrder)
	}
	encoded, err := json.Marshal(union)
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(t, string(encoded), `"zeta"`, `"alpha"`)
}

// Verifies native variants detach the section-order slice so later mutation of
// the caller's slice cannot change the message.
func TestDurablePromptOrderVariantDetachesOrder(t *testing.T) {
	a, z := "A", "Z"
	original := SystemMessage{
		Role:         SystemMessageRole,
		Content:      SystemContentText("base"),
		Sections:     SystemSections{"alpha": &a, "zeta": &z},
		SectionOrder: []string{"zeta", "alpha"},
	}
	variant := NewSystemMessageVariant(original)
	original.SectionOrder[0] = "alpha"
	if variant.System.SectionOrder[0] != "zeta" {
		t.Fatalf("variant shares caller section-order slice: %v", variant.System.SectionOrder)
	}
}

// Verifies SystemSectionNames resolves recorded order, ignores stale/duplicate
// names and always includes unlisted keys.
func TestDurablePromptOrderSectionNames(t *testing.T) {
	a, z, b := "A", "Z", "B"
	message := SystemMessage{
		Sections:     SystemSections{"alpha": &a, "zeta": &z, "beta": &b},
		SectionOrder: []string{"zeta", "zeta", "missing", "alpha"},
	}
	got := SystemSectionNames(message)
	want := []string{"zeta", "alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("SystemSectionNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SystemSectionNames = %v, want %v", got, want)
		}
	}
}
