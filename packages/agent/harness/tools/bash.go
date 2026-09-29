// Bash tool for the built-in execution tool set.
//
// This is a Go port of packages/agent/src/harness/tools/bash.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The tool executes a shell command through the injected ExecutionEnv, keeping
// combined stdout/stderr in one bounded view that is truncated tail-first to
// the shared line/byte limits. While the command runs, bounded incremental
// snapshots are forwarded to the harness progress callback; durable recovery
// checkpoints are requested at most once every two seconds and only when the
// encoded snapshot changed. A truncated command spills its complete output to
// an execution-environment-local temp file. Nonzero exit codes, timeouts and
// aborts are reported as tool errors after the retained output, matching the
// upstream message construction.
package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	outputcapture "github.com/minifish-org/pith/packages/agent/harness/utils/output_capture"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// maxBashTimeoutSeconds is the upstream MAX_TIMEOUT_SECONDS bound. It is the
// largest timeout that still fits a signed 32-bit millisecond duration.
const maxBashTimeoutSeconds = 2147483647.0 / 1000

// bashCheckpointIntervalMs is the minimum spacing between durable progress
// checkpoints.
const bashCheckpointIntervalMs = 2000

// bashToolSchema is the model-facing JSON Schema for the bash tool, mirroring
// the upstream TypeBox object: a required string command and an optional
// numeric timeout in seconds.
var bashToolSchema = json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Bash command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}},"required":["command"]}`)

// BashToolInput is the parsed parameter object for the bash tool.
type BashToolInput struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"`
}

// BashToolDetails carries the truncation metadata and the full-output spill
// path of a command whose output exceeded the capture limits. It is nil when
// the command produced no bounded-output metadata.
type BashToolDetails struct {
	Truncation     *harnesstypes.ShellOutputTruncation `json:"truncation,omitempty"`
	FullOutputPath *string                             `json:"fullOutputPath,omitempty"`
}

// BashExecution is the mutable command plan passed to a BashPrepare callback.
// The callback may rewrite Command and Cwd or add to Env before execution.
type BashExecution struct {
	Command    string            `json:"command"`
	Cwd        string            `json:"cwd"`
	Env        map[string]string `json:"env"`
	InheritEnv bool              `json:"inheritEnv"`
}

// BashPrepare lets the harness customize the command, working directory and
// environment immediately before execution. Returning an error aborts the
// tool call before any process is spawned.
type BashPrepare func(execution *BashExecution, toolContext ExecutionToolContext, ctx harnesscontext.Context) error

// BashToolOptions configures the bash tool.
type BashToolOptions struct {
	// CommandPrefix is prepended as a separate line before every command.
	CommandPrefix string
	// Prepare runs after the execution plan is built and before env.exec.
	Prepare BashPrepare
}

// CreateBashTool builds the bash tool bound to an ExecutionToolContext.
func CreateBashTool(options *BashToolOptions) harnesstypes.AgentHarnessTool[ExecutionToolContext, BashToolInput, *BashToolDetails] {
	return harnesstypes.AgentHarnessTool[ExecutionToolContext, BashToolInput, *BashToolDetails]{
		Tool: aitypes.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command in the current working directory. Returns combined stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.",
				truncate.DefaultMaxLines,
				truncate.DefaultMaxBytes/1024,
			),
			Input: aitypes.JSONSchemaToolInput(bashToolSchema),
		},
		Label:            "bash",
		PrepareArguments: parseBashToolInput,
		Replay:           "",
		Execute: func(
			_toolCallId string,
			params BashToolInput,
			onUpdate harnesstypes.AgentHarnessToolUpdateCallback[*BashToolDetails],
			toolContext ExecutionToolContext,
			_invocation harnesstypes.AgentHarnessToolInvocation,
			ctx harnesscontext.Context,
		) (agenttypes.AgentToolResult[*BashToolDetails], error) {
			return executeBash(options, params, toolContext, ctx, onUpdate)
		},
	}
}

// parseBashToolInput normalizes loose model input into a BashToolInput.
func parseBashToolInput(args any) (BashToolInput, error) {
	switch typed := args.(type) {
	case BashToolInput:
		return typed, nil
	case *BashToolInput:
		if typed == nil {
			return BashToolInput{}, nil
		}
		return *typed, nil
	case json.RawMessage:
		return decodeBashToolInput(typed)
	case []byte:
		return decodeBashToolInput(typed)
	case string:
		return decodeBashToolInput([]byte(typed))
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return BashToolInput{}, fmt.Errorf("bash: invalid arguments: %w", err)
		}
		return decodeBashToolInput(encoded)
	case nil:
		return BashToolInput{}, nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return BashToolInput{}, fmt.Errorf("bash: invalid arguments: %w", err)
		}
		return decodeBashToolInput(encoded)
	}
}

func decodeBashToolInput(raw []byte) (BashToolInput, error) {
	var input BashToolInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&input); err != nil {
		return BashToolInput{}, fmt.Errorf("bash: invalid arguments: %w", err)
	}
	return input, nil
}

// validateBashTimeout mirrors the upstream validation: an absent timeout is
// accepted, while a non-finite, non-positive or too-large timeout is rejected
// before the process starts.
func validateBashTimeout(timeout *float64) error {
	if timeout == nil {
		return nil
	}
	value := *timeout
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return errors.New("Invalid timeout: must be a finite number of seconds")
	}
	if value > maxBashTimeoutSeconds {
		return fmt.Errorf("Invalid timeout: maximum is %s seconds", formatBashNumber(maxBashTimeoutSeconds))
	}
	return nil
}

// formatBashNumber renders a JS number the way a template literal would.
func formatBashNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// bashProgressState tracks the bounded view and checkpoint decisions shared
// between the execution environment callback and the tool call. The mutex
// guards the mutable fields because the adaptive publisher may deliver final
// updates from its trailing timer goroutine.
type bashProgressState struct {
	mu               sync.Mutex
	accepting        bool
	view             *harnesstypes.ShellOutputView
	lastCheckpointAt int64
	lastCheckpoint   string
	onUpdate         harnesstypes.AgentHarnessToolUpdateCallback[*BashToolDetails]
}

// bashCheckpointSnapshot mirrors the JSON.stringify shape used upstream to
// decide whether a durable checkpoint is warranted. Details is omitted when
// absent, matching JSON.stringify's handling of undefined.
type bashCheckpointSnapshot struct {
	Content []aitypes.ContentBlock `json:"content"`
	Details *BashToolDetails       `json:"details,omitempty"`
}

// handle applies one bounded-output update and forwards the resulting
// snapshot. The external progress callback is invoked outside the lock.
func (s *bashProgressState) handle(update harnesstypes.ShellOutputUpdate, now int64) {
	s.mu.Lock()
	if !s.accepting {
		s.mu.Unlock()
		return
	}
	next := outputcapture.ApplyShellOutputUpdate(s.view, update)
	s.view = &next

	details := &BashToolDetails{FullOutputPath: next.SpillPath}
	if next.Truncation.Truncated {
		truncation := next.Truncation
		details.Truncation = &truncation
	}
	snapshot := bashCheckpointSnapshot{Content: []aitypes.ContentBlock{aitypes.TextBlock(next.Text)}, Details: details}
	encodedBytes, _ := json.Marshal(snapshot)
	encoded := string(encodedBytes)
	checkpoint := now-s.lastCheckpointAt >= bashCheckpointIntervalMs && encoded != s.lastCheckpoint
	if checkpoint {
		s.lastCheckpointAt = now
		s.lastCheckpoint = encoded
	}
	callback := s.onUpdate
	s.mu.Unlock()

	if callback == nil {
		return
	}
	var options *harnesstypes.AgentHarnessToolUpdateOptions
	if checkpoint {
		value := true
		options = &harnesstypes.AgentHarnessToolUpdateOptions{Checkpoint: &value}
	}
	callback(agenttypes.AgentToolResult[*BashToolDetails]{
		Content: snapshot.Content,
		Details: snapshot.Details,
	}, options)
}

func (s *bashProgressState) stop() {
	s.mu.Lock()
	s.accepting = false
	s.mu.Unlock()
}

func (s *bashProgressState) currentView() *harnesstypes.ShellOutputView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view
}

func executeBash(
	options *BashToolOptions,
	params BashToolInput,
	toolContext ExecutionToolContext,
	ctx harnesscontext.Context,
	onUpdate harnesstypes.AgentHarnessToolUpdateCallback[*BashToolDetails],
) (agenttypes.AgentToolResult[*BashToolDetails], error) {
	if err := validateBashTimeout(params.Timeout); err != nil {
		return agenttypes.AgentToolResult[*BashToolDetails]{}, err
	}
	env := toolContext.Env
	if env == nil {
		return agenttypes.AgentToolResult[*BashToolDetails]{}, errors.New("bash: missing execution environment")
	}

	command := params.Command
	if options != nil && options.CommandPrefix != "" {
		command = options.CommandPrefix + "\n" + command
	}
	execution := BashExecution{
		Command:    command,
		Cwd:        env.Cwd(),
		Env:        map[string]string{},
		InheritEnv: true,
	}
	if options != nil && options.Prepare != nil {
		if err := options.Prepare(&execution, toolContext, ctx); err != nil {
			return agenttypes.AgentToolResult[*BashToolDetails]{}, err
		}
	}

	retain := harnesstypes.ShellOutputTail
	spill := true
	progress := &bashProgressState{
		accepting:        true,
		lastCheckpointAt: time.Now().UnixMilli(),
		onUpdate:         onUpdate,
	}
	if onUpdate != nil {
		onUpdate(agenttypes.AgentToolResult[*BashToolDetails]{Content: []aitypes.ContentBlock{}}, nil)
	}

	cwd := execution.Cwd
	inheritEnv := execution.InheritEnv
	result := env.Exec(
		execution.Command,
		&harnesstypes.ShellExecOptions{
			Cwd:        &cwd,
			Env:        execution.Env,
			InheritEnv: &inheritEnv,
			Timeout:    params.Timeout,
			Capture: &harnesstypes.ShellOutputCaptureOptions{
				Limits: harnesstypes.ShellOutputLimits{
					MaxBytes: truncate.DefaultMaxBytes,
					MaxLines: truncate.DefaultMaxLines,
					Retain:   &retain,
				},
				Spill: &spill,
			},
			OnUpdate: func(update harnesstypes.ShellOutputUpdate, _ harnesstypes.Context) {
				progress.handle(update, time.Now().UnixMilli())
			},
		},
		ctx,
	)
	progress.stop()

	view := progress.currentView()
	outputText := ""
	if view != nil {
		outputText = view.Text
	}

	var capture *harnesstypes.ShellOutputMetadata
	if result.OK {
		capture = &result.Value.ShellOutputMetadata
	} else if view != nil {
		capture = &view.ShellOutputMetadata
	}

	var details *BashToolDetails
	if capture != nil && capture.Truncation.Truncated {
		truncation := capture.Truncation
		details = &BashToolDetails{Truncation: &truncation, FullOutputPath: capture.SpillPath}
		startLine := truncation.TotalLines - truncation.OutputLines + 1
		endLine := truncation.TotalLines
		spillPath := ""
		if capture.SpillPath != nil {
			spillPath = *capture.SpillPath
		}
		switch {
		case truncation.LastLinePartial:
			lastLineBytes := truncation.OutputBytes
			if capture.LastLineBytes != nil {
				lastLineBytes = *capture.LastLineBytes
			}
			outputText += fmt.Sprintf(
				"\n\n[Showing last %s of line %d (line is %s). Full output: %s]",
				truncate.FormatSize(truncation.OutputBytes),
				endLine,
				truncate.FormatSize(lastLineBytes),
				spillPath,
			)
		case truncation.TruncatedBy != nil && *truncation.TruncatedBy == "lines":
			outputText += fmt.Sprintf(
				"\n\n[Showing lines %d-%d of %d. Full output: %s]",
				startLine,
				endLine,
				truncation.TotalLines,
				spillPath,
			)
		default:
			outputText += fmt.Sprintf(
				"\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]",
				startLine,
				endLine,
				truncation.TotalLines,
				truncate.FormatSize(truncate.DefaultMaxBytes),
				spillPath,
			)
		}
	}

	if !result.OK {
		status := result.Error.Message
		switch result.Error.Code {
		case harnesstypes.ExecutionErrorTimeout:
			status = fmt.Sprintf("Command timed out after %s seconds", formatTimeoutSeconds(params.Timeout))
		case harnesstypes.ExecutionErrorAborted:
			status = "Command aborted"
		}
		if outputText != "" {
			return agenttypes.AgentToolResult[*BashToolDetails]{}, fmt.Errorf("%s\n\n%s", outputText, status)
		}
		return agenttypes.AgentToolResult[*BashToolDetails]{}, errors.New(status)
	}

	if result.Value.ExitCode != 0 {
		if outputText != "" {
			return agenttypes.AgentToolResult[*BashToolDetails]{}, fmt.Errorf("%s\n\nCommand exited with code %d", outputText, result.Value.ExitCode)
		}
		return agenttypes.AgentToolResult[*BashToolDetails]{}, fmt.Errorf("Command exited with code %d", result.Value.ExitCode)
	}

	text := outputText
	if text == "" {
		text = "(no output)"
	}
	return agenttypes.AgentToolResult[*BashToolDetails]{
		Content: []aitypes.ContentBlock{aitypes.TextBlock(text)},
		Details: details,
	}, nil
}

func formatTimeoutSeconds(timeout *float64) string {
	if timeout == nil {
		return "undefined"
	}
	return formatBashNumber(*timeout)
}
