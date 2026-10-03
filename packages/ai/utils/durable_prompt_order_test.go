package utils

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

func promptOrderDecode(t *testing.T, text string) types.SystemMessage {
	t.Helper()
	var message types.SystemMessage
	if err := json.Unmarshal([]byte(text), &message); err != nil {
		t.Fatalf("Unmarshal(%s): %v", text, err)
	}
	return message
}

func promptOrderAssert(t *testing.T, got, first, second string) {
	t.Helper()
	a, b := strings.Index(got, first), strings.Index(got, second)
	if a < 0 || b < 0 {
		t.Fatalf("missing %q or %q in %q", first, second, got)
	}
	if b <= a {
		t.Fatalf("expected %q before %q in %q", first, second, got)
	}
}

// Verifies that rendering honors the decoded section order instead of sorting
// section names alphabetically.
func TestDurablePromptOrderRenderHonorsRecordedOrder(t *testing.T) {
	message := promptOrderDecode(t, `{"role":"system","content":"base","sections":{"zeta":"Z-source","alpha":"A-source"},"timestamp":1}`)
	promptOrderAssert(t, GetSystemMessageText(message), "Z-source", "A-source")
	promptOrderAssert(t, RenderSystemMessageUpdate(message), "zeta", "alpha")
}

// Verifies that map-only callers without recorded order keep the deterministic
// sorted fallback.
func TestDurablePromptOrderRenderSortedFallback(t *testing.T) {
	a, z := "A", "Z"
	message := types.SystemMessage{Role: types.SystemMessageRole, Content: types.SystemContentText("base"), Sections: types.SystemSections{"zeta": &z, "alpha": &a}}
	promptOrderAssert(t, GetSystemMessageText(message), "A", "Z")
}

// Verifies replay removes a nulled position and appends a reintroduced name, and
// that the folded message records the resulting order.
func TestDurablePromptOrderReplayRemoveReadd(t *testing.T) {
	first := promptOrderDecode(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	remove := promptOrderDecode(t, `{"role":"system","content":"","sections":{"zeta":null},"timestamp":2}`)
	readd := promptOrderDecode(t, `{"role":"system","content":"extra","sections":{"zeta":"Z2"},"timestamp":3}`)
	messages := TranscriptMessages{types.NewSystemMessageVariant(first), types.NewSystemMessageVariant(remove), types.NewSystemMessageVariant(readd)}

	folded := GetCurrentSystemMessage(messages)
	if folded == nil {
		t.Fatal("no folded system message")
	}
	promptOrderAssert(t, GetSystemMessageText(*folded), "A", "Z2")
	if len(folded.SectionOrder) != 2 || folded.SectionOrder[0] != "alpha" || folded.SectionOrder[1] != "zeta" {
		t.Fatalf("folded SectionOrder = %v, want [alpha zeta]", folded.SectionOrder)
	}
	encoded, err := json.Marshal(folded)
	if err != nil {
		t.Fatal(err)
	}
	promptOrderAssert(t, string(encoded), `"alpha"`, `"zeta"`)
}

// Verifies replay keeps the position of a replaced section rather than moving it.
func TestDurablePromptOrderReplayReplacementKeepsPosition(t *testing.T) {
	first := promptOrderDecode(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	replace := promptOrderDecode(t, `{"role":"system","content":"","sections":{"zeta":"Z-new"},"timestamp":2}`)
	folded := GetCurrentSystemMessage(TranscriptMessages{types.NewSystemMessageVariant(first), types.NewSystemMessageVariant(replace)})
	if folded == nil {
		t.Fatal("no folded system message")
	}
	if len(folded.SectionOrder) != 2 || folded.SectionOrder[0] != "zeta" {
		t.Fatalf("replacement moved section: %v", folded.SectionOrder)
	}
	promptOrderAssert(t, GetSystemMessageText(*folded), "Z-new", "A")
}

// Verifies rendering and replay never mutate the caller's messages, section maps
// or recorded order.
func TestDurablePromptOrderInputOwnership(t *testing.T) {
	first := promptOrderDecode(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	remove := promptOrderDecode(t, `{"role":"system","content":"","sections":{"zeta":null},"timestamp":2}`)
	messages := TranscriptMessages{types.NewSystemMessageVariant(first), types.NewSystemMessageVariant(remove)}
	before, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	_ = GetCurrentSystemMessage(messages)
	_ = GetCurrentSystemPrompt(messages)
	_ = GetSystemMessageText(first)
	_ = RenderSystemMessageUpdate(first)
	after, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("replay mutated input: before=%s after=%s", before, after)
	}
}
