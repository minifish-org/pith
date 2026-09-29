package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op   string            `json:"op"`
		File string            `json:"file"`
		Fn   string            `json:"fn"`
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "session":
		return runSessionLifecycle(ctx)
	case "storage":
		return runStorageLifecycle(ctx)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

type branchView struct {
	Name string `json:"name"`
}

type sessionResult struct {
	Name      *string                        `json:"name"`
	Branch    branchView                     `json:"branch"`
	Label     *string                        `json:"label"`
	Again     *string                        `json:"again"`
	Remaining []harnesstypes.SessionMetadata `json:"remaining"`
}

// runSessionLifecycle mirrors the evaluator's memory-session operation using
// the real repository, branch and value APIs.
func runSessionLifecycle(ctx context.Context) (json.RawMessage, error) {
	repo := session.NewMemorySessionRepo(&session.MemorySessionRepoOptions{Now: func() float64 { return 1 }})
	sessionValue, err := repo.Create(harnesstypes.SessionCreateOptions{ID: stringPointer("session")}, ctx)
	if err != nil {
		return nil, err
	}
	closeAll := func() {
		_ = sessionValue.Close(ctx)
	}
	defer closeAll()

	name := "demo"
	if err := sessionValue.SetName(&name, ctx); err != nil {
		return nil, err
	}
	if _, err := sessionValue.CreateBranch("main", nil, ctx); err != nil {
		return nil, err
	}
	loadedName, err := sessionValue.GetName(ctx)
	if err != nil {
		return nil, err
	}
	branchValue, ok, err := sessionValue.Branch("main", ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("conformance: branch main is missing")
	}
	labelValue := "label"
	if err := sessionValue.SetLabel("e1", &labelValue, ctx); err != nil {
		return nil, err
	}
	loadedLabel, err := sessionValue.GetLabel("e1", ctx)
	if err != nil {
		return nil, err
	}
	metadata := sessionValue.Metadata()
	if err := sessionValue.Close(ctx); err != nil {
		return nil, err
	}
	reopened, err := repo.Open(metadata, ctx)
	if err != nil {
		return nil, err
	}
	again, err := reopened.GetName(ctx)
	if err != nil {
		return nil, err
	}
	if err := reopened.Close(ctx); err != nil {
		return nil, err
	}
	if err := repo.Delete(metadata, ctx); err != nil {
		return nil, err
	}
	remaining, err := repo.List(nil, ctx)
	if err != nil {
		return nil, err
	}
	result := sessionResult{
		Name:      loadedName,
		Branch:    branchView{Name: branchValue.Name()},
		Label:     loadedLabel,
		Again:     again,
		Remaining: remaining,
	}
	return json.Marshal(result)
}

type storageResult struct {
	FirstSeq int   `json:"firstSeq"`
	Seqs     []int `json:"seqs"`
	Value    any   `json:"value"`
}

// runStorageLifecycle exercises a few commits through the real MemoryStorage
// SDK for operations that request the storage surface.
func runStorageLifecycle(ctx context.Context) (json.RawMessage, error) {
	storage := session.NewMemoryStorage(&session.MemoryStorageOptions{Now: func() float64 { return 7 }})
	defer storage.Close(ctx)
	address := session.NewValue("conformance.value")
	result, err := storage.Commit([]harnesstypes.Write{
		session.SetValue(address, "first"),
		session.SetValue(address, "second"),
	}, ctx)
	if err != nil {
		return nil, err
	}
	stored, _, err := storage.GetValue(address, ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(storageResult{FirstSeq: result.FirstSeq, Seqs: result.Seqs, Value: stored.Value})
}

func stringPointer(value string) *string { return &value }
