package ai

import (
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

// Mirrors packages/ai/test/env-api-keys.test.ts: generic GitHub tokens are not
// Copilot credentials, and Anthropic keeps ANTHROPIC_AUTH_TOKEN out of API key
// lookup while still reporting it.
func TestFindEnvKeysAndGetEnvApiKey(t *testing.T) {
	t.Run("generic github tokens are not copilot credentials", func(t *testing.T) {
		t.Setenv("COPILOT_GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "gh-token")
		t.Setenv("GITHUB_TOKEN", "github-token")
		if keys := FindEnvKeys("github-copilot", nil); keys != nil {
			t.Fatalf("findEnvKeys = %v; want nil", keys)
		}
		if key := GetEnvApiKey("github-copilot", nil); key != nil {
			t.Fatalf("getEnvApiKey = %q; want nil", *key)
		}
	})

	t.Run("copilot credentials come from COPILOT_GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("COPILOT_GITHUB_TOKEN", "copilot-token")
		t.Setenv("GH_TOKEN", "gh-token")
		keys := FindEnvKeys("github-copilot", nil)
		if len(keys) != 1 || keys[0] != "COPILOT_GITHUB_TOKEN" {
			t.Fatalf("findEnvKeys = %v", keys)
		}
		if key := GetEnvApiKey("github-copilot", nil); key == nil || *key != "copilot-token" {
			t.Fatalf("getEnvApiKey = %v", key)
		}
	})

	t.Run("zai coding china plan", func(t *testing.T) {
		t.Setenv("ZAI_CODING_CN_API_KEY", "zai-coding-cn-token")
		keys := FindEnvKeys("zai-coding-cn", nil)
		if len(keys) != 1 || keys[0] != "ZAI_CODING_CN_API_KEY" {
			t.Fatalf("findEnvKeys = %v", keys)
		}
		if key := GetEnvApiKey("zai-coding-cn", nil); key == nil || *key != "zai-coding-cn-token" {
			t.Fatalf("getEnvApiKey = %v", key)
		}
	})

	t.Run("anthropic reports auth token but skips it for api key lookup", func(t *testing.T) {
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token")
		t.Setenv("ANTHROPIC_OAUTH_TOKEN", "oauth-token")
		t.Setenv("ANTHROPIC_API_KEY", "api-key")
		keys := FindEnvKeys("anthropic", nil)
		want := []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}
		if len(keys) != len(want) {
			t.Fatalf("findEnvKeys = %v", keys)
		}
		for i := range want {
			if keys[i] != want[i] {
				t.Fatalf("findEnvKeys = %v; want %v", keys, want)
			}
		}
		if key := GetEnvApiKey("anthropic", nil); key == nil || *key != "oauth-token" {
			t.Fatalf("getEnvApiKey = %v", key)
		}
	})

	t.Run("anthropic auth token alone yields no api key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token")
		t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		if keys := FindEnvKeys("anthropic", nil); len(keys) != 1 || keys[0] != "ANTHROPIC_AUTH_TOKEN" {
			t.Fatalf("findEnvKeys = %v", keys)
		}
		if key := GetEnvApiKey("anthropic", nil); key != nil {
			t.Fatalf("getEnvApiKey = %q; want nil", *key)
		}
	})

	t.Run("scoped provider env overrides process env", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "process-key")
		scoped := "scoped-key"
		env := types.ProviderEnv{"OPENAI_API_KEY": &scoped}
		if key := GetEnvApiKey("openai", env); key == nil || *key != "scoped-key" {
			t.Fatalf("getEnvApiKey = %v", key)
		}
	})
}
