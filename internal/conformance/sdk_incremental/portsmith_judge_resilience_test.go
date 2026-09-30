package sdk_incremental_test

import (
	"context"
	"encoding/json"
	at "github.com/minifish-org/pith/packages/agent/types"
	ai "github.com/minifish-org/pith/packages/ai/types"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"strings"
	"testing"
	"time"
)

func TestPortsmithJudgeSDKResilience(t *testing.T) {
	calls := 0
	executed := 0
	reg, _ := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{{Name: "danger", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (sdk.ToolResult, error) {
		executed++
		return sdk.ToolResult{}, nil
	}}}, []string{"danger"}, nil, sdk.ToolHooks{})
	fn := at.StreamFn(func(*ai.Model, *ai.TranscriptContext, *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls++
		if calls == 1 {
			call := ai.NewToolCall("cut", "danger", json.RawMessage(`{}`))
			return response(ai.StopReasonLength, ai.ContentBlock{Type: ai.ContentTypeToolCall, ToolCall: &call})
		}
		if calls == 2 {
			return response(ai.StopReasonError)
		}
		return response(ai.StopReasonStop, ai.TextBlock("recovered"))
	})
	s, err := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Tools: reg, Model: sdk.ModelOptions{Model: model(), StreamFn: fn}, Policy: sdk.RunPolicy{RetryAttempts: 2, RetryDelay: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := s.Prompt(ctx, "start")
	if err != nil || r.StopReason != ai.StopReasonStop || calls != 3 || executed != 0 {
		t.Fatalf("truncation/retry: %v calls=%d executed=%d", err, calls, executed)
	}
}
func TestPortsmithJudgeSDKCancellation(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	fn := at.StreamFn(func(m *ai.Model, c *ai.TranscriptContext, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		close(entered)
		go func() {
			select {
			case <-o.Signal:
				close(cancelled)
				msg := ai.NewAssistantMessage(m.Api, m.Provider, m.Id, 1)
				stream.Push(ai.NewErrorEvent(ai.StopReasonAborted, msg))
			case <-time.After(2 * time.Second):
				msg := ai.NewAssistantMessage(m.Api, m.Provider, m.Id, 1)
				stream.End(&msg)
			}
		}()
		return stream
	})
	s, err := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: model(), StreamFn: fn}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { _, e := s.Prompt(context.Background(), "wait"); done <- e }()
	select {
	case <-entered:
	case <-done:
		t.Fatal("prompt did not invoke stream")
	case <-time.After(time.Second):
		t.Fatal("provider not started")
	}
	if _, err = s.Prompt(context.Background(), "concurrent"); err == nil {
		t.Fatal("concurrent Prompt accepted")
	}
	s.Abort()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("abort swallowed")
		}
	case <-time.After(time.Second):
		t.Fatal("abort did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("provider not cancelled")
	}
}
func TestPortsmithJudgeSDKCompaction(t *testing.T) {
	summaries := 0
	requests := 0
	fn := at.StreamFn(func(m *ai.Model, c *ai.TranscriptContext, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		requests++
		return response(ai.StopReasonStop, ai.TextBlock("answer "+strings.Repeat("x", 300)))
	})
	small := model()
	small.ContextWindow = 256
	small.MaxTokens = 32
	s, err := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: small, StreamFn: fn}, Policy: sdk.RunPolicy{CompactReserveTokens: 64, KeepRecentMessages: 2, Summarize: func(ctx context.Context, m []at.AgentMessage) (string, error) {
		summaries++
		if len(m) == 0 {
			t.Error("empty summary input")
		}
		return "SUMMARY-SENTINEL", nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 4; i++ {
		if _, err = s.Prompt(ctx, strings.Repeat("long ", 90)); err != nil {
			t.Fatal(err)
		}
	}
	if summaries == 0 {
		t.Fatal("automatic compaction absent")
	}
	if err = s.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.Messages())
	if !strings.Contains(string(b), "SUMMARY-SENTINEL") {
		t.Fatal("summary not in transcript")
	}
}

func TestPortsmithJudgeSDKQueues(t *testing.T) {
	var session *sdk.AgentSession
	calls := 0
	seenSteer := false
	seenFollow := false
	fn := at.StreamFn(func(m *ai.Model, c *ai.TranscriptContext, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls++
		if calls > 6 {
			t.Error("queue never drained")
			return response(ai.StopReasonError)
		}
		b, _ := json.Marshal(c)
		text := string(b)
		seenSteer = seenSteer || strings.Contains(text, "steering-sentinel")
		seenFollow = seenFollow || strings.Contains(text, "followup-sentinel")
		if calls == 1 {
			if e := session.Steer("steering-sentinel"); e != nil {
				t.Error(e)
			}
			if e := session.FollowUp("followup-sentinel"); e != nil {
				t.Error(e)
			}
		}
		return response(ai.StopReasonStop, ai.TextBlock("done"))
	})
	var err error
	session, err = sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: model(), StreamFn: fn}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = session.Prompt(ctx, "start"); err != nil {
		t.Fatal(err)
	}
	if !seenSteer || !seenFollow {
		t.Fatal("queued messages not delivered before completion")
	}
}
func TestPortsmithJudgeSDKRetryExhaustion(t *testing.T) {
	calls := 0
	fn := at.StreamFn(func(*ai.Model, *ai.TranscriptContext, *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls++
		return response(ai.StopReasonError)
	})
	s, e := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: model(), StreamFn: fn}, Policy: sdk.RunPolicy{RetryAttempts: 2, RetryDelay: time.Millisecond}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, e = s.Prompt(ctx, "start"); e == nil || calls != 3 {
		t.Fatalf("retry exhaustion: calls %d error %v", calls, e)
	}
}
func TestPortsmithJudgeSDKCompactionRollback(t *testing.T) {
	fn := at.StreamFn(func(*ai.Model, *ai.TranscriptContext, *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		return response(ai.StopReasonStop, ai.TextBlock("keep history"))
	})
	s, e := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: model(), StreamFn: fn}, Policy: sdk.RunPolicy{KeepRecentMessages: 1, Summarize: func(context.Context, []at.AgentMessage) (string, error) { return "", context.Canceled }}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if _, e = s.Prompt(context.Background(), "user history"); e != nil {
			t.Fatal(e)
		}
	}
	before, _ := json.Marshal(s.Messages())
	if e = s.Compact(context.Background()); e == nil {
		t.Fatal("summary failure hidden")
	}
	after, _ := json.Marshal(s.Messages())
	if string(before) != string(after) {
		t.Fatal("failed compaction damaged history")
	}
}
