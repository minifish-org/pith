// Write tool for the Durable coding tool set.
//
// This is a Go port of packages/durable/src/tools/write.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The tool resolves the requested path through the injected environment,
// serializes mutations of the canonical path with the shared file mutation
// queue, and writes the content (the environment creates missing parent
// directories). Success reuses the caller's path spelling, and cancellation is
// checked both before and after the write so an aborted operation never reports
// a false success.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable/harness"
)

// writeToolSchema is the model-facing JSON Schema for the write tool, mirroring
// the upstream TypeBox object: required string path and content fields.
var writeToolSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}`)

// WriteToolInput is the parsed parameter object for the write tool.
type WriteToolInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// errOperationAborted is the source error a tool raises when its invocation
// context was already aborted at a cancellation checkpoint.
var errOperationAborted = errors.New("Operation aborted")

// CreateWriteTool builds the write tool.
func CreateWriteTool() harness.ToolRegistration {
	return harness.ToolRegistration{
		Declaration: types.Tool{
			Name:        "write",
			Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
			Input:       types.JSONSchemaToolInput(writeToolSchema),
		},
		Execute: func(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
			return executeWrite(ctx, raw, api)
		},
	}
}

func executeWrite(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
	var input WriteToolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("write: invalid arguments: %w", err)
		}
	}
	e, err := requireEnv(api)
	if err != nil {
		return harness.ToolResult{}, err
	}
	absolutePath, err := ResolveToolPath(ctx, e, input.Path)
	if err != nil {
		return harness.ToolResult{}, err
	}

	return WithFileMutationQueue(ctx, e, absolutePath, func() (harness.ToolResult, error) {
		if ctx.Err() != nil {
			return harness.ToolResult{}, errOperationAborted
		}
		if err := e.WriteFile(ctx, absolutePath, []byte(input.Content)); err != nil {
			return harness.ToolResult{}, err
		}
		if ctx.Err() != nil {
			return harness.ToolResult{}, errOperationAborted
		}
		return harness.ToolResult{
			Content: []types.ContentBlock{types.TextBlock(fmt.Sprintf("Successfully wrote to %s", input.Path))},
		}, nil
	})
}
