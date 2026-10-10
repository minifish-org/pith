// Package compaction is the Go port of the harness compaction module:
// packages/agent/src/harness/compaction/{utils,compaction,branch-summarization}.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package owns token estimation, compaction cut points, summary generation
// and branch summarization. Public data transfer types live in
// harness/types; this package re-exports them as aliases and keeps the
// implementation in its original responsibility directory.
package compaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// FileOperations are the file paths touched by a session branch or compaction
// range. It is the slice-backed Go projection of the upstream Set container.
type FileOperations = harnesstypes.FileOperations

// CreateFileOps creates an empty file-operation accumulator.
func CreateFileOps() FileOperations {
	return FileOperations{}
}

// ExtractFileOpsFromMessage adds file operations from assistant tool calls to
// an accumulator.
func ExtractFileOpsFromMessage(message agenttypes.AgentMessage, fileOps *FileOperations) {
	if fileOps == nil || message.Message == nil || message.Message.Assistant == nil {
		return
	}
	for _, block := range message.Message.Assistant.Content {
		if block.Type != aitypes.ContentTypeToolCall || block.ToolCall == nil {
			continue
		}
		call := block.ToolCall
		path, ok := toolCallStringArgument(call.Arguments, "path")
		if !ok || path == "" {
			continue
		}
		switch call.Name {
		case "read":
			fileOps.Read = addUnique(fileOps.Read, path)
		case "write":
			fileOps.Written = addUnique(fileOps.Written, path)
		case "edit":
			fileOps.Edited = addUnique(fileOps.Edited, path)
		}
	}
}

func addUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func toolCallStringArgument(raw json.RawMessage, name string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", false
	}
	value, ok := object[name]
	if !ok {
		return "", false
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", false
	}
	return text, true
}

// ComputeFileLists computes sorted read-only and modified file lists from
// accumulated operations.
func ComputeFileLists(fileOps FileOperations) (readFiles []string, modifiedFiles []string) {
	modified := map[string]struct{}{}
	for _, path := range fileOps.Edited {
		modified[path] = struct{}{}
	}
	for _, path := range fileOps.Written {
		modified[path] = struct{}{}
	}
	seenRead := map[string]struct{}{}
	for _, path := range fileOps.Read {
		if _, isModified := modified[path]; isModified {
			continue
		}
		if _, duplicate := seenRead[path]; duplicate {
			continue
		}
		seenRead[path] = struct{}{}
		readFiles = append(readFiles, path)
	}
	for path := range modified {
		modifiedFiles = append(modifiedFiles, path)
	}
	sortJSLike(readFiles)
	sortJSLike(modifiedFiles)
	return readFiles, modifiedFiles
}

// FormatFileOperations formats file lists as summary metadata tags.
func FormatFileOperations(readFiles []string, modifiedFiles []string) string {
	sections := []string{}
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// ToolResultMaxBytes bounds each model-facing summary excerpt in UTF-8 bytes.
const ToolResultMaxBytes = 32 << 10

// ToolResultMaxChars is retained for SDK source compatibility. Its unit has
// always been bytes in the Go port; use ToolResultMaxBytes in new code.
const ToolResultMaxChars = ToolResultMaxBytes

// SerializeConversation serializes LLM messages to plain text for
// summarization prompts.
func SerializeConversation(messages []aitypes.Message) string {
	parts := []string{}
	for _, msg := range messages {
		switch msg.Role {
		case aitypes.UserMessageRole:
			if msg.User == nil {
				continue
			}
			content := aiutils.ContentText(msg.User.Content, "")
			if content != "" {
				parts = append(parts, "[User]: "+content)
			}
		case aitypes.AssistantMessageRole:
			if msg.Assistant == nil {
				continue
			}
			thinkingParts := []string{}
			toolCalls := []string{}
			for _, block := range msg.Assistant.Content {
				switch block.Type {
				case aitypes.ContentTypeThinking:
					if block.Thinking != nil {
						thinkingParts = append(thinkingParts, block.Thinking.Thinking)
					}
				case aitypes.ContentTypeToolCall:
					if block.ToolCall != nil {
						argsStr := formatToolCallArguments(block.ToolCall.Arguments)
						toolCalls = append(toolCalls, block.ToolCall.Name+"("+argsStr+")")
					}
				}
			}
			if len(thinkingParts) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if hasTextBlock(msg.Assistant.Content) {
				parts = append(parts, "[Assistant]: "+aiutils.ContentText(msg.Assistant.Content))
			}
			if len(toolCalls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}
		case aitypes.ToolResultMessageRole:
			if msg.ToolResult == nil {
				continue
			}
			content := aiutils.ContentText(msg.ToolResult.Content, "")
			if content != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(content, ToolResultMaxChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func hasTextBlock(blocks []aitypes.ContentBlock) bool {
	for _, block := range blocks {
		if block.Type == aitypes.ContentTypeText {
			return true
		}
	}
	return false
}

func truncateForSummary(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	if maxBytes <= 0 {
		return ""
	}
	marker := "\n\n[... " + strconv.Itoa(len(text)) + " bytes in original; middle omitted]\n\n"
	if len(marker) >= maxBytes {
		return utf8Prefix(text, maxBytes)
	}
	available := maxBytes - len(marker)
	head := utf8Prefix(text, available/2)
	tail := utf8Suffix(text, available-len(head))
	const diagnosticsStart = "[Selected diagnostics]\n"
	const diagnosticsEnd = "\n[End of excerpt]\n"
	diagnostics := summaryDiagnostics(text[len(head):len(text)-len(tail)], available/4-len(diagnosticsStart)-len(diagnosticsEnd))
	if diagnostics != "" {
		diagnostics = diagnosticsStart + diagnostics + diagnosticsEnd
		available -= len(diagnostics)
		head = utf8Prefix(text, available/2)
		tail = utf8Suffix(text, available-len(head))
	}
	return head + marker + diagnostics + tail
}

func summaryDiagnostics(text string, maxBytes int) string {
	var result strings.Builder
	for text != "" && result.Len() < maxBytes {
		line, rest, _ := strings.Cut(text, "\n")
		text = rest
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "fatal") || strings.Contains(lower, "panic") || strings.Contains(lower, "exception") || strings.Contains(lower, "错误") || strings.Contains(lower, "失败") {
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(utf8Prefix(line, maxBytes-result.Len()))
		}
	}
	return result.String()
}

func utf8Prefix(text string, size int) string {
	if size >= len(text) {
		return text
	}
	if size <= 0 {
		return ""
	}
	for size > 0 && !utf8.RuneStart(text[size]) {
		size--
	}
	return text[:size]
}

func utf8Suffix(text string, size int) string {
	if size >= len(text) {
		return text
	}
	if size <= 0 {
		return ""
	}
	start := len(text) - size
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

// BuildSummaryPrompt applies an overall input budget after per-result excerpts.
// The estimate is four UTF-8 bytes/token, with a 256-token framing margin;
// provider tokenization can differ. Instructions and previous summaries remain
// intact. A shortened conversation is marked; the stored transcript is untouched.
func BuildSummaryPrompt(conversationText, suffix string, contextWindow float64, outputTokens int) (string, error) {
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	const framing = "<conversation>\n\n</conversation>\n\n"
	if outputTokens < 0 {
		outputTokens = 0
	}
	available := int(contextWindow) - outputTokens - (len(SummarizationSystemPrompt)+len(framing)+len(suffix)+3)/4 - 256
	if available <= 0 {
		return "", errors.New("Summary instructions and output budget exceed the model context window")
	}
	// Avoid integer overflow for invalid/extreme custom catalog metadata.
	maxBytes := available
	if available <= int(^uint(0)>>1)/4 {
		maxBytes *= 4
	}
	return "<conversation>\n" + truncateForSummary(conversationText, maxBytes) + "\n</conversation>\n\n" + suffix, nil
}

// SummaryOutputTokenLimit leaves input room on small-context models instead
// of allowing the default reserve to consume their entire context window.
func SummaryOutputTokenLimit(model aitypes.Model, requested int) int {
	if model.MaxTokens > 0 && float64(requested) > model.MaxTokens {
		requested = int(model.MaxTokens)
	}
	if model.ContextWindow > 0 && float64(requested) > model.ContextWindow/2 {
		requested = int(model.ContextWindow / 2)
	}
	if requested < 1 {
		return 1
	}
	return requested
}

// safeJSONStringify mirrors JSON.stringify with the upstream failure fallback.
func safeJSONStringify(value any) string {
	if raw, ok := value.(json.RawMessage); ok {
		compacted, err := compactJSON(raw)
		if err != nil {
			return string(raw)
		}
		return compacted
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[unserializable]"
	}
	if string(encoded) == "null" && value == nil {
		return "null"
	}
	return string(encoded)
}

func compactJSON(raw json.RawMessage) (string, error) {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// errNotObject reports a JSON payload that is not an object where one was
// expected.
var errNotObject = errors.New("value is not a JSON object")

type jsonPair struct {
	key   string
	value string
}

// orderedObjectPairs decodes a JSON object preserving member order, so the
// generated serialization follows the upstream insertion order rather than a
// random map order.
func orderedObjectPairs(raw json.RawMessage) ([]jsonPair, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errNotObject
	}
	pairs := []jsonPair{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errNotObject
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, jsonPair{key: key, value: string(encoded)})
	}
	return pairs, nil
}

func formatToolCallArguments(raw json.RawMessage) string {
	pairs, err := orderedObjectPairs(raw)
	if err != nil {
		return ""
	}
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, pair.key+"="+pair.value)
	}
	return strings.Join(parts, ", ")
}

// sortJSLike sorts strings using UTF-16 code-unit order, matching the default
// Array.prototype.sort() comparison.
func sortJSLike(values []string) {
	sort.SliceStable(values, func(i, j int) bool { return utf16Less(values[i], values[j]) })
}

func utf16Less(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}

// utf16Len returns the JavaScript string length: the number of UTF-16 code
// units rather than Unicode code points.
func utf16Len(value string) int {
	length := 0
	for _, r := range value {
		if r > 0xFFFF {
			length += 2
			continue
		}
		length++
	}
	return length
}
