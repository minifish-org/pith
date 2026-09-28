package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/minifish-org/pith/ai"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func p05User() []ai.Message {
	return []ai.Message{{Role: "user", Content: []ai.Content{{Type: "text", Text: "echo"}}}}
}
func p05Call(id, name, args string) ai.Content {
	return ai.Content{Type: "toolCall", ID: id, Name: name, Arguments: json.RawMessage(args)}
}
func p05Response(content []ai.Content, stop string) ai.Message {
	return ai.Message{Role: "assistant", Content: content, StopReason: stop}
}
func p05Stream(messages ...ai.Message) ai.StreamFn {
	n := 0
	return func(ctx context.Context, c ai.Context) (<-chan ai.Event, error) {
		if n >= len(messages) {
			return nil, errors.New("unexpected model call")
		}
		m := messages[n]
		n++
		ch := make(chan ai.Event, 1)
		ch <- ai.Event{Type: "done", Message: &m}
		close(ch)
		return ch, nil
	}
}
func p05Tool(exec func(context.Context, json.RawMessage, func(ToolResult)) (ToolResult, error)) Tool {
	return Tool{Declaration: ai.ToolDeclaration{Name: "echo", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)}, Execute: exec}
}
func p05Ctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func TestPortsmithJudgeP05_01(t *testing.T) {
	var expected []string
	b, e := os.ReadFile("testdata/p05-ts.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &expected); e != nil {
		t.Fatal(e)
	}
	trace := []string{}
	calls := 0
	cfg := LoopConfig{Stream: p05Stream(p05Response([]ai.Content{p05Call("call-1", "echo", `{"value":"hello"}`)}, "toolUse"), p05Response([]ai.Content{{Type: "text", Text: "done"}}, "stop")), Tools: []Tool{p05Tool(func(_ context.Context, args json.RawMessage, _ func(ToolResult)) (ToolResult, error) {
		calls++
		return ToolResult{Content: []ai.Content{{Type: "text", Text: "echoed: hello"}}}, nil
	})}}
	out, e := Run(p05Ctx(t), p05User(), cfg, func(ev Event) { trace = append(trace, ev.Type) })
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if !reflect.DeepEqual(trace, expected) {
		t.Fatalf("events %v != TS %v", trace, expected)
	}
	roles := []string{}
	for _, m := range out {
		if m.Role != "system" {
			roles = append(roles, m.Role)
		}
	}
	if !reflect.DeepEqual(roles, []string{"user", "assistant", "toolResult", "assistant"}) {
		t.Fatal(roles)
	}
}
func TestPortsmithJudgeP05_02(t *testing.T) {
	for _, kind := range []string{"unknown", "invalid", "error", "panic"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			name, args := "echo", `{"value":"x"}`
			if kind == "unknown" {
				name = "missing"
			}
			if kind == "invalid" {
				args = `{}`
			}
			cfg := LoopConfig{Stream: p05Stream(p05Response([]ai.Content{p05Call("1", name, args)}, "toolUse"), p05Response(nil, "stop")), Tools: []Tool{p05Tool(func(context.Context, json.RawMessage, func(ToolResult)) (ToolResult, error) {
				calls++
				if kind == "panic" {
					panic("fixture")
				}
				return ToolResult{}, errors.New("fixture")
			})}}
			out, e := Run(p05Ctx(t), p05User(), cfg, func(Event) {})
			if e != nil {
				t.Fatal(e)
			}
			results := 0
			for _, m := range out {
				if m.Role == "toolResult" {
					results++
					if !m.IsError || m.ToolCallID != "1" {
						t.Fatal(m)
					}
				}
			}
			if results != 1 {
				t.Fatal("missing result")
			}
			if (kind == "unknown" || kind == "invalid") && calls != 0 {
				t.Fatal("invalid executed")
			}
		})
	}
}
func TestPortsmithJudgeP05_03(t *testing.T) {
	var active atomic.Int32
	order := []string{}
	tool := p05Tool(func(_ context.Context, args json.RawMessage, _ func(ToolResult)) (ToolResult, error) {
		if active.Add(1) != 1 {
			t.Error("parallel")
		}
		defer active.Add(-1)
		var m map[string]string
		json.Unmarshal(args, &m)
		order = append(order, m["value"])
		return ToolResult{Content: []ai.Content{{Type: "text", Text: m["value"]}}}, nil
	})
	cfg := LoopConfig{Tools: []Tool{tool}, Stream: p05Stream(p05Response([]ai.Content{p05Call("a", "echo", `{"value":"A"}`), p05Call("b", "echo", `{"value":"B"}`)}, "toolUse"), p05Response(nil, "stop"))}
	out, e := Run(p05Ctx(t), p05User(), cfg, func(Event) {})
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for _, m := range out {
		if m.Role == "toolResult" {
			ids = append(ids, m.ToolCallID)
		}
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) || !reflect.DeepEqual(order, []string{"A", "B"}) {
		t.Fatal(ids, order)
	}
}
func TestPortsmithJudgeP05_04(t *testing.T) {
	calls := 0
	cfg := LoopConfig{Stream: p05Stream(p05Response([]ai.Content{p05Call("x", "echo", `{"value":"valid"}`)}, "length")), Tools: []Tool{p05Tool(func(context.Context, json.RawMessage, func(ToolResult)) (ToolResult, error) {
		calls++
		return ToolResult{}, nil
	})}}
	out, _ := Run(p05Ctx(t), p05User(), cfg, func(Event) {})
	if calls != 0 {
		t.Fatal("truncated tool executed")
	}
	for _, m := range out {
		if m.Role == "toolResult" && m.ToolCallID == "x" && m.IsError {
			return
		}
	}
	t.Fatal("missing truncated result")
}
func TestPortsmithJudgeP05_05(t *testing.T) {
	for _, reason := range []string{"error", "aborted"} {
		ends, calls := 0, 0
		cfg := LoopConfig{Stream: p05Stream(p05Response([]ai.Content{p05Call("x", "echo", `{"value":"x"}`)}, reason)), Tools: []Tool{p05Tool(func(context.Context, json.RawMessage, func(ToolResult)) (ToolResult, error) {
			calls++
			return ToolResult{}, nil
		})}}
		_, e := Run(p05Ctx(t), p05User(), cfg, func(ev Event) {
			if ev.Type == "agent_end" {
				ends++
			}
		})
		if e == nil || calls != 0 || ends != 1 {
			t.Fatal(reason, e, calls, ends)
		}
	}
}
func TestPortsmithJudgeP05_06(t *testing.T) {
	cfg := LoopConfig{Stream: p05Stream(p05Response(nil, "stop"))}
	if _, e := Continue(p05Ctx(t), nil, cfg, func(Event) {}); e == nil {
		t.Fatal("empty continued")
	}
	if _, e := Continue(p05Ctx(t), []ai.Message{{Role: "assistant"}}, cfg, func(Event) {}); e == nil {
		t.Fatal("assistant continued")
	}
	order := ""
	cfg.TransformContext = func(_ context.Context, m []ai.Message) ([]ai.Message, error) { order += "T"; return m, nil }
	cfg.ConvertToLLM = func(m []ai.Message) []ai.Message { order += "C"; return m }
	if _, e := Run(p05Ctx(t), p05User(), cfg, func(Event) {}); e != nil || order != "TC" {
		t.Fatal(e, order)
	}
}
func TestPortsmithJudgeP05_07(t *testing.T) {
	calls := 0
	cfg := LoopConfig{Tools: []Tool{p05Tool(func(context.Context, json.RawMessage, func(ToolResult)) (ToolResult, error) { return ToolResult{}, nil })}, Stream: func(context.Context, ai.Context) (<-chan ai.Event, error) {
		calls++
		return p05Stream(p05Response([]ai.Content{p05Call("x", "echo", `{"value":"x"}`)}, "toolUse"))(context.Background(), ai.Context{})
	}, FinishTurn: func(m []ai.Message) bool {
		if m[len(m)-1].Role != "toolResult" {
			t.Error("finish before results")
		}
		return true
	}}
	if _, e := Run(p05Ctx(t), p05User(), cfg, func(Event) {}); e != nil || calls != 1 {
		t.Fatal(e, calls)
	}
	cfg.FinishTurn = nil
	cfg.MaxTurns = 2
	calls = 0
	ends := 0
	if _, e := Run(p05Ctx(t), p05User(), cfg, func(ev Event) {
		if ev.Type == "agent_end" {
			ends++
		}
	}); e == nil || calls != 2 || ends != 1 {
		t.Fatal("limit", e, calls, ends)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ends = 0
	_, e := Run(ctx, p05User(), cfg, func(ev Event) {
		if ev.Type == "agent_end" {
			ends++
		}
	})
	if e == nil || ends != 1 {
		t.Fatal("cancel", e, ends)
	}
}
