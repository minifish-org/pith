// This file is a Go port of packages/ai/src/session-resources.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Session-scoped resource cleanups: callers register a cleanup callback and
// receive an unsubscribe function. A single cleanup pass invokes every still
// registered callback exactly once in registration order and aggregates the
// failures, mirroring the upstream Set + AggregateError behavior.
package ai

import (
	"fmt"
	"sync"
)

// SessionResourceCleanup releases a resource associated with an optional
// session id. Upstream returns void; a Go cleanup reports failure by returning
// an error, and a panic is also captured and aggregated.
type SessionResourceCleanup func(sessionID *string) error

// AggregateCleanupError reports every failure of one cleanup pass. It mirrors
// the upstream AggregateError("Failed to cleanup session resources").
type AggregateCleanupError struct {
	Message string
	Errors  []error
}

// Error implements error.
func (e *AggregateCleanupError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap exposes the aggregated failures.
func (e *AggregateCleanupError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return e.Errors
}

type sessionCleanupEntry struct {
	cleanup SessionResourceCleanup
	removed bool
}

// sessionCleanupRegistry keeps the registered cleanups in insertion order. A
// plain map would not preserve the observable cleanup order of the upstream
// Set, so an ordered slice is used instead.
var sessionCleanupRegistry = struct {
	mu      sync.Mutex
	entries []*sessionCleanupEntry
}{}

// RegisterSessionResourceCleanup registers cleanup and returns an idempotent
// unsubscribe function. Registration order is preserved for cleanup passes.
//
// Ports `registerSessionResourceCleanup` from
// packages/ai/src/session-resources.ts.
func RegisterSessionResourceCleanup(cleanup SessionResourceCleanup) func() {
	if cleanup == nil {
		return func() {}
	}
	entry := &sessionCleanupEntry{cleanup: cleanup}
	sessionCleanupRegistry.mu.Lock()
	sessionCleanupRegistry.entries = append(sessionCleanupRegistry.entries, entry)
	sessionCleanupRegistry.mu.Unlock()
	return func() {
		sessionCleanupRegistry.mu.Lock()
		entry.removed = true
		sessionCleanupRegistry.mu.Unlock()
	}
}

// CleanupSessionResources invokes every registered cleanup once. Failures are
// collected and returned as an *AggregateCleanupError; a nil result means every
// cleanup succeeded. Cleanups that panic are recorded as failures.
//
// Ports `cleanupSessionResources` from packages/ai/src/session-resources.ts.
func CleanupSessionResources(sessionID *string) error {
	sessionCleanupRegistry.mu.Lock()
	entries := append([]*sessionCleanupEntry(nil), sessionCleanupRegistry.entries...)
	sessionCleanupRegistry.mu.Unlock()

	var failures []error
	for _, entry := range entries {
		if entry.removed {
			continue
		}
		if err := runSessionCleanup(entry.cleanup, sessionID); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &AggregateCleanupError{Message: "Failed to cleanup session resources", Errors: failures}
}

// runSessionCleanup isolates one callback so a panic does not skip the
// remaining cleanups.
func runSessionCleanup(cleanup SessionResourceCleanup, sessionID *string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("session resource cleanup panicked: %v", recovered)
		}
	}()
	return cleanup(sessionID)
}
