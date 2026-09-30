// Retry policy and diagnostic handling for the embedded SDK session.
//
// This file is the Go adaptation of the retry half of
// packages/coding-agent/src/core/agent-session.ts plus the retry policy shared
// with packages/ai/src/utils/retry.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// The upstream session owns a settings-backed retry policy (enabled,
// maxRetries, baseDelayMs, maxAgentDelayMs). The embedded SDK exposes the same
// decisions through RunPolicy.RetryAttempts and RunPolicy.RetryDelay so a
// caller controls the budget explicitly. RetryAttempts counts *extra* requests
// after the first: zero selects the upstream default of three, a negative value
// disables retries. Transient 429/5xx/connection failures back off and retry;
// authentication and invalid-request failures are terminal.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// defaultRetryAttempts is the upstream default retry budget applied when
// RunPolicy.RetryAttempts is zero.
const defaultRetryAttempts = 3

// effectiveRetryAttempts maps the public RunPolicy.RetryAttempts contract onto
// a concrete extra-request budget: zero means the default, negative disables
// retries, and a positive value is used verbatim.
func effectiveRetryAttempts(configured int) int {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return defaultRetryAttempts
	default:
		return configured
	}
}

// retryDelayFor returns the cancellable backoff before an extra request. The
// caller's RunPolicy.RetryDelay is the base; attempts double it with an
// upstream-style cap. A zero base keeps retries immediate.
func (s *AgentSession) retryDelayFor(attempt int) time.Duration {
	s.mu.Lock()
	base := s.policy.RetryDelay
	s.mu.Unlock()
	if base <= 0 {
		return 0
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > aiutils.DefaultMaxAgentRetryDelayMs*time.Millisecond {
			return time.Duration(aiutils.DefaultMaxAgentRetryDelayMs) * time.Millisecond
		}
	}
	return delay
}

// sleepContext sleeps for d but returns early when ctx is cancelled. A zero or
// negative duration still observes an already-cancelled context.
func sleepContext(ctx context.Context, d time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isAbortRequested reports whether Abort was called for the active run.
func (s *AgentSession) isAbortRequested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortRequested
}

// takeMaxTurnsHit consumes the MaxTurns marker once.
func (s *AgentSession) takeMaxTurnsHit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit := s.maxTurnsHit
	s.maxTurnsHit = false
	return hit
}

// lastAssistantMessage returns the newest assistant message in a run result.
func lastAssistantMessage(messages []agenttypes.AgentMessage) (aitypes.AssistantMessage, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if assistant, ok := assistantMessageOf(&messages[index]); ok {
			return assistant, true
		}
	}
	return aitypes.AssistantMessage{}, false
}

// terminalErrorFor turns a final provider failure into a typed error while
// preserving a sanitized diagnostic and never exposing credentials.
func terminalErrorFor(assistant aitypes.AssistantMessage) error {
	message := ""
	if assistant.ErrorMessage != nil {
		message = sanitizeDiagnostic(*assistant.ErrorMessage)
	}
	return &TerminalRequestError{Message: message, StopReason: assistant.StopReason}
}

// emitRetryStart publishes an auto_retry_start event. It runs outside the
// session lock through publish.
func (s *AgentSession) emitRetryStart(attempt, maxAttempts int, assistant aitypes.AssistantMessage) {
	diagnostic := ""
	if assistant.ErrorMessage != nil {
		diagnostic = sanitizeDiagnostic(*assistant.ErrorMessage)
	}
	s.publish(SessionEvent{
		Type: SessionEventAutoRetryStart,
		Text: fmt.Sprintf("attempt=%d/%d error=%s", attempt, maxAttempts, diagnostic),
	})
}

// emitRetryEnd publishes an auto_retry_end event. A nil err means the backoff
// completed and the retried request will start.
func (s *AgentSession) emitRetryEnd(success bool, attempt int, err error) {
	text := fmt.Sprintf("attempt=%d success=%t", attempt, success)
	if err != nil {
		text += " error=" + sanitizeDiagnostic(err.Error())
	}
	s.publish(SessionEvent{Type: SessionEventAutoRetryEnd, Text: text, IsError: err != nil})
}

// secretPatterns redacts values that commonly appear in provider diagnostics.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;"]+`),
	regexp.MustCompile(`(?i)(api[_-]?key["'\s:=]+)[A-Za-z0-9._\-]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9._\-]{6,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
}

// sanitizeDiagnostic removes credential-shaped substrings from an error
// message before it is surfaced through an event or a TerminalRequestError.
func sanitizeDiagnostic(message string) string {
	cleaned := message
	for _, pattern := range secretPatterns {
		cleaned = pattern.ReplaceAllStringFunc(cleaned, func(match string) string {
			// Preserve the label prefix when the pattern captured one.
			if index := strings.Index(match, "="); index >= 0 {
				return match[:index+1] + "[redacted]"
			}
			if index := strings.Index(match, ":"); index >= 0 {
				return match[:index+1] + "[redacted]"
			}
			return "[redacted]"
		})
	}
	return cleaned
}

// IsRetryableAssistantError exposes the shared classifier for callers that
// build their own retry loop. It reports transient overload, rate-limit,
// server, stream and transport failures; context overflow is handled by
// compaction, not retry.
func IsRetryableAssistantError(message aitypes.AssistantMessage) bool {
	return aiutils.IsRetryableAssistantError(message)
}

// IsContextOverflowAssistant reports whether a provider response signals a
// context overflow. It is used by compaction diagnostics and by callers that
// pre-compute a reserve.
func IsContextOverflowAssistant(message aitypes.AssistantMessage, contextWindow *float64) bool {
	return aiutils.IsContextOverflow(message, contextWindow)
}
