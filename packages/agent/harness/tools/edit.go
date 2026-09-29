// Edit tool for the built-in execution tool set.
//
// This is a Go port of packages/agent/src/harness/tools/edit.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The tool resolves the requested path through the injected ExecutionEnv,
// serializes mutations of the canonical path with the shared file mutation
// queue, reads the file, applies every edits[].oldText against the same
// original content, and writes the result back while preserving the file's
// BOM and line-ending spelling. The returned details carry the display diff,
// the unified patch and the first changed line.
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

// editToolSchema is the model-facing JSON Schema for the edit tool. It mirrors
// the upstream TypeBox object: a required path and at least one replacement
// object with required oldText and newText strings.
var editToolSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}}},"required":["path","edits"]}`)

// EditToolInput is the parsed parameter object for the edit tool.
type EditToolInput struct {
	Path  string `json:"path"`
	Edits []Edit `json:"edits"`
}

// EditToolDetails carries the diff payload of a successful edit. FirstChangedLine
// is nil only if no change was detected (which the tool rejects before
// returning).
type EditToolDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

// CreateEditTool builds the edit tool bound to an ExecutionToolContext.
func CreateEditTool() harnesstypes.AgentHarnessTool[ExecutionToolContext, EditToolInput, *EditToolDetails] {
	return harnesstypes.AgentHarnessTool[ExecutionToolContext, EditToolInput, *EditToolDetails]{
		Tool: aitypes.Tool{
			Name:        "edit",
			Description: "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
			Input:       aitypes.JSONSchemaToolInput(editToolSchema),
		},
		Label:            "edit",
		PrepareArguments: prepareEditArguments,
		Replay:           "",
		Execute: func(
			_toolCallId string,
			params EditToolInput,
			_onUpdate harnesstypes.AgentHarnessToolUpdateCallback[*EditToolDetails],
			toolContext ExecutionToolContext,
			_invocation harnesstypes.AgentHarnessToolInvocation,
			ctx harnesscontext.Context,
		) (agenttypes.AgentToolResult[*EditToolDetails], error) {
			return executeEdit(params, toolContext.Env, ctx)
		},
	}
}

// prepareEditArguments normalizes loose model input into an EditToolInput. It
// accepts the legacy top-level oldText/newText form, an `edits` array, a single
// edit object in `edits`, or a JSON-encoded `edits` string, in the same order
// as the upstream implementation.
func prepareEditArguments(args any) (EditToolInput, error) {
	switch typed := args.(type) {
	case EditToolInput:
		return typed, nil
	case *EditToolInput:
		if typed == nil {
			return EditToolInput{}, nil
		}
		return *typed, nil
	}

	object, err := editArgumentObject(args)
	if err != nil {
		return EditToolInput{}, err
	}
	if object == nil {
		return EditToolInput{}, nil
	}

	if rawEdits, ok := object["edits"]; ok {
		switch value := rawEdits.(type) {
		case string:
			var parsed any
			if err := json.Unmarshal([]byte(value), &parsed); err == nil {
				if array, ok := parsed.([]any); ok {
					object["edits"] = array
				} else if single, ok := asSingleEditInput(parsed); ok {
					object["edits"] = []any{single}
				}
			}
		default:
			if single, ok := asSingleEditInput(value); ok {
				object["edits"] = []any{single}
			}
		}
	}

	oldText, hasOldText := object["oldText"].(string)
	newText, hasNewText := object["newText"].(string)
	if hasOldText && hasNewText {
		var edits []any
		if array, ok := object["edits"].([]any); ok {
			edits = append(edits, array...)
		}
		edits = append(edits, map[string]any{"oldText": oldText, "newText": newText})
		delete(object, "oldText")
		delete(object, "newText")
		object["edits"] = edits
	}

	return editInputFromObject(object)
}

func editArgumentObject(args any) (map[string]any, error) {
	switch typed := args.(type) {
	case json.RawMessage:
		return decodeEditArgumentObject(typed)
	case []byte:
		return decodeEditArgumentObject(typed)
	case string:
		return decodeEditArgumentObject([]byte(typed))
	case map[string]any:
		return typed, nil
	case nil:
		return nil, nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("edit: invalid arguments: %w", err)
		}
		return decodeEditArgumentObject(encoded)
	}
}

func decodeEditArgumentObject(raw []byte) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("edit: invalid arguments: %w", err)
	}
	if object == nil {
		return nil, nil
	}
	return object, nil
}

func asSingleEditInput(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	oldText, hasOldText := object["oldText"].(string)
	newText, hasNewText := object["newText"].(string)
	if !hasOldText || !hasNewText {
		return nil, false
	}
	return map[string]any{"oldText": oldText, "newText": newText}, true
}

func editInputFromObject(object map[string]any) (EditToolInput, error) {
	var input EditToolInput
	if path, ok := object["path"].(string); ok {
		input.Path = path
	}
	switch typed := object["edits"].(type) {
	case []any:
		for _, entry := range typed {
			edit, ok := editFromAny(entry)
			if !ok {
				continue
			}
			input.Edits = append(input.Edits, edit)
		}
	case []Edit:
		input.Edits = append(input.Edits, typed...)
	}
	return input, nil
}

func editFromAny(value any) (Edit, bool) {
	switch typed := value.(type) {
	case Edit:
		return typed, true
	case map[string]any:
		oldText, hasOldText := typed["oldText"].(string)
		newText, hasNewText := typed["newText"].(string)
		if !hasOldText || !hasNewText {
			return Edit{}, false
		}
		return Edit{OldText: oldText, NewText: newText}, true
	default:
		return Edit{}, false
	}
}

// ValidateEditInput is the exported form of the upstream validateEditInput
// helper. It rejects a missing or empty edit list.
func ValidateEditInput(input EditToolInput) (string, []Edit, error) {
	if len(input.Edits) == 0 {
		return "", nil, fmt.Errorf("Edit tool input is invalid. edits must contain at least one replacement.")
	}
	return input.Path, input.Edits, nil
}

func editAccessError(path string, fileErr *harnesstypes.FileError) error {
	return fmt.Errorf("Could not edit file: %s. Error code: %s.", path, fileErr.Code)
}

func executeEdit(params EditToolInput, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[*EditToolDetails], error) {
	if env == nil {
		return agenttypes.AgentToolResult[*EditToolDetails]{}, fmt.Errorf("edit: missing execution environment")
	}
	path, edits, err := ValidateEditInput(params)
	if err != nil {
		return agenttypes.AgentToolResult[*EditToolDetails]{}, err
	}

	resolved := resolveToolPathResult(env, path, ctx)
	if !resolved.OK {
		resolveErr := resolved.Error
		return agenttypes.AgentToolResult[*EditToolDetails]{}, &resolveErr
	}
	absolutePath := resolved.Value

	return WithFileMutationQueue(env, absolutePath, func() (agenttypes.AgentToolResult[*EditToolDetails], error) {
		if ctx != nil && ctx.Err() != nil {
			return agenttypes.AgentToolResult[*EditToolDetails]{}, fmt.Errorf("Operation aborted")
		}

		infoResult := env.FileInfo(absolutePath, ctx)
		if !infoResult.OK {
			infoErr := infoResult.Error
			return agenttypes.AgentToolResult[*EditToolDetails]{}, editAccessError(path, &infoErr)
		}
		if infoResult.Value.Kind != harnesstypes.FileKindFile && infoResult.Value.Kind != harnesstypes.FileKindSymlink {
			return agenttypes.AgentToolResult[*EditToolDetails]{}, fmt.Errorf("Could not edit file: %s. Path is not a file.", path)
		}

		readResult := env.ReadTextFile(absolutePath, ctx)
		if !readResult.OK {
			readErr := readResult.Error
			return agenttypes.AgentToolResult[*EditToolDetails]{}, editAccessError(path, &readErr)
		}
		if ctx != nil && ctx.Err() != nil {
			return agenttypes.AgentToolResult[*EditToolDetails]{}, fmt.Errorf("Operation aborted")
		}

		bom, content := StripBom(readResult.Value)
		originalEnding := DetectLineEnding(content)
		normalizedContent := NormalizeToLF(content)
		applied, applyErr := ApplyEditsToNormalizedContent(normalizedContent, edits, path)
		if applyErr != nil {
			return agenttypes.AgentToolResult[*EditToolDetails]{}, applyErr
		}
		if ctx != nil && ctx.Err() != nil {
			return agenttypes.AgentToolResult[*EditToolDetails]{}, fmt.Errorf("Operation aborted")
		}

		finalContent := bom + RestoreLineEndings(applied.NewContent, originalEnding)
		writeResult := env.WriteFile(absolutePath, []byte(finalContent), ctx)
		if !writeResult.OK {
			writeErr := writeResult.Error
			return agenttypes.AgentToolResult[*EditToolDetails]{}, editAccessError(path, &writeErr)
		}
		if ctx != nil && ctx.Err() != nil {
			return agenttypes.AgentToolResult[*EditToolDetails]{}, fmt.Errorf("Operation aborted")
		}

		diffString, firstChangedLine := GenerateDiffString(applied.BaseContent, applied.NewContent)
		return agenttypes.AgentToolResult[*EditToolDetails]{
			Content: []aitypes.ContentBlock{aitypes.TextBlock(fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), path))},
			Details: &EditToolDetails{
				Diff:             diffString,
				Patch:            GenerateUnifiedPatch(path, applied.BaseContent, applied.NewContent),
				FirstChangedLine: firstChangedLine,
			},
		}, nil
	}, ctx)
}
