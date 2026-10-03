// Bash tool for the Durable coding tool set.
//
// This is a Go port of packages/durable/src/tools/bash.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The tool runs a command through the environment's shell. Its output streams
// to api.Output, where the harness keeps the tail within the default limits;
// the result content is that retained output. Output beyond the limits is
// spilled to a file whose path is reported as a diagnostic. A nonzero exit or
// timeout returns an ordinary error, which makes an error result that still
// carries the streamed output and diagnostics.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/harness"
)

// maxBashTimeoutSeconds is the upstream MAX_TIMEOUT_SECONDS bound. It is the
// largest timeout that still fits a signed 32-bit millisecond duration.
const maxBashTimeoutSeconds = 2147483647.0 / 1000

// bashToolSchema is the model-facing JSON Schema for the bash tool, mirroring
// the upstream TypeBox object: a required string command and an optional
// numeric timeout in seconds.
var bashToolSchema = json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Bash command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}},"required":["command"]}`)

// BashToolInput is the parsed parameter object for the bash tool.
type BashToolInput struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"`
}

// BashExecution is the mutable command plan passed to a BashPrepare callback.
// The callback may rewrite Command and CWD or replace Env before execution.
type BashExecution struct {
	Command    string            `json:"command"`
	CWD        string            `json:"cwd"`
	Env        map[string]string `json:"env"`
	InheritEnv bool              `json:"inheritEnv"`
}

// BashPrepare lets the harness customize the command, working directory and
// environment immediately before execution. Returning an error aborts the tool
// call before any process is spawned.
type BashPrepare func(context.Context, *BashExecution, harness.ToolAPI) error

// BashToolOptions configures the bash tool.
type BashToolOptions struct {
	// CommandPrefix is prepended as a separate line before every command.
	CommandPrefix string
	// Prepare runs after the execution plan is built and before Exec.
	Prepare BashPrepare
}

// CreateBashTool builds the bash tool.
func CreateBashTool(options ...BashToolOptions) harness.ToolRegistration {
	var opts BashToolOptions
	if len(options) > 0 {
		opts = options[0]
	}
	return harness.ToolRegistration{
		Declaration: types.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command in the current working directory. Returns combined stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.",
				DefaultMaxLines,
				DefaultMaxBytes/1024,
			),
			Input: types.JSONSchemaToolInput(bashToolSchema),
		},
		OutputLimits: &harness.OutputLimits{Retain: "tail"},
		Execute: func(ctx context.Context, raw json.RawMessage, api harness.ToolAPI) (harness.ToolResult, error) {
			return executeBash(ctx, raw, api, opts)
		},
	}
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
		return fmt.Errorf("Invalid timeout: maximum is %s seconds", formatNumber(maxBashTimeoutSeconds))
	}
	return nil
}

// formatNumber renders a JS number the way a template literal would.
func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func executeBash(ctx context.Context, raw json.RawMessage, api harness.ToolAPI, options BashToolOptions) (harness.ToolResult, error) {
	var input BashToolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("bash: invalid arguments: %w", err)
		}
	}
	if err := validateBashTimeout(input.Timeout); err != nil {
		return harness.ToolResult{}, err
	}
	e, err := requireEnv(api)
	if err != nil {
		return harness.ToolResult{}, err
	}

	command := input.Command
	if options.CommandPrefix != "" {
		command = options.CommandPrefix + "\n" + command
	}
	execution := BashExecution{
		Command:    command,
		CWD:        e.CWD(),
		Env:        map[string]string{},
		InheritEnv: true,
	}
	if options.Prepare != nil {
		if err := options.Prepare(ctx, &execution, api); err != nil {
			return harness.ToolResult{}, err
		}
	}

	var timeout time.Duration
	if input.Timeout != nil {
		timeout = time.Duration(*input.Timeout * float64(time.Second))
	}
	result, execErr := e.Exec(ctx, execution.Command, env.ExecOptions{
		CWD:        execution.CWD,
		Env:        execution.Env,
		InheritEnv: &execution.InheritEnv,
		Timeout:    timeout,
		OnOutput: func(_ context.Context, chunk []byte) error {
			return api.Output(chunk)
		},
		Spill: &env.SpillOptions{AfterBytes: DefaultMaxBytes, AfterLines: DefaultMaxLines},
	})

	spillPath := result.SpillPath
	var typedErr *env.ExecutionError
	if execErr != nil && errors.As(execErr, &typedErr) {
		spillPath = typedErr.SpillPath
	}
	if spillPath != "" {
		_ = api.Diagnostic(harness.ToolDiagnostic{
			Severity: "info",
			Code:     "full_output",
			Message:  "Full output: " + spillPath,
		})
	}

	if execErr != nil {
		if errors.As(execErr, &typedErr) {
			if typedErr.Code == env.ExecutionErrorAborted && ctx.Err() != nil {
				return harness.ToolResult{}, execErr
			}
			if typedErr.Code == env.ExecutionErrorTimeout {
				return harness.ToolResult{}, fmt.Errorf("Command timed out after %s seconds", formatNumberValue(input.Timeout))
			}
			if typedErr.Code == env.ExecutionErrorAborted {
				return harness.ToolResult{}, errors.New("Command aborted")
			}
		}
		return harness.ToolResult{}, execErr
	}

	if result.ExitCode != 0 {
		return harness.ToolResult{}, fmt.Errorf("Command exited with code %d", result.ExitCode)
	}
	return harness.ToolResult{}, nil
}

func formatNumberValue(value *float64) string {
	if value == nil {
		return "undefined"
	}
	return formatNumber(*value)
}
