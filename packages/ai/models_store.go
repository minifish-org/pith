// This file is a Go port of packages/ai/src/models-store.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Persistent model catalogs keyed by provider id. The in-memory store returns a
// structural clone on every read and write, matching the upstream
// `structuredClone` semantics, so callers can mutate a returned entry without
// affecting the stored value.
package ai

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/minifish-org/pith/packages/ai/types"
)

// ModelsStoreEntry is a cached provider catalog.
type ModelsStoreEntry struct {
	// Models is the typed catalog view.
	Models []types.Model
	// LastModified is the Unix timestamp from the remote catalog's
	// Last-Modified header.
	LastModified *float64
	// CheckedAt is the Unix timestamp of the last completed remote check.
	CheckedAt *float64
	// Etag is the opaque validator from the remote catalog's ETag header,
	// stored verbatim (quotes included) and echoed back as If-None-Match.
	Etag *string

	// rawModels preserves the exact JSON object of every model element as it was
	// supplied. Upstream stores structural objects, so a partial model written
	// by a caller round-trips unchanged instead of being rewritten into every
	// typed default field.
	rawModels []json.RawMessage
}

// modelsStoreEntryJSON is the wire shape. Models stays raw so the exact element
// objects survive a decode/encode cycle.
type modelsStoreEntryJSON struct {
	Models       json.RawMessage `json:"models"`
	LastModified *float64        `json:"lastModified,omitempty"`
	CheckedAt    *float64        `json:"checkedAt,omitempty"`
	Etag         *string         `json:"etag,omitempty"`
}

// MarshalJSON emits the stored catalog, preserving the verbatim element objects
// when they were captured from JSON.
func (e ModelsStoreEntry) MarshalJSON() ([]byte, error) {
	var models any
	switch {
	case e.rawModels != nil:
		models = e.rawModels
	case e.Models != nil:
		models = e.Models
	default:
		models = []types.Model(nil)
	}
	return json.Marshal(struct {
		Models       any      `json:"models"`
		LastModified *float64 `json:"lastModified,omitempty"`
		CheckedAt    *float64 `json:"checkedAt,omitempty"`
		Etag         *string  `json:"etag,omitempty"`
	}{Models: models, LastModified: e.LastModified, CheckedAt: e.CheckedAt, Etag: e.Etag})
}

// UnmarshalJSON decodes the catalog and captures the verbatim element objects.
func (e *ModelsStoreEntry) UnmarshalJSON(data []byte) error {
	var wire modelsStoreEntryJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	e.LastModified = wire.LastModified
	e.CheckedAt = wire.CheckedAt
	e.Etag = wire.Etag
	e.rawModels = nil
	e.Models = nil
	if len(wire.Models) == 0 || string(wire.Models) == "null" {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(wire.Models, &raw); err != nil {
		return err
	}
	e.rawModels = raw
	if raw == nil {
		return nil
	}
	models := make([]types.Model, len(raw))
	for i, element := range raw {
		if err := json.Unmarshal(element, &models[i]); err != nil {
			return err
		}
	}
	e.Models = models
	return nil
}

// Clone returns a structural copy of the entry. A nil entry stays nil.
func (e *ModelsStoreEntry) Clone() *ModelsStoreEntry {
	if e == nil {
		return nil
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		// Fall back to a shallow copy; the entry is still usable for
		// in-memory caching even if a pathological model cannot be encoded.
		clone := *e
		clone.Models = append([]types.Model(nil), e.Models...)
		return &clone
	}
	var clone ModelsStoreEntry
	if err := json.Unmarshal(encoded, &clone); err != nil {
		shallow := *e
		shallow.Models = append([]types.Model(nil), e.Models...)
		return &shallow
	}
	return &clone
}

// ModelsStoreOperationOptions carries optional cancellation for a store
// operation.
type ModelsStoreOperationOptions struct {
	Signal context.Context
}

// ModelsStore is persistent model catalog storage keyed by provider ID.
type ModelsStore interface {
	Read(ctx context.Context, providerID string, options *ModelsStoreOperationOptions) (*ModelsStoreEntry, error)
	Write(ctx context.Context, providerID string, entry *ModelsStoreEntry, options *ModelsStoreOperationOptions) error
	Delete(ctx context.Context, providerID string, options *ModelsStoreOperationOptions) error
}

// InMemoryModelsStore is the default in-memory ModelsStore. Apps inject
// persistent stores.
//
// Ports `InMemoryModelsStore` from packages/ai/src/models-store.ts.
type InMemoryModelsStore struct {
	mu      sync.Mutex
	entries map[string]*ModelsStoreEntry
}

// NewInMemoryModelsStore builds an empty store.
func NewInMemoryModelsStore() *InMemoryModelsStore {
	return &InMemoryModelsStore{entries: map[string]*ModelsStoreEntry{}}
}

// storeSignal chooses the effective context for a store operation.
func storeSignal(ctx context.Context, options *ModelsStoreOperationOptions) context.Context {
	if options != nil && options.Signal != nil {
		return options.Signal
	}
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Read returns a structural clone of the stored entry, or nil when missing.
func (s *InMemoryModelsStore) Read(ctx context.Context, providerID string, options *ModelsStoreOperationOptions) (*ModelsStoreEntry, error) {
	signal := storeSignal(ctx, options)
	if err := contextError(signal); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[providerID].Clone(), nil
}

// Write stores a structural clone of the entry.
func (s *InMemoryModelsStore) Write(ctx context.Context, providerID string, entry *ModelsStoreEntry, options *ModelsStoreOperationOptions) error {
	signal := storeSignal(ctx, options)
	if err := contextError(signal); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]*ModelsStoreEntry{}
	}
	s.entries[providerID] = entry.Clone()
	return nil
}

// Delete removes the stored entry.
func (s *InMemoryModelsStore) Delete(ctx context.Context, providerID string, options *ModelsStoreOperationOptions) error {
	signal := storeSignal(ctx, options)
	if err := contextError(signal); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries != nil {
		delete(s.entries, providerID)
	}
	return nil
}

// contextError returns the context's cancellation error, if any.
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
