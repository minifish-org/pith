package sdk_incremental_test

import (
	"encoding/json"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPortsmithJudgeSDKStorage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	s, err := sdk.OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Append("message", json.RawMessage(`{"role":"user","content":"a"}`))
	if err != nil || a.ID == "" {
		t.Fatalf("append: %v", err)
	}
	b, err := s.Append("custom", json.RawMessage(`{"opaque":{"n":0,"nil":null}}`))
	if err != nil || b.ParentID != a.ID {
		t.Fatal("parent")
	}
	entries := s.Entries()
	entries[0].Payload[0] = '!'
	if !json.Valid(s.Entries()[0].Payload) {
		t.Fatal("mutable snapshot")
	}
	if err = s.Branch(a.ID); err != nil {
		t.Fatal(err)
	}
	c, err := s.Append("custom", json.RawMessage(`{"branch":true}`))
	if err != nil || c.ParentID != a.ID {
		t.Fatal("branch parent")
	}
	if err = s.Branch("missing"); err == nil {
		t.Fatal("unknown branch accepted")
	}
	if s.LeafID() != c.ID {
		t.Fatal("failed branch mutated leaf")
	}
	if len(s.Context()) != 2 || len(s.Entries()) != 3 {
		t.Fatal("tree flattened")
	}
	s.Close()
	s, err = sdk.OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.LeafID() != c.ID || len(s.Context()) != 2 {
		t.Fatal("branch not durable")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Append("custom", json.RawMessage(`{"ok":true}`)); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(s.Entries()) != 15 {
		t.Fatal("concurrent append lost")
	}
	corrupt := filepath.Join(t.TempDir(), "bad.jsonl")
	os.WriteFile(corrupt, []byte("{not json}\n"), 0600)
	if _, err = sdk.OpenSession(corrupt); err == nil {
		t.Fatal("corrupt storage silently accepted")
	}
}
