package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

const promptTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1kAAAAASUVORK5CYII="

func assertUserImage(t *testing.T, messages []agenttypes.AgentMessage, text, data string) {
	t.Helper()
	count := 0
	for _, message := range messages {
		if message.Message == nil || message.Message.User == nil {
			continue
		}
		blocks := message.Message.User.Content.Blocks
		if len(blocks) == 0 || blocks[0].Text == nil || blocks[0].Text.Text != text {
			continue
		}
		count++
		if len(blocks) != 2 || blocks[1].Image == nil || blocks[1].Image.Data != data || blocks[1].Image.MimeType != "image/png" {
			t.Fatalf("image content changed: %+v", blocks)
		}
	}
	if count != 1 {
		t.Fatalf("user input %q count = %d, want 1", text, count)
	}
}

// Exercise the real native adapter, transient retry and durable reopen. A DTO
// or fake-stream-only test cannot prove that images reach the provider wire.
func TestPromptOptionsImagesNativeRetryAndReopen(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-only-key" {
			t.Errorf("unexpected request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, body)
		count := len(requests)
		mu.Unlock()
		if count == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"503 try again"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"one pixel\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	manager, dir := sessionManager(t)
	model := sessionTestModel()
	model.BaseUrl = server.URL + "/v1"
	model.Input = []aitypes.ModelInputModality{aitypes.ModelInputText, aitypes.ModelInputImage}
	options := SessionOptions{
		Cwd: dir, Manager: manager,
		Model:  ModelOptions{Model: model, APIKey: func(context.Context, string) (string, error) { return "test-only-key", nil }},
		Policy: RunPolicy{RetryAttempts: 1, RetryDelay: time.Millisecond},
	}
	session, err := CreateAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close(); manager.Close() })
	images := []aitypes.ImageContent{aitypes.NewImageContent(promptTestPNG, "image/png")}
	result, err := session.Prompt(context.Background(), "Describe this image", PromptOptions{Images: images})
	if err != nil || result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("prompt: %s %v", result.StopReason, err)
	}
	images[0].Data = "changed by caller"
	assertUserImage(t, session.Messages(), "Describe this image", promptTestPNG)
	// Transcript snapshots also must not share image content with the session.
	snapshot := session.Messages()
	for i := range snapshot {
		if user := snapshot[i].Message; user != nil && user.User != nil {
			user.User.Content.Blocks[1].Image.Data = "changed snapshot"
		}
	}
	assertUserImage(t, session.Messages(), "Describe this image", promptTestPNG)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSession(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	options.Manager = reopened
	restored, err := CreateAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertUserImage(t, restored.Messages(), "Describe this image", promptTestPNG)
	if _, err := restored.Prompt(context.Background(), "What color is it?"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want initial + retry + reopened turn", len(requests))
	}
	for i, body := range requests {
		data, _ := json.Marshal(body)
		if strings.Count(string(data), "data:image/png;base64,"+promptTestPNG) != 1 {
			t.Errorf("request %d lost or duplicated image", i)
		}
		if strings.Count(string(data), "Describe this image") != 1 {
			t.Errorf("request %d duplicated prompt on retry/reopen", i)
		}
	}
}

func TestPromptOptionsDefaultExpansionAndLiteralOptOut(t *testing.T) {
	dir := t.TempDir()
	templateFile := filepath.Join(dir, "review.md")
	skillDir := filepath.Join(dir, "inspect")
	if err := os.Mkdir(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(templateFile, []byte("Review $1 with $ARGUMENTS"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("---\nname: inspect\ndescription: inspect files\n---\nRead the selected files."), 0600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	session, err := CreateAgentSession(SessionOptions{
		Cwd:       dir,
		Resources: ResourceOptions{TemplatePaths: []string{templateFile}, SkillPaths: []string{skillDir}},
		Model: ModelOptions{Model: sessionTestModel(), StreamFn: func(_ *aitypes.Model, transcript *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
			last := transcript.Messages[len(transcript.Messages)-1]
			if last.User == nil {
				t.Error("last message is not user")
			} else {
				seen = append(seen, last.User.Content.Blocks[0].Text.Text)
			}
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("ok"))
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx := context.Background()
	if _, err := session.Prompt(ctx, "/review literal"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, `/review "two words"`, PromptOptions{}); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := session.Prompt(ctx, "/review unchanged", PromptOptions{ExpandPromptTemplates: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, "/skill:inspect now", PromptOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, "/unknown keep me", PromptOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 5 || !strings.Contains(seen[0], "Review literal with literal") || !strings.Contains(seen[1], "Review two words with two words") || !strings.Contains(seen[2], "/review unchanged") || !strings.Contains(seen[3], "<skill") || !strings.Contains(seen[3], "Read the selected files.") || !strings.Contains(seen[4], "/unknown keep me") {
		t.Fatalf("expansion = %v", seen)
	}
	// Re-read the selected skill, as Pi does, and surface failure before a call.
	if err := os.Remove(skillFile); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, "/skill:inspect", PromptOptions{}); err == nil {
		t.Fatal("missing skill file silently accepted")
	}
	if len(seen) != 5 {
		t.Fatal("failed expansion made a provider request")
	}
}

func TestPromptOptionsImageQueues(t *testing.T) {
	for _, behavior := range []string{"steer", "followUp"} {
		for _, viaPrompt := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/prompt=%v", behavior, viaPrompt), func(t *testing.T) {
				var session *AgentSession
				registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{sessionTool("queue", func(context.Context, json.RawMessage) (ToolResult, error) {
					images := []aitypes.ImageContent{aitypes.NewImageContent(promptTestPNG, "image/png")}
					options := PromptOptions{Images: images}
					var err error
					if viaPrompt {
						options.StreamingBehavior = behavior
						_, err = session.Prompt(context.Background(), "queued picture", options)
					} else if behavior == "steer" {
						err = session.Steer("queued picture", options)
					} else {
						err = session.FollowUp("queued picture", options)
					}
					images[0].Data = "caller mutation"
					return ToolResult{}, err
				})}, []string{"queue"}, nil, ToolHooks{})
				if err != nil {
					t.Fatal(err)
				}
				requests := 0
				session, err = CreateAgentSession(SessionOptions{
					Cwd: t.TempDir(), Tools: registry,
					Model: ModelOptions{Model: sessionTestModel(), StreamFn: func(_ *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
						requests++
						if requests == 1 {
							return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("queue-call", "queue", `{}`))
						}
						return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				result, err := session.Prompt(context.Background(), "start")
				if err != nil {
					t.Fatal(err)
				}
				wantRequests := 2
				if behavior == "followUp" {
					wantRequests = 3
				}
				if requests != wantRequests {
					t.Fatalf("requests = %d, want %d", requests, wantRequests)
				}
				assertUserImage(t, session.Messages(), "queued picture", promptTestPNG)
				assertUserImage(t, result.Messages, "queued picture", promptTestPNG)
				for _, message := range result.Messages {
					if message.Message == nil || message.Message.User == nil {
						continue
					}
					for _, block := range message.Message.User.Content.Blocks {
						if block.Image != nil {
							block.Image.Data = "changed result"
						}
					}
				}
				assertUserImage(t, session.Messages(), "queued picture", promptTestPNG)
			})
		}
	}
}

func TestPromptOptionsValidationAndBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	session, err := CreateAgentSession(SessionOptions{Cwd: t.TempDir(), Model: ModelOptions{Model: sessionTestModel(), StreamFn: func(_ *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		close(started)
		<-release
		return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Prompt(ctx, "cancelled", PromptOptions{StreamingBehavior: "steer"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if _, err := session.Prompt(context.Background(), "invalid", PromptOptions{StreamingBehavior: "typo"}); err == nil {
		t.Fatal("invalid behavior accepted")
	}
	if _, err := session.Prompt(context.Background(), "ambiguous", PromptOptions{}, PromptOptions{}); err == nil {
		t.Fatal("multiple options values accepted")
	}
	if err := session.Steer("ambiguous", PromptOptions{}, PromptOptions{}); err == nil {
		t.Fatal("multiple steering options accepted")
	}
	done := make(chan error, 1)
	go func() { _, err := session.Prompt(context.Background(), "running"); done <- err }()
	<-started
	_, busyErr := session.Prompt(context.Background(), "busy", PromptOptions{})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(busyErr, ErrAgentSessionBusy) {
		t.Fatalf("busy: %v", busyErr)
	}
	if len(session.Messages()) != 2 {
		t.Fatal("invalid/cancelled/busy input changed transcript")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(context.Background(), "closed", PromptOptions{}); !errors.Is(err, ErrAgentSessionClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestPromptOptionsPendingImagesSurviveRebuild(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%v", retry), func(t *testing.T) {
			var session *AgentSession
			queue := func() {
				for _, name := range []string{"first", "second"} {
					options := PromptOptions{Images: []aitypes.ImageContent{aitypes.NewImageContent(promptTestPNG, "image/png")}}
					if err := session.Steer("steer "+name, options); err != nil {
						t.Error(err)
					}
					if err := session.FollowUp("follow "+name, options); err != nil {
						t.Error(err)
					}
				}
			}
			requests := 0
			stream := func(_ *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
				requests++
				if retry && requests == 1 {
					queue()
					return sessionDone(aitypes.StopReasonError)
				}
				return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
			}
			var err error
			session, err = CreateAgentSession(SessionOptions{
				Cwd: t.TempDir(), Model: ModelOptions{Model: sessionTestModel(), StreamFn: stream},
				Policy: RunPolicy{RetryAttempts: 1, RetryDelay: time.Millisecond},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if !retry {
				queue()
				model := sessionTestModel()
				model.Id = "replacement-model"
				if err := session.SetModel(ModelOptions{Model: model}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := session.Prompt(context.Background(), "original"); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"steer first", "steer second", "follow first", "follow second"} {
				assertUserImage(t, session.Messages(), name, promptTestPNG)
			}
			var order []string
			for _, message := range session.Messages() {
				if message.Message != nil && message.Message.User != nil {
					user := message.Message.User
					if len(user.Content.Blocks) > 0 {
						order = append(order, user.Content.Blocks[0].Text.Text)
					}
				}
			}
			if strings.Join(order, ",") != "original,steer first,steer second,follow first,follow second" {
				t.Fatalf("pending queue order = %v", order)
			}
		})
	}
}

func TestPromptOptionsCompactionPreservesQueuesAndRejectsMutations(t *testing.T) {
	var session *AgentSession
	summaries := 0
	var err error
	session, err = CreateAgentSession(SessionOptions{
		Cwd: t.TempDir(),
		Model: ModelOptions{Model: sessionTestModel(), StreamFn: func(_ *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
		}},
		Policy: RunPolicy{KeepRecentMessages: 1, Summarize: func(ctx context.Context, _ []agenttypes.AgentMessage) (string, error) {
			summaries++
			if _, err := session.Prompt(ctx, "during compact", PromptOptions{StreamingBehavior: "steer"}); !errors.Is(err, ErrAgentSessionBusy) {
				t.Errorf("prompt during compaction = %v", err)
			}
			if err := session.Steer("during compact"); !errors.Is(err, ErrAgentSessionBusy) {
				t.Errorf("steer during compaction = %v", err)
			}
			if err := session.SetModel(ModelOptions{Model: sessionTestModel()}); !errors.Is(err, ErrAgentSessionBusy) {
				t.Errorf("model mutation during compaction = %v", err)
			}
			if err := session.SetActiveTools(nil); !errors.Is(err, ErrAgentSessionBusy) {
				t.Errorf("tool mutation during compaction = %v", err)
			}
			return "compacted history", nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := session.Prompt(ctx, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.FollowUp("after compaction", PromptOptions{Images: []aitypes.ImageContent{aitypes.NewImageContent(promptTestPNG, "image/png")}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	if summaries != 1 {
		t.Fatalf("summaries = %d", summaries)
	}
	if _, err := session.Prompt(ctx, "resume"); err != nil {
		t.Fatal(err)
	}
	assertUserImage(t, session.Messages(), "after compaction", promptTestPNG)
}
