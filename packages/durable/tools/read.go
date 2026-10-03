// Read tool for the Durable coding tool set.
//
// This is a Go port of packages/durable/src/tools/read.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The tool resolves the requested path through the injected environment and
// sniffs image containers before decoding text. This release does not
// synthesize image reading: an image path is reported as an unsupported-image
// error diagnostic. Text output is truncated head-first to the shared
// line/byte limits and reports continuation through info diagnostics; the
// result content is only file text.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable/harness"
)

// readToolSchema is the model-facing JSON Schema for the read tool, mirroring
// the upstream TypeBox object: a required string path and optional numeric
// offset and limit.
var readToolSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"Maximum number of lines to read"}},"required":["path"]}`)

// ReadToolInput is the parsed parameter object for the read tool.
type ReadToolInput struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

// TruncationInfo is the read-tool view of a truncation decision. It is the
// upstream `Omit<TruncationResult, "content">`: the shown text is the result
// content, never repeated inside details.
type TruncationInfo struct {
	Truncated             bool    `json:"truncated"`
	TruncatedBy           *string `json:"truncatedBy"`
	TotalLines            int     `json:"totalLines"`
	TotalBytes            int     `json:"totalBytes"`
	OutputLines           int     `json:"outputLines"`
	OutputBytes           int     `json:"outputBytes"`
	LastLinePartial       bool    `json:"lastLinePartial"`
	FirstLineExceedsLimit bool    `json:"firstLineExceedsLimit"`
	MaxLines              int     `json:"maxLines"`
	MaxBytes              int     `json:"maxBytes"`
}

// ReadToolDetails carries the truncation metadata of a text read. It is nil for
// untruncated reads and for image reads.
type ReadToolDetails struct {
	Truncation *TruncationInfo `json:"truncation,omitempty"`
}

// CreateReadTool builds the read tool.
func CreateReadTool() harness.ToolRegistration {
	return harness.ToolRegistration{
		Declaration: types.Tool{
			Name: "read",
			Description: fmt.Sprintf(
				"Read the contents of a text file. Output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.",
				DefaultMaxLines,
				DefaultMaxBytes/1024,
			),
			Input: types.JSONSchemaToolInput(readToolSchema),
		},
		Execute: func(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
			return executeRead(ctx, raw, api)
		},
	}
}

func executeRead(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
	var input ReadToolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("read: invalid arguments: %w", err)
		}
	}
	e, err := requireEnv(api)
	if err != nil {
		return harness.ToolResult{}, err
	}
	absolutePath, err := ResolveReadToolPath(ctx, e, input.Path)
	if err != nil {
		return harness.ToolResult{}, err
	}
	bytes, err := e.ReadBinaryFile(ctx, absolutePath)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if mime := DetectSupportedImageMimeType(bytes); mime != nil {
		return harness.ToolResult{
			Content: []types.ContentBlock{},
			IsError: true,
			Diagnostics: []harness.ToolDiagnostic{{
				Severity: "error",
				Code:     "unsupported_image",
				Message:  fmt.Sprintf("%s is an image (%s); reading images is not supported", input.Path, *mime),
			}},
		}, nil
	}
	return readTextResult(input, decodeText(bytes))
}

func readTextResult(input ReadToolInput, textContent string) (harness.ToolResult, error) {
	allLines := strings.Split(textContent, "\n")
	totalFileLines := len(allLines)
	startLine := 0
	if input.Offset != nil && *input.Offset > 0 {
		startLine = *input.Offset - 1
	}
	startLineDisplay := startLine + 1
	if startLine >= len(allLines) {
		return harness.ToolResult{}, fmt.Errorf("Offset %s is beyond end of file (%d lines total)", formatOffset(input.Offset), len(allLines))
	}

	var selectedContent string
	var userLimitedLines *int
	if input.Limit != nil {
		endLine := startLine + *input.Limit
		if endLine > len(allLines) {
			endLine = len(allLines)
		}
		if endLine < startLine {
			endLine = startLine
		}
		selectedContent = strings.Join(allLines[startLine:endLine], "\n")
		limited := endLine - startLine
		userLimitedLines = &limited
	} else {
		selectedContent = strings.Join(allLines[startLine:], "\n")
	}

	head := TruncateHead(selectedContent)
	outputText := head.Content
	var details *ReadToolDetails
	var diagnostics []harness.ToolDiagnostic

	switch {
	case head.FirstLineExceedsLimit:
		lineBytes := []byte(allLines[startLine])
		end := harness.CharacterEnd(lineBytes, DefaultMaxBytes)
		outputText = string(lineBytes[:end])
		diagnostics = append(diagnostics, harness.ToolDiagnostic{
			Severity: "warn",
			Code:     "truncated",
			Message: fmt.Sprintf(
				"Line %d is %s, exceeds the %s limit; showing its first %s. Use bash: sed -n '%dp' %s | tail -c +%d",
				startLineDisplay,
				FormatSize(len(lineBytes)),
				FormatSize(DefaultMaxBytes),
				FormatSize(end),
				startLineDisplay,
				input.Path,
				end+1,
			),
		})
		info := truncationInfoOf(head)
		info.OutputBytes = end
		info.OutputLines = 1
		details = &ReadToolDetails{Truncation: &info}
	case head.Truncated:
		endLineDisplay := startLineDisplay + head.OutputLines - 1
		nextOffset := endLineDisplay + 1
		limitText := ""
		if head.TruncatedBy != nil && *head.TruncatedBy != "lines" {
			limitText = fmt.Sprintf(" (%s limit)", FormatSize(DefaultMaxBytes))
		}
		diagnostics = append(diagnostics, harness.ToolDiagnostic{
			Severity: "info",
			Code:     "truncated",
			Message:  fmt.Sprintf("Showing lines %d-%d of %d%s. Use offset=%d to continue.", startLineDisplay, endLineDisplay, totalFileLines, limitText, nextOffset),
		})
		info := truncationInfoOf(head)
		details = &ReadToolDetails{Truncation: &info}
	case userLimitedLines != nil && startLine+*userLimitedLines < len(allLines):
		remaining := len(allLines) - (startLine + *userLimitedLines)
		nextOffset := startLine + *userLimitedLines + 1
		diagnostics = append(diagnostics, harness.ToolDiagnostic{
			Severity: "info",
			Message:  fmt.Sprintf("%d more lines in file. Use offset=%d to continue.", remaining, nextOffset),
		})
	}

	result := harness.ToolResult{Diagnostics: diagnostics}
	if outputText == "" {
		result.Content = []types.ContentBlock{}
	} else {
		result.Content = []types.ContentBlock{types.TextBlock(outputText)}
	}
	if details != nil {
		encoded, err := json.Marshal(details)
		if err != nil {
			return harness.ToolResult{}, err
		}
		result.Details = encoded
	}
	return result, nil
}

func truncationInfoOf(result TruncationResult) TruncationInfo {
	return TruncationInfo{
		Truncated:             result.Truncated,
		TruncatedBy:           result.TruncatedBy,
		TotalLines:            result.TotalLines,
		TotalBytes:            result.TotalBytes,
		OutputLines:           result.OutputLines,
		OutputBytes:           result.OutputBytes,
		LastLinePartial:       result.LastLinePartial,
		FirstLineExceedsLimit: result.FirstLineExceedsLimit,
		MaxLines:              result.MaxLines,
		MaxBytes:              result.MaxBytes,
	}
}

func formatOffset(offset *int) string {
	if offset == nil {
		return "0"
	}
	return fmt.Sprintf("%d", *offset)
}

// decodeText mirrors the upstream TextDecoder default (non-fatal UTF-8): valid
// input is preserved exactly and invalid byte runs are replaced by U+FFFD.
func decodeText(content []byte) string {
	if utf8.Valid(content) {
		return string(content)
	}
	return strings.ToValidUTF8(string(content), "\uFFFD")
}
