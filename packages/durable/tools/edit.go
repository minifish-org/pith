// Edit tool for the Durable coding tool set.
//
// This is a Go port of packages/durable/src/tools/edit.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The tool resolves the requested path through the injected environment,
// serializes mutations of the canonical path with the shared file mutation
// queue, reads the file, applies every edits[].oldText against the same
// original content, and writes the result back while preserving the file's BOM
// and line-ending spelling. The returned details carry the display diff, the
// unified patch and the first changed line.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/harness"
)

// editToolSchema is the model-facing JSON Schema for the edit tool, mirroring
// the upstream TypeBox object: a required path and at least one replacement
// object with required oldText and newText strings.
var editToolSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}}},"required":["path","edits"]}`)

// EditToolInput is the parsed parameter object for the edit tool.
type EditToolInput struct {
	Path  string `json:"path"`
	Edits []Edit `json:"edits"`
}

// EditToolDetails carries the diff payload of a successful edit.
type EditToolDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

// CreateEditTool builds the edit tool.
func CreateEditTool() harness.ToolRegistration {
	return harness.ToolRegistration{
		Declaration: types.Tool{
			Name:        "edit",
			Description: "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
			Input:       types.JSONSchemaToolInput(editToolSchema),
		},
		PrepareArguments: prepareEditArguments,
		Execute: func(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
			return executeEdit(ctx, raw, api)
		},
	}
}

// prepareEditArguments repairs shapes models commonly send: `edits` as a JSON
// string or as a single edit object, and a top-level `oldText`/`newText` pair.
// It works on a copy; the call's arguments stay unchanged.
func prepareEditArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return raw, nil
	}

	args := make(map[string]any, len(object)+1)
	for key, value := range object {
		args[key] = value
	}

	if editsValue, ok := args["edits"]; ok {
		if encoded, isString := editsValue.(string); isString {
			var parsed any
			if err := json.Unmarshal([]byte(encoded), &parsed); err == nil {
				if array, isArray := parsed.([]any); isArray {
					args["edits"] = array
				} else if single, ok := asSingleEditInput(parsed); ok {
					args["edits"] = []any{single}
				}
			}
		} else if single, ok := asSingleEditInput(editsValue); ok {
			args["edits"] = []any{single}
		}
	}

	oldText, hasOldText := args["oldText"].(string)
	newText, hasNewText := args["newText"].(string)
	if hasOldText && hasNewText {
		var edits []any
		if array, ok := args["edits"].([]any); ok {
			edits = append(edits, array...)
		}
		edits = append(edits, map[string]any{"oldText": oldText, "newText": newText})
		delete(args, "oldText")
		delete(args, "newText")
		args["edits"] = edits
	}

	encoded, err := json.Marshal(args)
	if err != nil {
		return raw, err
	}
	return encoded, nil
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

// ValidateEditInput is the exported form of the upstream validateEditInput
// helper. It rejects a missing or empty edit list.
func ValidateEditInput(input EditToolInput) (string, []Edit, error) {
	if len(input.Edits) == 0 {
		return "", nil, errors.New("Edit tool input is invalid. edits must contain at least one replacement.")
	}
	return input.Path, input.Edits, nil
}

func editAccessError(path string, err error) error {
	code := ""
	var fileErr *env.FileError
	if errors.As(err, &fileErr) {
		code = fileErr.Code
	}
	return fmt.Errorf("Could not edit file: %s. Error code: %s.", path, code)
}

func executeEdit(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
	var input EditToolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("edit: invalid arguments: %w", err)
		}
	}
	path, edits, err := ValidateEditInput(input)
	if err != nil {
		return harness.ToolResult{}, err
	}
	e, err := requireEnv(api)
	if err != nil {
		return harness.ToolResult{}, err
	}
	absolutePath, err := ResolveToolPath(ctx, e, path)
	if err != nil {
		return harness.ToolResult{}, err
	}

	return WithFileMutationQueue(ctx, e, absolutePath, func() (harness.ToolResult, error) {
		if ctx.Err() != nil {
			return harness.ToolResult{}, errOperationAborted
		}
		info, err := e.FileInfo(ctx, absolutePath)
		if err != nil {
			return harness.ToolResult{}, editAccessError(path, err)
		}
		if info.Kind != "file" && info.Kind != "symlink" {
			return harness.ToolResult{}, fmt.Errorf("Could not edit file: %s. Path is not a file.", path)
		}

		content, err := e.ReadTextFile(ctx, absolutePath)
		if err != nil {
			return harness.ToolResult{}, editAccessError(path, err)
		}
		if ctx.Err() != nil {
			return harness.ToolResult{}, errOperationAborted
		}

		bom, text := StripBom(content)
		originalEnding := DetectLineEnding(text)
		normalizedContent := NormalizeToLF(text)
		applied, err := ApplyEditsToNormalizedContent(normalizedContent, edits, path)
		if err != nil {
			return harness.ToolResult{}, err
		}
		if ctx.Err() != nil {
			return harness.ToolResult{}, errOperationAborted
		}

		finalContent := bom + RestoreLineEndings(applied.NewContent, originalEnding)
		if err := e.WriteFile(ctx, absolutePath, []byte(finalContent)); err != nil {
			return harness.ToolResult{}, editAccessError(path, err)
		}
		if ctx.Err() != nil {
			return harness.ToolResult{}, errOperationAborted
		}

		diffString, firstChangedLine := GenerateDiffString(applied.BaseContent, applied.NewContent)
		details, err := json.Marshal(EditToolDetails{
			Diff:             diffString,
			Patch:            GenerateUnifiedPatch(path, applied.BaseContent, applied.NewContent),
			FirstChangedLine: firstChangedLine,
		})
		if err != nil {
			return harness.ToolResult{}, err
		}
		return harness.ToolResult{
			Content: []types.ContentBlock{types.TextBlock(fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), path))},
			Details: details,
		}, nil
	})
}
