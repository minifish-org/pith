package api_test

import (
	"context"
	"encoding/json"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPortsmithJudgePiV1ClassifierWire(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer fake" {
			t.Error("classifier URL/auth incorrect", r.URL, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		questions, _ := body["questions"].(map[string]any)
		yes, _ := questions["yes"].(map[string]any)
		if body["model"] != "fixture" || yes["type"] != "noul" {
			t.Error("native classifier bool wire mapping missing", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"answers":{"yes":{"type":"noul","noul":0.75},"category":{"type":"choice","choice":"a","probabilities":{"a":0.8,"b":0.2},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`))
		} else {
			_, _ = w.Write([]byte(`{"answers":{"yes":{"type":"noul","noul":0.75}}}`))
		}
	}))
	defer server.Close()
	var model types.ClassifierModel
	raw, _ := json.Marshal(map[string]any{"type": "classifier", "id": "fixture", "name": "Fixture", "provider": "typesafe", "api": "typesafe-system-one", "baseUrl": server.URL + "/v1", "cost": map[string]any{"input": 1, "output": 2, "cacheRead": 0, "cacheWrite": 0}})
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatal(err)
	}
	var request types.ClassifierContext
	if err := json.Unmarshal([]byte(`{"state":{"task":"example"},"questions":{"yes":{"type":"bool","instructions":"valid?","criteria":{"true":"yes","false":"no"}},"category":{"type":"choice","instructions":"category?","criteria":{"a":"first","b":"second"}}}}`), &request); err != nil {
		t.Fatal(err)
	}
	key := "fake"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		result := api.TypesafeSystemOneClassify(ctx, &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key}})
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			answers, _ := value["answers"].(map[string]any)
			yes, _ := answers["yes"].(map[string]any)
			category, _ := answers["category"].(map[string]any)
			if value["stopReason"] != "stop" || yes["type"] != "bool" || yes["probability"] != 0.75 || category["choice"] != "a" {
				t.Fatalf("classification result mapping lost: %s", raw)
			}
		} else if value["stopReason"] != "error" {
			t.Fatalf("missing required answer accepted: %s", raw)
		}
	}
}
