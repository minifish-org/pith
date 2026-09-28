package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func p09Build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pith")
	cmd := exec.Command("go", "build", "-mod=readonly", "-p", "1", "-o", bin, ".")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build %v %s", e, b)
	}
	return bin
}
func p09Run(t *testing.T, events bool) (string, string, int32) {
	t.Helper()
	root := t.TempDir()
	marker := "PITH_MARKER_482617"
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte(marker), 0600)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		var p struct{ Messages []map[string]any }
		if e := json.NewDecoder(r.Body).Decode(&p); e != nil {
			t.Error(e)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"read-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"notes.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		toolCount := 0
		for _, m := range p.Messages {
			if m["role"] == "tool" {
				toolCount++
				if m["tool_call_id"] != "read-1" || !strings.Contains(m["content"].(string), marker) {
					t.Error("wrong tool result", m)
				}
			}
		}
		if n != 2 || toolCount != 1 {
			t.Error("tool roundtrip", n, toolCount)
		}
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"`+marker+`"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	bin := p09Build(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := []string{"--root", root, "--prompt", "Read notes.txt and report its marker"}
	if events {
		args = append(args, "--events")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir(), "PITH_BASE_URL=" + server.URL + "/v1", "PITH_MODEL=fixture-model", "PITH_API_KEY=SECRET_NOT_IN_OUTPUT"}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if e := cmd.Run(); e != nil {
		t.Fatal(e, stderr.String())
	}
	return stdout.String(), stderr.String(), requests.Load()
}
func TestPortsmithJudgeP09_01(t *testing.T) {
	out, _, requests := p09Run(t, false)
	if !strings.Contains(out, "PITH_MARKER_482617") || requests != 2 {
		t.Fatal(out, requests)
	}
}
func TestPortsmithJudgeP09_02(t *testing.T) {
	_, events, _ := p09Run(t, true)
	if strings.Count(events, "tool_execution_start") != 1 || strings.Count(events, "tool_execution_end") != 1 {
		t.Fatal("missing or duplicate tool events", events)
	}
}
func TestPortsmithJudgeP09_03(t *testing.T) {
	out, events, _ := p09Run(t, true)
	if strings.Contains(out+events, "SECRET_NOT_IN_OUTPUT") {
		t.Fatal("key leaked")
	}
}
func TestPortsmithJudgeP09_04(t *testing.T) {
	out, _, _ := p09Run(t, false)
	if !strings.Contains(out, "PITH_MARKER_482617") {
		t.Fatal("binary required development runtime")
	}
}
func TestPortsmithJudgeP09_05(t *testing.T) {
	bin := p09Build(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--root", t.TempDir(), "--prompt", "x")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	if e := cmd.Run(); e == nil {
		t.Fatal("missing provider configuration accepted")
	}
}
