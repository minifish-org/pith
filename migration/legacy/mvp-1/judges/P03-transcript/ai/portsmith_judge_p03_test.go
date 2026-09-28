package ai

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func p03Tool(name string) ToolDeclaration {
	return ToolDeclaration{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func TestPortsmithJudgeP03_01(t *testing.T) {
	if CreateInitialSystemMessage("", nil) != nil {
		t.Fatal("fake system")
	}
	c := NormalizeContext(Context{SystemPrompt: "hello", Tools: []ToolDeclaration{p03Tool("read")}, Messages: []Message{{Role: "user"}}})
	if c.SystemPrompt != "" || len(c.Tools) != 0 || len(c.Messages) != 2 || c.Messages[0].Role != "system" || c.Messages[1].Role != "user" {
		t.Fatal(c)
	}
	if m := CreateInitialSystemMessage("", []ToolDeclaration{p03Tool("x")}); m == nil || len(m.ToolsAdded) != 1 {
		t.Fatal(m)
	}
}
func TestPortsmithJudgeP03_02(t *testing.T) {
	a, b, c := "A", "B", "C"
	msgs := []Message{{Role: "system", Content: []Content{{Type: "text", Text: "first"}}, Sections: []Section{{Name: "a", Value: &a}, {Name: "b", Value: &b}}}, {Role: "user"}, {Role: "system", Content: []Content{{Type: "text", Text: "second"}}, Sections: []Section{{Name: "a", Value: &c}, {Name: "b", Value: nil}}}}
	if got := GetCurrentSystemPrompt(msgs); got != "first\n\nsecond\n\nC" {
		t.Fatal(got)
	}
	if GetCurrentSystemMessage(nil) != nil {
		t.Fatal("empty system")
	}
}
func TestPortsmithJudgeP03_03(t *testing.T) {
	a, b := p03Tool("a"), p03Tool("b")
	changed := a
	changed.Description = "new"
	added, removed := GetToolStateChanges([]ToolDeclaration{a, b}, []ToolDeclaration{changed})
	if len(added) != 1 || added[0].Description != "new" || !reflect.DeepEqual(removed, []ToolReference{{Name: "a"}, {Name: "b"}}) {
		t.Fatal(added, removed)
	}
	d := ToToolDeclaration(a)
	d.Parameters[0] = 'x'
	if a.Parameters[0] != '{' {
		t.Fatal("schema alias")
	}
}
func TestPortsmithJudgeP03_04(t *testing.T) {
	msgs := []Message{{Role: "system", ToolsAdded: []ToolDeclaration{p03Tool("a"), p03Tool("b")}}, {Role: "system", ToolsRemoved: []ToolReference{{Name: "a"}}, ToolsAdded: []ToolDeclaration{p03Tool("c")}}}
	tools := GetCurrentTools(msgs)
	if len(tools) != 2 || tools[0].Name != "b" || tools[1].Name != "c" {
		t.Fatal(tools)
	}
}
func TestPortsmithJudgeP03_05(t *testing.T) {
	var fixture struct {
		Prompt string
		Tools  []string
	}
	raw, e := os.ReadFile("testdata/p03-ts.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	a, b, c := "A", "B", "C"
	msgs := []Message{{Role: "system", Content: []Content{{Type: "text", Text: "base"}}, Sections: []Section{{Name: "a", Value: &a}, {Name: "b", Value: &b}}, ToolsAdded: []ToolDeclaration{p03Tool("x"), p03Tool("y")}}, {Role: "system", Content: []Content{{Type: "text", Text: "more"}}, Sections: []Section{{Name: "a", Value: &c}, {Name: "b", Value: nil}}, ToolsRemoved: []ToolReference{{Name: "x"}}, ToolsAdded: []ToolDeclaration{p03Tool("z"), p03Tool("x")}}}
	for i := 0; i < 30; i++ {
		if p := GetCurrentSystemPrompt(msgs); p != fixture.Prompt {
			t.Fatal(p, fixture.Prompt)
		}
		names := []string{}
		for _, tool := range GetCurrentTools(msgs) {
			names = append(names, tool.Name)
		}
		if !reflect.DeepEqual(names, fixture.Tools) {
			t.Fatal(names, fixture.Tools)
		}
	}
}
