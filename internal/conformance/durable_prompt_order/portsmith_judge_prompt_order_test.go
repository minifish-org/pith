package durable_prompt_order_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	utils "github.com/minifish-org/pith/packages/ai/utils"
)

func orderedDecode(t *testing.T, text string) ai.SystemMessage {
	t.Helper()
	var m ai.SystemMessage
	if e := json.Unmarshal([]byte(text), &m); e != nil {
		t.Fatal(e)
	}
	return m
}
func orderedPosition(t *testing.T, got, first, second string) {
	t.Helper()
	a, b := strings.Index(got, first), strings.Index(got, second)
	if a < 0 || b <= a {
		t.Fatalf("ordered %q before %q not preserved in %q", first, second, got)
	}
}

func TestPortsmithJudgeDurablePromptSourceOrder(t *testing.T) {
	m := orderedDecode(t, `{"role":"system","content":"base","sections":{"zeta":"Z-source","alpha":"A-source"},"timestamp":1}`)
	orderedPosition(t, utils.GetSystemMessageText(m), "Z-source", "A-source")
	orderedPosition(t, utils.RenderSystemMessageUpdate(m), "zeta", "alpha")
	encoded, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	orderedPosition(t, string(encoded), `"zeta"`, `"alpha"`)
	if strings.Contains(string(encoded), "SectionOrder") || strings.Contains(string(encoded), "sectionOrder") {
		t.Fatalf("native metadata leaked into source wire: %s", encoded)
	}
	var union ai.Message
	if e = json.Unmarshal(encoded, &union); e != nil {
		t.Fatal(e)
	}
	roundtrip, e := json.Marshal(union)
	if e != nil {
		t.Fatal(e)
	}
	orderedPosition(t, string(roundtrip), `"zeta"`, `"alpha"`)
}

func TestPortsmithJudgeDurablePromptRemoveReaddReplay(t *testing.T) {
	first := orderedDecode(t, `{"role":"system","content":"base","sections":{"zeta":"Z","alpha":"A"},"timestamp":1}`)
	remove := orderedDecode(t, `{"role":"system","content":"","sections":{"zeta":null},"timestamp":2}`)
	readd := orderedDecode(t, `{"role":"system","content":"extra","sections":{"zeta":"Z2"},"timestamp":3}`)
	messages := utils.TranscriptMessages{ai.NewSystemMessageVariant(first), ai.NewSystemMessageVariant(remove), ai.NewSystemMessageVariant(readd)}
	before, e := json.Marshal(messages)
	if e != nil {
		t.Fatal(e)
	}
	folded := utils.GetCurrentSystemMessage(messages)
	if folded == nil {
		t.Fatal("no folded system message")
	}
	orderedPosition(t, utils.GetSystemMessageText(*folded), "A", "Z2")
	encoded, e := json.Marshal(folded)
	if e != nil {
		t.Fatal(e)
	}
	orderedPosition(t, string(encoded), `"alpha"`, `"zeta"`)
	after, e := json.Marshal(messages)
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(after) {
		t.Fatal("prompt replay mutated historical maps or section order")
	}
}

func TestPortsmithJudgeDurablePromptNativeOrderAndFallback(t *testing.T) {
	a, z := "A", "Z"
	ordered := ai.SystemMessage{Role: ai.SystemMessageRole, Content: ai.SystemContentText("base"), Sections: ai.SystemSections{"alpha": &a, "zeta": &z}}
	order := reflect.ValueOf(&ordered).Elem().FieldByName("SectionOrder")
	if !order.IsValid() || order.Type() != reflect.TypeOf([]string{}) || !order.CanSet() {
		t.Fatal("SystemMessage lacks writable []string SectionOrder metadata")
	}
	order.Set(reflect.ValueOf([]string{"zeta", "zeta", "missing", "alpha"}))
	orderedPosition(t, utils.GetSystemMessageText(ordered), "Z", "A")
	encoded, e := json.Marshal(ordered)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(encoded), `"zeta"`) != 1 {
		t.Fatalf("duplicate ordered section property: %s", encoded)
	}
	orderedPosition(t, string(encoded), `"zeta"`, `"alpha"`)
	unordered := ordered
	reflect.ValueOf(&unordered).Elem().FieldByName("SectionOrder").Set(reflect.Zero(reflect.TypeOf([]string{})))
	orderedPosition(t, utils.GetSystemMessageText(unordered), "A", "Z")
	if e = json.Unmarshal([]byte(`{"role":"system","content":"","sections":{"beta":"B"},"timestamp":4}`), &ordered); e != nil {
		t.Fatal(e)
	}
	decodedOrder := reflect.ValueOf(ordered).FieldByName("SectionOrder").Interface().([]string)
	if len(decodedOrder) != 1 || decodedOrder[0] != "beta" {
		t.Fatalf("decode retained stale native section order: %v", decodedOrder)
	}
}
