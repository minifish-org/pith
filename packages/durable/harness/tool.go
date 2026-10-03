package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
	"github.com/minifish-org/pith/packages/durable"
)

// ToolTaskInput is the input of one tool task.
type ToolTaskInput struct {
	Assistant durable.EntryID `json:"assistant"`
	CallID    string          `json:"callId"`
}

// ToolTaskCheckpoint is the durable checkpoint of a tool task.
type ToolTaskCheckpoint struct {
	Phase     string          `json:"phase"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Replay    string          `json:"replay,omitempty"`
}

// ToolTaskResult is the result of one tool task.
type ToolTaskResult struct {
	EntryID durable.EntryID `json:"entryId"`
	Control *ToolControl    `json:"control,omitempty"`
}

// ToolTask returns the built-in tool task definition.
func ToolTask() durable.TaskDefinition {
	return durable.TaskDefinition{
		Name:    toolTaskName,
		Version: 1,
		Initial: func(json.RawMessage) (json.RawMessage, error) {
			return mustJSON(ToolTaskCheckpoint{Phase: "call"}), nil
		},
		Phases: map[string]durable.PhaseHandler{
			"call":    toolCallPhase,
			"execute": toolExecutePhase,
		},
		Abort: toolAbortPhase,
	}
}

func parseToolCheckpoint(raw []byte) ToolTaskCheckpoint {
	var checkpoint ToolTaskCheckpoint
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &checkpoint)
	}
	return checkpoint
}

func parseToolInput(raw []byte) ToolTaskInput {
	var input ToolTaskInput
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &input)
	}
	return input
}

func toolCallPhase(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	input := parseToolInput(task.Input)
	call, err := readToolCall(ctx, runtime, input)
	if err != nil {
		return err
	}
	agent, err := runtime.agent(ctx)
	if err != nil {
		return err
	}
	var tool *ToolRegistration
	for i := range agent.Tools {
		if agent.Tools[i].Declaration.Name == call.Name {
			tool = &agent.Tools[i]
			break
		}
	}
	if tool == nil {
		errorResult := harnessError("tool_unavailable", fmt.Sprintf("Tool %s is not available", call.Name))
		return settleTool(ctx, runtime, call, "completed", func(*toolSlotView) ToolResult { return errorResult })
	}
	prepared, err := prepareToolArguments(tool, call.Arguments)
	if err != nil {
		invalid := harnessError("invalid_arguments", err.Error())
		return settleTool(ctx, runtime, call, "completed", func(*toolSlotView) ToolResult { return invalid })
	}
	checked, err := validateToolArguments(tool, call, prepared)
	if err != nil {
		invalid := harnessError("invalid_arguments", err.Error())
		return settleTool(ctx, runtime, call, "completed", func(*toolSlotView) ToolResult { return invalid })
	}
	args := checked
	block := ""
	if err := runtime.eachHook(ctx, toolTaskName, "beforeTool", func(handler HookHandler) error {
		if block != "" {
			return nil
		}
		out, err := handler(ctx, mustJSON(map[string]any{"id": call.Id, "type": "toolCall", "name": call.Name, "arguments": rawToValue(args)}), runtime)
		if err != nil {
			block = err.Error()
			return nil
		}
		var decision struct {
			Arguments json.RawMessage `json:"arguments"`
			Block     string          `json:"block"`
		}
		if err := json.Unmarshal(out, &decision); err != nil {
			return nil
		}
		if decision.Block != "" {
			block = decision.Block
		} else if len(decision.Arguments) > 0 {
			args = decision.Arguments
		}
		return nil
	}); err != nil {
		return err
	}
	if block != "" {
		blocked := harnessError("blocked", fmt.Sprintf("Tool call blocked: %s", block))
		return settleTool(ctx, runtime, call, "completed", func(*toolSlotView) ToolResult { return blocked })
	}
	final, err := validateToolArguments(tool, call, args)
	if err != nil {
		invalid := harnessError("invalid_arguments", err.Error())
		return settleTool(ctx, runtime, call, "completed", func(*toolSlotView) ToolResult { return invalid })
	}
	replay := tool.Replay
	if replay == "" {
		replay = "unsafe"
	}
	if err := runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if slot := toolSlot(live, runtime.TaskID()); slot != nil {
			slot["status"] = "running"
		}
		current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(ToolTaskCheckpoint{Phase: "execute", Arguments: final, Replay: replay})}
		return nil
	}); err != nil {
		return err
	}
	return runTool(ctx, runtime, call, *tool, final)
}

func toolExecutePhase(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseToolCheckpoint(task.State.Checkpoint)
	input := parseToolInput(task.Input)
	call, err := readToolCall(ctx, runtime, input)
	if err != nil {
		return err
	}
	agent, err := runtime.agent(ctx)
	if err != nil {
		return err
	}
	var tool *ToolRegistration
	for i := range agent.Tools {
		if agent.Tools[i].Declaration.Name == call.Name {
			tool = &agent.Tools[i]
			break
		}
	}
	if checkpoint.Replay == "safe" && tool != nil && tool.Replay == "safe" {
		if err := runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
			live, err := loadLive(ctx, tx, runtime.ConversationID())
			if err != nil {
				return err
			}
			if slot := toolSlot(live, runtime.TaskID()); slot != nil {
				clearProgress(slot)
			}
			return nil
		}); err != nil {
			return err
		}
		return runTool(ctx, runtime, call, *tool, checkpoint.Arguments)
	}
	message := fmt.Sprintf("Tool %s was interrupted and may have partially run", call.Name)
	return settleTool(ctx, runtime, call, "failed", func(slot *toolSlotView) ToolResult {
		return fromSlot(slot, "interrupted", message)
	})
}

func toolAbortPhase(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	input := parseToolInput(task.Input)
	call, err := readToolCall(ctx, runtime, input)
	if err != nil {
		return err
	}
	message := fmt.Sprintf("Tool %s was aborted", call.Name)
	return settleTool(ctx, runtime, call, "aborted", func(slot *toolSlotView) ToolResult {
		return fromSlot(slot, "aborted", message)
	})
}

func readToolCall(ctx context.Context, runtime taskRuntime, input ToolTaskInput) (types.ToolCall, error) {
	entry, err := runtime.entryOf(ctx, entryKindAssistant, input.Assistant)
	if err != nil {
		return types.ToolCall{}, err
	}
	if entry == nil || len(entry.Model) == 0 || entry.Model[0].Assistant == nil {
		return types.ToolCall{}, fmt.Errorf("Entry %d has no tool call %s", input.Assistant, input.CallID)
	}
	for _, block := range entry.Model[0].Assistant.Content {
		if block.ToolCall != nil && block.ToolCall.Id == input.CallID {
			return *block.ToolCall, nil
		}
	}
	return types.ToolCall{}, fmt.Errorf("Entry %d has no tool call %s", input.Assistant, input.CallID)
}

func prepareToolArguments(tool *ToolRegistration, args json.RawMessage) (json.RawMessage, error) {
	if tool.PrepareArguments == nil {
		return args, nil
	}
	return tool.PrepareArguments(args)
}

func validateToolArguments(tool *ToolRegistration, call types.ToolCall, args json.RawMessage) (json.RawMessage, error) {
	toolCall := call
	toolCall.Arguments = args
	validated, err := utils.ValidateToolArguments(tool.Declaration, toolCall)
	if err != nil {
		return nil, err
	}
	return mustJSON(validated), nil
}

func rawToValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	return value
}

// toolSlotView is a durable partial tool slot used to rebuild an interrupted
// result after recovery.
type toolSlotView struct {
	Output       string
	DroppedBytes int
	DroppedLines int
	Details      json.RawMessage
	Diagnostics  []ToolDiagnostic
	present      bool
}

func fromSlot(slot *toolSlotView, code, message string) ToolResult {
	diagnostics := []ToolDiagnostic{}
	droppedBytes := 0
	droppedLines := 0
	content := []types.ContentBlock{}
	var details json.RawMessage
	if slot != nil && slot.present {
		diagnostics = append(diagnostics, slot.Diagnostics...)
		droppedBytes = slot.DroppedBytes
		droppedLines = slot.DroppedLines
		if slot.Output != "" {
			content = append(content, types.TextBlock(slot.Output))
		}
		details = slot.Details
	}
	if droppedBytes > 0 {
		diagnostics = append(diagnostics, truncatedDiagnostic(droppedLines, droppedBytes, ""))
	}
	diagnostics = append(diagnostics, ToolDiagnostic{Severity: "error", Code: code, Message: message})
	return ToolResult{Content: content, IsError: true, Diagnostics: diagnostics, Details: details}
}

func harnessError(code, message string) ToolResult {
	return ToolResult{Content: []types.ContentBlock{}, IsError: true, Diagnostics: []ToolDiagnostic{{Severity: "error", Code: code, Message: message}}}
}

func truncatedDiagnostic(droppedLines, droppedBytes int, retain string) ToolDiagnostic {
	kept := ""
	if retain == "head" {
		kept = " to its beginning"
	} else if retain == "tail" {
		kept = " to its end"
	}
	return ToolDiagnostic{
		Severity: "warn",
		Code:     "truncated",
		Message:  fmt.Sprintf("Output truncated%s: %d lines, %d bytes dropped", kept, droppedLines, droppedBytes),
	}
}

// runTool executes one resolved tool and settles its result.
func runTool(ctx context.Context, runtime taskRuntime, call types.ToolCall, tool ToolRegistration, args json.RawMessage) error {
	limits := OutputLimits{MaxBytes: DefaultMaxBytes, MaxLines: DefaultMaxLines, Retain: "head"}
	if tool.OutputLimits != nil {
		if tool.OutputLimits.MaxBytes > 0 {
			limits.MaxBytes = tool.OutputLimits.MaxBytes
		}
		if tool.OutputLimits.MaxLines > 0 {
			limits.MaxLines = tool.OutputLimits.MaxLines
		}
		if tool.OutputLimits.Retain != "" {
			limits.Retain = tool.OutputLimits.Retain
		}
	}
	reported := &reportedOutput{output: NewOutputBuffer(limits), limits: limits}
	api := &toolAPI{runtime: runtime, call: call, reported: reported}
	progress := NewProgress(func() (int, error) {
		return publishProgress(ctx, runtime, reported)
	}, func(err error) { runtime.Report(err) })
	api.progress = progress

	var result ToolResult
	var ending = "completed"
	var failureMessage string
	execErr := func() error {
		executionEnv, err := runtime.environment(ctx)
		if err != nil {
			return err
		}
		api.execEnv = executionEnv
		var execError error
		result, execError = tool.Execute(ctx, args, api)
		return execError
	}()
	if execErr != nil {
		if ctx.Err() != nil {
			api.markEnded()
			progress.Stop()
			return ctx.Err()
		}
		result = ToolResult{IsError: true, Diagnostics: []ToolDiagnostic{{Severity: "error", Code: "tool_error", Message: execErr.Error()}}}
		failureMessage = fmt.Sprintf("Tool %s threw", call.Name)
		ending = "failed"
	}
	api.markEnded()
	reported.output.End()
	progress.Stop()
	settled, err := finalToolResult(ctx, runtime, call, result, reported)
	if err != nil {
		return err
	}
	return settleToolResult(ctx, runtime, call, ending, failureMessage, func(*toolSlotView) ToolResult { return settled })
}

func finalToolResult(ctx context.Context, runtime taskRuntime, call types.ToolCall, result ToolResult, reported *reportedOutput) (ToolResult, error) {
	var harnessDiagnostics []ToolDiagnostic
	var retained *BoundedOutput
	if result.Content == nil {
		snapshot := reported.output.Snapshot()
		retained = &snapshot
		if snapshot.Text != "" {
			result.Content = []types.ContentBlock{types.TextBlock(snapshot.Text)}
		} else {
			result.Content = []types.ContentBlock{}
		}
	}
	if result.Details == nil && reported.details != nil {
		result.Details = reported.details
	}
	result.Diagnostics = append(append([]ToolDiagnostic{}, reported.diagnostics...), result.Diagnostics...)
	if err := runtime.eachHook(ctx, toolTaskName, "afterTool", func(handler HookHandler) error {
		payload := mustJSON(map[string]any{"call": rawToValue(mustJSON(call)), "result": toolResultToValue(result)})
		out, err := handler(ctx, payload, runtime)
		if err != nil {
			return err
		}
		if len(out) == 0 {
			return nil
		}
		var replaced struct {
			Result *json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(out, &replaced); err == nil && replaced.Result != nil {
			updated, err := toolResultFromValue(*replaced.Result)
			if err == nil {
				result = updated
			}
		}
		return nil
	}); err != nil {
		return ToolResult{}, err
	}
	if retained != nil && retained.DroppedBytes > 0 {
		harnessDiagnostics = append(harnessDiagnostics, truncatedDiagnostic(retained.DroppedLines, retained.DroppedBytes, reported.limits.Retain))
	}
	bounded, droppedBytes, droppedLines := boundContent(result.Content, reported.limits)
	result.Content = bounded
	if droppedBytes > 0 {
		harnessDiagnostics = append(harnessDiagnostics, truncatedDiagnostic(droppedLines, droppedBytes, reported.limits.Retain))
	}
	result.Diagnostics = append(result.Diagnostics, harnessDiagnostics...)
	return result, nil
}

func toolResultToValue(result ToolResult) any {
	payload := map[string]any{
		"content":     contentToValue(result.Content),
		"isError":     result.IsError,
		"diagnostics": result.Diagnostics,
	}
	if result.Details != nil {
		payload["details"] = rawToValue(result.Details)
	}
	if result.Usage != nil {
		payload["usage"] = result.Usage
	}
	if result.Control != nil {
		payload["control"] = result.Control
	}
	return payload
}

func toolResultFromValue(raw json.RawMessage) (ToolResult, error) {
	var payload struct {
		Content     json.RawMessage  `json:"content"`
		IsError     bool             `json:"isError"`
		Details     json.RawMessage  `json:"details"`
		Diagnostics []ToolDiagnostic `json:"diagnostics"`
		Usage       *types.Usage     `json:"usage"`
		Control     *ToolControl     `json:"control"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ToolResult{}, err
	}
	result := ToolResult{IsError: payload.IsError, Diagnostics: payload.Diagnostics, Usage: payload.Usage, Control: payload.Control}
	if len(payload.Content) > 0 {
		result.Content = decodeContentBlocks(rawToValue(payload.Content))
	}
	if len(payload.Details) > 0 {
		result.Details = payload.Details
	}
	return result, nil
}

func contentToValue(content []types.ContentBlock) []any {
	out := make([]any, 0, len(content))
	for _, block := range content {
		out = append(out, rawToValue(mustJSON(block)))
	}
	return out
}

func boundContent(content []types.ContentBlock, limits OutputLimits) ([]types.ContentBlock, int, int) {
	var textBuilder string
	var textIndexes []int
	for i, block := range content {
		if block.Text != nil {
			textBuilder += block.Text.Text
			textIndexes = append(textIndexes, i)
		}
	}
	bounded := BoundOutput(textBuilder, limits)
	if bounded.DroppedBytes == 0 || len(textIndexes) == 0 {
		return content, 0, 0
	}
	keep := textIndexes[0]
	if limits.Retain == "tail" {
		keep = textIndexes[len(textIndexes)-1]
	}
	result := make([]types.ContentBlock, 0, len(content))
	for i, block := range content {
		if block.Text == nil {
			result = append(result, block)
			continue
		}
		if i == keep {
			updated := *block.Text
			updated.Text = bounded.Text
			result = append(result, types.ContentBlock{Type: types.ContentTypeText, Text: &updated})
		}
	}
	return result, bounded.DroppedBytes, bounded.DroppedLines
}

// reportedOutput is what a running tool published.
type reportedOutput struct {
	output      *OutputBuffer
	limits      OutputLimits
	diagnostics []ToolDiagnostic
	details     json.RawMessage
}

func publishProgress(ctx context.Context, runtime taskRuntime, reported *reportedOutput) (int, error) {
	snapshot := reported.output.Snapshot()
	diagnostics := append([]ToolDiagnostic{}, reported.diagnostics...)
	details := reported.details
	err := runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		slot := toolSlot(live, runtime.TaskID())
		if slot == nil {
			return nil
		}
		if str(slot["output"]) != snapshot.Text {
			slot["output"] = snapshot.Text
		}
		if snapshot.DroppedBytes > 0 {
			slot["droppedBytes"] = snapshot.DroppedBytes
		}
		if snapshot.DroppedLines > 0 {
			slot["droppedLines"] = snapshot.DroppedLines
		}
		if details != nil {
			slot["details"] = rawToValue(details)
		}
		if len(diagnostics) > 0 {
			list, _ := slot["diagnostics"].([]any)
			for _, diagnostic := range diagnostics {
				list = append(list, rawToValue(mustJSON(diagnostic)))
			}
			slot["diagnostics"] = list
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(snapshot.Text), nil
}

func slotView(live map[string]any, taskID durable.TaskID) *toolSlotView {
	slot := toolSlot(live, taskID)
	if slot == nil {
		return nil
	}
	view := &toolSlotView{
		Output:       str(slot["output"]),
		DroppedBytes: intOf(slot["droppedBytes"]),
		DroppedLines: intOf(slot["droppedLines"]),
		present:      true,
	}
	if details, ok := slot["details"]; ok {
		view.Details = mustJSON(details)
	}
	if raw, ok := slot["diagnostics"].([]any); ok {
		for _, item := range raw {
			data, _ := json.Marshal(item)
			var diagnostic ToolDiagnostic
			if err := json.Unmarshal(data, &diagnostic); err == nil {
				view.Diagnostics = append(view.Diagnostics, diagnostic)
			}
		}
	}
	return view
}

// settleTool builds and settles a result from the durable slot.
func settleTool(ctx context.Context, runtime taskRuntime, call types.ToolCall, ending string, build func(*toolSlotView) ToolResult) error {
	return settleToolResult(ctx, runtime, call, ending, "", build)
}

func settleToolResult(ctx context.Context, runtime taskRuntime, call types.ToolCall, ending, failureMessage string, build func(*toolSlotView) ToolResult) error {
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		view := slotView(live, runtime.TaskID())
		result := build(view)
		entry, err := appendToolResult(ctx, tx, runtime.ConversationID(), call, result, runtime.Now())
		if err != nil {
			return err
		}
		if slot := toolSlot(live, runtime.TaskID()); slot != nil {
			entryID := entry.ID
			finishSlot(slot, &entryID)
		}
		switch ending {
		case "aborted":
			current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeAborted, Result: mustJSON(map[string]any{"entryId": int64(entry.ID)})}}
			return nil
		case "failed":
			current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: failureMessage}, Result: mustJSON(map[string]any{"entryId": int64(entry.ID)})}}
			return nil
		default:
			payload := map[string]any{"entryId": int64(entry.ID)}
			if result.Control != nil {
				payload["control"] = rawToValue(mustJSON(result.Control))
			}
			current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(payload)}}
			return nil
		}
	})
}

// appendToolResult appends a pi.tool-result entry and records its usage.
func appendToolResult(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, call types.ToolCall, result ToolResult, timestamp int64) (*durable.EntryRecord, error) {
	diagnostics := append([]ToolDiagnostic{}, result.Diagnostics...)
	content := append([]types.ContentBlock{}, result.Content...)
	if len(diagnostics) > 0 {
		content = append(content, types.TextBlock(renderDiagnostics(diagnostics)))
	}
	message := types.NewToolResultMessage(call.Id, call.Name, content, result.IsError, float64(timestamp))
	if result.Details != nil {
		message.Details = result.Details
	}
	if result.Usage != nil {
		message.Usage = result.Usage
		_ = recordUsage(ctx, tx, conversationID, "tools", call.Name, *result.Usage)
	}
	data := mustJSON(map[string]any{"diagnostics": diagnostics})
	return tx.AppendEntry(ctx, conversationID, durable.EntryDraft{Kind: entryKindToolResult, Model: []types.Message{{Role: types.ToolResultMessageRole, ToolResult: &message}}, Data: data})
}

func renderDiagnostics(diagnostics []ToolDiagnostic) string {
	out := "<harness>\n"
	for i, diagnostic := range diagnostics {
		if i > 0 {
			out += "\n"
		}
		out += fmt.Sprintf("[%s] %s", diagnostic.Severity, diagnostic.Message)
	}
	out += "\n</harness>"
	return out
}

func intOf(value any) int {
	n, _ := asInt64(value)
	return int(n)
}

var _ = errors.New
