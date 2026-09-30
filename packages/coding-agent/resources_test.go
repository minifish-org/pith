package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadResourcesDiscoveryAndAssembly(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "review", "SKILL.md")
	writeTestFile(t, skill, "---\nname: review\ndescription: Review changes\n---\nReview carefully.\n")
	template := filepath.Join(dir, "templates", "greet.md")
	writeTestFile(t, template, "---\ndescription: Greeting\n---\nHello $1; $ARGUMENTS")
	context := filepath.Join(dir, "AGENTS.md")
	writeTestFile(t, context, "Project instruction sentinel")

	resourceSet, err := LoadResources(ResourceOptions{
		Cwd:                dir,
		SkillPaths:         []string{filepath.Join(dir, "skills")},
		TemplatePaths:      []string{filepath.Join(dir, "templates")},
		ContextFiles:       []string{context},
		SystemPrompt:       "base",
		AppendSystemPrompt: []string{"append"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resourceSet.Skills) != 1 || resourceSet.Skills[0].Name != "review" || !strings.Contains(resourceSet.Skills[0].Body, "Review carefully") {
		t.Fatalf("skill discovery: %#v", resourceSet.Skills)
	}
	if len(resourceSet.Templates) != 1 {
		t.Fatalf("template discovery: %#v", resourceSet.Templates)
	}
	for _, want := range []string{"base", "Project instruction sentinel", "append"} {
		if !strings.Contains(resourceSet.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q: %q", want, resourceSet.SystemPrompt)
		}
	}
	if len(resourceSet.ContextFiles) != 1 {
		t.Fatalf("context files: %#v", resourceSet.ContextFiles)
	}

	expanded, err := ExpandTemplate(resourceSet.Templates[0], []string{"Ada", "Lovelace"})
	if err != nil || !strings.Contains(expanded, "Hello Ada; Ada Lovelace") {
		t.Fatalf("expansion %q %v", expanded, err)
	}

	// Reload reflects edits: no persistent discovery cache.
	writeTestFile(t, context, "Updated sentinel")
	resourceSet, err = LoadResources(ResourceOptions{Cwd: dir, ContextFiles: []string{context}})
	if err != nil || !strings.Contains(resourceSet.SystemPrompt, "Updated sentinel") {
		t.Fatalf("reload stale: %v %q", err, resourceSet.SystemPrompt)
	}
}

func TestSkillDuplicatePrecedenceAndCollisionDiagnostic(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first", "SKILL.md")
	second := filepath.Join(dir, "second", "SKILL.md")
	writeTestFile(t, first, "---\nname: collision\ndescription: first\n---\nfirst body")
	writeTestFile(t, second, "---\nname: collision\ndescription: second\n---\nsecond body")

	result := LoadSkills(LoadSkillsOptions{
		Cwd:        dir,
		SkillPaths: []string{filepath.Join(dir, "first"), filepath.Join(dir, "second")},
	})
	if len(result.Skills) != 1 || result.Skills[0].Description != "first" {
		t.Fatalf("first skill should win: %#v", result.Skills)
	}
	foundCollision := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Type == "collision" && diagnostic.Collision != nil {
			foundCollision = true
			if diagnostic.Collision.WinnerPath != first || diagnostic.Collision.LoserPath != second {
				t.Fatalf("collision paths: %#v", diagnostic.Collision)
			}
		}
	}
	if !foundCollision {
		t.Fatalf("expected collision diagnostic: %#v", result.Diagnostics)
	}
}

func TestTemplateDuplicateCollision(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "one", "dup.md"), "first body")
	writeTestFile(t, filepath.Join(dir, "two", "dup.md"), "second body")
	result := LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd:         dir,
		PromptPaths: []string{filepath.Join(dir, "one"), filepath.Join(dir, "two")},
	})
	if len(result.Templates) != 1 || result.Templates[0].Body != "first body" {
		t.Fatalf("first template should win: %#v", result.Templates)
	}
	if len(result.Diagnostics) == 0 || result.Diagnostics[len(result.Diagnostics)-1].Type != "collision" {
		t.Fatalf("expected collision diagnostic: %#v", result.Diagnostics)
	}
}

func TestMissingPathDiagnostics(t *testing.T) {
	dir := t.TempDir()
	result, err := LoadResources(ResourceOptions{
		Cwd:           dir,
		SkillPaths:    []string{filepath.Join(dir, "nope-skills")},
		TemplatePaths: []string{filepath.Join(dir, "nope-templates")},
		ContextFiles:  []string{filepath.Join(dir, "nope-context.md")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) < 3 {
		t.Fatalf("expected diagnostics for each missing path: %#v", result.Diagnostics)
	}
	joined := strings.Join(result.Diagnostics, "\n")
	for _, want := range []string{"nope-skills", "nope-templates", "nope-context.md"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing diagnostic for %q: %v", want, result.Diagnostics)
		}
	}
}

func TestRecursiveSkillDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "skills", "top", "SKILL.md"), "---\nname: top\ndescription: top skill\n---\ntop")
	writeTestFile(t, filepath.Join(dir, "skills", "nested", "deeper", "SKILL.md"), "---\nname: deeper\ndescription: deep skill\n---\ndeep")
	// A root-level .md with a description is a skill too.
	writeTestFile(t, filepath.Join(dir, "skills", "root.md"), "---\nname: root\ndescription: root note\n---\nroot")

	result, err := LoadResources(ResourceOptions{Cwd: dir, SkillPaths: []string{filepath.Join(dir, "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, skill := range result.Skills {
		names[skill.Name] = true
	}
	for _, want := range []string{"top", "deeper", "root"} {
		if !names[want] {
			t.Fatalf("missing recursive skill %q: %#v", want, names)
		}
	}
}

func TestExplicitEmptySystemPrompt(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty-system.md")
	writeTestFile(t, empty, "")
	context := filepath.Join(dir, "AGENTS.md")
	writeTestFile(t, context, "context sentinel")

	resourceSet, err := LoadResources(ResourceOptions{Cwd: dir, SystemPrompt: empty, ContextFiles: []string{context}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resourceSet.SystemPrompt, DefaultSystemPromptPreamble) {
		t.Fatalf("explicit empty prompt fell back to default: %q", resourceSet.SystemPrompt)
	}
	if !strings.Contains(resourceSet.SystemPrompt, "context sentinel") {
		t.Fatalf("context missing from empty prompt: %q", resourceSet.SystemPrompt)
	}
}

func TestBinaryAndMalformedResourcesDoNotPanic(t *testing.T) {
	dir := t.TempDir()
	// Invalid YAML frontmatter in a declared SKILL.md is a parse diagnostic.
	writeTestFile(t, filepath.Join(dir, "bad", "SKILL.md"), "---\n: : :\n---\nbody")
	// Binary bytes without frontmatter: skipped, not executed.
	if err := os.WriteFile(filepath.Join(dir, "binary.md"), []byte{0x00, 0x01, 0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory named like a template file must not be read as text.
	if err := os.MkdirAll(filepath.Join(dir, "templates", "dir.md"), 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := LoadResources(ResourceOptions{
		Cwd:           dir,
		SkillPaths:    []string{dir},
		TemplatePaths: []string{filepath.Join(dir, "templates")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) == 0 {
		t.Fatalf("expected a parse diagnostic for malformed frontmatter")
	}
}

func TestFencedCommandsAreNotExecuted(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	body := "---\nname: runner\ndescription: runner\n---\nInstructions:\n\n```sh\necho pwned > " + marker + "\n```\n"
	writeTestFile(t, filepath.Join(dir, "skills", "runner", "SKILL.md"), body)

	result, err := LoadResources(ResourceOptions{Cwd: dir, SkillPaths: []string{filepath.Join(dir, "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || !strings.Contains(result.Skills[0].Body, "echo pwned") {
		t.Fatalf("fenced command body not preserved as text: %#v", result.Skills)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("resource loading executed a fenced command")
	}
}

func TestSymlinkLoopDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "skills", "real")
	writeTestFile(t, filepath.Join(real, "sub", "SKILL.md"), "---\nname: sub\ndescription: sub skill\n---\nbody")
	if err := os.Symlink(real, filepath.Join(real, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	done := make(chan LoadSkillsResult, 1)
	go func() {
		done <- LoadSkills(LoadSkillsOptions{Cwd: dir, SkillPaths: []string{filepath.Join(dir, "skills")}})
	}()
	select {
	case result := <-done:
		if len(result.Skills) != 1 || result.Skills[0].Name != "sub" {
			t.Fatalf("unexpected skills: %#v", result.Skills)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("skill discovery did not terminate on a symlink loop")
	}
}

func TestSubstituteArgsFullSemantics(t *testing.T) {
	cases := []struct {
		name    string
		content string
		args    []string
		want    string
	}{
		{"positional", "$1:$2:$@", []string{"x", "y"}, "x:y:x y"},
		{"missing", "$1 $2 $3 $4 $5", []string{"a", "b"}, "a b   "},
		{"arguments", "$ARGUMENTS", []string{"one", "two"}, "one two"},
		{"no-recursion", "$ARGUMENTS", []string{"$1", "$ARGUMENTS"}, "$1 $ARGUMENTS"},
		{"default-present", "${1:-7}", []string{"3"}, "3"},
		{"default-missing", "${1:-7}", []string{}, "7"},
		{"default-empty", "${1:-brief}", []string{""}, "brief"},
		{"default-all", "${@:-fallback}", []string{}, "fallback"},
		{"default-no-recursion", "${3:-$ARGUMENTS}", []string{"a", "b"}, "$ARGUMENTS"},
		{"slice", "${@:2}", []string{"a", "b", "c"}, "b c"},
		{"slice-length", "${@:2:2}", []string{"a", "b", "c", "d"}, "b c"},
		{"slice-zero", "${@:0}", []string{"a", "b"}, "a b"},
		{"slice-out-of-range", "${@:9}", []string{"a"}, ""},
		{"zero-index", "$0", []string{"a"}, ""},
		{"decimal", "$1.5", []string{"a"}, "a.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubstituteArgs(tc.content, tc.args); got != tc.want {
				t.Fatalf("SubstituteArgs(%q, %#v) = %q, want %q", tc.content, tc.args, got, tc.want)
			}
		})
	}
}

func TestParseCommandArgsQuoting(t *testing.T) {
	got := ParseCommandArgs(`a 'b c' "d e"`)
	want := []string{"a", "b c", "d e"}
	if len(got) != len(want) {
		t.Fatalf("ParseCommandArgs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseCommandArgs = %#v, want %#v", got, want)
		}
	}
}

func TestExpandPromptTemplateCommand(t *testing.T) {
	templates := []PromptTemplate{{Name: "greet", Body: "Hello $1; $ARGUMENTS"}}
	if got := ExpandPromptTemplate("/greet Ada Lovelace", templates); got != "Hello Ada; Ada Lovelace" {
		t.Fatalf("ExpandPromptTemplate = %q", got)
	}
	if got := ExpandPromptTemplate("/unknown x", templates); got != "/unknown x" {
		t.Fatalf("unknown template should pass through: %q", got)
	}
	if got := ExpandPromptTemplate("plain text", templates); got != "plain text" {
		t.Fatalf("non-command should pass through: %q", got)
	}
}

func TestLoadProjectContextFilesOrderAndOverride(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project", "nested")
	writeTestFile(t, filepath.Join(agentDir, "AGENTS.override.md"), "global override")
	writeTestFile(t, filepath.Join(agentDir, "AGENTS.md"), "global base")
	writeTestFile(t, filepath.Join(root, "project", "AGENTS.md"), "project instructions")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.override.md"), "nested override")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	files := LoadProjectContextFiles(cwd, agentDir)
	byPath := map[string]string{}
	for _, file := range files {
		byPath[file.Path] = file.Content
	}
	if byPath[filepath.Join(agentDir, "AGENTS.override.md")] != "global override" {
		t.Fatalf("global override not preferred: %#v", byPath)
	}
	if byPath[filepath.Join(root, "project", "AGENTS.md")] != "project instructions" {
		t.Fatalf("project instructions missing: %#v", byPath)
	}
	if byPath[filepath.Join(cwd, "AGENTS.override.md")] != "nested override" {
		t.Fatalf("nested override missing: %#v", byPath)
	}
	if _, ok := byPath[filepath.Join(agentDir, "AGENTS.md")]; ok {
		t.Fatal("override should shadow base in the same directory")
	}
}

func TestProjectTrustStoreAndOptions(t *testing.T) {
	agentDir := t.TempDir()
	store := NewProjectTrustStore(agentDir)
	project := filepath.Join(agentDir, "workspace")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if decision := store.Get(project); decision != nil {
		t.Fatalf("empty store should be unset: %v", *decision)
	}
	if err := store.Set(project, ProjectTrustDecision(boolPointer(true))); err != nil {
		t.Fatal(err)
	}
	if decision := store.Get(project); decision == nil || !*decision {
		t.Fatalf("stored decision not read back")
	}

	options := GetProjectTrustOptions(project, true)
	if len(options) != 5 {
		t.Fatalf("expected 5 trust options, got %d: %#v", len(options), options)
	}
	if parent, ok := GetProjectTrustParentPath(project); !ok || parent == project {
		t.Fatalf("parent path: %q %v", parent, ok)
	}

	// Trust resolution is headless: with no UI the ask policy denies.
	trusted, err := ResolveProjectTrusted(ResolveProjectTrustedOptions{Cwd: project, TrustStore: store})
	if err != nil || !trusted {
		t.Fatalf("store decision should resolve trusted: %v %v", trusted, err)
	}
	untrustedProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(untrustedProject, ConfigDirName, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	untrusted, err := ResolveProjectTrusted(ResolveProjectTrustedOptions{Cwd: untrustedProject, TrustStore: NewProjectTrustStore(t.TempDir())})
	if err != nil || untrusted {
		t.Fatalf("unknown project with no UI should be untrusted: %v %v", untrusted, err)
	}
}

func TestHasTrustRequiringProjectResources(t *testing.T) {
	dir := t.TempDir()
	if HasTrustRequiringProjectResources(dir) {
		t.Fatal("empty project should not require trust")
	}
	if err := os.MkdirAll(filepath.Join(dir, ConfigDirName, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(dir) {
		t.Fatal("project skills should require trust")
	}
}

func TestBuildSystemPromptAndDiff(t *testing.T) {
	sections, err := BuildSystemPromptSections(BuildSystemPromptOptions{
		CustomPrompt:       "custom",
		AppendSystemPrompt: []string{"more"},
		Cwd:                "/work",
		ContextFiles:       []ContextFile{{Path: "/work/AGENTS.md", Content: "rules"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sections["preamble"] != "custom" {
		t.Fatalf("preamble: %q", sections["preamble"])
	}
	if !strings.Contains(sections["project_context"], "rules") || !strings.Contains(sections["addendum"], "more") {
		t.Fatalf("sections: %#v", sections)
	}

	rendered, err := BuildSystemPrompt(BuildSystemPromptOptions{CustomPrompt: "custom", Cwd: "/work"})
	if err != nil || !strings.Contains(rendered, "<cwd>") {
		t.Fatalf("rendered: %q %v", rendered, err)
	}

	forced := ""
	state, err := BuildSystemPromptState(BuildSystemPromptOptions{ForceSystemPrompt: &forced})
	if err != nil || state.Content != "" || state.Sections != nil {
		t.Fatalf("forced state: %#v %v", state, err)
	}

	previous := map[string]*string{"preamble": strPtr("custom"), "old": strPtr("gone")}
	patch := DiffSystemPromptSections(previous, SystemPromptSections{"preamble": "custom", "new": "value"})
	if patch == nil || patch["new"] == nil || patch["old"] != nil {
		t.Fatalf("patch: %#v", patch)
	}
	if DiffSystemPromptSections(previous, SystemPromptSections{"preamble": "custom"}) == nil {
		t.Fatal("removal-only patch should not be nil")
	}
	if _, err := BuildSystemPromptSections(BuildSystemPromptOptions{Sections: map[string]string{"Bad Name": "x"}}); err == nil {
		t.Fatal("invalid section name should error")
	}
}

func strPtr(value string) *string { return &value }

func TestFormatSkillsForPromptExcludesDisabled(t *testing.T) {
	skills := []Skill{
		{Name: "visible", Description: "Use <this> & that", File: "/skills/visible/SKILL.md"},
		{Name: "hidden", Description: "Hidden", File: "/skills/hidden/SKILL.md", DisableModelInvocation: true},
	}
	rendered := FormatSkillsForPrompt(skills, "read")
	if !strings.Contains(rendered, "<name>visible</name>") {
		t.Fatalf("visible skill missing: %q", rendered)
	}
	if strings.Contains(rendered, "hidden") {
		t.Fatalf("disabled skill should be excluded: %q", rendered)
	}
	if !strings.Contains(rendered, "&lt;this&gt; &amp; that") {
		t.Fatalf("XML escaping missing: %q", rendered)
	}
	if FormatSkillsForPrompt([]Skill{skills[1]}, "read") != "" {
		t.Fatal("disabled-only list should render empty")
	}
}

func TestDefaultResourceLoaderReloadPicksUpEdits(t *testing.T) {
	dir := t.TempDir()
	contextPath := filepath.Join(dir, "AGENTS.md")
	writeTestFile(t, contextPath, "first")
	loader := NewDefaultResourceLoader(DefaultResourceLoaderOptions{Cwd: dir})
	if err := loader.Reload(nil); err != nil {
		t.Fatal(err)
	}
	if got := loader.GetAgentsFiles(); len(got) != 1 || got[0].Content != "first" {
		t.Fatalf("agents files: %#v", got)
	}
	writeTestFile(t, contextPath, "second")
	if err := loader.Reload(nil); err != nil {
		t.Fatal(err)
	}
	if got := loader.GetAgentsFiles(); len(got) != 1 || got[0].Content != "second" {
		t.Fatalf("reload did not pick up edit: %#v", got)
	}
}

func TestResourceExtensionPathsFromTrustedHost(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "extra", "SKILL.md"), "---\nname: extra\ndescription: extra\n---\nbody")
	loader := NewDefaultResourceLoader(DefaultResourceLoaderOptions{Cwd: dir})
	loader.ExtendResources(ResourceExtensionPaths{
		SkillPaths: []ResourcePathEntry{{Path: filepath.Join(dir, "extra"), Metadata: PathMetadata{Source: "host", Scope: SourceScopeTemporary, Origin: SourceOriginTopLevel}}},
	})
	if got := loader.GetSkills(); len(got.Skills) != 1 || got.Skills[0].Name != "extra" {
		t.Fatalf("extended skill not loaded: %#v", got.Skills)
	}
}
