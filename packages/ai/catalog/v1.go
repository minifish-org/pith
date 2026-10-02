// This file ports the versioned V1 catalog access path of Pi AI 1.0.0.
//
// The immutable `assets/catalog/*.json` assets ship from the exact Pi AI 1.0.0
// npm release whose gitHead is the target commit. They are embedded verbatim
// under `v1data` and exposed through V1Models/V1Manifest so that modern
// registry/cache metadata can read the frozen provenance without disturbing the
// historical `catalog.MODELS`/`IMAGE_MODELS` legacy catalog, which keeps using
// the separate `data` path in data_json_d.go.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package catalog

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

// v1DataFS embeds the frozen V1 release catalogs, one JSON document per
// provider plus the pinned publication manifest. catalog-manifest.json is
// provenance metadata, not provider model data.
//
//go:embed v1data
var v1DataFS embed.FS

// V1 model kinds. Only these three participate in V1 catalog identity; a
// missing `type` on a record means chat (see types.GetModelType).
const (
	v1KindChat       = "chat"
	v1KindImage      = "image"
	v1KindClassifier = "classifier"
)

// V1Manifest returns a defensive copy of the pinned release manifest embedded
// under v1data/catalog-manifest.json. It preserves schemaVersion, generatedAt,
// structureHash and every per-provider digest. A missing asset yields nil.
func V1Manifest() json.RawMessage {
	raw, err := v1DataFS.ReadFile("v1data/catalog-manifest.json")
	if err != nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// V1Models returns the frozen model records for one provider and one model
// kind, as defensive copies of the original JSON objects.
//
// Records are ordered by stable API/id order (`api/kind:id`). Unknown providers
// and unknown kinds return an empty result. The records are returned verbatim,
// so type, costs, limits, compatibility, extra provider settings and null
// thinking-level mappings are preserved.
func V1Models(provider string, kind string) []json.RawMessage {
	if provider == "" || !v1KnownKind(kind) {
		return nil
	}
	raw, err := v1DataFS.ReadFile("v1data/" + provider + ".json")
	if err != nil {
		return nil
	}
	var groups map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil
	}

	prefix := kind + ":"
	type record struct {
		sortKey string
		raw     json.RawMessage
	}
	records := make([]record, 0)
	for _, entries := range groups {
		for key, value := range entries {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			var probe struct {
				Id  string `json:"id"`
				Api string `json:"api"`
			}
			if err := json.Unmarshal(value, &probe); err != nil {
				continue
			}
			records = append(records, record{
				sortKey: probe.Api + "/" + kind + ":" + probe.Id,
				raw:     value,
			})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].sortKey < records[j].sortKey
	})

	out := make([]json.RawMessage, 0, len(records))
	for _, r := range records {
		out = append(out, append(json.RawMessage(nil), r.raw...))
	}
	return out
}

// V1ProviderNames returns the provider ids that have a V1 catalog document, in
// stable lexical order. It is used by registry/cache metadata.
func V1ProviderNames() []string {
	entries, err := v1DataFS.ReadDir("v1data")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if name == "catalog-manifest.json" {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(names)
	return names
}

// v1KnownKind reports whether kind is one of the three V1 model kinds.
func v1KnownKind(kind string) bool {
	switch kind {
	case v1KindChat, v1KindImage, v1KindClassifier:
		return true
	default:
		return false
	}
}

// V1AnyModelsOfKind returns the frozen models of one provider and kind as tagged
// types.AnyModel values, ordered by API/id. Unknown providers and kinds yield an
// empty result.
func V1AnyModelsOfKind(provider string, kind string) []types.AnyModel {
	if provider == "" || !v1KnownKind(kind) {
		return nil
	}
	records := v1ModelRecords(provider, kind)
	if len(records) == 0 {
		return nil
	}
	out := make([]types.AnyModel, 0, len(records))
	for _, raw := range records {
		var model types.AnyModel
		if err := json.Unmarshal(raw, &model); err != nil {
			continue
		}
		out = append(out, model)
	}
	return out
}

// V1AnyModels returns every frozen model of one provider as tagged
// types.AnyModel values, ordered by API/kind/id.
func V1AnyModels(provider string) []types.AnyModel {
	out := make([]types.AnyModel, 0)
	for _, kind := range []string{v1KindChat, v1KindImage, v1KindClassifier} {
		out = append(out, V1AnyModelsOfKind(provider, kind)...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// v1ModelRecords returns the verbatim JSON records of one provider and kind in
// stable API/id order.
func v1ModelRecords(provider string, kind string) []json.RawMessage {
	raw, err := v1DataFS.ReadFile("v1data/" + provider + ".json")
	if err != nil {
		return nil
	}
	var groups map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil
	}
	prefix := kind + ":"
	records := make([]struct {
		sortKey string
		raw     json.RawMessage
	}, 0)
	for _, entries := range groups {
		for key, value := range entries {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			var probe struct {
				Id  string `json:"id"`
				Api string `json:"api"`
			}
			if err := json.Unmarshal(value, &probe); err != nil {
				continue
			}
			records = append(records, struct {
				sortKey string
				raw     json.RawMessage
			}{sortKey: probe.Api + "/" + kind + ":" + probe.Id, raw: value})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].sortKey < records[j].sortKey })
	out := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		out = append(out, append(json.RawMessage(nil), record.raw...))
	}
	return out
}
