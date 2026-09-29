// Self-tests for the native pith CLI: flag parsing and configuration
// validation. The end-to-end tool loop lives in tools_delivery_test.go.
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpListsAllFlags(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("help returned error: %v", err)
	}
	output := stdout.String()
	for _, flag := range []string{"--prompt", "--base-url", "--model", "--api-key", "--cwd", "--session"} {
		if !strings.Contains(output, flag) {
			t.Fatalf("help output missing %s: %q", flag, output)
		}
	}
}

func TestMissingConfigurationFailsCleanly(t *testing.T) {
	cases := [][]string{
		{"--prompt", "hello"},
		{"--prompt", "hello", "--model", "fixture"},
		{"--prompt", "hello", "--base-url", "http://127.0.0.1:1/v1"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err == nil {
			t.Fatalf("expected failure for %v", args)
		}
		if strings.Contains(stdout.String(), "panic:") || strings.Contains(stderr.String(), "panic:") {
			t.Fatalf("configuration failure panicked for %v", args)
		}
	}
}

func TestMissingAPIKeyMentionsEnvironmentVariable(t *testing.T) {
	t.Setenv(apiKeyEnvVar, "")
	err := run([]string{"--prompt", "hello", "--model", "fixture", "--base-url", "http://127.0.0.1:1/v1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected missing API key error")
	}
	if !strings.Contains(err.Error(), apiKeyEnvVar) {
		t.Fatalf("error does not mention %s: %v", apiKeyEnvVar, err)
	}
}
