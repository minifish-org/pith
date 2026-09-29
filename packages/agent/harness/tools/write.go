// Write tool for the built-in execution tool set.
//
// This is a Go port of packages/agent/src/harness/tools/write.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The tool resolves the requested path through the injected ExecutionEnv,
// serializes mutations of the canonical path with the shared file mutation
// queue, and writes the content (the environment creates missing parent
// directories). Success reuses the caller's path spelling, and cancellation is
// checked both before and after the write so an aborted operation never
// reports a false success.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// writeToolSchema is the model-facing JSON Schema for the write tool, mirroring
// the upstream TypeBox object: required string path and content fields.
var writeToolSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}`)

// WriteToolInput is the parsed parameter object for the write tool.
type WriteToolInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// CreateWriteTool builds the write tool. The upstream details payload is
// `undefined`, so the result details are untyped and always nil.
func CreateWriteTool() harnesstypes.AgentHarnessTool[ExecutionToolContext, WriteToolInput, any] {
	return harnesstypes.AgentHarnessTool[ExecutionToolContext, WriteToolInput, any]{
		Tool: aitypes.Tool{
			Name:        "write",
			Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
			Input:       aitypes.JSONSchemaToolInput(writeToolSchema),
		},
		Label:            "write",
		PrepareArguments: parseWriteToolInput,
		Execute: func(
			_toolCallId string,
			params WriteToolInput,
			_onUpdate harnesstypes.AgentHarnessToolUpdateCallback[any],
			toolContext ExecutionToolContext,
			_invocation harnesstypes.AgentHarnessToolInvocation,
			ctx harnesscontext.Context,
		) (agenttypes.AgentToolResult[any], error) {
			return executeWrite(params, toolContext.Env, ctx)
		},
	}
}

func parseWriteToolInput(args any) (WriteToolInput, error) {
	switch typed := args.(type) {
	case WriteToolInput:
		return typed, nil
	case *WriteToolInput:
		if typed == nil {
			return WriteToolInput{}, nil
		}
		return *typed, nil
	case json.RawMessage:
		return decodeWriteToolInput(typed)
	case []byte:
		return decodeWriteToolInput(typed)
	case string:
		return decodeWriteToolInput([]byte(typed))
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return WriteToolInput{}, fmt.Errorf("write: invalid arguments: %w", err)
		}
		return decodeWriteToolInput(encoded)
	case nil:
		return WriteToolInput{}, nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return WriteToolInput{}, fmt.Errorf("write: invalid arguments: %w", err)
		}
		return decodeWriteToolInput(encoded)
	}
}

func decodeWriteToolInput(raw []byte) (WriteToolInput, error) {
	var input WriteToolInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&input); err != nil {
		return WriteToolInput{}, fmt.Errorf("write: invalid arguments: %w", err)
	}
	return input, nil
}

func executeWrite(params WriteToolInput, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[any], error) {
	if env == nil {
		return agenttypes.AgentToolResult[any]{}, fmt.Errorf("write: missing execution environment")
	}

	resolved := resolveToolPathResult(env, params.Path, ctx)
	if !resolved.OK {
		resolveErr := resolved.Error
		return agenttypes.AgentToolResult[any]{}, &resolveErr
	}
	absolutePath := resolved.Value

	return WithFileMutationQueue(env, absolutePath, func() (agenttypes.AgentToolResult[any], error) {
		if ctx != nil && ctx.Err() != nil {
			return agenttypes.AgentToolResult[any]{}, fmt.Errorf("Operation aborted")
		}
		writeResult := env.WriteFile(absolutePath, []byte(params.Content), ctx)
		if !writeResult.OK {
			writeErr := writeResult.Error
			return agenttypes.AgentToolResult[any]{}, &writeErr
		}
		if ctx != nil && ctx.Err() != nil {
			return agenttypes.AgentToolResult[any]{}, fmt.Errorf("Operation aborted")
		}
		return agenttypes.AgentToolResult[any]{
			Content: []aitypes.ContentBlock{aitypes.TextBlock(fmt.Sprintf("Successfully wrote to %s", params.Path))},
			Details: nil,
		}, nil
	}, ctx)
}
