package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/compat"
	"github.com/minifish-org/pith/packages/ai/providers"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK; it never reads
// expected results, golden files or the TypeScript sources, and it does not
// implement any SDK behavior itself.
//
// The ai-surface batch defines the `call` operation over the compat entry point:
// the input names a source file and an exported function, and args are passed
// through to the real Go equivalent.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	_ = ctx
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
	case "call":
		return runCall(envelope.File, envelope.Fn, envelope.Args)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

func runCall(file string, fn string, args []json.RawMessage) (json.RawMessage, error) {
	if file != "packages/ai/src/compat.ts" {
		return nil, fmt.Errorf("conformance: unsupported source file %q", file)
	}
	switch fn {
	case "getProviders":
		return json.Marshal(compat.GetProviders())
	case "getModels":
		provider, err := stringArg(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(compat.GetModels(providers.BuiltinProvider(provider)))
	case "getModel":
		provider, err := stringArg(args, 0)
		if err != nil {
			return nil, err
		}
		modelID, err := stringArg(args, 1)
		if err != nil {
			return nil, err
		}
		return json.Marshal(compat.GetModel(providers.BuiltinProvider(provider), modelID))
	default:
		return nil, fmt.Errorf("conformance: unsupported function %q", fn)
	}
}

func stringArg(args []json.RawMessage, index int) (string, error) {
	if index >= len(args) {
		return "", fmt.Errorf("conformance: missing argument %d", index)
	}
	var value string
	if err := json.Unmarshal(args[index], &value); err != nil {
		return "", fmt.Errorf("conformance: argument %d must be a string: %w", index, err)
	}
	return value, nil
}
