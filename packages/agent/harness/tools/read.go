// Read tool for the built-in execution tool set.
//
// This is a Go port of packages/agent/src/harness/tools/read.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The tool reads a file through the injected ExecutionEnv, sniffing image
// containers before decoding text. Text output is truncated head-first to the
// shared line/byte limits and reports continuation offsets; image output is
// returned as a base64 attachment unless a ReadImageProcessor is injected.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
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

// ReadToolDetails carries the truncation metadata of a text read. It is nil for
// untruncated reads and for image reads, matching the upstream
// `ReadToolDetails | undefined` result.
type ReadToolDetails struct {
	Truncation *truncate.TruncationResult `json:"truncation,omitempty"`
}

// ReadImageProcessorResult is the discriminated outcome of an injected image
// processor. When OK is true the converted Data/MimeType/Hints are used; when
// false the Message is appended to the text fallback.
type ReadImageProcessorResult struct {
	OK       bool     `json:"ok"`
	Data     string   `json:"data,omitempty"`
	MimeType string   `json:"mimeType,omitempty"`
	Hints    []string `json:"hints,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// ReadImageProcessorOptions configures one processor invocation.
type ReadImageProcessorOptions struct {
	AutoResizeImages bool `json:"autoResizeImages"`
}

// ReadImageProcessor converts or resizes an image before it is attached. The
// upstream processor receives the raw bytes, the sniffed MIME type, the
// auto-resize option and the invocation context.
type ReadImageProcessor func(bytes []byte, mimeType string, options ReadImageProcessorOptions, ctx harnesscontext.Context) (ReadImageProcessorResult, error)

// ReadToolOptions configures the read tool.
type ReadToolOptions struct {
	// AutoResizeImages is passed to the image processor. Default: true.
	AutoResizeImages *bool
	// ImageProcessor, when non-nil, converts supported images and is required
	// to convert BMP images.
	ImageProcessor ReadImageProcessor
}

func (o *ReadToolOptions) autoResizeImages() bool {
	if o == nil || o.AutoResizeImages == nil {
		return true
	}
	return *o.AutoResizeImages
}

// CreateReadTool builds the read tool bound to an ExecutionToolContext.
func CreateReadTool(options *ReadToolOptions) harnesstypes.AgentHarnessTool[ExecutionToolContext, ReadToolInput, *ReadToolDetails] {
	return harnesstypes.AgentHarnessTool[ExecutionToolContext, ReadToolInput, *ReadToolDetails]{
		Tool: aitypes.Tool{
			Name: "read",
			Description: fmt.Sprintf(
				"Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. For text files, output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.",
				truncate.DefaultMaxLines,
				truncate.DefaultMaxBytes/1024,
			),
			Input: aitypes.JSONSchemaToolInput(readToolSchema),
		},
		Label:            "read",
		PrepareArguments: parseReadToolInput,
		Replay:           "",
		Execute: func(
			_toolCallId string,
			params ReadToolInput,
			_onUpdate harnesstypes.AgentHarnessToolUpdateCallback[*ReadToolDetails],
			toolContext ExecutionToolContext,
			_invocation harnesstypes.AgentHarnessToolInvocation,
			ctx harnesscontext.Context,
		) (agenttypes.AgentToolResult[*ReadToolDetails], error) {
			return executeRead(options, params, toolContext.Env, ctx)
		},
	}
}

func parseReadToolInput(args any) (ReadToolInput, error) {
	switch typed := args.(type) {
	case ReadToolInput:
		return typed, nil
	case *ReadToolInput:
		if typed == nil {
			return ReadToolInput{}, nil
		}
		return *typed, nil
	case json.RawMessage:
		return decodeReadToolInput(typed)
	case []byte:
		return decodeReadToolInput(typed)
	case string:
		return decodeReadToolInput([]byte(typed))
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ReadToolInput{}, fmt.Errorf("read: invalid arguments: %w", err)
		}
		return decodeReadToolInput(encoded)
	case nil:
		return ReadToolInput{}, nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ReadToolInput{}, fmt.Errorf("read: invalid arguments: %w", err)
		}
		return decodeReadToolInput(encoded)
	}
}

func decodeReadToolInput(raw []byte) (ReadToolInput, error) {
	var input ReadToolInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&input); err != nil {
		return ReadToolInput{}, fmt.Errorf("read: invalid arguments: %w", err)
	}
	return input, nil
}

func executeRead(options *ReadToolOptions, params ReadToolInput, env harnesstypes.ExecutionEnv, ctx harnesscontext.Context) (agenttypes.AgentToolResult[*ReadToolDetails], error) {
	if env == nil {
		return agenttypes.AgentToolResult[*ReadToolDetails]{}, fmt.Errorf("read: missing execution environment")
	}
	absolutePath, err := resolveReadPath(env, params.Path, ctx)
	if err != nil {
		return agenttypes.AgentToolResult[*ReadToolDetails]{}, err
	}
	content, err := readBinaryFile(env, absolutePath, ctx)
	if err != nil {
		return agenttypes.AgentToolResult[*ReadToolDetails]{}, err
	}

	if mime := DetectSupportedImageMimeType(content); mime != nil {
		return executeReadImage(options, content, *mime, params.Path, ctx)
	}
	return executeReadText(params, content)
}

func executeReadImage(options *ReadToolOptions, content []byte, mimeType string, path string, ctx harnesscontext.Context) (agenttypes.AgentToolResult[*ReadToolDetails], error) {
	if options != nil && options.ImageProcessor != nil {
		processed, err := options.ImageProcessor(content, mimeType, ReadImageProcessorOptions{AutoResizeImages: options.autoResizeImages()}, ctx)
		if err != nil {
			return agenttypes.AgentToolResult[*ReadToolDetails]{}, err
		}
		if !processed.OK {
			return readTextResult(fmt.Sprintf("Read image file [%s]\n%s", mimeType, processed.Message), nil), nil
		}
		hints := ""
		if len(processed.Hints) > 0 {
			hints = "\n" + strings.Join(processed.Hints, "\n")
		}
		return readImageResult(fmt.Sprintf("Read image file [%s]%s", processed.MimeType, hints), processed.Data, processed.MimeType), nil
	}
	if mimeType == "image/bmp" {
		return readTextResult("Read image file [image/bmp]\n[Image omitted: configure an imageProcessor to convert BMP images.]", nil), nil
	}
	return readImageResult(fmt.Sprintf("Read image file [%s]", mimeType), EncodeBase64(content), mimeType), nil
}

func executeReadText(params ReadToolInput, content []byte) (agenttypes.AgentToolResult[*ReadToolDetails], error) {
	text := decodeText(content)
	allLines := strings.Split(text, "\n")
	totalFileLines := len(allLines)

	startLine := 0
	if params.Offset != nil && *params.Offset > 0 {
		startLine = *params.Offset - 1
	}
	startLineDisplay := startLine + 1
	if startLine >= len(allLines) {
		return agenttypes.AgentToolResult[*ReadToolDetails]{}, fmt.Errorf("Offset %s is beyond end of file (%d lines total)", formatOffset(params.Offset), len(allLines))
	}

	var selectedContent string
	var userLimitedLines *int
	if params.Limit != nil {
		endLine := startLine + *params.Limit
		if endLine > len(allLines) {
			endLine = len(allLines)
		}
		sliceEnd := endLine
		if sliceEnd < startLine {
			sliceEnd = startLine
		}
		selectedContent = strings.Join(allLines[startLine:sliceEnd], "\n")
		limited := endLine - startLine
		userLimitedLines = &limited
	} else {
		selectedContent = strings.Join(allLines[startLine:], "\n")
	}

	truncation := truncate.TruncateHead(selectedContent, truncate.TruncationOptions{})
	var outputText string
	var details *ReadToolDetails
	switch {
	case truncation.FirstLineExceedsLimit:
		firstLineSize := truncate.FormatSize(truncate.Utf8ByteLength(allLines[startLine]))
		outputText = fmt.Sprintf(
			"[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay,
			firstLineSize,
			truncate.FormatSize(truncate.DefaultMaxBytes),
			startLineDisplay,
			params.Path,
			truncate.DefaultMaxBytes,
		)
		details = &ReadToolDetails{Truncation: truncationPointer(truncation)}
	case truncation.Truncated:
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		nextOffset := endLineDisplay + 1
		outputText = truncation.Content
		if truncation.TruncatedBy != nil && *truncation.TruncatedBy == "lines" {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			outputText += fmt.Sprintf(
				"\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay,
				endLineDisplay,
				totalFileLines,
				truncate.FormatSize(truncate.DefaultMaxBytes),
				nextOffset,
			)
		}
		details = &ReadToolDetails{Truncation: truncationPointer(truncation)}
	case userLimitedLines != nil && startLine+*userLimitedLines < len(allLines):
		remaining := len(allLines) - (startLine + *userLimitedLines)
		nextOffset := startLine + *userLimitedLines + 1
		outputText = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]", truncation.Content, remaining, nextOffset)
	default:
		outputText = truncation.Content
	}

	return readTextResult(outputText, details), nil
}

func truncationPointer(value truncate.TruncationResult) *truncate.TruncationResult {
	return &value
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

func readBinaryFile(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) ([]byte, error) {
	result := env.ReadBinaryFile(path, ctx)
	if !result.OK {
		err := result.Error
		return nil, &err
	}
	return result.Value, nil
}

func resolveReadPath(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) (string, error) {
	result := resolveReadToolPathResult(env, path, ctx)
	if !result.OK {
		err := result.Error
		return "", &err
	}
	return result.Value, nil
}

func readTextResult(text string, details *ReadToolDetails) agenttypes.AgentToolResult[*ReadToolDetails] {
	return agenttypes.AgentToolResult[*ReadToolDetails]{
		Content: []aitypes.ContentBlock{aitypes.TextBlock(text)},
		Details: details,
	}
}

func readImageResult(text string, data string, mimeType string) agenttypes.AgentToolResult[*ReadToolDetails] {
	return agenttypes.AgentToolResult[*ReadToolDetails]{
		Content: []aitypes.ContentBlock{
			aitypes.TextBlock(text),
			aitypes.ImageBlock(data, mimeType),
		},
		Details: nil,
	}
}
