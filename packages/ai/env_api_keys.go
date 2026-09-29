// This file is a Go port of packages/ai/src/env-api-keys.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream loads node:fs/os/path through a bundler-opaque dynamic import so the
// module works in browsers. Go always has the standard library, so file checks
// use os/path directly; the Vertex ADC fallback is preserved.
package ai

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// Anthropic env var names. ANTHROPIC_AUTH_TOKEN participates in env discovery
// and status but is skipped by GetEnvApiKey because requests must pass it as an
// Authorization: Bearer header.
const (
	ANTHROPIC_AUTH_TOKEN_ENV  = "ANTHROPIC_AUTH_TOKEN"
	ANTHROPIC_OAUTH_TOKEN_ENV = "ANTHROPIC_OAUTH_TOKEN"
	ANTHROPIC_API_KEY_ENV     = "ANTHROPIC_API_KEY"
)

var (
	vertexADCMu     sync.Mutex
	vertexADCCached *bool
)

func hasVertexADCCredentials(env types.ProviderEnv) bool {
	if explicit := utils.GetProviderEnvValue("GOOGLE_APPLICATION_CREDENTIALS", env); explicit != nil && *explicit != "" {
		return fileExists(*explicit)
	}
	vertexADCMu.Lock()
	defer vertexADCMu.Unlock()
	if vertexADCCached != nil {
		return *vertexADCCached
	}
	result := fileExists(filepath.Join(homeDir(), ".config", "gcloud", "application_default_credentials.json"))
	vertexADCCached = &result
	return result
}

func fileExists(path string) bool {
	if strings.HasPrefix(path, "~") {
		path = filepath.Join(homeDir(), strings.TrimPrefix(path, "~"))
	}
	_, err := os.Stat(path)
	return err == nil
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func getAPIKeyEnvVars(provider string) []string {
	switch provider {
	case "github-copilot":
		return []string{"COPILOT_GITHUB_TOKEN"}
	case "anthropic":
		return []string{ANTHROPIC_AUTH_TOKEN_ENV, ANTHROPIC_OAUTH_TOKEN_ENV, ANTHROPIC_API_KEY_ENV}
	}

	envMap := map[string]string{
		"ant-ling":                   "ANT_LING_API_KEY",
		"qwen-token-plan":            "QWEN_TOKEN_PLAN_API_KEY",
		"qwen-token-plan-cn":         "QWEN_TOKEN_PLAN_CN_API_KEY",
		"qwen-token-plan-individual": "QWEN_TOKEN_PLAN_API_KEY",
		"openai":                     "OPENAI_API_KEY",
		"azure-openai-responses":     "AZURE_OPENAI_API_KEY",
		"nvidia":                     "NVIDIA_API_KEY",
		"deepseek":                   "DEEPSEEK_API_KEY",
		"google":                     "GEMINI_API_KEY",
		"google-vertex":              "GOOGLE_CLOUD_API_KEY",
		"groq":                       "GROQ_API_KEY",
		"cerebras":                   "CEREBRAS_API_KEY",
		"xai":                        "XAI_API_KEY",
		"radius":                     "RADIUS_API_KEY",
		"openrouter":                 "OPENROUTER_API_KEY",
		"vercel-ai-gateway":          "AI_GATEWAY_API_KEY",
		"zai":                        "ZAI_API_KEY",
		"zai-coding-cn":              "ZAI_CODING_CN_API_KEY",
		"mistral":                    "MISTRAL_API_KEY",
		"minimax":                    "MINIMAX_API_KEY",
		"minimax-cn":                 "MINIMAX_CN_API_KEY",
		"moonshotai":                 "MOONSHOT_API_KEY",
		"moonshotai-cn":              "MOONSHOT_API_KEY",
		"huggingface":                "HF_TOKEN",
		"fireworks":                  "FIREWORKS_API_KEY",
		"together":                   "TOGETHER_API_KEY",
		"baseten":                    "BASETEN_API_KEY",
		"opencode":                   "OPENCODE_API_KEY",
		"opencode-go":                "OPENCODE_API_KEY",
		"kimi-coding":                "KIMI_API_KEY",
		"meta":                       "META_API_KEY",
		"cloudflare-workers-ai":      "CLOUDFLARE_API_KEY",
		"cloudflare-ai-gateway":      "CLOUDFLARE_API_KEY",
		"xiaomi":                     "XIAOMI_API_KEY",
		"xiaomi-token-plan-cn":       "XIAOMI_TOKEN_PLAN_CN_API_KEY",
		"xiaomi-token-plan-ams":      "XIAOMI_TOKEN_PLAN_AMS_API_KEY",
		"xiaomi-token-plan-sgp":      "XIAOMI_TOKEN_PLAN_SGP_API_KEY",
	}
	for known, envVar := range envMap {
		if known == provider {
			return []string{envVar}
		}
	}
	return nil
}

// FindEnvKeys finds configured environment variables that can provide an API
// key for a provider. It only reports actual API key variables and excludes
// ambient credential sources.
//
// Ports `findEnvKeys` from packages/ai/src/env-api-keys.ts.
func FindEnvKeys(provider string, env types.ProviderEnv) []string {
	envVars := getAPIKeyEnvVars(provider)
	if len(envVars) == 0 {
		return nil
	}
	found := make([]string, 0, len(envVars))
	for _, envVar := range envVars {
		if value := utils.GetProviderEnvValue(envVar, env); value != nil && *value != "" {
			found = append(found, envVar)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return found
}

// GetEnvApiKey gets an API key for a provider from known environment variables.
// It will not return API keys for providers that require OAuth tokens.
//
// Ports `getEnvApiKey` from packages/ai/src/env-api-keys.ts.
func GetEnvApiKey(provider string, env types.ProviderEnv) *string {
	envKeys := FindEnvKeys(provider, env)
	if len(envKeys) > 0 {
		apiKeyEnv := envKeys[0]
		if provider == "anthropic" {
			for _, key := range envKeys {
				if key != ANTHROPIC_AUTH_TOKEN_ENV {
					apiKeyEnv = key
					break
				}
			}
			if apiKeyEnv == ANTHROPIC_AUTH_TOKEN_ENV {
				apiKeyEnv = ""
			}
		}
		if apiKeyEnv != "" {
			if value := utils.GetProviderEnvValue(apiKeyEnv, env); value != nil {
				return value
			}
		}
	}

	if provider == "google-vertex" {
		hasCredentials := hasVertexADCCredentials(env)
		hasProject := utils.GetProviderEnvValue("GOOGLE_CLOUD_PROJECT", env) != nil || utils.GetProviderEnvValue("GCLOUD_PROJECT", env) != nil
		hasLocation := utils.GetProviderEnvValue("GOOGLE_CLOUD_LOCATION", env) != nil
		if hasCredentials && hasProject && hasLocation {
			value := "<authenticated>"
			return &value
		}
	}

	if provider == "amazon-bedrock" {
		if utils.GetProviderEnvValue("AWS_PROFILE", env) != nil ||
			(utils.GetProviderEnvValue("AWS_ACCESS_KEY_ID", env) != nil && utils.GetProviderEnvValue("AWS_SECRET_ACCESS_KEY", env) != nil) ||
			utils.GetProviderEnvValue("AWS_BEARER_TOKEN_BEDROCK", env) != nil ||
			utils.GetProviderEnvValue("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", env) != nil ||
			utils.GetProviderEnvValue("AWS_CONTAINER_CREDENTIALS_FULL_URI", env) != nil ||
			utils.GetProviderEnvValue("AWS_WEB_IDENTITY_TOKEN_FILE", env) != nil {
			value := "<authenticated>"
			return &value
		}
	}

	return nil
}
