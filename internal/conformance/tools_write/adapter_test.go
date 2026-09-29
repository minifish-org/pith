package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	env "github.com/minifish-org/pith/packages/agent/harness/env"
	tools "github.com/minifish-org/pith/packages/agent/harness/tools"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The tools-write batch exercises the write tool: a temporary local execution
// environment and a pre-seeded file.txt are prepared, then the real
// CreateWriteTool result and the resulting file content are returned. Error
// classification, path resolution, parent-directory creation and cancellation
// all come from the production tool.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op      string          `json:"op"`
		File    string          `json:"file"`
		Fn      string          `json:"fn"`
		Text    *string         `json:"text"`
		Bytes   []int           `json:"bytes"`
		Args    json.RawMessage `json:"args"`
		Inspect *string         `json:"inspect"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "tool":
		return runWriteTool(ctx, envelope.File, envelope.Fn, envelope.Text, envelope.Bytes, envelope.Args, envelope.Inspect)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

func runWriteTool(ctx context.Context, file string, fn string, text *string, bytesValue []int, args json.RawMessage, inspect *string) (json.RawMessage, error) {
	if !strings.HasSuffix(file, "harness/tools/write.ts") {
		return nil, fmt.Errorf("conformance: unsupported source %q", file)
	}
	if fn != "createWriteTool" {
		return nil, fmt.Errorf("conformance: unsupported write function %q", fn)
	}

	root, err := os.MkdirTemp("", "pith-conformance-")
	if err != nil {
		return nil, fmt.Errorf("conformance: create temp root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})

	var initial []byte
	if bytesValue != nil {
		initial = make([]byte, len(bytesValue))
		for index, value := range bytesValue {
			initial[index] = byte(value)
		}
	} else if text != nil {
		initial = []byte(*text)
	}
	if seed := environment.WriteFile("file.txt", initial, ctx); !seed.OK {
		seedErr := seed.Error
		return nil, &seedErr
	}

	tool := tools.CreateWriteTool()
	params, err := tool.PrepareArguments(args)
	if err != nil {
		return nil, err
	}

	result, err := tool.Execute("c1", params, func(agenttypes.AgentToolResult[any], *harnesstypes.AgentHarnessToolUpdateOptions) {
	}, tools.ExecutionToolContext{Env: environment}, writeInvocation{}, ctx)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{}
	resultPayload := map[string]any{"content": result.Content}
	if result.Details != nil {
		resultPayload["details"] = result.Details
	}
	payload["result"] = resultPayload

	if inspect != nil {
		after, err := os.ReadFile(filepath.Join(root, *inspect))
		if err != nil {
			return nil, fmt.Errorf("conformance: read inspected file: %w", err)
		}
		payload["after"] = strings.ReplaceAll(string(after), root, "$ROOT")
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("conformance: encode result: %w", err)
	}
	return encoded, nil
}

// writeInvocation is a minimal durable identity for the write tool. The write
// tool never touches invocation memo state, but the interface value must be
// non-nil so a future change cannot silently dereference a typed nil.
type writeInvocation struct{}

func (writeInvocation) InvocationID() string                                 { return "i1" }
func (writeInvocation) OperationID() string                                  { return "r1" }
func (writeInvocation) TurnID() string                                       { return "t1" }
func (writeInvocation) GetMemo(string) (harnesstypes.JsonValue, bool, error) { return nil, false, nil }
func (writeInvocation) SetMemo(string, harnesstypes.JsonValue) error         { return nil }
