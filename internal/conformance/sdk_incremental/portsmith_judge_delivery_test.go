package sdk_incremental_test

import (
	"context"
	"encoding/json"
	"fmt"
	ai "github.com/minifish-org/pith/packages/ai/types"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"go/ast"
	"go/parser"
	"go/token"
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

func TestPortsmithJudgeSDKHTTP(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("wrong endpoint or credentials")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"id\":\"one\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"v1\",\"type\":\"function\",\"function\":{\"name\":\"verify_candidate\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else {
			encoded, _ := json.Marshal(body)
			if !strings.Contains(string(encoded), "offline-verification-passed") {
				t.Error("tool result not sent over HTTP")
			}
			fmt.Fprint(w, "data: {\"id\":\"two\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"finished\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	m := model()
	m.BaseUrl = srv.URL + "/v1"
	m.MaxTokens = 4096
	reg, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{{Name: "verify_candidate", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (sdk.ToolResult, error) {
		return sdk.ToolResult{Content: []ai.ContentBlock{ai.TextBlock("offline-verification-passed")}}, nil
	}}}, []string{"verify_candidate"}, nil, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: t.TempDir(), Tools: reg, Model: sdk.ModelOptions{Model: m, APIKey: func(context.Context, string) (string, error) { return "fixture-key", nil }}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := s.Prompt(ctx, "verify")
	if err != nil || r.StopReason != ai.StopReasonStop || requests.Load() != 2 {
		t.Fatalf("real provider/tool loop %v requests %d", err, requests.Load())
	}
}
func TestPortsmithJudgeSDKSourceMaps(t *testing.T) {
	root := filepath.Clean("../../..")
	expectedData, err := os.ReadFile("testdata/sdk-exports.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected []struct{ Source, Upstream string }
	if err = json.Unmarshal(expectedData, &expected); err != nil {
		t.Fatal(err)
	}
	type row struct{ Source, Upstream, GoPackage, GoSymbol, GoFile, Reason, Disposition string }
	rows := map[string]row{}
	implemented := map[string]int{}
	files, err := filepath.Glob(filepath.Join(root, "packages/coding-agent/source_map_*.json"))
	if err != nil || len(files) != 7 {
		t.Fatal("expected seven source maps", err)
	}
	for _, file := range files {
		data, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		var part []row
		if e = json.Unmarshal(data, &part); e != nil {
			t.Fatal(e)
		}
		for _, r := range part {
			key := r.Source + "#" + r.Upstream
			if _, ok := rows[key]; ok {
				t.Fatal("duplicate mapping", key)
			}
			rows[key] = r
		}
	}
	for _, ex := range expected {
		r, ok := rows[ex.Source+"#"+ex.Upstream]
		if !ok || len(r.Reason) < 12 {
			t.Fatal("missing or unexplained mapping", ex)
		}
		if r.Disposition == "excluded" {
			continue
		}
		implemented[r.Source]++
		if r.GoFile == "" || filepath.IsAbs(r.GoFile) || strings.Contains(r.GoFile, "..") || filepath.Dir(r.GoFile) != r.GoPackage || !strings.HasSuffix(r.GoFile, ".go") {
			t.Fatal("invalid target file", r)
		}
		f, e := parser.ParseFile(token.NewFileSet(), filepath.Join(root, r.GoFile), nil, 0)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				found = found || (d.Recv == nil && d.Name.Name == r.GoSymbol)
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch sp := sp.(type) {
					case *ast.TypeSpec:
						found = found || sp.Name.Name == r.GoSymbol
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							found = found || n.Name == r.GoSymbol
						}
					}
				}
			}
		}
		if !found || !ast.IsExported(r.GoSymbol) {
			t.Fatal("target declaration not found", r.GoFile, r.GoSymbol)
		}
	}
	for _, ex := range expected {
		if implemented[ex.Source] == 0 {
			t.Fatal("source entirely excluded despite selected scope", ex.Source)
		}
	}
	for _, doc := range []string{"docs/sdk/README.md", "docs/sdk/compatibility.md"} {
		b, e := os.ReadFile(filepath.Join(root, doc))
		if e != nil || len(b) < 300 {
			t.Fatal("missing SDK guide", doc)
		}
	}
}

func TestPortsmithJudgeSDKCrossBuild(t *testing.T) {
	root := filepath.Clean("../../..")
	for _, name := range []string{"minimal", "custom-tool", "events", "resume", "resources", "web"} {
		if _, e := os.Stat(filepath.Join(root, "examples/embedded-sdk", name, "main.go")); e != nil {
			t.Fatal(e)
		}
	}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"windows", "amd64"}} {
		cmd := exec.Command("go", "build", "-mod=readonly", "./packages/coding-agent", "./examples/embedded-sdk/...")
		cmd.Dir = root
		// Remove inherited platform settings before adding the target.
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CGO_ENABLED=") && !strings.HasPrefix(v, "GOOS=") && !strings.HasPrefix(v, "GOARCH=") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CGO_ENABLED=0", "GOOS="+target[0], "GOARCH="+target[1])
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v %s", target, e, out)
		}
	}
}
