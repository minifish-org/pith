package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/minifish-org/pith/ai"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func p06Wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("blocked")
	}
}
func TestPortsmithJudgeP06_01(t *testing.T) {
	base := []ai.Message{{Role: "user", Content: []ai.Content{{Type: "text", Text: "baseline"}}}}
	a := New(Options{SystemPrompt: "system", Messages: base, Loop: LoopConfig{Stream: p05Stream(p05Response(nil, "stop"))}})
	base[0].Content[0].Text = "mutated"
	if e := a.Prompt(p05Ctx(t), "new"); e != nil {
		t.Fatal(e)
	}
	if e := a.Reset(); e != nil {
		t.Fatal(e)
	}
	s := a.State()
	if len(s.Messages) != 2 || s.Messages[0].Role != "system" || s.Messages[1].Content[0].Text != "baseline" {
		t.Fatal(s)
	}
}
func TestPortsmithJudgeP06_02(t *testing.T) {
	started := make(chan struct{})
	a := New(Options{Loop: LoopConfig{Stream: func(ctx context.Context, _ ai.Context) (<-chan ai.Event, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}})
	done := make(chan struct{})
	go func() { defer close(done); a.Prompt(context.Background(), "x") }()
	p06Wait(t, started)
	before := a.State()
	if a.Prompt(p05Ctx(t), "y") == nil || a.Continue(p05Ctx(t)) == nil || a.Reset() == nil {
		t.Fatal("busy accepted")
	}
	if len(a.State().Messages) != len(before.Messages) {
		t.Fatal("busy mutated state")
	}
	a.Abort()
	p06Wait(t, done)
	if a.State().IsStreaming {
		t.Fatal("still busy")
	}
}
func TestPortsmithJudgeP06_03(t *testing.T) {
	for _, f := range []ai.StreamFn{p05Stream(p05Response(nil, "stop")), func(context.Context, ai.Context) (<-chan ai.Event, error) { return nil, errors.New("provider") }} {
		a := New(Options{Loop: LoopConfig{Stream: f}})
		a.Prompt(p05Ctx(t), "x")
		if s := a.State(); s.IsStreaming || len(s.PendingTools) > 0 {
			t.Fatal(s)
		}
		if e := a.WaitForIdle(p05Ctx(t)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestPortsmithJudgeP06_04(t *testing.T) {
	a := New(Options{Loop: LoopConfig{Stream: p05Stream(p05Response(nil, "stop"))}})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unsub := a.Subscribe(func(_ context.Context, ev Event) error {
		if ev.Type == "agent_end" {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	})
	go func() {
		defer close(done)
		if e := a.Prompt(context.Background(), "x"); e != nil {
			t.Error(e)
		}
	}()
	p06Wait(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if a.WaitForIdle(ctx) == nil {
		t.Fatal("idle before subscriber")
	}
	select {
	case <-done:
		t.Fatal("prompt returned early")
	default:
	}
	close(release)
	p06Wait(t, done)
	unsub()
	var late func(ToolResult)
	var updates atomic.Int32
	a = New(Options{Loop: LoopConfig{Stream: p05Stream(p05Response([]ai.Content{p05Call("x", "echo", `{"value":"x"}`)}, "toolUse"), p05Response(nil, "stop")), Tools: []Tool{p05Tool(func(_ context.Context, _ json.RawMessage, update func(ToolResult)) (ToolResult, error) {
		late = update
		return ToolResult{}, nil
	})}}})
	a.Subscribe(func(_ context.Context, e Event) error {
		if e.Type == "tool_execution_update" {
			updates.Add(1)
		}
		return nil
	})
	if e := a.Prompt(p05Ctx(t), "x"); e != nil {
		t.Fatal(e)
	}
	if late == nil {
		t.Fatal("no updater")
	}
	late(ToolResult{})
	if updates.Load() != 0 {
		t.Fatal("late update")
	}
}
func TestPortsmithJudgeP06_05(t *testing.T) {
	a := New(Options{Loop: LoopConfig{Stream: p05Stream(p05Response([]ai.Content{{Type: "text", Text: "ok"}}, "stop"))}})
	a.Subscribe(func(_ context.Context, _ Event) error { _ = a.State(); return nil })
	if e := a.Prompt(p05Ctx(t), "x"); e != nil {
		t.Fatal(e)
	}
	s := a.State()
	s.Messages[0].Content[0].Text = "corrupt"
	if a.State().Messages[0].Content[0].Text == "corrupt" {
		t.Fatal("aliased state")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.State(); a.Abort() }()
	}
	wg.Wait()
}
