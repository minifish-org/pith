// Barrel and registration surface for the built-in execution tools.
//
// This is a Go port of packages/agent/src/harness/tools/index.ts at Pi
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// index.ts is a pure re-export barrel: it forwards the concrete declarations
// from bash.ts, edit.ts, read.ts, tool-context.ts and write.ts. Go has no barrel
// re-export, so this file keeps the concrete declarations in their own files
// (the provenance-preserving one-to-one mapping) and adds the explicit
// registration helper the SDK assembly entry point uses to install the built-in
// tool set.
package tools

import (
	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// BuiltinTool is a type-erased view of one built-in AgentHarnessTool. The four
// built-in tools are generic in their parameters and details, so the SDK
// assembly cannot store them in one homogeneous slice without an adapter. The
// adapter keeps the declaration and the prepare/execute closures together and
// erases only the details type.
type BuiltinTool struct {
	// Tool is the model-facing declaration.
	Tool aitypes.Tool
	// Label is the human-readable tool label.
	Label string
	// PrepareArguments coerces loose model input into the typed parameters.
	PrepareArguments func(args any) (any, error)
	// Execute runs the tool against an injected ExecutionEnv.
	Execute func(
		toolCallID string,
		params any,
		env harnesstypes.ExecutionEnv,
		ctx harnesscontext.Context,
	) (agenttypes.AgentToolResult[any], error)
}

// CreateBuiltinTools builds the four built-in execution tools (read, write,
// edit, bash) in their stable registration order. Image processing is left to
// the caller: the read tool defaults to passing images through unchanged, which
// is the upstream behavior when no processor is supplied.
func CreateBuiltinTools() []BuiltinTool {
	read := CreateReadTool(nil)
	write := CreateWriteTool()
	edit := CreateEditTool()
	bash := CreateBashTool(nil)

	return []BuiltinTool{
		{
			Tool:  read.Tool,
			Label: read.Label,
			PrepareArguments: func(args any) (any, error) {
				return read.PrepareArguments(args)
			},
			Execute: func(toolCallID string, params any, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[any], error) {
				typed, _ := params.(ReadToolInput)
				result, err := read.Execute(toolCallID, typed, nil, ExecutionToolContext{Env: env}, nil, ctx)
				return eraseToolResult(result), err
			},
		},
		{
			Tool:  write.Tool,
			Label: write.Label,
			PrepareArguments: func(args any) (any, error) {
				return write.PrepareArguments(args)
			},
			Execute: func(toolCallID string, params any, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[any], error) {
				typed, _ := params.(WriteToolInput)
				result, err := write.Execute(toolCallID, typed, nil, ExecutionToolContext{Env: env}, nil, ctx)
				return eraseToolResult(result), err
			},
		},
		{
			Tool:  edit.Tool,
			Label: edit.Label,
			PrepareArguments: func(args any) (any, error) {
				return edit.PrepareArguments(args)
			},
			Execute: func(toolCallID string, params any, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[any], error) {
				typed, _ := params.(EditToolInput)
				result, err := edit.Execute(toolCallID, typed, nil, ExecutionToolContext{Env: env}, nil, ctx)
				return eraseToolResult(result), err
			},
		},
		{
			Tool:  bash.Tool,
			Label: bash.Label,
			PrepareArguments: func(args any) (any, error) {
				return bash.PrepareArguments(args)
			},
			Execute: func(toolCallID string, params any, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[any], error) {
				typed, _ := params.(BashToolInput)
				result, err := bash.Execute(toolCallID, typed, nil, ExecutionToolContext{Env: env}, nil, ctx)
				return eraseToolResult(result), err
			},
		},
	}
}

// eraseToolResult widens a typed tool result to the type-erased registration
// view without dropping content, usage or the termination hint.
func eraseToolResult[T any](result agenttypes.AgentToolResult[T]) agenttypes.AgentToolResult[any] {
	return agenttypes.AgentToolResult[any]{
		Content:   result.Content,
		Details:   result.Details,
		Usage:     result.Usage,
		Terminate: result.Terminate,
	}
}
