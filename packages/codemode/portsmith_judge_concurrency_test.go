package codemode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/codemode"
)

func judgeConcurrencySandbox(t *testing.T, options codemode.SandboxOptions) *codemode.Sandbox {
	t.Helper()
	options.Timeout = 10 * time.Second
	s, err := codemode.NewSandbox(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("close sandbox: %v", err)
		}
	})
	return s
}

func TestPortsmithJudgeCodemodeAwaitOrder(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	record := func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, string(args))
		mu.Unlock()
		return nil, nil
	}
	s := judgeConcurrencySandbox(t, codemode.SandboxOptions{
		Globals: []codemode.Tool{
			{Name: "attach", Execute: record},
			{Name: "models.list", Spread: true, Execute: record},
		},
		Tools: []codemode.Tool{{Name: "echo", Execute: func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
			return args, nil
		}}},
	})
	r := s.Execute(context.Background(), `
		await attach({ ref: 1 });
		const attached = attach("saved promise");
		await attached;
		await models.list("classifier", undefined, 3);
		return [typeof attach, Object.keys(models), await tools.echo(2)];
	`, codemode.ExecuteOptions{})
	if !r.OK || string(r.Value) != `["function",["list"],2]` {
		t.Fatalf("explicit awaits and namespace result: %+v", r)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	want := []string{`{"ref":1}`, `"saved promise"`, `["classifier",null,3]`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit awaits did not preserve payloads and order: got %q, want %q", got, want)
	}
	if len(r.Calls) != 1 || r.Calls[0].Name != "echo" || r.Calls[0].Status != "ok" {
		t.Fatalf("globals appeared in tool records or echo did not finish: %+v", r.Calls)
	}
}

func TestPortsmithJudgeCodemodeConcurrentGlobals(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	s := judgeConcurrencySandbox(t, codemode.SandboxOptions{
		Globals: []codemode.Tool{{Name: "meet", Execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			select {
			case started <- string(args):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-release:
				return args, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}},
		Tools: []codemode.Tool{{Name: "releaseWhenBothStarted", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			seen := map[string]bool{}
			for range 2 {
				select {
				case payload := <-started:
					seen[payload] = true
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if len(seen) != 2 || !seen[`"first"`] || !seen[`"second"`] {
				return nil, fmt.Errorf("concurrent global arguments: %v", seen)
			}
			releaseOnce.Do(func() { close(release) })
			return json.RawMessage(`true`), nil
		}}},
	})
	r := s.Execute(context.Background(), `
		const first = meet("first");
		const second = meet("second");
		await tools.releaseWhenBothStarted();
		return await Promise.all([first, second]);
	`, codemode.ExecuteOptions{})
	if !r.OK || string(r.Value) != `["first","second"]` {
		t.Fatalf("callbacks must overlap and both saved promises must finish: %+v", r)
	}
	if len(r.Calls) != 1 || r.Calls[0].Name != "releaseWhenBothStarted" || r.Calls[0].Status != "ok" {
		t.Fatalf("concurrency barrier record or global exclusion: %+v", r.Calls)
	}
}

func TestPortsmithJudgeCodemodeUnawaitedCancellation(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	s := judgeConcurrencySandbox(t, codemode.SandboxOptions{
		Globals: []codemode.Tool{{Name: "pending", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		}}},
		Tools: []codemode.Tool{{Name: "waitUntilStarted", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			select {
			case <-started:
				return json.RawMessage(`true`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}},
	})
	r := s.Execute(context.Background(), `
		pending();
		await tools.waitUntilStarted();
		return "early";
	`, codemode.ExecuteOptions{})
	if !r.OK || string(r.Value) != `"early"` {
		t.Fatalf("return must settle without awaiting a pending global: %+v", r)
	}
	if len(r.Calls) != 1 || r.Calls[0].Name != "waitUntilStarted" || r.Calls[0].Status != "ok" {
		t.Fatalf("pending globals must not appear in tool records: %+v", r.Calls)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("return did not cancel the already-started pending global")
	}
}
