package ai_test

import (
	"encoding/json"
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/providers"
	"github.com/minifish-org/pith/packages/ai/types"
	"testing"
)

func TestPortsmithJudgePiV1RuntimeCatalog(t *testing.T) {
	models := providers.BuiltinModels(nil)
	flash := models.GetModel("deepseek", "deepseek-flash")
	if flash == nil || flash.ContextWindow != 1000000 || flash.MaxTokens != 384000 || !flash.Reasoning {
		t.Fatalf("modern runtime does not use V1 release catalog: %+v", flash)
	}
	if flash.ThinkingLevelMap == nil {
		t.Fatal("V1 thinking levels lost")
	}
	for _, level := range []types.ModelThinkingLevel{types.ThinkingLow, types.ThinkingHigh, types.ThinkingMax} {
		if _, mapped, supported := flash.ThinkingLevelMap.Lookup(level); !mapped || !supported {
			t.Error("missing actual flash level", level)
		}
	}
}

func TestPortsmithJudgePiV1ModelIdentity(t *testing.T) {
	var a, b types.Model
	_ = json.Unmarshal([]byte(`{"id":"same","provider":"p","api":"a"}`), &a)
	_ = json.Unmarshal([]byte(`{"id":"same","provider":"p","api":"a","type":"chat"}`), &b)
	if !ai.ModelsAreEqual(&a, &b) {
		t.Fatal("legacy and explicit chat identities differ")
	}
	image := json.RawMessage(`{"id":"same","provider":"p","api":"a","type":"image"}`)
	classifier := json.RawMessage(`{"id":"same","provider":"p","api":"a","type":"classifier"}`)
	if ai.ModelsAreEqual(&a, image) || ai.ModelsAreEqual(image, classifier) {
		t.Fatal("cross-kind ids incorrectly collide")
	}
	if ai.HasApi(image, types.Api("a")) {
		t.Fatal("image must not satisfy chat api predicate")
	}
}

func TestPortsmithJudgePiV1MixedBuiltinCatalog(t *testing.T) {
	models := providers.BuiltinModels(nil)
	raw, err := json.Marshal(models.GetAllModels("typesafe"))
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err = json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record["type"] == "classifier" && record["id"] == "jev-latest" && record["api"] == "typesafe-system-one" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new mixed catalog/provider missing Jev classifier: %s", raw)
	}
	if len(models.GetModels("typesafe")) != 0 {
		t.Fatal("classifier leaked into legacy chat-only catalog")
	}
}
