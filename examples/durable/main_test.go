package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func offlineConfig(backend, state string) config {
	return config{
		Offline:  true,
		Backend:  backend,
		State:    state,
		Provider: "offline-scripted",
		ModelID:  "offline-scripted",
		Prompt:   "What does the Durable SDK do?",
	}
}

func runExample(t *testing.T, cfg config) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := run(ctx, cfg, &output); err != nil {
		t.Fatalf("run: %v", err)
	}
	return output.String()
}

func assertOfflineOutput(t *testing.T, output string) {
	t.Helper()
	for _, want := range []string{
		"OFFLINE mode",
		"no real usage or cost",
		"assistant reply:",
		"deduplicated submission",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestExampleOfflineMemory(t *testing.T) {
	assertOfflineOutput(t, runExample(t, offlineConfig("memory", "")))
}

func TestExampleOfflineJSONL(t *testing.T) {
	assertOfflineOutput(t, runExample(t, offlineConfig("jsonl", t.TempDir())))
}

func TestExampleOfflineSQLite(t *testing.T) {
	assertOfflineOutput(t, runExample(t, offlineConfig("sqlite", filepath.Join(t.TempDir(), "example.sqlite"))))
}

func TestExampleLiveRequiresKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config{
		Offline:   false,
		Backend:   "memory",
		Provider:  "openai-compatible",
		ModelID:   "gpt-4o-mini",
		BaseURL:   "http://127.0.0.1:1/v1",
		APIKeyEnv: "PITH_DURABLE_EXAMPLE_MISSING_KEY",
		Prompt:    "hello",
	}
	var output bytes.Buffer
	err := run(ctx, cfg, &output)
	if err == nil || !strings.Contains(err.Error(), "PITH_DURABLE_EXAMPLE_MISSING_KEY") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestLastAssistantText(t *testing.T) {
	if got := lastAssistantText(nil); got != "" {
		t.Fatalf("empty messages = %q", got)
	}
}
