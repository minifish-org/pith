package ai

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPortsmithJudgeP02_01(t *testing.T) {
	m := Message{Role: "toolResult", ToolCallID: "call-1", ToolName: "read", IsError: true, Timestamp: 1700000000123, Content: []Content{{Type: "text", Text: "失败"}, {Type: "toolCall", ID: "2", Name: "n", Arguments: json.RawMessage(`{"x":0}`)}}}
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	for _, key := range []string{"role", "toolCallId", "toolName", "isError", "timestamp", "content"} {
		if _, ok := fields[key]; !ok {
			t.Fatal("JSON key", key)
		}
	}
	var back Message
	if e = json.Unmarshal(raw, &back); e != nil || !reflect.DeepEqual(m, back) {
		t.Fatal(string(raw), back, e)
	}
	clone := CloneMessage(m)
	clone.Content[0].Text = "changed"
	if m.Content[0].Text != "失败" {
		t.Fatal("aliased message")
	}
}
func TestPortsmithJudgeP02_02(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[]`, `0`, `false`, `{"missing":null,"zero":0,"flag":false}`} {
		c := Content{Type: "toolCall", Arguments: json.RawMessage(raw)}
		m := CloneMessage(Message{Content: []Content{c}})
		if string(m.Content[0].Arguments) != raw {
			t.Fatal("raw value changed", raw)
		}
	}
	for _, content := range [][]Content{nil, {}} {
		m := CloneMessage(Message{Content: content})
		if (m.Content == nil) != (content == nil) {
			t.Fatal("nil vs empty")
		}
	}
}
func TestPortsmithJudgeP02_03(t *testing.T) {
	m := CloneMessage(Message{Content: []Content{{Type: "toolCall", Arguments: json.RawMessage(`{"n":9007199254740993}`)}}})
	if !strings.Contains(string(m.Content[0].Arguments), "9007199254740993") {
		t.Fatal("precision")
	}
}
func TestPortsmithJudgeP02_04(t *testing.T) {
	for _, ev := range []Event{{Type: "start", Message: &Message{}}, {Type: "text_delta", Delta: "a"}, {Type: "done", Message: &Message{StopReason: "stop"}}, {Type: "error", Error: "before start"}} {
		if e := ValidateEvent(ev); e != nil {
			t.Fatal(e)
		}
	}
	for _, ev := range []Event{{Type: "alien"}, {Type: "done"}, {Type: "error"}} {
		if ValidateEvent(ev) == nil {
			t.Fatal("accepted invalid event", ev)
		}
	}
}
func TestPortsmithJudgeP02_05(t *testing.T) {
	s := "section"
	cost := 0.0
	m := Message{Content: []Content{{Arguments: json.RawMessage(`{"x":1}`)}}, ToolsAdded: []ToolDeclaration{{Parameters: json.RawMessage(`{"type":"object"}`)}}, Sections: []Section{{Name: "s", Value: &s}}, Usage: &Usage{Input: 3, Cost: &cost}}
	copy := CloneEvent(Event{Type: "start", Message: &m})
	copy.Message.Content[0].Arguments[5] = '2'
	copy.Message.ToolsAdded[0].Parameters[0] = 'x'
	*copy.Message.Sections[0].Value = "other"
	copy.Message.Usage.Input = 99
	*copy.Message.Usage.Cost = 1
	if string(m.Content[0].Arguments) != `{"x":1}` || m.ToolsAdded[0].Parameters[0] != '{' || s != "section" || m.Usage.Input != 3 || cost != 0 {
		t.Fatal("shallow event")
	}
	if CloneMessage(Message{Usage: &Usage{}}).Usage.Cost != nil {
		t.Fatal("unknown cost became free")
	}
}
