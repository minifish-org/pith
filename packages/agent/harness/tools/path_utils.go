// Path normalization and resolution for the built-in tools.
//
// This is a Go port of packages/agent/src/harness/tools/path-utils.ts at Pi
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41be.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package tools

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

var unicodeSpaces = regexp.MustCompile("[\u00a0\u2000-\u200a\u202f\u205f\u3000]")

var amPmPattern = regexp.MustCompile(`(?i) (AM|PM)\.`)

const narrowNoBreakSpace = "\u202f"

func normalizeToolPath(path string) string {
	normalized := unicodeSpaces.ReplaceAllString(path, " ")
	if strings.HasPrefix(normalized, "@") {
		return normalized[1:]
	}
	return normalized
}

// ResolveToolPath normalizes a tool path and resolves it against the
// environment working directory.
func ResolveToolPath(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) string {
	return harnesstypes.GetOrThrow(resolveToolPathResult(env, path, ctx))
}

// resolveToolPathResult is the fallible form of ResolveToolPath used by tools
// that propagate backend failures as Go errors instead of panicking.
func resolveToolPathResult(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	return env.AbsolutePath(normalizeToolPath(path), ctx)
}

// ResolveReadToolPath resolves a path and then probes known textual variants
// produced by copy/paste (narrow no-break space, decomposed Unicode and
// typographic apostrophes). The first existing variant wins; the resolved path
// is returned when none exists.
func ResolveReadToolPath(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) string {
	return harnesstypes.GetOrThrow(resolveReadToolPathResult(env, path, ctx))
}

// resolveReadToolPathResult is the fallible form of ResolveReadToolPath. It
// preserves the upstream variant ordering and the first-existing-wins rule
// while returning the failing Result instead of throwing.
func resolveReadToolPathResult(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	resolved := resolveToolPathResult(env, path, ctx)
	if !resolved.OK {
		return resolved
	}
	variants := []string{
		resolved.Value,
		amPmPattern.ReplaceAllString(resolved.Value, narrowNoBreakSpace+"$1."),
		norm.NFD.String(resolved.Value),
		strings.ReplaceAll(resolved.Value, "'", "\u2019"),
		norm.NFD.String(strings.ReplaceAll(resolved.Value, "'", "\u2019")),
	}
	seen := map[string]bool{}
	for _, variant := range variants {
		if seen[variant] {
			continue
		}
		seen[variant] = true
		exists := env.Exists(variant, ctx)
		if !exists.OK {
			return harnesstypes.Err[string, harnesstypes.FileError](exists.Error)
		}
		if exists.Value {
			return harnesstypes.Ok[string, harnesstypes.FileError](variant)
		}
	}
	return resolved
}
