package agent

import (
	"errors"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// drainNextQueue selects at the final/continuation boundary under the same
// locks as promotion, so moving the last follow-up cannot make the loop stop
// with an instruction still pending. The bool identifies the selected queue.
func (a *Agent) drainNextQueue() ([]agenttypes.AgentMessage, bool) {
	a.steeringQueue.mu.Lock()
	defer a.steeringQueue.mu.Unlock()
	a.followUpQueue.mu.Lock()
	defer a.followUpQueue.mu.Unlock()
	if len(a.steeringQueue.messages) > 0 {
		return a.steeringQueue.drainLocked(), true
	}
	return a.followUpQueue.drainLocked(), false
}

// UpdateQueuedMessage atomically replaces, moves or deletes host-identified
// input that has not been drained by the agent loop. A nil replacement deletes
// it; otherwise steer selects the destination queue. Moving queues appends to
// the destination, while an edit in the same queue preserves its position.
//
// beforeCommit, if provided, persists the change before it becomes deliverable.
// It runs only when the ID is still pending; an error leaves both queues intact.
// It must not call back into Agent or AgentSession, and should finish promptly.
// A false result means the message has already been drained or does not exist.
// This transaction is a native Go host API, beyond Pi's whole-queue controls.
func (a *Agent) UpdateQueuedMessage(id string, replacement *agenttypes.AgentMessage, steer bool, beforeCommit func() error) (bool, error) {
	if id == "" {
		return false, errors.New("pending message ID is required")
	}
	// Always lock in this order. Delivery locks only its individual queue.
	a.steeringQueue.mu.Lock()
	defer a.steeringQueue.mu.Unlock()
	a.followUpQueue.mu.Lock()
	defer a.followUpQueue.mu.Unlock()
	for _, source := range []*pendingMessageQueue{a.steeringQueue, a.followUpQueue} {
		for index, message := range source.messages {
			if message.QueueID != id {
				continue
			}
			if beforeCommit != nil {
				if err := beforeCommit(); err != nil {
					return false, err
				}
			}
			target := a.followUpQueue
			if steer {
				target = a.steeringQueue
			}
			if replacement != nil {
				updated := *replacement
				updated.QueueID = id
				if source == target {
					source.messages[index] = updated
					return true, nil
				}
				target.messages = append(target.messages, updated)
			}
			copy(source.messages[index:], source.messages[index+1:])
			source.messages[len(source.messages)-1] = agenttypes.AgentMessage{}
			source.messages = source.messages[:len(source.messages)-1]
			return true, nil
		}
	}
	return false, nil
}
