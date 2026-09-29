package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	env "github.com/minifish-org/pith/packages/agent/harness/env"
	tools "github.com/minifish-org/pith/packages/agent/harness/tools"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden vectors or TS
// sources, and it does not implement SDK behavior itself.
//
// The tools-bash batch exercises the bash tool inside a temporary local
// execution environment. Command preparation, environment/cwd injection,
// bounded combined output, truncation, spill files, exit-code handling,
// timeouts and aborts all come from the production tool.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op   string          `json:"op"`
		File string          `json:"file"`
		Fn   string          `json:"fn"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "tool":
		return runBashTool(ctx, envelope.File, envelope.Fn, envelope.Args)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

func runBashTool(ctx context.Context, file string, fn string, args json.RawMessage) (json.RawMessage, error) {
	if !strings.HasSuffix(file, "harness/tools/bash.ts") {
		return nil, fmt.Errorf("conformance: unsupported source %q", file)
	}
	if fn != "createBashTool" {
		return nil, fmt.Errorf("conformance: unsupported bash function %q", fn)
	}

	root, err := os.MkdirTemp("", "pith-conformance-")
	if err != nil {
		return nil, fmt.Errorf("conformance: create temp root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	tool := tools.CreateBashTool(nil)

	params, err := tool.PrepareArguments(args)
	if err != nil {
		return nil, err
	}

	result, err := tool.Execute("c1", params, noopBashUpdate, tools.ExecutionToolContext{Env: environment}, nil, ctx)
	if err != nil {
		return nil, err
	}

	resultPayload := map[string]any{"content": result.Content}
	if result.Details != nil {
		resultPayload["details"] = result.Details
	}
	payload := map[string]any{"result": resultPayload}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("conformance: encode result: %w", err)
	}
	return encoded, nil
}

func noopBashUpdate(agenttypes.AgentToolResult[*tools.BashToolDetails], *harnesstypes.AgentHarnessToolUpdateOptions) {
}
