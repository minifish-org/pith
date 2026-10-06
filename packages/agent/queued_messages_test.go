package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func identifiedInput(id, text string) agenttypes.AgentMessage {
	message := userMessage(text, 1)
	message.QueueID = id
	return message
}

func pendingTestAgent() *Agent {
	return &Agent{steeringQueue: newPendingMessageQueue(agenttypes.QueueModeOneAtATime), followUpQueue: newPendingMessageQueue(agenttypes.QueueModeOneAtATime)}
}

func queueIDs(messages []agenttypes.AgentMessage) []string {
	ids := make([]string, len(messages))
	for i, message := range messages {
		ids[i] = message.QueueID
	}
	return ids
}

func TestUpdateQueuedMessageIdentityOrderAndPersistence(t *testing.T) {
	a := pendingTestAgent()
	a.Steer(identifiedInput("existing-instruction", "same"))
	for _, id := range []string{"edit", "delete", "promote", "untouched"} {
		a.FollowUp(identifiedInput(id, "same"))
	}
	edited := identifiedInput("ignored-replacement-id", "edited")
	commits := 0
	if ok, err := a.UpdateQueuedMessage("edit", &edited, false, func() error { commits++; return nil }); !ok || err != nil {
		t.Fatalf("edit: %v %v", ok, err)
	}
	if ok, err := a.UpdateQueuedMessage("delete", nil, false, nil); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	promoted := identifiedInput("promote", "same")
	if ok, err := a.UpdateQueuedMessage("promote", &promoted, true, nil); !ok || err != nil {
		t.Fatalf("promote: %v %v", ok, err)
	}
	steer, follow := a.PendingMessages()
	if commits != 1 || !reflect.DeepEqual(queueIDs(steer), []string{"existing-instruction", "promote"}) || !reflect.DeepEqual(queueIDs(follow), []string{"edit", "untouched"}) {
		t.Fatalf("wrong identity/order: %v %v, commits=%d", queueIDs(steer), queueIDs(follow), commits)
	}
	if follow[0].Message.User.Content.Text != "edited" {
		t.Fatal("edit changed the wrong duplicate input")
	}
	wantError := errors.New("journal unavailable")
	if ok, err := a.UpdateQueuedMessage("edit", nil, false, func() error { return wantError }); ok || !errors.Is(err, wantError) {
		t.Fatalf("failed persistence: %v %v", ok, err)
	}
	_, follow = a.PendingMessages()
	if !reflect.DeepEqual(queueIDs(follow), []string{"edit", "untouched"}) {
		t.Fatal("failed persistence changed queue")
	}
	_ = a.followUpQueue.drain()
	if ok, err := a.UpdateQueuedMessage("edit", &edited, true, func() error { t.Error("committed consumed input"); return nil }); ok || err != nil {
		t.Fatalf("consumed input was re-enqueued: %v %v", ok, err)
	}
	raw, err := json.Marshal(edited)
	if err != nil || strings.Contains(string(raw), "ignored-replacement-id") || strings.Contains(string(raw), "QueueID") {
		t.Fatalf("host identity leaked into provider/transcript JSON: %s %v", raw, err)
	}
}

func TestUpdateQueuedMessageRacesDeliveryWithoutReplay(t *testing.T) {
	for _, deleteInput := range []bool{false, true} {
		for i := 0; i < 200; i++ {
			a := pendingTestAgent()
			a.FollowUp(identifiedInput("id", "old"))
			replacement := identifiedInput("id", "new")
			var input *agenttypes.AgentMessage
			if !deleteInput {
				input = &replacement
			}
			var drained []agenttypes.AgentMessage
			var updated bool
			var err error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); drained = a.followUpQueue.drain() }()
			go func() { defer wg.Done(); updated, err = a.UpdateQueuedMessage("id", input, false, nil) }()
			wg.Wait()
			if err != nil || a.HasQueuedMessages() || len(drained) > 1 {
				t.Fatalf("delivery race left/duplicated input: %v %v", drained, err)
			}
			if deleteInput && updated {
				if len(drained) != 0 {
					t.Fatal("deleted input delivered")
				}
			} else {
				want := "old"
				if updated {
					want = "new"
				}
				if len(drained) != 1 || drained[0].Message.User.Content.Text != want {
					t.Fatalf("delivery did not reflect winning mutation: %v, updated=%v", drained, updated)
				}
			}
		}
	}
}

func TestPromotionBetweenFinalQueuePollsIsDeliveredBeforeFollowUps(t *testing.T) {
	for _, otherPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-message", true: "with-ordinary-input"}[otherPending], func(t *testing.T) {
			requests := 0
			streamFn := func(*aitypes.Model, *aitypes.TranscriptContext, *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
				requests++
				stream := aitypes.NewAssistantMessageEventStream()
				message := aitypes.NewAssistantMessage(aitypes.ApiOpenAICompletions, aitypes.ProviderOpenAI, "fixture", 1)
				message.Content = []aitypes.ContentBlock{aitypes.TextBlock("done")}
				message.StopReason = aitypes.StopReasonStop
				stream.Push(aitypes.NewDoneEvent(aitypes.StopReasonStop, message))
				return stream
			}
			a, err := NewAgent(AgentOptions{StreamFn: streamFn})
			if err != nil {
				t.Fatal(err)
			}
			if otherPending {
				a.FollowUp(identifiedInput("ordinary", "ordinary"))
			}
			promoted := identifiedInput("promote", "instruction")
			a.FollowUp(promoted)
			config := a.createLoopConfig(false)
			poll, polls := config.GetSteeringMessages, 0
			config.GetSteeringMessages = func() ([]agenttypes.AgentMessage, error) {
				messages, err := poll()
				polls++
				if polls == 2 {
					// Force the real boundary: steering returned empty, but the
					// fallback follow-up poll has not yet consumed pending input.
					if ok, updateErr := a.UpdateQueuedMessage("promote", &promoted, true, nil); !ok || updateErr != nil {
						t.Fatalf("boundary promotion: %v %v", ok, updateErr)
					}
				}
				return messages, err
			}
			var received []string
			_, err = RunAgentLoop([]agenttypes.AgentMessage{userMessage("start", 1)}, agenttypes.AgentContext{}, config, func(event agenttypes.AgentEvent) error {
				if event.Type == agenttypes.AgentEventMessageEnd && event.Message != nil && event.Message.QueueID != "" {
					received = append(received, event.Message.QueueID)
				}
				return nil
			}, nil, streamFn)
			want := []string{"promote"}
			if otherPending {
				want = append(want, "ordinary")
			}
			if err != nil || !reflect.DeepEqual(received, want) || requests != len(want)+1 || a.HasQueuedMessages() {
				t.Fatalf("late promotion was lost or reordered: %v, requests=%d, err=%v", received, requests, err)
			}
		})
	}
}
