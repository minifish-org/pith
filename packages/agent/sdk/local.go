// Local execution environment facade.
//
// This is a Go port of packages/agent/src/node.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// node.ts re-exports NodeExecutionEnv, the Node-backed ExecutionEnv, and then
// forwards the whole agent package. The Go equivalent is the standard-library
// LocalExecutionEnv in packages/agent/harness/env; this file surfaces it on the
// SDK entry point so embedders only import the SDK.
package sdk

import (
	"github.com/minifish-org/pith/packages/agent/harness/env"
)

// LocalExecutionEnv is the Go counterpart of NodeExecutionEnv: the local
// filesystem and shell execution environment implementing the FileSystem and
// Shell protocols.
type LocalExecutionEnv = env.LocalExecutionEnv

// LocalExecutionEnvOptions configures a LocalExecutionEnv.
type LocalExecutionEnvOptions = env.LocalExecutionEnvOptions

// NewLocalExecutionEnv builds a LocalExecutionEnv with the supplied options.
func NewLocalExecutionEnv(options LocalExecutionEnvOptions) *LocalExecutionEnv {
	return env.NewLocalExecutionEnv(options)
}
