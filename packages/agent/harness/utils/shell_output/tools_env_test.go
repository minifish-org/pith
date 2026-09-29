package shell_output

import (
	"context"
	"strings"
	"testing"

	env "github.com/minifish-org/pith/packages/agent/harness/env"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

func TestExecuteShellWithCaptureCollectsBoundedView(t *testing.T) {
	root := t.TempDir()
	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	result := harnesstypes.GetOrThrow(ExecuteShellWithCapture(environment, "printf hello", nil, ctx))
	if result.Output != "hello" {
		t.Fatalf("output = %q", result.Output)
	}
	if result.Truncated {
		t.Fatalf("small output should not be truncated: %#v", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("exit code = %#v", result.ExitCode)
	}
}

func TestExecuteShellWithCaptureSpillsLargeOutput(t *testing.T) {
	root := t.TempDir()
	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	result := harnesstypes.GetOrThrow(ExecuteShellWithCapture(environment, "yes line | head -n 5000", nil, ctx))
	if !result.Truncated {
		t.Fatalf("large output should be truncated: %#v", result)
	}
	if result.FullOutputPath == nil {
		t.Fatalf("expected a full output path: %#v", result)
	}
	full := harnesstypes.GetOrThrow(environment.ReadTextFile(*result.FullOutputPath, ctx))
	if len(result.Output) >= len(full) {
		t.Fatalf("bounded view %d should be smaller than full output %d", len(result.Output), len(full))
	}
	if strings.Count(full, "\n") <= 2000 {
		t.Fatalf("spilled file lost lines: %d", strings.Count(full, "\n"))
	}
}

func TestExecuteShellWithCaptureReportsCancellation(t *testing.T) {
	root := t.TempDir()
	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := harnesstypes.GetOrThrow(ExecuteShellWithCapture(environment, "sleep 30", nil, ctx))
	if !result.Cancelled {
		t.Fatalf("expected cancelled capture: %#v", result)
	}
}

func TestSanitizeBinaryOutput(t *testing.T) {
	if got := SanitizeBinaryOutput("a\x00b\tc\nd\re"); got != "ab\tc\nde" {
		t.Fatalf("SanitizeBinaryOutput = %q", got)
	}
}
