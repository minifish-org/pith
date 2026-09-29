package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/minifish-org/pith/packages/ai/types"
)

// ModelGroups is the shape of one catalog JSON document: a map of API group
// name to a map of model id to the model entry (upstream `ModelGroups`).
//
// Entries are stored as CatalogModel so the exact JSON object of every model is
// preserved; the group and entry names come from the document itself.
type ModelGroups = map[string]map[string]CatalogModel

// ModelCatalog is the flattened provider catalog: a map of model id to model
// entry (upstream `ModelCatalog`).
type ModelCatalog = map[string]CatalogModel

// CatalogModel is one model entry of a bundled catalog.
//
// It keeps the model's exact JSON object and also exposes the typed
// types.Model view, so callers can either losslessly round-trip the catalog or
// work with the shared model DTO. Absent, null and zero values are preserved
// because the raw object is never rewritten.
type CatalogModel struct {
	// Model is the typed view of the entry.
	Model types.Model
	// raw is the verbatim JSON object.
	raw json.RawMessage
}

// Raw returns the verbatim JSON object of the entry.
func (m CatalogModel) Raw() json.RawMessage {
	return append(json.RawMessage(nil), m.raw...)
}

// MarshalJSON emits the verbatim JSON object.
func (m CatalogModel) MarshalJSON() ([]byte, error) {
	if len(m.raw) == 0 {
		// A zero value with no captured object marshals as its typed view so a
		// caller-constructed value still serializes.
		return json.Marshal(m.Model)
	}
	return append([]byte(nil), m.raw...), nil
}

// UnmarshalJSON captures the verbatim object and decodes the typed view.
func (m *CatalogModel) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		m.raw = nil
		m.Model = types.Model{}
		return nil
	}
	if err := json.Unmarshal(trimmed, &m.Model); err != nil {
		return err
	}
	m.raw = append(json.RawMessage(nil), trimmed...)
	return nil
}

// NewCatalogModel builds a catalog entry from a typed model. The raw object is
// derived from the typed view on demand.
func NewCatalogModel(model types.Model) CatalogModel {
	return CatalogModel{Model: model}
}

// FlattenModelCatalog merges the model groups of a provider catalog into a
// single id-keyed catalog.
//
// It is the Go equivalent of the upstream
// `flattenModelCatalog(provider, groups)` helper: it concatenates every group
// object in document order. The provider argument is accepted for parity with
// the upstream signature and asserted onto the resulting entries so the
// flattened catalog always reports the owning provider.
func FlattenModelCatalog[T any](provider string, groups map[string]map[string]T) map[string]T {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]T)
	for _, name := range names {
		for id, model := range groups[name] {
			out[id] = model
		}
	}
	return out
}

// FlattenRawGroups merges the API groups of one catalog JSON document into a
// single id-keyed map, preserving each model's verbatim JSON object. The
// provider argument is recorded on the typed view of every entry.
func FlattenRawGroups(provider string, document []byte) (ModelCatalog, error) {
	groupOrder, err := topLevelKeys(document)
	if err != nil {
		return nil, err
	}
	out := make(ModelCatalog)
	for _, group := range groupOrder {
		entries := map[string]json.RawMessage{}
		if err := decodeObjectField(document, group, &entries); err != nil {
			return nil, fmt.Errorf("catalog: group %q: %w", group, err)
		}
		for id, raw := range entries {
			var model CatalogModel
			if err := json.Unmarshal(raw, &model); err != nil {
				return nil, fmt.Errorf("catalog: model %q in group %q: %w", id, group, err)
			}
			if model.Model.Provider == "" {
				model.Model.Provider = types.ProviderId(provider)
			}
			out[id] = model
		}
	}
	return out, nil
}

// topLevelKeys returns the top-level object keys of a JSON document in document
// order.
func topLevelKeys(document []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("catalog: expected a JSON object")
	}
	var keys []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("catalog: expected an object key")
		}
		keys = append(keys, key)
		// Skip the value so the next token is the following key.
		if err := skipValue(decoder); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// decodeObjectField decodes one top-level field of a JSON object into target.
func decodeObjectField(document []byte, field string, target any) error {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(document, &wrapper); err != nil {
		return err
	}
	raw, ok := wrapper[field]
	if !ok {
		return fmt.Errorf("catalog: missing field %q", field)
	}
	return json.Unmarshal(raw, target)
}

// skipValue consumes a single JSON value from the decoder.
func skipValue(decoder *json.Decoder) error {
	var raw json.RawMessage
	return decoder.Decode(&raw)
}

// MustDocument loads an embedded catalog document, panicking on failure. It is
// used by the generated provider variables, which are initialized from the
// frozen assets that ship with the binary.
func MustDocument(name string) []byte {
	document, err := catalogDataFS.ReadFile("data/" + name)
	if err != nil {
		panic(fmt.Sprintf("catalog: embedded data/%s: %v", name, err))
	}
	return document
}

// MustFlatten builds a flattened provider catalog from an embedded document.
func MustFlatten(provider, name string) ModelCatalog {
	catalog, err := FlattenRawGroups(provider, MustDocument(name))
	if err != nil {
		panic(fmt.Sprintf("catalog: flatten %s: %v", name, err))
	}
	return catalog
}
