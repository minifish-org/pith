package codemode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/minifish-org/pith/packages/codemode"
)

func TestCodemodeDeadlinePrecedence(t *testing.T) {
	tool := codemode.Tool{Name: "deadline", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return json.RawMessage(`{"set":false}`), nil
		}
		return json.Marshal(map[string]any{"set": true, "remainingMs": time.Until(deadline).Milliseconds()})
	}}
	for _, test := range []struct {
		name    string
		sandbox time.Duration
		source  string
		execute time.Duration
		caller  time.Duration
		want    time.Duration
	}{
		{name: "default has no deadline"},
		{name: "sandbox fallback", sandbox: 20 * time.Second, want: 20 * time.Second},
		{name: "source overrides sandbox", sandbox: 20 * time.Second, source: "10000", want: 10 * time.Second},
		{name: "execute overrides source", sandbox: 20 * time.Second, source: "10000", execute: 30 * time.Second, want: 30 * time.Second},
		{name: "execute disables sandbox and source", sandbox: 20 * time.Second, source: "10000", execute: -1},
		{name: "source enables disabled fallback", sandbox: -1, source: "10000", want: 10 * time.Second},
		{name: "caller bounds disabled execution", execute: -1, caller: 10 * time.Second, want: 10 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			sandbox := newTestSandbox(t, codemode.SandboxOptions{Tools: []codemode.Tool{tool}, Timeout: test.sandbox})
			ctx := context.Background()
			if test.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.caller)
				defer cancel()
			}
			source := "return await tools.deadline();"
			if test.source != "" {
				source = "// @options: {\"timeout_ms\":" + test.source + "}\n" + source
			}
			result := sandbox.Execute(ctx, source, codemode.ExecuteOptions{Timeout: test.execute})
			if !result.OK {
				t.Fatalf("execution failed: %+v", result.Error)
			}
			var observed struct {
				Set         bool
				RemainingMs int64
			}
			if err := json.Unmarshal(result.Value, &observed); err != nil {
				t.Fatal(err)
			}
			if test.want == 0 {
				if observed.Set {
					t.Fatalf("unexpected default deadline: %+v", observed)
				}
			} else if !observed.Set || observed.RemainingMs > test.want.Milliseconds() || observed.RemainingMs < (test.want-2*time.Second).Milliseconds() {
				t.Fatalf("deadline %+v, expected %s", observed, test.want)
			}
		})
	}
}

func TestCodemodeSourceDeadlineCancelsCPUAndTools(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	sandbox := newTestSandbox(t, codemode.SandboxOptions{Timeout: time.Second, Tools: []codemode.Tool{{
		Name: "wait", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			cancelled <- struct{}{}
			return nil, ctx.Err()
		},
	}}})
	for _, code := range []string{"while (true) {}", "await tools.wait();"} {
		result := sandbox.Execute(context.Background(), "// @options: {\"timeout_ms\":50}\n"+code, codemode.ExecuteOptions{})
		if result.OK || result.Error == nil || result.Error.Kind != "timeout" {
			t.Fatalf("deadline did not cancel %q: %+v", code, result)
		}
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("nested tool context was not cancelled")
	}
}

func TestCodemodeRejectsInvalidDeadline(t *testing.T) {
	for _, value := range []string{"-1", "-9007199254740991", "0", "1.5", "2147483648"} {
		_, err := codemode.ParseCodemodeSource(fmt.Sprintf("// @options: {\"timeout_ms\":%s}\ntext(1)", value))
		if err == nil {
			t.Fatalf("accepted invalid deadline %s", value)
		}
	}
}

func TestCodemodeTextBudgetPreservesDataAndExecution(t *testing.T) {
	sandbox := newTestSandbox(t, codemode.SandboxOptions{})
	result := sandbox.Execute(context.Background(), `// @options: {"max_output_tokens":2}
text("你好🙂"); console.log("later");
image("data:image/png;base64,iVBORw0KGg==");
store("after", {done:true}); return {whole:"完整结构化值"};`, codemode.ExecuteOptions{})
	if !result.OK || !result.OutputTruncated {
		t.Fatalf("budget not applied: %+v", result)
	}
	if len(result.Output) != 2 || result.Output[0].Text != "你好" || !utf8.ValidString(result.Output[0].Text) || result.Output[1].Type != "image" {
		t.Fatalf("budget damaged text/images or output order: %+v", result.Output)
	}
	if !strings.Contains(string(result.Value), "完整结构化值") || result.StoreWrites == nil || string(result.StoreWrites.Set["after"]) != `{"done":true}` {
		t.Fatalf("budget stopped execution or truncated machine data: %+v", result)
	}
	zero := sandbox.Execute(context.Background(), "// @options: {\"max_output_tokens\":0}\ntext('hidden'); image('data:image/png;base64,iVBORw0KGg=='); return 42;", codemode.ExecuteOptions{})
	if !zero.OK || !zero.OutputTruncated || len(zero.Output) != 1 || zero.Output[0].Type != "image" || string(zero.Value) != "42" {
		t.Fatalf("zero budget: %+v", zero)
	}
	plain := sandbox.Execute(context.Background(), "text('unbudgeted');", codemode.ExecuteOptions{})
	if !plain.OK || plain.OutputTruncated || len(plain.Output) != 1 || plain.Output[0].Text != "unbudgeted" {
		t.Fatalf("omission changed output: %+v", plain)
	}
}

func TestBudgetTextOutputSharesBudgetWithoutMutatingInput(t *testing.T) {
	input := []codemode.OutputItem{{Type: "text", Text: "123"}, {Type: "image", Data: "image"}, {Type: "text", Text: "456"}}
	wantInput := append([]codemode.OutputItem{}, input...)
	output, truncated := codemode.BudgetTextOutput(input, 1)
	if !truncated || len(output) != 3 || output[0].Text != "123" || output[2].Text != "4" {
		t.Fatalf("not one shared budget: %+v", output)
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatal("budget mutated source items")
	}
	output, truncated = codemode.BudgetTextOutput(input, 1<<63-1)
	if truncated || !reflect.DeepEqual(output, input) {
		t.Fatal("large budget overflowed")
	}
}
