package codingagent

import (
	"errors"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// ErrPendingMessageNotFound means input is no longer pending. Never re-enqueue
// it on this error: it may already be on its way into the current transcript.
var ErrPendingMessageNotFound = errors.New("pending message has already been received or removed")

// PendingMessageUpdate edits, promotes or deletes one QueueID. Mode is "steer"
// or "followUp" for replacements; Text and Options replace the pending input.
// Options expand skills/templates exactly as Steer and FollowUp do.
type PendingMessageUpdate struct {
	Text    string
	Mode    string
	Options PromptOptions
	Delete  bool
	// BeforeCommit persists the host's change before delivery. It must not
	// re-enter the session or agent. Failure leaves the pending input intact.
	BeforeCommit func() error
}

// UpdatePendingMessage changes only input that is still queued. Delivery and
// mutation are serialized, including any host persistence callback. This is a
// native host extension; Pi's terminal API only exposes whole-queue removal.
func (s *AgentSession) UpdatePendingMessage(id string, update PendingMessageUpdate) error {
	if id == "" {
		return errors.New("pending message ID is required")
	}
	var replacement *agenttypes.AgentMessage
	if !update.Delete {
		if update.Mode != "steer" && update.Mode != "followUp" {
			return errors.New("pending message mode must be steer or followUp")
		}
		if update.Options.StreamingBehavior != "" {
			return errors.New("streaming behavior is only valid with Prompt")
		}
		text, images, err := s.preparePromptInput(update.Text, update.Options)
		if err != nil {
			return err
		}
		message := userAgentMessageWithImages(text, images)
		replacement = &message
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.agent == nil {
		return ErrAgentSessionClosed
	}
	updated, err := s.agent.UpdateQueuedMessage(id, replacement, update.Mode == "steer", update.BeforeCommit)
	if err != nil {
		return err
	}
	if !updated {
		return ErrPendingMessageNotFound
	}
	return nil
}
