package codemode_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/minifish-org/pith/packages/codemode"
)

func TestSandboxCompilationReuseKeepsHostBindingsAndLifetimeSeparate(t *testing.T) {
	makeSandbox := func(value string) *codemode.Sandbox {
		return newTestSandbox(t, codemode.SandboxOptions{Tools: []codemode.Tool{{
			Name: "identity",
			Execute: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.Marshal(value)
			},
		}}})
	}
	first := makeSandbox("first")
	second := makeSandbox("second")
	result := first.Execute(context.Background(), `globalThis.previous = true; return await tools.identity({});`, codemode.ExecuteOptions{})
	if got := decodeValue(t, result); got != "first" {
		t.Fatalf("wrong first host binding: %v", got)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	result = second.Execute(context.Background(), `if (typeof previous !== 'undefined') throw new Error('shared VM state'); return await tools.identity({});`, codemode.ExecuteOptions{})
	if got := decodeValue(t, result); got != "second" {
		t.Fatalf("wrong second host binding after closing first: %v", got)
	}
	third := makeSandbox("third")
	result = third.Execute(context.Background(), `return await tools.identity({});`, codemode.ExecuteOptions{})
	if got := decodeValue(t, result); got != "third" {
		t.Fatalf("closed sandbox invalidated later construction: %v", got)
	}
}
