// Codemode integration for the embedded SDK.
//
// This file ports the native behavior of
// packages/coding-agent/src/extensions/codemode/tool.ts and execute.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. The `codemode` tool
// runs a JavaScript body in the accepted CGO-free QuickJS sandbox
// (packages/codemode) and exposes the registry's tools to the script as
// `tools.<identifier>(args)`.
//
// Nested calls never bypass the registry: each one re-enters schema validation
// and the Before/After permission hooks through ToolRegistry.executeTool, and
// the deny list applies to every exposure. A tool's structured content becomes
// the script call's return value. A successful script's store() writes are
// persisted on the session branch (see codemode_store.go); a failed script
// commits nothing.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/codemode"
)

// CodemodeToolName is the registered name of the code-input tool.
const CodemodeToolName = "codemode"

// codemodeSchema is the tool parameter schema: one `code` string. The upstream
// tool also accepts a leading options comment inside the source, so the JSON
// shape is intentionally minimal.
var codemodeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "code": {
      "type": "string",
      "description": "JavaScript async function body. Call tools.<name>(args) to invoke a tool."
    }
  },
  "required": ["code"]
}`)

// NewCodemodeTool builds a code-input tool that runs scripts in the native
// Codemode sandbox. Every tool registered in the registry becomes callable from
// a script as `tools.<identifier>`; deferred and codemode exposure tools are
// reachable this way even though they are not declared to the provider.
func NewCodemodeTool(registry *ToolRegistry, options *codemode.SandboxOptions) (ToolDefinition, error) {
	if registry == nil {
		return ToolDefinition{}, errors.New("codemode requires a tool registry")
	}
	sandboxOptions := codemode.SandboxOptions{}
	if options != nil {
		sandboxOptions.Timeout = options.Timeout
		sandboxOptions.MemoryLimitBytes = options.MemoryLimitBytes
		sandboxOptions.MaxStackBytes = options.MaxStackBytes
	}
	sandboxOptions.Tools = append(sandboxOptions.Tools, registry.codemodeToolSpecs()...)

	sandbox, err := codemode.NewSandbox(sandboxOptions)
	if err != nil {
		return ToolDefinition{}, err
	}

	definition := ToolDefinition{
		Name:         CodemodeToolName,
		Description:  codemodeDescription(registry),
		Parameters:   codemodeSchema,
		OutputSchema: json.RawMessage(`{"type": "object"}`),
		Exposure:     ExposureDirect,
		close:        func() error { return sandbox.Close(context.Background()) },
	}
	definition.Execute = func(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
		return executeCodemode(ctx, registry, sandbox, arguments)
	}
	return definition, nil
}

// codemodeToolSpecs adapts every registered tool to a script-callable spec.
func (r *ToolRegistry) codemodeToolSpecs() []codemode.Tool {
	definitions := r.allDefinitions()
	specs := make([]codemode.Tool, 0, len(definitions))
	for _, definition := range definitions {
		def := definition
		if def.exposure() == ExposureHidden {
			continue
		}
		specs = append(specs, codemode.Tool{
			Name:         def.Name,
			Description:  def.Description,
			InputSchema:  def.Parameters,
			OutputSchema: def.OutputSchema,
			Execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
				result, err := r.ExecuteNested(ctx, ToolCall{Name: def.Name, Arguments: args})
				if err != nil {
					return nil, err
				}
				if len(result.StructuredContent) > 0 {
					return result.StructuredContent, nil
				}
				text := toolResultText(result)
				encoded, err := json.Marshal(text)
				if err != nil {
					return nil, err
				}
				return encoded, nil
			},
		})
	}
	return specs
}

// executeCodemode parses the code input, runs it against the sandbox and maps
// the sandbox result onto a tool result. Successful store writes are committed
// through the registry's Codemode store; failures commit nothing.
func executeCodemode(ctx context.Context, registry *ToolRegistry, sandbox *codemode.Sandbox, arguments json.RawMessage) (ToolResult, error) {
	var input struct {
		Code string `json:"code"`
	}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &input); err != nil {
			return ToolResult{}, fmt.Errorf("codemode arguments: %w", err)
		}
	}
	if strings.TrimSpace(input.Code) == "" {
		return ToolResult{}, errors.New("codemode requires code")
	}

	executeOptions := codemode.ExecuteOptions{}
	if store := registry.getCodemodeStore(); store != nil {
		executeOptions.Store = store.Snapshot()
	}

	result := sandbox.Execute(ctx, input.Code, executeOptions)

	if result.OK && result.StoreWrites != nil {
		if store := registry.getCodemodeStore(); store != nil {
			if err := store.Append(*result.StoreWrites); err != nil {
				return ToolResult{}, err
			}
		}
	}

	blocks := make([]aitypes.ContentBlock, 0, len(result.Output)+1)
	for _, item := range result.Output {
		if item.Type == "image" {
			blocks = append(blocks, aitypes.ImageBlock(item.Data, item.MimeType))
			continue
		}
		blocks = append(blocks, aitypes.TextBlock(item.Text))
	}
	if result.OK {
		if len(result.Value) > 0 && string(result.Value) != "null" {
			blocks = append(blocks, aitypes.TextBlock(string(result.Value)))
		}
		return ToolResult{
			Content:           blocks,
			StructuredContent: result.Value,
		}, nil
	}

	message := codemodeErrorMessage(result.Error)
	blocks = append(blocks, aitypes.TextBlock(message))
	return ToolResult{Content: blocks, IsError: true}, nil
}

// codemodeErrorMessage renders a sandbox failure the way upstream does.
func codemodeErrorMessage(err *codemode.ExecutionError) string {
	if err == nil {
		return "Script error"
	}
	switch err.Kind {
	case "timeout":
		return "Script timed out: " + err.Message
	case "aborted":
		return "Script aborted: " + err.Message
	case "sandbox":
		return "Script sandbox failed: " + err.Message
	default:
		if err.Stack != "" {
			return err.Stack
		}
		if err.Name != "" {
			return err.Name + ": " + err.Message
		}
		return err.Message
	}
}

// codemodeDescription lists the callable tool identifiers. The upstream
// description is budgeted; the Go port uses a compact, deterministic listing.
func codemodeDescription(registry *ToolRegistry) string {
	var builder strings.Builder
	builder.WriteString("Run JavaScript in a sandboxed runtime. Call tools.<name>(args) to invoke a tool.\n\nCallable tools:")
	for _, definition := range registry.allDefinitions() {
		builder.WriteString("\n- ")
		builder.WriteString(codemode.ToIdentifier(definition.Name))
		if strings.TrimSpace(definition.Description) != "" {
			builder.WriteString(": ")
			builder.WriteString(strings.TrimSpace(definition.Description))
		}
	}
	return builder.String()
}
