package codingagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestUpdatePendingMessageExpansionImagesRebuildAndEvents(t *testing.T) {
	dir := t.TempDir()
	template := filepath.Join(dir, "review.md")
	if err := os.WriteFile(template, []byte("Review $ARGUMENTS"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := CreateAgentSession(SessionOptions{
		Cwd: dir, Resources: ResourceOptions{TemplatePaths: []string{template}},
		Model: ModelOptions{Model: sessionTestModel(), StreamFn: func(*aitypes.Model, *aitypes.TranscriptContext, *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	image := aitypes.NewImageContent(promptTestPNG, "image/png")
	for _, id := range []string{"edit", "delete", "promote"} {
		if err := s.FollowUp("same", PromptOptions{QueueID: id, Images: []aitypes.ImageContent{image}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpdatePendingMessage("edit", PendingMessageUpdate{Text: "/review images", Mode: "followUp", Options: PromptOptions{Images: []aitypes.ImageContent{image}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePendingMessage("delete", PendingMessageUpdate{Delete: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePendingMessage("promote", PendingMessageUpdate{Text: "instruction", Mode: "steer", Options: PromptOptions{Images: []aitypes.ImageContent{image}}}); err != nil {
		t.Fatal(err)
	}
	model := sessionTestModel()
	model.Id = "changed"
	if err := s.SetModel(ModelOptions{Model: model}); err != nil {
		t.Fatal(err)
	}
	// Rebuilt agents must remain addressable by the same native IDs.
	if err := s.UpdatePendingMessage("edit", PendingMessageUpdate{Text: "/review revised images", Mode: "followUp", Options: PromptOptions{Images: []aitypes.ImageContent{image}}}); err != nil {
		t.Fatal(err)
	}
	var received []string
	s.Subscribe(func(event SessionEvent) {
		if event.Type == SessionEventMessageEnd && event.Message != nil && event.Message.QueueID != "" {
			received = append(received, event.Message.QueueID)
		}
	})
	if _, err := s.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	assertUserImage(t, s.Messages(), "instruction", promptTestPNG)
	assertUserImage(t, s.Messages(), "Review revised images", promptTestPNG)
	if !reflect.DeepEqual(received, []string{"promote", "edit"}) {
		t.Fatalf("received IDs: %v", received)
	}
	if err := s.UpdatePendingMessage("edit", PendingMessageUpdate{Delete: true, BeforeCommit: func() error { t.Error("persisted consumed input"); return nil }}); !errors.Is(err, ErrPendingMessageNotFound) {
		t.Fatalf("late edit: %v", err)
	}
	if err := s.UpdatePendingMessage("missing", PendingMessageUpdate{Mode: "invalid"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePendingMessage("missing", PendingMessageUpdate{Delete: true}); !errors.Is(err, ErrAgentSessionClosed) {
		t.Fatalf("closed session: %v", err)
	}
}
