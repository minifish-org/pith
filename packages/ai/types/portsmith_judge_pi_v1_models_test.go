package types_test

import (
	"encoding/json"
	"github.com/minifish-org/pith/packages/ai/types"
	"testing"
)

func TestPortsmithJudgePiV1ModelKinds(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"id":"same","provider":"p","api":"openai-completions"}`, "chat"},
		{`{"type":"chat","id":"same","provider":"p","api":"openai-completions"}`, "chat"},
		{`{"type":"image","id":"same","provider":"p","api":"openrouter-images"}`, "image"},
		{`{"type":"classifier","id":"same","provider":"p","api":"typesafe-system-one"}`, "classifier"},
		{`{"type":"future","id":"same","provider":"p"}`, "future"},
	} {
		model := json.RawMessage(tc.raw)
		if got := types.GetModelType(model); got != tc.want {
			t.Errorf("%s kind: got %q want %q", tc.raw, got, tc.want)
		}
		for _, kind := range []string{"chat", "image", "classifier"} {
			if types.IsModelType(model, kind) != (tc.want == kind) {
				t.Errorf("incorrect narrowing of %s to %s", tc.raw, kind)
			}
		}
	}
	legacy := types.Model{Id: "old", Provider: "p"}
	if types.GetModelType(legacy) != "chat" || types.GetModelType(&legacy) != "chat" {
		t.Fatal("legacy chat compatibility lost")
	}
	if types.IsModelType(nil, "chat") {
		t.Fatal("nil must not be a chat model")
	}
}
