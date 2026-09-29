// This file is a Go port of packages/agent/src/stream-fn.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The default stream function is a process-wide fallback installed by a host
// that owns a concrete model runtime. Keeping it in its own tiny file mirrors
// upstream and lets Agent and the low-level loop share one accessor without
// depending on a provider catalog or SDK facade.
package agent

import (
	"errors"
	"sync"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

var (
	defaultStreamFnMu sync.RWMutex
	defaultStreamFn   agenttypes.StreamFn
)

// SetDefaultStreamFn configures the fallback used by Agent and the low-level
// loops when callers omit a stream function. Passing nil clears the fallback.
func SetDefaultStreamFn(streamFn agenttypes.StreamFn) {
	defaultStreamFnMu.Lock()
	defaultStreamFn = streamFn
	defaultStreamFnMu.Unlock()
}

// GetDefaultStreamFn returns the configured fallback stream function. It
// reports an error when no fallback has been installed, which callers surface
// as a construction failure instead of a nil dereference later.
func GetDefaultStreamFn() (agenttypes.StreamFn, error) {
	defaultStreamFnMu.RLock()
	streamFn := defaultStreamFn
	defaultStreamFnMu.RUnlock()
	if streamFn == nil {
		return nil, errors.New("agent: no default stream function configured. Pass StreamFn explicitly or call SetDefaultStreamFn")
	}
	return streamFn, nil
}
