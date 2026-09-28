package ai

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func p04(t *testing.T, schema, input string) json.RawMessage {
	t.Helper()
	v, e := ValidateArguments(ToolDeclaration{Name: "t", Parameters: json.RawMessage(schema)}, json.RawMessage(input))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPortsmithJudgeP04_01(t *testing.T) {
	in := json.RawMessage(`{"n":"12"}`)
	copy := append([]byte{}, in...)
	v, e := ValidateArguments(ToolDeclaration{Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`)}, in)
	if e != nil || string(in) != string(copy) {
		t.Fatal(v, e, "mutated input")
	}
	var x map[string]any
	json.Unmarshal(v, &x)
	if x["n"] != float64(12) {
		t.Fatal(string(v))
	}
}
func TestPortsmithJudgeP04_02(t *testing.T) {
	s := `{"type":"object","required":["n"],"properties":{"n":{"type":"integer","enum":[1,2]}},"additionalProperties":false}`
	for _, in := range []string{`{}`, `{"n":"bad"}`, `{"n":3}`, `{"n":1,"x":1}`} {
		_, e := ValidateArguments(ToolDeclaration{Parameters: json.RawMessage(s)}, json.RawMessage(in))
		var ve *ValidationError
		if e == nil || !errors.As(e, &ve) || ve.Reason == "" {
			t.Fatal("invalid accepted/unstructured error", in, e)
		}
	}
}
func TestPortsmithJudgeP04_03(t *testing.T) {
	var fixtures []struct {
		Schema json.RawMessage
		Input  json.RawMessage
		Output json.RawMessage
		OK     bool
	}
	b, e := os.ReadFile("testdata/p04-ts.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &fixtures); e != nil {
		t.Fatal(e)
	}
	if len(fixtures) < 5 {
		t.Fatal("missing baseline")
	}
	for _, f := range fixtures {
		v, e := ValidateArguments(ToolDeclaration{Parameters: f.Schema}, f.Input)
		if (e == nil) != f.OK {
			t.Fatal(string(f.Input), e)
		}
		if f.OK {
			var a, b any
			json.Unmarshal(v, &a)
			json.Unmarshal(f.Output, &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatal(string(v), string(f.Output))
			}
		}
	}
}
func TestPortsmithJudgeP04_04(t *testing.T) {
	v := p04(t, `{"type":"object","properties":{"optional":{"type":"string"},"nullable":{"$ref":"#/$defs/n"}},"$defs":{"n":{"type":["string","null"]}}}`, `{"optional":null,"nullable":null}`)
	var m map[string]any
	json.Unmarshal(v, &m)
	if _, ok := m["optional"]; ok {
		t.Fatal("optional null")
	}
	if value, ok := m["nullable"]; !ok || value != nil {
		t.Fatal("nullable ref")
	}
}
func TestPortsmithJudgeP04_05(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		s := `{"type":"object","properties":{"v":{"` + keyword + `":[{"type":"number"},{"type":"string"},{"type":"null"}]}}}`
		v := p04(t, s, `{"v":"12"}`)
		var m map[string]any
		json.Unmarshal(v, &m)
		if m["v"] != "12" {
			t.Fatal(string(v))
		}
	}
	v := p04(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"n":9007199254740993}`)
	var m map[string]json.RawMessage
	json.Unmarshal(v, &m)
	if string(m["n"]) != "9007199254740993" {
		t.Fatal("precision", string(v))
	}
}
