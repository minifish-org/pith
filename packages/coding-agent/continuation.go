// Continuation and raw-output handling for the embedded SDK.
//
// This file carries the headless Go adaptation of
// packages/coding-agent/src/core/output-guard.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe, plus the continuation helpers that
// keep a length-truncated assistant response from ending a run.
//
// The upstream output guard temporarily redirects process.stdout to stderr so
// the TUI owns the real terminal while a request is streamed. The embedded SDK
// has no terminal renderer, so the Go adaptation provides the same primitive as
// a small, race-free writer: TakeOverStdout swaps os.Stdout for os.Stderr and
// WriteRawStdout serializes direct writes to the saved descriptor. There is no
// process-global registration and no hidden goroutine that outlives the caller.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"os"
	"sync"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

var (
	outputGuardMu    sync.Mutex
	outputGuardTaken bool
	outputGuardRaw   *os.File
	outputGuardOrig  *os.File
	// outputGuardTail is closed once every enqueued raw write has completed.
	outputGuardTail = closedSignal()
)

func closedSignal() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// TakeOverStdout redirects ordinary stdout writes to stderr and remembers the
// original descriptor for raw output. It is idempotent.
func TakeOverStdout() {
	outputGuardMu.Lock()
	defer outputGuardMu.Unlock()
	if outputGuardTaken {
		return
	}
	outputGuardRaw = os.Stdout
	outputGuardOrig = os.Stdout
	os.Stdout = os.Stderr
	outputGuardTaken = true
}

// RestoreStdout restores the original stdout descriptor. It is idempotent and
// safe when stdout was never taken over.
func RestoreStdout() {
	outputGuardMu.Lock()
	defer outputGuardMu.Unlock()
	if !outputGuardTaken {
		return
	}
	if outputGuardOrig != nil {
		os.Stdout = outputGuardOrig
	}
	outputGuardTaken = false
}

// IsStdoutTakenOver reports whether TakeOverStdout has redirected stdout.
func IsStdoutTakenOver() bool {
	outputGuardMu.Lock()
	defer outputGuardMu.Unlock()
	return outputGuardTaken
}

// WriteRawStdout enqueues a write to the saved raw stdout descriptor. Writes
// are serialized in call order. It is a no-op before TakeOverStdout.
func WriteRawStdout(text string) {
	if text == "" {
		return
	}
	outputGuardMu.Lock()
	if !outputGuardTaken || outputGuardRaw == nil {
		outputGuardMu.Unlock()
		return
	}
	raw := outputGuardRaw
	previous := outputGuardTail
	next := make(chan struct{})
	outputGuardTail = next
	outputGuardMu.Unlock()

	go func() {
		defer close(next)
		<-previous
		_, _ = raw.WriteString(text)
	}()
}

// WaitForRawStdoutBackpressure blocks until every enqueued raw write and the
// writes they were ordered behind have completed.
func WaitForRawStdoutBackpressure() {
	for {
		outputGuardMu.Lock()
		tail := outputGuardTail
		outputGuardMu.Unlock()
		if tail == nil {
			return
		}
		<-tail
		outputGuardMu.Lock()
		done := outputGuardTail == tail
		outputGuardMu.Unlock()
		if done {
			return
		}
	}
}

// FlushRawStdout waits for pending raw writes and flushes the descriptor.
func FlushRawStdout() {
	WaitForRawStdoutBackpressure()
	outputGuardMu.Lock()
	raw := outputGuardRaw
	taken := outputGuardTaken
	outputGuardMu.Unlock()
	if taken && raw != nil {
		_ = raw.Sync()
	}
}

// truncationContinuationNeeded reports whether a finalized assistant response
// must be followed by a continuation request instead of ending the run. A
// length stop is never a completed answer, even when its tool calls parsed.
func truncationContinuationNeeded(stopReason aitypes.StopReason) bool {
	return stopReason == aitypes.StopReasonLength
}
