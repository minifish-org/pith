// Path normalization and resolution for the Durable coding tools.
//
// This is a Go port of packages/durable/src/tools/path-utils.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
package tools

import (
	"context"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/minifish-org/pith/packages/durable/env"
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
func ResolveToolPath(ctx context.Context, e env.ExecutionEnv, path string) (string, error) {
	return e.AbsolutePath(ctx, normalizeToolPath(path))
}

// ResolveReadToolPath resolves a path and then probes known textual variants
// produced by copy/paste (narrow no-break space, decomposed Unicode and
// typographic apostrophes). The first existing variant wins; the resolved path
// is returned when none exists.
func ResolveReadToolPath(ctx context.Context, e env.ExecutionEnv, path string) (string, error) {
	resolved, err := ResolveToolPath(ctx, e, path)
	if err != nil {
		return "", err
	}
	variants := []string{
		resolved,
		amPmPattern.ReplaceAllString(resolved, narrowNoBreakSpace+"$1."),
		norm.NFD.String(resolved),
		strings.ReplaceAll(resolved, "'", "\u2019"),
		norm.NFD.String(strings.ReplaceAll(resolved, "'", "\u2019")),
	}
	seen := map[string]bool{}
	for _, variant := range variants {
		if seen[variant] {
			continue
		}
		seen[variant] = true
		exists, err := e.Exists(ctx, variant)
		if err != nil {
			return "", err
		}
		if exists {
			return variant, nil
		}
	}
	return resolved, nil
}
