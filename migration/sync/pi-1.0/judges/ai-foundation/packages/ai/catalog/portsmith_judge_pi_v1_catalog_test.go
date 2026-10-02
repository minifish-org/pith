package catalog_test

import (
	"encoding/json"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPortsmithJudgePiV1Catalog(t *testing.T) {
	frozenManifest, err := os.ReadFile("testdata/pi-v1-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest, expectedManifest any
	if err = json.Unmarshal(catalog.V1Manifest(), &manifest); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(frozenManifest, &expectedManifest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest, expectedManifest) {
		t.Fatal("V1 catalog provenance differs from pinned release")
	}
	files, err := filepath.Glob("testdata/pi-v1-catalog/*.json")
	if err != nil || len(files) < 40 {
		t.Fatal("missing pinned release catalog fixtures", err)
	}
	for _, file := range files {
		provider := strings.TrimSuffix(filepath.Base(file), ".json")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var groups map[string]map[string]json.RawMessage
		if err = json.Unmarshal(data, &groups); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"chat", "image", "classifier"} {
			want := map[string]any{}
			for api, records := range groups {
				for key, raw := range records {
					if strings.HasPrefix(key, kind+":") {
						var v any
						if err = json.Unmarshal(raw, &v); err != nil {
							t.Fatal(err)
						}
						want[api+"/"+key] = v
					}
				}
			}
			got := map[string]any{}
			previous := ""
			for _, raw := range catalog.V1Models(provider, kind) {
				var v map[string]any
				if err = json.Unmarshal(raw, &v); err != nil {
					t.Fatal(err)
				}
				id, _ := v["id"].(string)
				api, _ := v["api"].(string)
				key := api + "/" + kind + ":" + id
				if key < previous {
					t.Fatal("catalog order is not stable API/id order")
				}
				previous = key
				if _, duplicate := got[key]; duplicate {
					t.Fatal("duplicate catalog identity", key)
				}
				got[key] = v
			}
			if !reflect.DeepEqual(got, want) {
				keys := make([]string, 0, len(want))
				for k := range want {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				t.Errorf("%s/%s frozen release records differ: want %d got %d; expected ids %v", provider, kind, len(want), len(got), keys)
			}
		}
	}
	first := catalog.V1Models("deepseek", "chat")
	if len(first) == 0 {
		t.Fatal("missing DeepSeek V1 catalog")
	}
	first[0][0] = '!'
	again := catalog.V1Models("deepseek", "chat")
	if len(again) == 0 || !json.Valid(again[0]) {
		t.Fatal("caller corrupted embedded catalog")
	}
	if len(catalog.V1Models("unknown", "chat")) != 0 || len(catalog.V1Models("deepseek", "unknown")) != 0 {
		t.Fatal("unknown lookup must be empty")
	}
}
