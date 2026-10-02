// Persisted Codemode store for the embedded SDK.
//
// This file ports the store-reading half of
// packages/coding-agent/src/extensions/codemode/execute.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe. A successful script's store()
// writes are appended to the session branch as `codemode-store` custom entries;
// load() replays those entries from the root. Failed scripts commit nothing.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"encoding/json"
	"sync"

	"github.com/minifish-org/pith/packages/codemode"
)

// CodemodeStoreEntry is the custom entry type holding one script's store writes.
const CodemodeStoreEntry = "codemode-store"

// CodemodeStoreEntryData is the payload of a `codemode-store` entry.
type CodemodeStoreEntryData struct {
	Set    map[string]json.RawMessage `json:"set"`
	Delete []string                   `json:"delete"`
}

// CodemodeStore reads and writes the persisted Codemode store for a session
// branch. It is safe for concurrent use; the session manager serializes the
// underlying appends.
type CodemodeStore struct {
	mu      sync.Mutex
	manager *SessionManager
}

// NewCodemodeStore binds a store to a session manager. A nil manager makes the
// store an in-memory no-op that still reports an empty snapshot.
func NewCodemodeStore(manager *SessionManager) *CodemodeStore {
	return &CodemodeStore{manager: manager}
}

// Snapshot replays the `codemode-store` entries on the active branch from the
// root and returns the resulting key/value map. Each entry deletes its keys
// first, then applies its set. The returned map is independent of the branch.
func (s *CodemodeStore) Snapshot() map[string]json.RawMessage {
	if s == nil || s.manager == nil {
		return map[string]json.RawMessage{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	branch := s.manager.GetBranch("")
	values := map[string]json.RawMessage{}
	for _, entry := range branch {
		data, ok := codemodeStoreDataOf(entry)
		if !ok {
			continue
		}
		for _, key := range data.Delete {
			delete(values, key)
		}
		for key, value := range data.Set {
			values[key] = append(json.RawMessage(nil), value...)
		}
	}
	return values
}

// Append persists one script's store writes as a `codemode-store` custom entry.
// It must only be called for a successful execution.
func (s *CodemodeStore) Append(writes codemode.StoreWrites) error {
	if s == nil || s.manager == nil {
		return nil
	}
	if len(writes.Set) == 0 && len(writes.Delete) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(CodemodeStoreEntryData{
		Set:    writes.Set,
		Delete: append([]string(nil), writes.Delete...),
	})
	if err != nil {
		return err
	}
	_, err = s.manager.AppendCustomEntry(CodemodeStoreEntry, data)
	return err
}

// ReadCodemodeStore replays `codemode-store` entries for a raw branch. It is
// exported so callers with a branch view can resolve load() values without a
// CodemodeStore wrapper.
func ReadCodemodeStore(branch []SessionEntry) map[string]json.RawMessage {
	values := map[string]json.RawMessage{}
	for _, entry := range branch {
		data, ok := codemodeStoreDataOf(entry)
		if !ok {
			continue
		}
		for _, key := range data.Delete {
			delete(values, key)
		}
		for key, value := range data.Set {
			values[key] = append(json.RawMessage(nil), value...)
		}
	}
	return values
}

func codemodeStoreDataOf(entry SessionEntry) (CodemodeStoreEntryData, bool) {
	if entry.Type != "custom" {
		return CodemodeStoreEntryData{}, false
	}
	var payload struct {
		CustomType string          `json:"customType"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return CodemodeStoreEntryData{}, false
	}
	if payload.CustomType != CodemodeStoreEntry || len(payload.Data) == 0 {
		return CodemodeStoreEntryData{}, false
	}
	var data CodemodeStoreEntryData
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return CodemodeStoreEntryData{}, false
	}
	if data.Set == nil {
		data.Set = map[string]json.RawMessage{}
	}
	return data, true
}
