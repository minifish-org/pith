// Environment access for the Durable coding tools.
//
// This is a Go port of packages/durable/src/tools/env.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
package tools

import (
	"errors"

	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/harness"
)

// errNoEnvironment is the ordinary error a tool reports when no execution
// environment is configured for its invocation.
var errNoEnvironment = errors.New("No execution environment is configured")

// requireEnv returns the call's execution environment. A tool without one fails
// with an ordinary error.
func requireEnv(api harness.ToolAPI) (env.ExecutionEnv, error) {
	if api == nil {
		return nil, errNoEnvironment
	}
	e := api.Env()
	if e == nil {
		return nil, errNoEnvironment
	}
	return e, nil
}
