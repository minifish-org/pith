package sdk_incremental_test

import (
	"encoding/json"
	ai "github.com/minifish-org/pith/packages/ai/types"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"os"
	"path/filepath"
	"testing"
)

func model() *ai.Model {
	return &ai.Model{Id: "test", Name: "test", Api: ai.ApiOpenAICompletions, Provider: ai.ProviderOpenAI, BaseUrl: "http://localhost.invalid/v1", ContextWindow: 1000000, MaxTokens: 384000}
}
func TestPortsmithJudgeSDKConfiguration(t *testing.T) {
	m := model()
	resolved, err := sdk.ResolveModel(sdk.ModelOptions{Model: m})
	if err != nil || resolved == nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.ContextWindow != 1000000 || resolved.MaxTokens != 384000 {
		t.Fatal("model capacity silently reduced")
	}
	resolved.Id = "changed"
	if m.Id != "test" {
		t.Fatal("model aliases input")
	}
	max := 8192
	resolved, err = sdk.ResolveModel(sdk.ModelOptions{Model: m, MaxTokens: &max})
	if err != nil || resolved.MaxTokens != 8192 {
		t.Fatal("explicit max tokens ignored")
	}
	max = 400000
	if _, err = sdk.ResolveModel(sdk.ModelOptions{Model: m, MaxTokens: &max}); err == nil {
		t.Fatal("capacity overrun accepted")
	}
	if _, err = sdk.ResolveModel(sdk.ModelOptions{}); err == nil {
		t.Fatal("missing model accepted")
	}
	d := t.TempDir()
	g := filepath.Join(d, "global.json")
	p := filepath.Join(d, "project.json")
	os.WriteFile(g, []byte(`{"flag":true,"nested":{"a":1,"b":2},"unknown":"preserve"}`), 0600)
	os.WriteFile(p, []byte(`{"flag":false,"nested":{"b":3}}`), 0600)
	s, err := sdk.LoadSettings(g, p, sdk.Settings{"zero": json.RawMessage(`0`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(s["flag"]) != "false" || string(s["zero"]) != "0" || string(s["unknown"]) != `"preserve"` {
		t.Fatalf("merge %#v", s)
	}
	var nested map[string]int
	json.Unmarshal(s["nested"], &nested)
	if nested["a"] != 1 || nested["b"] != 3 {
		t.Fatal("nested merge")
	}
	out := filepath.Join(d, "saved.json")
	if err = sdk.SaveSettings(out, s); err != nil {
		t.Fatal(err)
	}
	back, err := sdk.LoadSettings(out, "", nil)
	if err != nil || string(back["unknown"]) != `"preserve"` {
		t.Fatal("round trip")
	}
	os.WriteFile(p, []byte(`{"broken":`), 0600)
	if _, err = sdk.LoadSettings(g, p, nil); err == nil {
		t.Fatal("malformed settings hidden")
	}
}
