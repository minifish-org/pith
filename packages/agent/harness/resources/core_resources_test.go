package resources

import (
	"context"
	"strings"
	"testing"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

func TestParseCommandArgs(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"quotes", `a 'b c' "d e"`, []string{"a", "b c", "d e"}},
		{"whitespace", "  a   b ", []string{"a", "b"}},
		{"unicode-empty-quotes", `中文 ""`, []string{"中文"}},
		{"empty", "", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCommandArgs(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseCommandArgs(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseCommandArgs(%q) = %#v, want %#v", tc.input, got, tc.want)
				}
			}
		})
	}
}

func TestSubstituteArgs(t *testing.T) {
	cases := []struct {
		name    string
		content string
		args    []string
		want    string
	}{
		{"positional", "$1:$2:$@", []string{"x", "y"}, "x:y:x y"},
		{"missing", "$3/$1", []string{"a"}, "/a"},
		{"arguments", "$ARGUMENTS", []string{"one", "two"}, "one two"},
		{"sliced", "${@:2}", []string{"a", "b", "c"}, "b c"},
		{"sliced-length", "${@:2:1}", []string{"a", "b", "c"}, "b"},
		{"sliced-out-of-range", "${@:9}", []string{"a"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubstituteArgs(tc.content, tc.args); got != tc.want {
				t.Fatalf("SubstituteArgs(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestFormatResourceInvocations(t *testing.T) {
	skill := harnesstypes.Skill{
		Name:     "inspect",
		FilePath: "/project/.pi/skills/inspect/SKILL.md",
		Content:  "Use inspection tools.",
	}
	additional := "Check errors."
	wantSkill := "<skill name=\"inspect\" location=\"/project/.pi/skills/inspect/SKILL.md\">\n" +
		"References are relative to /project/.pi/skills/inspect.\n\nUse inspection tools.\n</skill>\n\nCheck errors."
	if got := FormatSkillInvocation(skill, &additional); got != wantSkill {
		t.Fatalf("FormatSkillInvocation mismatch:\n got %q\nwant %q", got, wantSkill)
	}
	if got := FormatSkillInvocation(skill, nil); !strings.HasSuffix(got, "</skill>") || strings.Contains(got, "Check errors.") {
		t.Fatalf("FormatSkillInvocation without additional instructions = %q", got)
	}

	template := harnesstypes.PromptTemplate{Name: "review", Content: "Review $1 with $ARGUMENTS"}
	if got := FormatPromptTemplateInvocation(template, []string{"a.ts", "care"}); got != "Review a.ts with a.ts care" {
		t.Fatalf("FormatPromptTemplateInvocation = %q", got)
	}
}

func TestFormatSkillsForSystemPrompt(t *testing.T) {
	visible := harnesstypes.Skill{Name: "visible", Description: "Use <this> & that", FilePath: "/skills/visible/SKILL.md", Content: "visible content"}
	second := harnesstypes.Skill{Name: "second", Description: "Second skill", FilePath: "/skills/second/SKILL.md", Content: "second content"}
	disabled := harnesstypes.Skill{Name: "hidden", Description: "Hidden", FilePath: "/skills/hidden/SKILL.md", DisableModelInvocation: boolPointer(true)}

	got := FormatSkillsForSystemPrompt([]harnesstypes.Skill{visible, disabled, second})
	want := "The following skills provide specialized instructions for specific tasks.\n" +
		"Read the full skill file when the task matches its description.\n" +
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n\n" +
		"<available_skills>\n" +
		"  <skill>\n    <name>visible</name>\n    <description>Use &lt;this&gt; &amp; that</description>\n    <location>/skills/visible/SKILL.md</location>\n  </skill>\n" +
		"  <skill>\n    <name>second</name>\n    <description>Second skill</description>\n    <location>/skills/second/SKILL.md</location>\n  </skill>\n" +
		"</available_skills>"
	if got != want {
		t.Fatalf("FormatSkillsForSystemPrompt mismatch:\n got %q\nwant %q", got, want)
	}
	if FormatSkillsForSystemPrompt([]harnesstypes.Skill{disabled}) != "" {
		t.Fatal("disabled-only skill list must render empty")
	}

	escaped := FormatSkillsForSystemPrompt([]harnesstypes.Skill{{
		Name:        "a&b",
		Description: `Quote "double" and 'single'`,
		FilePath:    `/skills/<bad>&"quote"/SKILL.md`,
	}})
	if !strings.Contains(escaped, "<name>a&amp;b</name>\n    <description>Quote &quot;double&quot; and &apos;single&apos;</description>\n    <location>/skills/&lt;bad&gt;&amp;&quot;quote&quot;/SKILL.md</location>") {
		t.Fatalf("unescaped XML in %q", escaped)
	}
}

func TestLoadPromptTemplates(t *testing.T) {
	env := newMemoryEnv(map[string]string{
		"/templates/review.md": "---\ndescription: Review changes\nargument-hint: path\n---\nReview the diff.",
		"/templates/plain.md":  "First line of the template body that is definitely longer than sixty characters.\nMore.",
		"/templates/skip.txt":  "not markdown",
	})
	result, err := LoadPromptTemplates(env, []string{"/templates", "/missing"}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PromptTemplates) != 2 {
		t.Fatalf("loaded %d templates, want 2: %#v", len(result.PromptTemplates), result.PromptTemplates)
	}
	if result.PromptTemplates[0].Name != "plain" || result.PromptTemplates[1].Name != "review" {
		t.Fatalf("unexpected order: %#v", result.PromptTemplates)
	}
	if result.PromptTemplates[1].Description == nil || *result.PromptTemplates[1].Description != "Review changes" {
		t.Fatalf("frontmatter description not used: %#v", result.PromptTemplates[1].Description)
	}
	if result.PromptTemplates[0].Description == nil || len(*result.PromptTemplates[0].Description) != 63 {
		t.Fatalf("first-line description truncation mismatch: %#v", result.PromptTemplates[0].Description)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func TestLoadPromptTemplatesFileAndParseFailure(t *testing.T) {
	env := newMemoryEnv(map[string]string{
		"/templates/review.md": "Review the diff.",
		"/templates/bad.md":    "---\n: : :\n---\nbody",
	})
	result, err := LoadPromptTemplates(env, []string{"/templates/review.md"}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PromptTemplates) != 1 || result.PromptTemplates[0].Name != "review" {
		t.Fatalf("file load mismatch: %#v", result.PromptTemplates)
	}
}

func TestLoadSkills(t *testing.T) {
	env := newMemoryEnv(map[string]string{
		"/skills/demo/SKILL.md":        "---\nname: demo\ndescription: Demo skill\n---\nDo the demo.",
		"/skills/demo/notes.md":        "---\ndescription: Root note\n---\nnote body",
		"/skills/demo/hidden/SKILL.md": "---\nname: hidden\ndescription: Hidden skill\n---\nhidden",
		"/skills/demo/.gitignore":      "hidden/\nnotes.md\n",
		"/skills/rejected/SKILL.md":    "---\nname: mismatch\ndescription: Bad name\n---\nbody",
		"/skills/rejected/no-front.md": "no frontmatter",
	})
	result, err := LoadSkills(env, []string{"/skills"}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, skill := range result.Skills {
		names = append(names, skill.Name)
	}
	if len(names) != 2 || names[0] != "demo" || names[1] != "mismatch" {
		t.Fatalf("unexpected skills: %#v (diagnostics %#v)", names, result.Diagnostics)
	}
	if result.Skills[0].DisableModelInvocation != nil {
		t.Fatalf("disable-model-invocation should default to absent: %#v", result.Skills[0].DisableModelInvocation)
	}
	foundInvalid := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == SkillDiagnosticInvalidMetadata {
			foundInvalid = true
		}
	}
	if !foundInvalid {
		t.Fatalf("expected invalid_metadata diagnostic: %#v", result.Diagnostics)
	}
}

func TestLoadSkillsDisableModelInvocation(t *testing.T) {
	env := newMemoryEnv(map[string]string{
		"/skills/hidden/SKILL.md": "---\nname: hidden\ndescription: Hidden\ndisable-model-invocation: true\n---\nbody",
	})
	result, err := LoadSkills(env, []string{"/skills"}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].DisableModelInvocation == nil || !*result.Skills[0].DisableModelInvocation {
		t.Fatalf("disable-model-invocation not preserved: %#v", result.Skills)
	}
}

// memoryEnv is a minimal in-memory ExecutionEnv used by these tests. It embeds
// the interface so only the operations exercised by the loaders are needed.
type memoryEnv struct {
	harnesstypes.ExecutionEnv
	files map[string]string
}

func newMemoryEnv(files map[string]string) *memoryEnv {
	return &memoryEnv{files: files}
}

func (e *memoryEnv) Cwd() string { return "/" }

func (e *memoryEnv) JoinPath(parts []string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	joined := strings.Join(parts, "/")
	for strings.Contains(joined, "//") {
		joined = strings.ReplaceAll(joined, "//", "/")
	}
	return harnesstypes.Ok[string, harnesstypes.FileError](joined)
}

func (e *memoryEnv) FileInfo(path string, ctx harnesstypes.Context) harnesstypes.Result[harnesstypes.FileInfo, harnesstypes.FileError] {
	if content, ok := e.files[path]; ok {
		return harnesstypes.Ok[harnesstypes.FileInfo, harnesstypes.FileError](harnesstypes.FileInfo{
			Name: baseName(path),
			Path: path,
			Kind: harnesstypes.FileKindFile,
			Size: len(content),
		})
	}
	for filePath := range e.files {
		if strings.HasPrefix(filePath, path+"/") {
			return harnesstypes.Ok[harnesstypes.FileInfo, harnesstypes.FileError](harnesstypes.FileInfo{
				Name: baseName(path),
				Path: path,
				Kind: harnesstypes.FileKindDirectory,
			})
		}
	}
	return harnesstypes.Err[harnesstypes.FileInfo, harnesstypes.FileError](harnesstypes.FileError{Code: harnesstypes.FileErrorNotFound, Message: "not found"})
}

func (e *memoryEnv) ListDir(path string, ctx harnesstypes.Context) harnesstypes.Result[[]harnesstypes.FileInfo, harnesstypes.FileError] {
	seen := map[string]bool{}
	entries := []harnesstypes.FileInfo{}
	prefix := path + "/"
	for filePath, content := range e.files {
		if !strings.HasPrefix(filePath, prefix) {
			continue
		}
		rest := filePath[len(prefix):]
		name := rest
		if index := strings.Index(rest, "/"); index >= 0 {
			name = rest[:index]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		childPath := prefix + name
		kind := harnesstypes.FileKindDirectory
		size := 0
		if _, ok := e.files[childPath]; ok {
			kind = harnesstypes.FileKindFile
			size = len(content)
		}
		entries = append(entries, harnesstypes.FileInfo{Name: name, Path: childPath, Kind: kind, Size: size})
	}
	return harnesstypes.Ok[[]harnesstypes.FileInfo, harnesstypes.FileError](entries)
}

func (e *memoryEnv) ReadTextFile(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	if content, ok := e.files[path]; ok {
		return harnesstypes.Ok[string, harnesstypes.FileError](content)
	}
	return harnesstypes.Err[string, harnesstypes.FileError](harnesstypes.FileError{Code: harnesstypes.FileErrorNotFound, Message: "not found"})
}

func (e *memoryEnv) CanonicalPath(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	return harnesstypes.Ok[string, harnesstypes.FileError](path)
}

func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}
