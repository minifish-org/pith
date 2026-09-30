package codingagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestSessionMemoryModeIsFileless(t *testing.T) {
	session, err := OpenSession("")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if session.IsPersisted() {
		t.Fatal("empty path must be memory-only")
	}
	if session.SessionFile() != "" {
		t.Fatal("memory session must not have a file")
	}
	if len(session.Entries()) != 0 || len(session.Context()) != 0 {
		t.Fatal("new memory session must be empty")
	}
	if session.LeafID() != "" {
		t.Fatal("new memory session leaf must be root")
	}
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if len(session.Entries()) != 1 {
		t.Fatal("append into memory failed")
	}
}

func TestSessionEntriesAreImmutableSnapshots(t *testing.T) {
	session, err := OpenSession("")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"a"}`)); err != nil {
		t.Fatal(err)
	}
	first := session.Entries()
	first[0].Payload[0] = '!'
	first[0].ID = "mutated"
	second := session.Entries()
	if !json.Valid(second[0].Payload) {
		t.Fatal("Entries returned a shared payload buffer")
	}
	if second[0].ID == "mutated" {
		t.Fatal("Entries returned shared struct values")
	}
	context := session.Context()
	context[0].Payload[len(context[0].Payload)-1] = '!'
	if !json.Valid(session.Context()[0].Payload) {
		t.Fatal("Context returned a shared payload buffer")
	}
}

func TestSessionBranchIsolationAndDurability(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := session.Append("message", json.RawMessage(`{"role":"user","content":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := session.Append("message", json.RawMessage(`{"role":"user","content":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := session.Append("message", json.RawMessage(`{"role":"user","content":"c"}`))
	if err != nil || b.ParentID != a.ID || c.ParentID != b.ID {
		t.Fatalf("chain: %v %#v", err, []SessionEntry{a, b, c})
	}
	if err := session.Branch(a.ID); err != nil {
		t.Fatal(err)
	}
	d, err := session.Append("custom", json.RawMessage(`{"branch":true}`))
	if err != nil || d.ParentID != a.ID {
		t.Fatalf("branch append: %v %#v", err, d)
	}
	if err := session.Branch("missing"); err == nil {
		t.Fatal("unknown branch accepted")
	}
	if session.LeafID() != d.ID {
		t.Fatal("failed branch mutated the leaf")
	}
	if len(session.Entries()) != 4 {
		t.Fatalf("siblings were deleted: %d", len(session.Entries()))
	}
	if len(session.Context()) != 2 {
		t.Fatalf("active branch wrong: %d", len(session.Context()))
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.LeafID() != d.ID || len(reopened.Context()) != 2 || len(reopened.Entries()) != 4 {
		t.Fatalf("reopen lost state: leaf=%s context=%d entries=%d", reopened.LeafID(), len(reopened.Context()), len(reopened.Entries()))
	}
	if got := reopened.Context(); got[0].ID != a.ID || got[1].ID != d.ID {
		t.Fatalf("reopened branch wrong: %#v", got)
	}
}

func TestSessionLeafPersistsWithoutSubsequentAppend(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := session.Append("message", json.RawMessage(`{"role":"user","content":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := session.Branch(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.LeafID() != a.ID {
		t.Fatalf("leaf not durable without append: %q", reopened.LeafID())
	}
	if len(reopened.Context()) != 1 || reopened.Context()[0].ID != a.ID {
		t.Fatalf("context after reopen: %#v", reopened.Context())
	}
	continued, err := reopened.Append("message", json.RawMessage(`{"role":"user","content":"branch"}`))
	if err != nil || continued.ParentID != a.ID {
		t.Fatalf("append after durable branch: %v %#v", err, continued)
	}
}

func TestSessionBranchRootAndEmptySession(t *testing.T) {
	session, err := OpenSession("")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := session.Branch(""); err != nil {
		t.Fatal(err)
	}
	if session.LeafID() != "" || len(session.Context()) != 0 {
		t.Fatal("branch to root failed")
	}
	root, err := session.Append("message", json.RawMessage(`{"role":"user","content":"root"}`))
	if err != nil || root.ParentID != "" {
		t.Fatalf("root append: %v %#v", err, root)
	}
}

func TestSessionPreservesUnknownPayloadFields(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"opaque":{"n":0,"nil":null,"list":[1,2]},"extra":"x"}`)
	if _, err := session.Append("custom", payload); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if string(reopened.Entries()[0].Payload) != string(payload) {
		t.Fatalf("payload changed: %s", reopened.Entries()[0].Payload)
	}
}

func TestSessionMalformedAndUnsupportedFilesFail(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"malformed-complete": "{not json}\n",
		"no-header":          "{\"type\":\"message\",\"id\":\"1\"}\n",
		"version-2":          "{\"type\":\"session\",\"version\":2,\"id\":\"x\",\"timestamp\":\"t\",\"cwd\":\"\"}\n",
		"version-99":         "{\"type\":\"session\",\"version\":99,\"id\":\"x\",\"timestamp\":\"t\",\"cwd\":\"\"}\n",
		"missing-version":    "{\"type\":\"session\",\"id\":\"x\",\"timestamp\":\"t\",\"cwd\":\"\"}\n",
		"duplicate-id": "{\"type\":\"session\",\"version\":3,\"id\":\"x\",\"timestamp\":\"t\",\"cwd\":\"\"}\n" +
			"{\"id\":\"dup\",\"type\":\"custom\",\"payload\":null}\n" +
			"{\"id\":\"dup\",\"type\":\"custom\",\"payload\":null}\n",
		"missing-parent": "{\"type\":\"session\",\"version\":3,\"id\":\"x\",\"timestamp\":\"t\",\"cwd\":\"\"}\n" +
			"{\"id\":\"child\",\"parentId\":\"ghost\",\"type\":\"custom\",\"payload\":null}\n",
		"cycle": "{\"type\":\"session\",\"version\":3,\"id\":\"x\",\"timestamp\":\"t\",\"cwd\":\"\"}\n" +
			"{\"id\":\"a\",\"parentId\":\"b\",\"type\":\"custom\",\"payload\":null}\n" +
			"{\"id\":\"b\",\"parentId\":\"a\",\"type\":\"custom\",\"payload\":null}\n",
	}
	for name, content := range cases {
		file := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(file)
		if _, err := OpenSession(file); err == nil {
			t.Fatalf("%s: malformed session silently accepted", name)
		}
		after, _ := os.ReadFile(file)
		if string(before) != string(after) {
			t.Fatalf("%s: rejected file was mutated", name)
		}
	}
}

func TestSessionCrashTailRecoveryKeepsValidPrefix(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"a", "b", "c"} {
		if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"`+content+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Append an unterminated, incomplete crash tail.
	handle, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString(`{"id":"partial","type":"mess`); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	reopened, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Entries()) != 3 {
		t.Fatalf("valid prefix lost: %d", len(reopened.Entries()))
	}
	if reopened.LeafID() != reopened.Entries()[2].ID {
		t.Fatal("recovered leaf wrong")
	}
	// A second append must produce a coherent file again.
	if _, err := reopened.Append("custom", json.RawMessage(`{"after":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(again.Entries()) != 4 {
		t.Fatalf("append after recovery failed: %d", len(again.Entries()))
	}
	// The recovered prefix bytes must still be present.
	final, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(final), string(valid)) {
		t.Fatal("crash-tail recovery erased a valid prefix")
	}
}

func TestSessionWriteFailureDoesNotMutateMemory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"a"}`)); err != nil {
		t.Fatal(err)
	}
	// Replace the file with a directory so the next append cannot open it.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"b"}`)); err == nil {
		t.Fatal("append to broken storage succeeded")
	}
	if len(session.Entries()) != 1 {
		t.Fatal("failed append mutated memory")
	}
	if err := session.Branch(session.Entries()[0].ID); err == nil {
		t.Fatal("branch to broken storage succeeded")
	}
}

func TestSessionOpenDiskFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(filepath.Join(blocker, "nested", "session.jsonl")); err == nil {
		t.Fatal("open under a file-shaped directory succeeded")
	}
}

func TestSessionFileModeIsPrivate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSessionConcurrentAppendAndSnapshots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	session, err := OpenSession(file)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var wait sync.WaitGroup
	for i := 0; i < 24; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := session.Append("custom", json.RawMessage(`{"ok":true}`)); err != nil {
				t.Error(err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			entries := session.Entries()
			context := session.Context()
			for index := range entries {
				if entries[index].ID == "" {
					t.Error("empty id in snapshot")
				}
			}
			for index := range context {
				if context[index].ID == "" {
					t.Error("empty id in context snapshot")
				}
			}
			session.GetTree()
		}()
	}
	wait.Wait()
	if len(session.Entries()) != 24 {
		t.Fatalf("concurrent appends lost: %d", len(session.Entries()))
	}
}

func TestSessionAfterCloseRejectsMutation(t *testing.T) {
	session, err := OpenSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append("custom", json.RawMessage(`{}`)); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("append after close = %v", err)
	}
	if err := session.Branch(""); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("branch after close = %v", err)
	}
}

func TestBuildSessionContextWithCompaction(t *testing.T) {
	messages := []SessionEntry{
		{ID: "1", Type: "message", Payload: json.RawMessage(`{"role":"user","content":"first"}`)},
		{ID: "2", ParentID: "1", Type: "message", Payload: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"response1"}],"api":"openai-completions","provider":"openai","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1}`)},
		{ID: "3", ParentID: "2", Type: "message", Payload: json.RawMessage(`{"role":"user","content":"second"}`)},
		{ID: "4", ParentID: "3", Type: "message", Payload: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"response2"}],"api":"openai-completions","provider":"openai","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1}`)},
		{ID: "5", ParentID: "4", Type: "compaction", Payload: json.RawMessage(`{"summary":"compacted","firstKeptEntryId":"3","tokensBefore":1000}`)},
		{ID: "6", ParentID: "5", Type: "thinking_level_change", Payload: json.RawMessage(`{"thinkingLevel":"high"}`)},
		{ID: "7", ParentID: "6", Type: "message", Payload: json.RawMessage(`{"role":"user","content":"third"}`)},
	}
	context := BuildSessionContext(messages, nil)
	if len(context.Messages) != 4 {
		t.Fatalf("compaction context messages = %d, want 4", len(context.Messages))
	}
	if context.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q", context.ThinkingLevel)
	}
	if context.Model == nil || context.Model.Provider != "openai" || context.Model.ModelID != "m" {
		t.Fatalf("model = %#v", context.Model)
	}
	// The compaction entry itself is the first projected source and the first
	// message is its rendered summary.
	projection := BuildSessionProjection(messages, nil)
	var summarySeen bool
	for _, entry := range projection.Entries {
		if entry.SourceEntry.Type == "compaction" && len(entry.Messages) > 0 {
			summarySeen = true
		}
	}
	if !summarySeen {
		t.Fatal("compaction summary missing from projection")
	}
	if latest, ok := GetLatestCompactionEntry(messages); !ok || latest.ID != "5" {
		t.Fatal("latest compaction lookup failed")
	}
	if ids := BuildContextEntries(messages, nil); len(ids) != 5 {
		t.Fatalf("context entries = %d, want 5", len(ids))
	}
}

func TestMigrateSessionEntriesRenamesHookMessage(t *testing.T) {
	version := 2
	entries := []FileEntry{
		{Header: &SessionHeader{Type: "session", Version: &version, ID: "s"}},
		{Entry: &SessionEntry{ID: "1", Type: "message", Payload: json.RawMessage(`{"role":"hookMessage","content":"x","timestamp":1}`)}},
	}
	migrated := MigrateSessionEntries(entries)
	if *migrated[0].Header.Version != CurrentSessionVersion {
		t.Fatal("header version not migrated")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(migrated[1].Entry.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["role"]) != `"custom"` {
		t.Fatalf("role not migrated: %s", fields["role"])
	}
	if string(fields["content"]) != `"x"` {
		t.Fatal("unknown fields lost during migration")
	}
}

func TestTypedEntrySchemasRoundTrip(t *testing.T) {
	parent := "root"
	message := SessionMessageEntry{}
	message.Type = "message"
	message.ID = "m"
	message.ParentID = &parent
	message.Timestamp = "2025-01-01T00:00:00Z"
	message.Message = json.RawMessage(`{"role":"user","content":"hi"}`)
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionMessageEntry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "m" || *decoded.ParentID != "root" {
		t.Fatalf("typed round trip: %#v", decoded)
	}

	thinking := ThinkingLevelChangeEntry{}
	thinking.Type = "thinking_level_change"
	thinking.ID = "t"
	thinking.ThinkingLevel = "high"
	raw, _ = json.Marshal(thinking)
	if !strings.Contains(string(raw), `"thinkingLevel":"high"`) {
		t.Fatalf("thinking entry shape: %s", raw)
	}

	compaction := CompactionEntry{}
	compaction.Type = "compaction"
	compaction.ID = "c"
	compaction.Summary = "s"
	compaction.FirstKeptEntryID = "k"
	compaction.TokensBefore = 10
	raw, _ = json.Marshal(compaction)
	if !strings.Contains(string(raw), `"firstKeptEntryId":"k"`) {
		t.Fatalf("compaction entry shape: %s", raw)
	}
}

func TestMessageHelpers(t *testing.T) {
	if BashExecutionToText(BashExecutionMessage{Command: "ls"}) != "Ran `ls`\n(no output)" {
		t.Fatal("bash text without output")
	}
	fromID := "from"
	branch := CreateBranchSummaryMessage("sum", &fromID, TimestampFromString("2025-01-01T00:00:00Z"))
	if branch.Role != BranchSummaryRole || branch.Summary != "sum" {
		t.Fatalf("branch summary: %#v", branch)
	}
	custom := CreateCustomMessage("t", aitypes.UserContentText("hello"), true, nil, TimestampFromNumber(0))
	if custom.Role != CustomRole || custom.Content.Text != "hello" {
		t.Fatalf("custom message: %#v", custom)
	}

	bashRaw, _ := json.Marshal(BashExecutionMessage{Role: BashExecutionRole, Command: "echo hi", Output: "hi", Timestamp: 5})
	transcript := []agenttypes.AgentMessage{
		agenttypes.NewCustomMessage(BashExecutionRole, bashRaw),
		agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("plain", 1))),
	}
	converted, err := ConvertToLlm(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 2 {
		t.Fatalf("convertToLlm length = %d", len(converted))
	}
	if converted[0].User == nil || len(converted[0].User.Content.Blocks) == 0 || converted[0].User.Content.Blocks[0].Text == nil || converted[0].User.Content.Blocks[0].Text.Text != "Ran `echo hi`\n```\nhi\n```" {
		t.Fatalf("bash execution not rendered: %#v", converted[0])
	}

	excluded := true
	excludedRaw, _ := json.Marshal(BashExecutionMessage{Role: BashExecutionRole, Command: "secret", ExcludeFromContext: &excluded})
	converted, err = ConvertToLlm([]agenttypes.AgentMessage{agenttypes.NewCustomMessage(BashExecutionRole, excludedRaw)})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 0 {
		t.Fatal("excluded bash execution leaked into context")
	}
}

func TestParseSessionEntriesStrictness(t *testing.T) {
	valid := "{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"\"}\n" +
		"{\"id\":\"1\",\"type\":\"custom\",\"payload\":{\"a\":1}}\n"
	entries, err := ParseSessionEntries(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Header == nil || entries[1].Entry == nil {
		t.Fatalf("parse: %#v", entries)
	}
	if _, err := ParseSessionEntries("{\"id\":\"1\"}\nnot json\n"); err == nil {
		t.Fatal("malformed complete record accepted")
	}
}

func TestSessionTreeExposesSiblings(t *testing.T) {
	session, err := OpenSession("")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	a, _ := session.Append("message", json.RawMessage(`{"role":"user","content":"a"}`))
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := session.Branch(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append("message", json.RawMessage(`{"role":"user","content":"c"}`)); err != nil {
		t.Fatal(err)
	}
	tree := session.GetTree()
	if len(tree) != 1 {
		t.Fatalf("tree roots = %d", len(tree))
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(tree[0].Children))
	}
}

func TestReadonlySessionManagerInterface(t *testing.T) {
	var _ ReadonlySessionManager = (*SessionManager)(nil)
}

func TestSessionCwdHelpers(t *testing.T) {
	session, err := OpenSession("")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if issue := GetMissingSessionCwdIssue(session, "/fallback"); issue != nil {
		t.Fatalf("memory session must not report a cwd issue: %#v", issue)
	}
	issue := SessionCwdIssue{SessionCwd: "/gone", FallbackCwd: "/now"}
	if err := AssertSessionCwdExists(session, "/now"); err != nil {
		t.Fatal(err)
	}
	message := FormatMissingSessionCwdError(issue)
	if !strings.Contains(message, "/gone") || !strings.Contains(message, "/now") {
		t.Fatalf("missing cwd error text: %q", message)
	}
	if prompt := FormatMissingSessionCwdPrompt(issue); !strings.Contains(prompt, "continue in current cwd") {
		t.Fatalf("missing cwd prompt: %q", prompt)
	}
	var missing *MissingSessionCwdError
	if !errors.As(&MissingSessionCwdError{Issue: issue}, &missing) {
		t.Fatal("MissingSessionCwdError does not implement error")
	}
}

func TestSessionIDValidation(t *testing.T) {
	for _, valid := range []string{"a", "abc12345", "a.b-c_d"} {
		if err := AssertValidSessionID(valid); err != nil {
			t.Fatalf("valid id %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "-ab", "ab-", "a/b", "a b"} {
		if err := AssertValidSessionID(invalid); err == nil {
			t.Fatalf("invalid id %q accepted", invalid)
		}
	}
}

func TestGetDefaultSessionDir(t *testing.T) {
	got := GetDefaultSessionDir("/Users/me/project", "/agent")
	want := filepath.Join("/agent", "sessions", "--Users-me-project--")
	if got != want {
		t.Fatalf("session dir = %q, want %q", got, want)
	}
}
