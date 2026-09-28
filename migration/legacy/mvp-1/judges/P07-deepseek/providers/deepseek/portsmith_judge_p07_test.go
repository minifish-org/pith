package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/minifish-org/pith/ai"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func p07Chunk(delta string, finish string) string {
	return `data: {"model":"actual","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n"
}
func p07Collect(t *testing.T, url string, c ai.Context) ([]ai.Event, error) {
	t.Helper()
	p, e := New(Config{BaseURL: url + "/v1", Model: "configured", APIKey: "fixture-secret", MaxTokens: 123})
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, e := p.Stream(ctx, c)
	if e != nil {
		return nil, e
	}
	out := []ai.Event{}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out, nil
			}
			out = append(out, ev)
		case <-ctx.Done():
			t.Fatal("stream did not close")
			return nil, ctx.Err()
		}
	}
}
func p07Final(t *testing.T, events []ai.Event) *ai.Message {
	t.Helper()
	terminal := 0
	var m *ai.Message
	for i, e := range events {
		if e.Type == "error" {
			t.Fatal(e.Error)
		}
		if e.Type == "done" {
			terminal++
			m = e.Message
			if i != len(events)-1 {
				t.Fatal("event after terminal")
			}
		}
	}
	if terminal != 1 || m == nil {
		t.Fatal("no unique terminal", events)
	}
	return m
}
func p07Failed(events []ai.Event, e error) bool {
	if e != nil {
		return true
	}
	n := 0
	for _, v := range events {
		if v.Type == "done" {
			return false
		}
		if v.Type == "error" {
			n++
		}
	}
	return n == 1
}
func TestPortsmithJudgeP07_01(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		t.Run(fmt.Sprint(withTools), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
					t.Error("request", r.Method, r.URL)
				}
				var p map[string]any
				if e := json.NewDecoder(r.Body).Decode(&p); e != nil {
					t.Error(e)
				}
				if p["model"] != "configured" || p["max_tokens"] != float64(123) || p["stream"] != true {
					t.Error(p)
				}
				if _, ok := p["max_completion_tokens"]; ok {
					t.Error("wrong max tokens")
				}
				_, has := p["tools"]
				if has != withTools {
					t.Error("tools presence", p)
				}
				if !withTools {
					if _, ok := p["tool_choice"]; ok {
						t.Error("empty tool choice")
					}
				}
				thinking, _ := p["thinking"].(map[string]any)
				if thinking["type"] != "disabled" {
					t.Error("thinking not off")
				}
				messages, _ := p["messages"].([]any)
				roles := []string{}
				for _, m := range messages {
					roles = append(roles, m.(map[string]any)["role"].(string))
				}
				if strings.Join(roles, ",") != "system,user,assistant,tool" {
					t.Error(roles)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, p07Chunk(`{"content":"ok"}`, `"stop"`)+"data: [DONE]\n\n")
			}))
			defer server.Close()
			c := ai.Context{SystemPrompt: "system", Messages: []ai.Message{{Role: "user", Content: []ai.Content{{Type: "text", Text: "x"}}}, {Role: "assistant", Content: []ai.Content{{Type: "toolCall", ID: "1", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}}}, {Role: "toolResult", ToolCallID: "1", ToolName: "read", Content: []ai.Content{{Type: "text", Text: "result"}}}}}
			if withTools {
				c.Tools = []ai.ToolDeclaration{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}
			}
			events, e := p07Collect(t, server.URL, c)
			if e != nil {
				t.Fatal(e)
			}
			p07Final(t, events)
		})
	}
}
func TestPortsmithJudgeP07_02(t *testing.T) {
	chunks := []string{p07Chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{\"path\":"}},{"index":1,"id":"b","function":{"name":"read","arguments":"{\"path\":"}}]}`, `null`), p07Chunk(`{"tool_calls":[{"index":1,"function":{"arguments":"\"二\"}"}},{"index":0,"function":{"arguments":"\"一\"}"}}]}`, `"tool_calls"`), "data: [DONE]\n\n"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		data := strings.ReplaceAll(strings.Join(chunks, ""), "\n", "\r\n")
		for _, b := range []byte(data) {
			w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	events, e := p07Collect(t, server.URL, ai.Context{})
	if e != nil {
		t.Fatal(e)
	}
	m := p07Final(t, events)
	calls := []ai.Content{}
	for _, c := range m.Content {
		if c.Type == "toolCall" {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 || calls[0].ID != "a" || calls[1].ID != "b" || string(calls[0].Arguments) != `{"path":"一"}` || string(calls[1].Arguments) != `{"path":"二"}` {
		t.Fatal(calls)
	}
	if m.StopReason != "toolUse" {
		t.Fatal(m.StopReason)
	}
	// Earlier published messages must not change when later deltas arrive.
	for _, ev := range events {
		if ev.Type == "start" && ev.Message != nil && len(ev.Message.Content) > 0 {
			if string(ev.Message.Content[0].Arguments) == `{"path":"一"}` {
				t.Fatal("mutable start snapshot")
			}
		}
	}
}
func TestPortsmithJudgeP07_03(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, p07Chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{"}}]}`, `"tool_calls"`)+"data: [DONE]\n\n")
	}))
	defer server.Close()
	events, e := p07Collect(t, server.URL, ai.Context{})
	if !p07Failed(events, e) {
		t.Fatal("invalid JSON succeeded", events, e)
	}
}
func TestPortsmithJudgeP07_04(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, p07Chunk(`{"content":"hello"}`, `"stop"`)+`data: {"model":"actual","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	events, e := p07Collect(t, server.URL, ai.Context{})
	if e != nil {
		t.Fatal(e)
	}
	m := p07Final(t, events)
	if m.Model != "actual" || m.RawStopReason != "stop" || m.Usage == nil || m.Usage.Input != 7 || m.Usage.Output != 3 || m.Usage.TotalTokens != 10 || m.Usage.Cost != nil {
		t.Fatal(m)
	}
}
func TestPortsmithJudgeP07_05(t *testing.T) {
	for _, body := range []string{p07Chunk(`{"content":"x"}`, `"alien"`), p07Chunk(`{"content":"x"}`, `null`), "data: broken\n\n"} {
		var count atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, body)
		}))
		events, e := p07Collect(t, server.URL, ai.Context{})
		server.Close()
		if !p07Failed(events, e) || count.Load() != 1 {
			t.Fatal("stream failure/replay", events, e, count.Load())
		}
	}
}
func TestPortsmithJudgeP07_06(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"fixture"}}`)
		}))
		events, e := p07Collect(t, server.URL, ai.Context{})
		server.Close()
		if !p07Failed(events, e) {
			t.Fatal(status, events, e)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	p, e := New(Config{BaseURL: server.URL + "/v1", Model: "m", APIKey: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	ch, e := p.Stream(ctx, ai.Context{})
	if e == nil {
		events := []ai.Event{}
		for ev := range ch {
			events = append(events, ev)
		}
		if !p07Failed(events, nil) {
			t.Fatal("cancel succeeded")
		}
	}
}
