// Filesystem and shell context required by the built-in execution tools.
//
// This is a Go port of packages/agent/src/harness/tools/tool-context.ts at Pi
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41be.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package tools

import harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"

// ExecutionToolContext is the filesystem and shell context injected into the
// built-in execution tools.
type ExecutionToolContext struct {
	Env harnesstypes.ExecutionEnv
}
