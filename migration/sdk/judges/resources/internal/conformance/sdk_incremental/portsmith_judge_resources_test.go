package sdk_incremental_test

import (
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortsmithJudgeSDKResources(t *testing.T) {
	d := t.TempDir()
	skill := filepath.Join(d, "skills", "review", "SKILL.md")
	os.MkdirAll(filepath.Dir(skill), 0700)
	os.WriteFile(skill, []byte("---\nname: review\ndescription: Review changes\n---\nReview carefully.\n"), 0600)
	tpl := filepath.Join(d, "templates", "greet.md")
	os.MkdirAll(filepath.Dir(tpl), 0700)
	os.WriteFile(tpl, []byte("---\ndescription: Greeting\n---\nHello $1; $ARGUMENTS"), 0600)
	ctx := filepath.Join(d, "AGENTS.md")
	os.WriteFile(ctx, []byte("Project instruction sentinel"), 0600)
	r, err := sdk.LoadResources(sdk.ResourceOptions{Cwd: d, SkillPaths: []string{filepath.Join(d, "skills")}, TemplatePaths: []string{filepath.Dir(tpl)}, ContextFiles: []string{ctx}, SystemPrompt: "base", AppendSystemPrompt: []string{"append"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skills) != 1 || r.Skills[0].Name != "review" || !strings.Contains(r.Skills[0].Body, "Review carefully") {
		t.Fatal("skill discovery")
	}
	if len(r.Templates) != 1 || !strings.Contains(r.SystemPrompt, "Project instruction sentinel") || !strings.Contains(r.SystemPrompt, "append") {
		t.Fatal("resources not assembled")
	}
	expanded, err := sdk.ExpandTemplate(r.Templates[0], []string{"Ada", "Lovelace"})
	if err != nil || !strings.Contains(expanded, "Hello Ada; Ada Lovelace") {
		t.Fatalf("expansion %q %v", expanded, err)
	}
	// Reload reflects disk changes; no persistent global discovery cache.
	os.WriteFile(ctx, []byte("Updated sentinel"), 0600)
	r, err = sdk.LoadResources(sdk.ResourceOptions{Cwd: d, ContextFiles: []string{ctx}})
	if err != nil || !strings.Contains(r.SystemPrompt, "Updated sentinel") {
		t.Fatal("reload stale")
	}
}
