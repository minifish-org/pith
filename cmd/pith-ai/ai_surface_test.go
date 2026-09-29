package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

func newTestCLI(t *testing.T) (*CLI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return &CLI{
		Stdin:    strings.NewReader(""),
		Stdout:   stdout,
		Stderr:   stderr,
		AuthFile: filepath.Join(t.TempDir(), "auth.json"),
	}, stdout, stderr
}

func TestOAuthProvidersExposeLoginFlows(t *testing.T) {
	providers := OAuthProviders()
	if len(providers) == 0 {
		t.Fatal("expected at least one OAuth-capable builtin provider")
	}
	seen := map[string]bool{}
	for _, provider := range providers {
		if provider.Auth().OAuth == nil {
			t.Fatalf("provider %q listed without an OAuth flow", provider.ID())
		}
		seen[provider.ID()] = true
	}
	for _, expected := range []string{"anthropic", "openai-codex", "github-copilot", "xai"} {
		if !seen[expected] {
			t.Fatalf("expected OAuth provider %q in %v", expected, seen)
		}
	}
	if seen["openai"] {
		t.Fatal("api-key-only providers must not be listed")
	}
}

func TestListCommandDoesNotLeakCredentials(t *testing.T) {
	cli, stdout, _ := newTestCLI(t)
	if err := cli.Run([]string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "anthropic") {
		t.Fatalf("list output missing providers: %q", output)
	}
	for _, secret := range []string{"refresh-token", "access-token"} {
		if strings.Contains(output, secret) {
			t.Fatalf("list output leaked credential material")
		}
	}
}

func TestHelpCommandDocumentsCommands(t *testing.T) {
	cli, stdout, _ := newTestCLI(t)
	if err := cli.Run([]string{"help"}); err != nil {
		t.Fatalf("help: %v", err)
	}
	output := stdout.String()
	for _, want := range []string{"login", "list", "Providers:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q: %q", want, output)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	cli, _, _ := newTestCLI(t)
	err := cli.Run([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "Unknown command") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestAuthFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	credential := authtypes.NewOAuthCredential("refresh-token", "access-token", 123)
	credential.SetExtra("accountId", "acct-1")
	auth := map[string]authtypes.OAuthCredential{"anthropic": *credential}
	if err := SaveAuth(path, auth); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded := LoadAuth(path)
	stored, ok := loaded["anthropic"]
	if !ok {
		t.Fatalf("credential missing from %v", loaded)
	}
	if stored.Refresh != "refresh-token" || stored.Access != "access-token" {
		t.Fatalf("credential not preserved: %#v", stored)
	}
	if value, ok := stored.ExtraString("accountId"); !ok || value != "acct-1" {
		t.Fatalf("extension field not preserved: %#v", stored.Extra)
	}
}

func TestLoadAuthMissingFileIsEmpty(t *testing.T) {
	loaded := LoadAuth(filepath.Join(t.TempDir(), "absent.json"))
	if loaded == nil || len(loaded) != 0 {
		t.Fatalf("expected empty map, got %#v", loaded)
	}
}
