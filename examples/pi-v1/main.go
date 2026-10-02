// Command pi-v1 demonstrates the Pi 1.0 (V1) model catalog and the structured
// classifier protocol through the native Go SDK.
//
// It reads the mixed builtin catalog (chat, image and classifier records) and
// the frozen V1 release catalog, then performs a real offline classification
// against a loopback HTTP server that speaks the TypeSafe System One wire
// protocol. No paid provider, credential or network egress is involved.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/providers"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pi-v1 example:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models := providers.BuiltinModels(nil)

	fmt.Println("--- builtin chat model from the V1 release catalog ---")
	if flash := models.GetModel("deepseek", "deepseek-flash"); flash != nil {
		fmt.Printf("%s/%s context=%g maxTokens=%g reasoning=%t api=%s\n",
			flash.Provider, flash.Id, flash.ContextWindow, flash.MaxTokens, flash.Reasoning, flash.Api)
	}

	fmt.Println("--- mixed classifier records (a separate model kind) ---")
	for _, model := range models.GetModelsOfType(aitypes.ModelTypeClassifier, "typesafe") {
		if model.Classifier == nil {
			continue
		}
		fmt.Printf("%s/%s api=%s type=%s\n", model.Classifier.Provider, model.Classifier.Id, model.Classifier.Api, model.Type)
	}
	fmt.Printf("legacy chat-only listing omits classifiers: %d records\n", len(models.GetModels("typesafe")))

	fmt.Println("--- frozen V1 catalog access (defensive copies) ---")
	v1 := catalog.V1Models("deepseek", "chat")
	fmt.Printf("V1 deepseek chat records: %d\n", len(v1))
	fmt.Printf("V1 catalog manifest bytes: %d\n", len(catalog.V1Manifest()))

	server := httptest.NewServer(http.HandlerFunc(classifierHandler))
	defer server.Close()

	model := aitypes.ClassifierModel{
		Type:     aitypes.ModelTypeClassifier,
		Id:       "fixture-classifier",
		Name:     "Fixture Classifier",
		Api:      aitypes.ClassifierApiTypesafeSystemOne,
		Provider: "typesafe",
		BaseUrl:  server.URL + "/v1",
	}
	var request aitypes.ClassifierContext
	if err := json.Unmarshal([]byte(`{
		"state": {"task": "review a pull request"},
		"questions": {
			"merge": {"type": "bool", "instructions": "Is the change safe to merge?", "criteria": {"true": "safe", "false": "unsafe"}}
		}
	}`), &request); err != nil {
		return err
	}
	key := "offline-fixture-key"
	result := api.TypesafeSystemOneClassify(ctx, &model, &request, &aitypes.ClassifierOptions{
		ProviderRequestOptions: aitypes.ProviderRequestOptions{APIKey: &key},
	})
	if result.StopReason != aitypes.ClassifierStopReasonStop {
		return fmt.Errorf("classification failed: %v", result.ErrorMessage)
	}
	answer := result.Answers["merge"]
	fmt.Printf("--- classification ---\nstop=%s bool=%v probability=%.2f\n",
		result.StopReason, *answer.Probability > 0.5, *answer.Probability)
	return nil
}

// classifierHandler is a minimal TypeSafe System One server. It asserts the
// request shape and answers with the wire form of a bool question.
func classifierHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"answers": map[string]any{
			"merge": map[string]any{"type": "noul", "noul": 0.92},
		},
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 1},
	})
}
