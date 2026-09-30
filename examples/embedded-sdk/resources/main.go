// Command resources demonstrates the headless resource surface: explicit skill
// paths, prompt templates and project context files, plus template expansion.
// It writes its fixtures to a temp dir and makes no model call.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	codingagent "github.com/minifish-org/pith/packages/coding-agent"
)

func main() {
	dir, err := os.MkdirTemp("", "pith-resources-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	write(filepath.Join(dir, "skills", "review", "SKILL.md"),
		"---\nname: review\ndescription: Review a change carefully\n---\nCheck correctness and tests.\n")
	write(filepath.Join(dir, "prompts", "greet.md"),
		"---\ndescription: Greeting\n---\nHello $1; $ARGUMENTS\n")
	write(filepath.Join(dir, "AGENTS.md"), "Project instruction: keep the public API stable.\n")

	// Paths are explicit. The SDK never reads the user's home directory.
	resources, err := codingagent.LoadResources(codingagent.ResourceOptions{
		Cwd:           dir,
		SkillPaths:    []string{filepath.Join(dir, "skills")},
		TemplatePaths: []string{filepath.Join(dir, "prompts")},
		ContextFiles:  []string{filepath.Join(dir, "AGENTS.md")},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "load resources:", err)
		os.Exit(1)
	}

	fmt.Println("skills:", len(resources.Skills))
	for _, skill := range resources.Skills {
		fmt.Printf("  %s: %s\n", skill.Name, skill.Description)
	}
	fmt.Println("templates:", len(resources.Templates))
	for _, template := range resources.Templates {
		fmt.Printf("  %s: %s\n", template.Name, template.Description)
	}
	fmt.Println("context files:", len(resources.ContextFiles))
	fmt.Println("system prompt contains project instruction:", strings.Contains(resources.SystemPrompt, "keep the public API stable"))

	if len(resources.Templates) > 0 {
		expanded, err := codingagent.ExpandTemplate(resources.Templates[0], []string{"Ada", "Lovelace"})
		if err != nil {
			fmt.Fprintln(os.Stderr, "expand template:", err)
			os.Exit(1)
		}
		fmt.Println("expanded:", expanded)
	}
}

func write(path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
