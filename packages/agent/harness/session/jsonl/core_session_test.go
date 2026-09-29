package jsonl

import (
	"testing"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

func TestIsJsonlStorageHeader(t *testing.T) {
	valid := map[string]any{"kind": "header", "v": float64(4), "id": "session", "cwd": "/workspace", "storageVersion": float64(1), "createdAt": float64(1700000000000)}
	if !IsJsonlStorageHeader(valid) {
		t.Fatal("expected valid header")
	}
	invalid := map[string]any{"kind": "header", "v": float64(3), "id": "session", "cwd": "/workspace", "storageVersion": float64(1), "createdAt": float64(0)}
	if IsJsonlStorageHeader(invalid) {
		t.Fatal("expected version 3 to be rejected")
	}
}

func TestParseJsonlSessionHeader(t *testing.T) {
	parsed, err := ParseJsonlSessionHeader(`{"type":"session","version":3,"id":"legacy","timestamp":"2023-11-14T22:13:20.000Z","cwd":"/workspace"}`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Format != "v3-legacy" || parsed.V3 == nil || parsed.V3.ID != "legacy" {
		t.Fatalf("unexpected parse %+v", parsed)
	}
	if _, err := ParseJsonlSessionHeader(`{"kind":"nope"}`); err == nil {
		t.Fatal("expected unsupported header to reject")
	}
}

func TestSerializeParseTransactionRoundTrip(t *testing.T) {
	entry := harnesstypes.CustomEntry{
		EntryBase:  harnesstypes.EntryBase{ID: "a", ParentID: nil, Seq: 1, Timestamp: 10, Type: harnesstypes.EntryTypeCustom},
		CustomType: "note",
		Data:       map[string]any{"kept": true},
	}
	writes := []session.CommittedWrite{
		{Kind: "entry", Entry: entry, Seq: 1, Timestamp: 10},
		{Kind: "value", Op: "set", Namespace: "pi.session.name", Key: "", Value: "demo", Seq: 2},
		{Kind: "list", Op: "append", Namespace: "test.events", Key: "", Value: "event", Seq: 3},
	}
	line, err := SerializeJsonlTransaction(writes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseJsonlTransaction(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 3 {
		t.Fatalf("writes = %d", len(parsed))
	}
	if parsed[0].Kind != "entry" || session.EntryID(parsed[0].Entry) != "a" {
		t.Fatalf("entry = %+v", parsed[0])
	}
	if parsed[1].Kind != "value" || parsed[1].Value != "demo" {
		t.Fatalf("value = %+v", parsed[1])
	}
	if parsed[2].Kind != "list" || parsed[2].Value != "event" {
		t.Fatalf("list = %+v", parsed[2])
	}
}

func TestSplitCompleteLines(t *testing.T) {
	lines, torn := splitCompleteLines("a\nb\n")
	if torn || len(lines) != 2 {
		t.Fatalf("complete: %v %v", lines, torn)
	}
	lines, torn = splitCompleteLines("a\nb")
	if !torn || len(lines) != 1 || lines[0] != "a" {
		t.Fatalf("torn: %v %v", lines, torn)
	}
	lines, torn = splitCompleteLines("partial")
	if !torn || len(lines) != 0 {
		t.Fatalf("single torn: %v %v", lines, torn)
	}
}
