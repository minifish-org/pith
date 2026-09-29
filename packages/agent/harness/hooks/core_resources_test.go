package harnesshooks

import (
	"context"
	"testing"

	harnessexecution "github.com/minifish-org/pith/packages/agent/harness/execution"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestHookRegistryBeforeRunAggregatesMessages(t *testing.T) {
	registry := NewHookRegistry(nil)
	registry.On(HookBeforeRun, func(ctx Context, event any) (any, error) {
		return &BeforeRunResult{Messages: []agenttypes.AgentMessage{agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("injected", 0)))}}, nil
	})
	gate, control := harnessexecution.CreateGate()
	result, err := registry.RunWithGate(HookBeforeRun, &BeforeRunEvent{Prompt: []agenttypes.AgentMessage{}}, gate, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runResult, ok := result.(*BeforeRunResult)
	if !ok || runResult == nil || len(runResult.Messages) != 1 {
		t.Fatalf("unexpected hook result: %#v", result)
	}
	control.Close(nil)
}

func TestHookRegistryReportsAndContinues(t *testing.T) {
	reported := 0
	registry := NewHookRegistry(func(ctx Context, err error, hook HookName, lane string) {
		reported++
	})
	registry.On(HookBeforeRun, func(ctx Context, event any) (any, error) {
		return nil, context.DeadlineExceeded
	})
	registry.On(HookBeforeRun, func(ctx Context, event any) (any, error) {
		return &BeforeRunResult{Messages: []agenttypes.AgentMessage{agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("second", 0)))}}, nil
	})
	gate, control := harnessexecution.CreateGate()
	defer control.Close(nil)
	result, err := registry.RunWithGate(HookBeforeRun, &BeforeRunEvent{}, gate, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reported != 1 {
		t.Fatalf("expected one reported error, got %d", reported)
	}
	runResult, _ := result.(*BeforeRunResult)
	if runResult == nil || len(runResult.Messages) != 1 {
		t.Fatalf("second handler did not run: %#v", result)
	}
}

func TestHookRegistryFailClosed(t *testing.T) {
	registry := NewHookRegistry(nil)
	registry.On(HookBeforeDrive, func(ctx Context, event any) (any, error) {
		return nil, context.Canceled
	})
	gate, control := harnessexecution.CreateGate()
	defer control.Close(nil)
	_, err := registry.RunWithGate(HookBeforeDrive, &struct{}{}, gate, context.Background())
	if err == nil {
		t.Fatal("before_drive must fail closed")
	}
}

func TestApplyStreamOptionsPatch(t *testing.T) {
	timeout := 10
	transport := aitypes.Transport("sse")
	base := harnesstypes.AgentHarnessStreamOptions{
		TimeoutMs: &timeout,
		Headers:   map[string]string{"x": "1", "y": "2"},
		Metadata:  map[string]any{"keep": "value", "drop": "old"},
	}
	header := "3"
	patch := harnesstypes.AgentHarnessStreamOptionsPatch{
		Transport: &transport,
		Headers:   map[string]*string{"y": nil, "z": &header},
		Metadata:  map[string]any{"drop": nil, "add": 1.0},
	}
	got := ApplyStreamOptionsPatch(base, patch)
	if got.Transport == nil || *got.Transport != "sse" {
		t.Fatalf("transport not applied: %#v", got.Transport)
	}
	if got.Headers["x"] != "1" || got.Headers["z"] != "3" {
		t.Fatalf("headers not applied: %#v", got.Headers)
	}
	if _, ok := got.Headers["y"]; ok {
		t.Fatalf("header y should be deleted: %#v", got.Headers)
	}
	if _, ok := got.Metadata["drop"]; ok {
		t.Fatalf("metadata drop should be deleted: %#v", got.Metadata)
	}
	if got.Metadata["add"] != 1.0 {
		t.Fatalf("metadata add not applied: %#v", got.Metadata)
	}
	if got.TimeoutMs == nil || *got.TimeoutMs != 10 {
		t.Fatalf("unpatched scalar changed: %#v", got.TimeoutMs)
	}
}
