// Package shell_output is the Go port of
// packages/agent/src/harness/utils/shell-output.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// It is a compatibility collector for callers that need one bounded final
// view. Source-side capture, adaptive publication and spilling remain owned by
// the execution environment.
package shell_output

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	outputcapture "github.com/minifish-org/pith/packages/agent/harness/utils/output_capture"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
)

// ShellCaptureProgress is the bounded output view plus its truncation state.
type ShellCaptureProgress struct {
	Output         string                    `json:"output"`
	Truncation     truncate.TruncationResult `json:"truncation"`
	FullOutputPath *string                   `json:"fullOutputPath,omitempty"`
	LastLineBytes  int                       `json:"lastLineBytes"`
}

// ShellCaptureOptions configures a compatibility shell capture. It excludes
// capture and onUpdate, which the collector owns.
type ShellCaptureOptions struct {
	Cwd        *string           `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	InheritEnv *bool             `json:"inheritEnv,omitempty"`
	Timeout    *float64          `json:"timeout,omitempty"`
	// OnChunk receives each incremental chunk together with a progress getter.
	OnChunk func(chunk string, getProgress func() ShellCaptureProgress, ctx harnesstypes.Context) `json:"-"`
	// ReturnExecutionErrors returns shell execution failures with captured
	// output instead of as a failed Result.
	ReturnExecutionErrors bool `json:"returnExecutionErrors,omitempty"`
}

// ShellCaptureResult is the final compatibility capture.
type ShellCaptureResult struct {
	ShellCaptureProgress
	ExitCode       *int                         `json:"exitCode"`
	Cancelled      bool                         `json:"cancelled"`
	Truncated      bool                         `json:"truncated"`
	ExecutionError *harnesstypes.ExecutionError `json:"executionError,omitempty"`
}

func boolPointer(value bool) *bool { return &value }

func progressFrom(output harnesstypes.ShellOutputView) ShellCaptureProgress {
	progress := ShellCaptureProgress{
		Output: output.Text,
		Truncation: truncate.TruncationResult{
			Content:               output.Text,
			Truncated:             output.Truncation.Truncated,
			TruncatedBy:           output.Truncation.TruncatedBy,
			TotalLines:            output.Truncation.TotalLines,
			TotalBytes:            output.Truncation.TotalBytes,
			OutputLines:           output.Truncation.OutputLines,
			OutputBytes:           output.Truncation.OutputBytes,
			LastLinePartial:       output.Truncation.LastLinePartial,
			FirstLineExceedsLimit: output.Truncation.FirstLineExceedsLimit,
			MaxLines:              output.Truncation.MaxLines,
			MaxBytes:              output.Truncation.MaxBytes,
		},
	}
	if output.SpillPath != nil {
		progress.FullOutputPath = output.SpillPath
	}
	if output.LastLineBytes != nil {
		progress.LastLineBytes = *output.LastLineBytes
	}
	return progress
}

// ExecuteShellWithCapture runs a command through the execution environment and
// collects one bounded final view, spilling complete output when truncated.
func ExecuteShellWithCapture(
	env harnesstypes.ExecutionEnv,
	command string,
	options *ShellCaptureOptions,
	ctx harnesstypes.Context,
) harnesstypes.Result[ShellCaptureResult, harnesstypes.ExecutionError] {
	var output *harnesstypes.ShellOutputView
	execOptions := &harnesstypes.ShellExecOptions{
		Capture: &harnesstypes.ShellOutputCaptureOptions{
			Limits: harnesstypes.ShellOutputLimits{
				MaxBytes: truncate.DefaultMaxBytes,
				MaxLines: truncate.DefaultMaxLines,
				Retain:   retentionPointer(harnesstypes.ShellOutputTail),
			},
			Spill: boolPointer(true),
		},
	}
	if options != nil {
		execOptions.Cwd = options.Cwd
		execOptions.Env = options.Env
		execOptions.InheritEnv = options.InheritEnv
		execOptions.Timeout = options.Timeout
	}
	execOptions.OnUpdate = func(update harnesstypes.ShellOutputUpdate, updateContext harnesstypes.Context) {
		previous := output
		view := outputcapture.ApplyShellOutputUpdate(output, update)
		output = &view
		var chunk string
		hasChunk := false
		switch update.Kind {
		case harnesstypes.ShellOutputUpdateAppend, harnesstypes.ShellOutputUpdateSlide:
			if update.Text != nil {
				chunk = *update.Text
				hasChunk = true
			}
		case harnesstypes.ShellOutputUpdateReplace:
			if previous == nil {
				chunk = output.Text
				hasChunk = true
			}
		}
		// A metadata-only update and a post-cap replacement contain no new
		// incremental chunk. Reporting their complete view would duplicate
		// bytes for callers that accumulate this compatibility callback.
		if hasChunk && options != nil && options.OnChunk != nil {
			current := output
			options.OnChunk(chunk, func() ShellCaptureProgress { return progressFrom(*current) }, updateContext)
		}
	}

	result := env.Exec(command, execOptions, ctx)

	if output == nil {
		empty := truncate.TruncateTail("", truncate.TruncationOptions{})
		view := harnesstypes.ShellOutputView{
			Text: empty.Content,
			ShellOutputMetadata: harnesstypes.ShellOutputMetadata{
				Truncation: harnesstypes.ShellOutputTruncationFrom(empty),
			},
		}
		output = &view
	}
	progress := progressFrom(*output)
	if !result.OK {
		if result.Error.Code == harnesstypes.ExecutionErrorAborted || (ctx != nil && ctx.Err() != nil) {
			return harnesstypes.Ok[ShellCaptureResult, harnesstypes.ExecutionError](ShellCaptureResult{
				ShellCaptureProgress: progress,
				Cancelled:            true,
				Truncated:            progress.Truncation.Truncated,
			})
		}
		if options != nil && options.ReturnExecutionErrors {
			failure := result.Error
			return harnesstypes.Ok[ShellCaptureResult, harnesstypes.ExecutionError](ShellCaptureResult{
				ShellCaptureProgress: progress,
				Truncated:            progress.Truncation.Truncated,
				ExecutionError:       &failure,
			})
		}
		return harnesstypes.Err[ShellCaptureResult, harnesstypes.ExecutionError](result.Error)
	}
	exitCode := result.Value.ExitCode
	return harnesstypes.Ok[ShellCaptureResult, harnesstypes.ExecutionError](ShellCaptureResult{
		ShellCaptureProgress: progress,
		ExitCode:             &exitCode,
		Truncated:            result.Value.Truncation.Truncated,
	})
}

// SanitizeBinaryOutput removes invalid control characters from captured bytes.
func SanitizeBinaryOutput(text string) string {
	return outputcapture.SanitizeShellOutput(text)
}

var _ = outputcapture.OutputMinEmitIntervalMs

func retentionPointer(value harnesstypes.ShellOutputRetention) *harnesstypes.ShellOutputRetention {
	return &value
}
