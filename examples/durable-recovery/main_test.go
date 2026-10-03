package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestApplyEffectDeduplicatesByIdempotencyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effects.json")
	first, err := applyEffect(path, "charge-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := applyEffect(path, "charge-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Calls != 1 || second.Calls != 2 || len(second.Effects) != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	third, err := applyEffect(path, "charge-2")
	if err != nil {
		t.Fatal(err)
	}
	if third.Calls != 3 || len(third.Effects) != 2 {
		t.Fatalf("third=%+v", third)
	}
	reloaded, err := readEffectState(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Calls != 3 || len(reloaded.Effects) != 2 {
		t.Fatalf("reloaded=%+v", reloaded)
	}
}

func TestReadLinesHandlesMissingAndTrailing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.log")
	lines, err := readLines(missing)
	if err != nil || lines != nil {
		t.Fatalf("missing lines=%v err=%v", lines, err)
	}
	path := filepath.Join(dir, "present.log")
	if err := appendLine(path, "a"); err != nil {
		t.Fatal(err)
	}
	if err := appendLine(path, "b"); err != nil {
		t.Fatal(err)
	}
	lines, err = readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("lines=%v", lines)
	}
}

// buildExample compiles the runnable example so its subprocess re-exec works.
func buildExample(t *testing.T) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "durable-recovery")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", executable, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build example: %v\n%s", err, output)
	}
	return executable
}

func runDemo(t *testing.T, backend, replay string) string {
	t.Helper()
	executable := buildExample(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-backend", backend, "-replay", replay)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run demo: %v\n%s", err, output)
	}
	return string(output)
}

func TestRecoveryDemoKillRestartAndTicker(t *testing.T) {
	cases := []struct {
		backend string
		replay  string
		calls   int
	}{
		{"sqlite", "safe", 2},
		{"sqlite", "unsafe", 1},
		{"jsonl", "safe", 2},
		{"jsonl", "unsafe", 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.backend+"-"+testCase.replay, func(t *testing.T) {
			output := runDemo(t, testCase.backend, testCase.replay)
			for _, want := range []string{
				"durable-recovery demo:",
				"pre-kill: calls=1 effects=1",
				"recovered: calls=",
				"crash recovery complete",
				"ticker: reopened and completed (3 distinct steps)",
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("output missing %q:\n%s", want, output)
				}
			}
			want := "recovered: calls=" + strconv.Itoa(testCase.calls) + " effects=1"
			if !strings.Contains(output, want) {
				t.Fatalf("output missing %q:\n%s", want, output)
			}
		})
	}
}

func TestChildPhaseRequiresDirectory(t *testing.T) {
	executable := buildExample(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-phase", "first")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	err := command.Run()
	if err == nil {
		t.Fatal("child phase without -dir should fail")
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("unexpected error: %v", err)
	}
}
