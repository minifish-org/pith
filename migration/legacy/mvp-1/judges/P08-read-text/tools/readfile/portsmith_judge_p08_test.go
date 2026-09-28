package readfile

import (
	"context"
	"encoding/json"
	"github.com/minifish-org/pith/agent"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func p08Read(t *testing.T, root, args string) (string, error, bool) {
	t.Helper()
	tool, e := New(root)
	if e != nil {
		return "", e, false
	}
	result, e := tool.Execute(context.Background(), json.RawMessage(args), func(agent.ToolResult) {})
	text := ""
	for _, c := range result.Content {
		text += c.Text
	}
	return text, e, result.IsError
}
func TestPortsmithJudgeP08_01(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree"), 0600)
	text, e, bad := p08Read(t, root, `{"path":"a.txt","offset":2,"limit":1}`)
	if e != nil || bad || !strings.Contains(text, "two") || strings.Contains(text, "one\n") {
		t.Fatal(text, e, bad)
	}
	os.WriteFile(filepath.Join(root, "empty"), nil, 0600)
	text, e, bad = p08Read(t, root, `{"path":"empty"}`)
	if e != nil || bad || text != "" {
		t.Fatal(text, e, bad)
	}
	for _, args := range []string{`{"path":"missing"}`, `{"path":"a.txt","offset":99}`, `{"path":"a.txt","offset":0}`, `{"path":"a.txt","limit":0}`} {
		_, e, bad = p08Read(t, root, args)
		if e == nil && !bad {
			t.Fatal("invalid read succeeded", args)
		}
	}
}
func TestPortsmithJudgeP08_02(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a"), []byte("一\r\n二\r\n三"), 0600)
	v, e, bad := p08Read(t, root, `{"path":"a"}`)
	if e != nil || bad || v != "一\r\n二\r\n三" {
		t.Fatal(v, e)
	}
	os.WriteFile(filepath.Join(root, "lines"), []byte(strings.Repeat("ROW\n", 2001)), 0600)
	v, e, bad = p08Read(t, root, `{"path":"lines"}`)
	if e != nil || bad || strings.Count(v, "ROW") != 2000 {
		t.Fatal("line cap", e, strings.Count(v, "ROW"))
	}
	os.WriteFile(filepath.Join(root, "huge"), []byte(strings.Repeat("汉", 20000)), 0600)
	v, e, _ = p08Read(t, root, `{"path":"huge"}`)
	if e != nil {
		t.Fatal(e)
	}
	if !utf8.ValidString(v) || len(v) > 50*1024 {
		t.Fatal("long line cap")
	}
	os.WriteFile(filepath.Join(root, "bytes"), []byte(strings.Repeat(strings.Repeat("x", 100)+"\n", 1000)), 0600)
	v, e, bad = p08Read(t, root, `{"path":"bytes"}`)
	if e != nil || bad || strings.Count(v, "x") > 50*1024 {
		t.Fatal("byte cap")
	}
}
func TestPortsmithJudgeP08_03(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("DO_NOT_READ"), 0600)
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"../secret", "link/secret", ".", filepath.Join(outside, "secret")} {
		args, _ := json.Marshal(map[string]any{"path": name})
		text, e, bad := p08Read(t, root, string(args))
		if (e == nil && !bad) || strings.Contains(text, "DO_NOT_READ") {
			t.Fatal("escaped", name, text, e)
		}
	}
	tool, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, e := tool.Execute(ctx, json.RawMessage(`{"path":"x"}`), func(agent.ToolResult) {})
	if e == nil && !res.IsError {
		t.Fatal("cancel ignored")
	}
}
func TestPortsmithJudgeP08_04(t *testing.T) {
	root := t.TempDir()
	var fixtures []struct {
		Input     string
		Content   string
		Truncated bool
	}
	b, err := os.ReadFile("testdata/p08-ts.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 5 {
		t.Fatal("missing TS truncation cases")
	}
	for _, f := range fixtures {
		if err := os.WriteFile(filepath.Join(root, "fixture"), []byte(f.Input), 0600); err != nil {
			t.Fatal(err)
		}
		text, err, bad := p08Read(t, root, `{"path":"fixture"}`)
		if err != nil || bad || (!f.Truncated && text != f.Content) || (f.Truncated && !strings.HasPrefix(text, f.Content)) {
			t.Fatal("TS text mismatch", err, bad)
		}
	}
	os.WriteFile(filepath.Join(root, "image.png"), []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0}, 0600)
	_, e, bad := p08Read(t, root, `{"path":"image.png"}`)
	if e == nil && !bad {
		t.Fatal("image accepted")
	}
	os.WriteFile(filepath.Join(root, "plain"), []byte("exact text"), 0600)
	for i := 0; i < 30; i++ {
		v, e, bad := p08Read(t, root, `{"path":"plain"}`)
		if e != nil || bad || v != "exact text" {
			t.Fatal(v, e)
		}
	}
}
func TestPortsmithJudgeP08_05(t *testing.T) {
	root := t.TempDir()
	tool, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	if tool.Declaration.Name != "read" || !json.Valid(tool.Declaration.Parameters) {
		t.Fatal(tool.Declaration)
	}
	t.Setenv("PATH", t.TempDir())
	os.WriteFile(filepath.Join(root, "a"), []byte("no external runtime"), 0600)
	v, e, bad := p08Read(t, root, `{"path":"a"}`)
	if e != nil || bad || v != "no external runtime" {
		t.Fatal(v, e)
	}
}
