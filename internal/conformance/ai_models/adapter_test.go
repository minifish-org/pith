package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The supported operations are the ones the ai-models batch defines:
// model-store (the in-memory catalog store), call (the exported model utility
// functions) and session-resource (the session cleanup registry). Any other
// operation is reported as an error rather than silently succeeding.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "model-store":
		return runModelStoreCase(ctx, input)
	case "call":
		return runCallCase(ctx, input)
	case "session-resource":
		return runSessionResourceCase(input)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

// runModelStoreCase drives the real InMemoryModelsStore through the shared
// scenario: write a catalog, mutate the caller's object and the first read,
// re-read and finally delete. Structural-clone isolation is the behavior under
// test.
func runModelStoreCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Aborted bool `json:"aborted"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("conformance: invalid model-store input: %w", err)
	}

	store := ai.NewInMemoryModelsStore()

	if request.Aborted {
		aborted, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := store.Read(aborted, "p", &ai.ModelsStoreOperationOptions{Signal: aborted}); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("conformance: aborted store read unexpectedly succeeded")
	}

	// The shared scenario writes a partial catalog entry. Decoding it from JSON
	// captures the exact element objects so the typed store preserves them.
	var entry ai.ModelsStoreEntry
	if err := json.Unmarshal([]byte(`{"models":[{"id":"one"}],"etag":"\"v1\"","checkedAt":0}`), &entry); err != nil {
		return nil, fmt.Errorf("conformance: invalid scenario entry: %w", err)
	}
	if err := store.Write(ctx, "p", &entry, nil); err != nil {
		return nil, err
	}

	// Mutating the caller's object must not affect the stored value.
	if len(entry.Models) > 0 {
		entry.Models[0].Id = "wrong"
	}

	first, err := store.Read(ctx, "p", nil)
	if err != nil {
		return nil, err
	}
	if first == nil {
		return nil, fmt.Errorf("conformance: read returned no entry")
	}
	original, err := json.Marshal(first)
	if err != nil {
		return nil, err
	}

	// Mutating a read result must not affect the stored value either.
	if len(first.Models) > 0 {
		first.Models[0].Id = "wrong2"
	}
	second, err := store.Read(ctx, "p", nil)
	if err != nil {
		return nil, err
	}
	reread, err := json.Marshal(second)
	if err != nil {
		return nil, err
	}

	if err := store.Delete(ctx, "p", nil); err != nil {
		return nil, err
	}
	deleted, err := store.Read(ctx, "p", nil)
	if err != nil {
		return nil, err
	}
	deletedValue := json.RawMessage(`{"$undefined":true}`)
	if deleted != nil {
		encoded, marshalErr := json.Marshal(deleted)
		if marshalErr != nil {
			return nil, marshalErr
		}
		deletedValue = encoded
	}

	return json.Marshal(map[string]json.RawMessage{
		"original": original,
		"reread":   reread,
		"deleted":  deletedValue,
	})
}

// runCallCase dispatches to the exported model utility functions. The bridge
// only decodes arguments and encodes results; the behavior lives in the SDK.
func runCallCase(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Fn   string            `json:"fn"`
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("conformance: invalid call input: %w", err)
	}

	switch request.Fn {
	case "modelsAreEqual":
		first, err := decodeModelArg(request.Args, 0)
		if err != nil {
			return nil, err
		}
		second, err := decodeModelArg(request.Args, 1)
		if err != nil {
			return nil, err
		}
		return json.Marshal(ai.ModelsAreEqual(first, second))

	case "hasApi":
		model, err := decodeModelArg(request.Args, 0)
		if err != nil {
			return nil, err
		}
		var api types.Api
		if err := decodeArg(request.Args, 1, &api); err != nil {
			return nil, err
		}
		matched := model != nil && ai.HasApi(*model, api)
		return json.Marshal(matched)

	case "getSupportedThinkingLevels":
		model, err := decodeModelArg(request.Args, 0)
		if err != nil {
			return nil, err
		}
		if model == nil {
			return nil, fmt.Errorf("conformance: getSupportedThinkingLevels requires a model")
		}
		return json.Marshal(ai.GetSupportedThinkingLevels(*model))

	case "clampThinkingLevel":
		model, err := decodeModelArg(request.Args, 0)
		if err != nil {
			return nil, err
		}
		if model == nil {
			return nil, fmt.Errorf("conformance: clampThinkingLevel requires a model")
		}
		var level types.ModelThinkingLevel
		if err := decodeArg(request.Args, 1, &level); err != nil {
			return nil, err
		}
		return json.Marshal(ai.ClampThinkingLevel(*model, level))

	case "calculateCost":
		model, err := decodeModelArg(request.Args, 0)
		if err != nil {
			return nil, err
		}
		if model == nil {
			return nil, fmt.Errorf("conformance: calculateCost requires a model")
		}
		var usage types.Usage
		if err := decodeArg(request.Args, 1, &usage); err != nil {
			return nil, err
		}
		cost := ai.CalculateCost(*model, &usage)
		return json.Marshal(cost)

	default:
		return nil, fmt.Errorf("conformance: unsupported call %q", request.Fn)
	}
}

func decodeModelArg(args []json.RawMessage, index int) (*types.Model, error) {
	if index >= len(args) {
		return nil, fmt.Errorf("conformance: missing argument %d", index)
	}
	if string(args[index]) == "null" {
		return nil, nil
	}
	var model types.Model
	if err := json.Unmarshal(args[index], &model); err != nil {
		return nil, err
	}
	return &model, nil
}

func decodeArg(args []json.RawMessage, index int, target any) error {
	if index >= len(args) {
		return fmt.Errorf("conformance: missing argument %d", index)
	}
	return json.Unmarshal(args[index], target)
}

// runSessionResourceCase registers two cleanups, unsubscribes one, then runs a
// cleanup pass. It reports the invocation order, the number of aggregated
// failures and whether the unsubscribed cleanup ran.
func runSessionResourceCase(input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Fail bool `json:"fail"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("conformance: invalid session-resource input: %w", err)
	}

	order := []string{}
	unsubscribedRan := false
	remove := ai.RegisterSessionResourceCleanup(func(sessionID *string) error {
		order = append(order, "removed")
		unsubscribedRan = true
		return nil
	})
	ai.RegisterSessionResourceCleanup(func(sessionID *string) error {
		order = append(order, "first")
		if request.Fail {
			return fmt.Errorf("fixture error")
		}
		return nil
	})
	ai.RegisterSessionResourceCleanup(func(sessionID *string) error {
		order = append(order, "second")
		return nil
	})
	remove()

	err := ai.CleanupSessionResources(nil)
	failures := 0
	if err != nil {
		var aggregate *ai.AggregateCleanupError
		if !errorAs(err, &aggregate) {
			return nil, err
		}
		failures = len(aggregate.Errors)
	}

	return json.Marshal(map[string]any{
		"order":           order,
		"failures":        failures,
		"unsubscribedRan": unsubscribedRan,
	})
}

// errorAs is a small local wrapper so the adapter can classify the aggregate
// error without importing errors at the call site.
func errorAs(err error, target **ai.AggregateCleanupError) bool {
	aggregate, ok := err.(*ai.AggregateCleanupError)
	if !ok {
		return false
	}
	*target = aggregate
	return true
}
