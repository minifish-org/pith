// This file is a Go port of packages/ai/src/utils/provider-env.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream has a Bun-sandbox fallback that reads /proc/self/environ when a
// compiled Bun binary sees an empty process.env. A Go program always has
// os.Environ(), so that fallback is unnecessary; it is registered as a
// runtime-adaptation and cannot be exercised from Go.
package utils

import (
	"os"

	"github.com/minifish-org/pith/packages/ai/types"
)

// GetProviderEnvValue resolves a provider env value from scoped overrides, then
// the normal process environment. An empty scoped value falls through, matching
// the upstream `||` chain.
func GetProviderEnvValue(name string, env types.ProviderEnv) *string {
	if env != nil {
		if value, ok := env[name]; ok && value != nil && *value != "" {
			return value
		}
	}
	if value, ok := os.LookupEnv(name); ok {
		return &value
	}
	return nil
}
