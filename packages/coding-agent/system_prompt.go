// System prompt construction for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/system-prompt.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. The upstream builder is
// deliberately structured: an ordered map of independently replaceable
// sections that the transcript can later diff and patch. The Go adaptation
// keeps that structure (preamble plus tagged sections) but drops the
// TUI documentation pointers, tool snippet tables and host-specific default
// prompt that only made sense for the interactive Pi binary. The default
// preamble is a short SDK preamble; callers that want the full Pi prompt pass
// it through CustomPrompt.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultSystemPromptPreamble is used when the caller does not supply a custom
// system prompt. It intentionally describes the headless SDK rather than the
// interactive Pi application.
const DefaultSystemPromptPreamble = "You are an expert coding assistant operating inside the Pith embedded SDK. " +
	"You help users by reading files, executing commands, editing code, and writing new files."

// BuildSystemPromptOptions configures BuildSystemPrompt.
type BuildSystemPromptOptions struct {
	// CustomPrompt replaces the default preamble when non-empty. When
	// ExplicitSystemPrompt is set an empty CustomPrompt produces an empty
	// preamble instead of the default.
	CustomPrompt string
	// ExplicitSystemPrompt records that the caller supplied a prompt source,
	// so an empty CustomPrompt is honored verbatim.
	ExplicitSystemPrompt bool
	// ForceSystemPrompt replaces the entire rendered prompt and drops all
	// structured sections. Nil means no forced prompt.
	ForceSystemPrompt *string
	// SelectedTools are the tool names considered active for rule selection.
	SelectedTools []string
	// ToolSnippets are one-line descriptions keyed by tool name.
	ToolSnippets map[string]string
	// ToolGuidelines are guideline bullets keyed by tool name.
	ToolGuidelines map[string][]string
	// PromptGuidelines are extra guideline bullets appended after tool rules.
	PromptGuidelines []string
	// AppendSystemPrompt is text appended before project context and skills.
	AppendSystemPrompt []string
	// Sections are additional XML-wrapped prompt sections keyed by tag name.
	Sections map[string]string
	// Cwd is the working directory rendered as the cwd section.
	Cwd string
	// ContextFiles are pre-loaded AGENTS.md style files.
	ContextFiles []ContextFile
	// Skills are pre-loaded skills rendered into the skills section.
	Skills []Skill
}

// NormalizedBuildSystemPromptOptions is the collection-complete shape exposed
// to callers that want to mutate a copy of the options.
type NormalizedBuildSystemPromptOptions struct {
	CustomPrompt         string
	ExplicitSystemPrompt bool
	ForceSystemPrompt    *string
	SelectedTools        []string
	ToolSnippets         map[string]string
	ToolGuidelines       map[string][]string
	PromptGuidelines     []string
	AppendSystemPrompt   []string
	Sections             map[string]string
	Cwd                  string
	ContextFiles         []ContextFile
	Skills               []Skill
}

// SystemPromptSections is the ordered map of prompt sections. "preamble" is
// untagged text; every other section is wrapped in a tag of the same name.
type SystemPromptSections map[string]string

// SystemPromptState is the prompt state for an input: either an opaque forced
// prompt or the structured section map.
type SystemPromptState struct {
	Content  string
	Sections SystemPromptSections
}

var systemPromptSectionName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// NormalizeBuildSystemPromptOptions copies the input into the mutable,
// collection-complete shape used by the section builder.
func NormalizeBuildSystemPromptOptions(input BuildSystemPromptOptions) NormalizedBuildSystemPromptOptions {
	selectedTools := make([]string, 0, len(input.SelectedTools))
	selectedTools = append(selectedTools, input.SelectedTools...)

	snippets := make(map[string]string, len(input.ToolSnippets))
	for name, snippet := range input.ToolSnippets {
		snippets[name] = snippet
	}

	guidelines := make(map[string][]string, len(input.ToolGuidelines))
	for name, bullets := range input.ToolGuidelines {
		guidelines[name] = append([]string(nil), bullets...)
	}

	sections := make(map[string]string, len(input.Sections))
	for name, content := range input.Sections {
		sections[name] = content
	}

	contextFiles := make([]ContextFile, 0, len(input.ContextFiles))
	for _, file := range input.ContextFiles {
		contextFiles = append(contextFiles, file)
	}

	skills := make([]Skill, 0, len(input.Skills))
	for _, skill := range input.Skills {
		skills = append(skills, skill)
	}

	return NormalizedBuildSystemPromptOptions{
		CustomPrompt:         input.CustomPrompt,
		ExplicitSystemPrompt: input.ExplicitSystemPrompt,
		ForceSystemPrompt:    input.ForceSystemPrompt,
		SelectedTools:        selectedTools,
		ToolSnippets:         snippets,
		ToolGuidelines:       guidelines,
		PromptGuidelines:     append([]string(nil), input.PromptGuidelines...),
		AppendSystemPrompt:   append([]string(nil), input.AppendSystemPrompt...),
		Sections:             sections,
		Cwd:                  input.Cwd,
		ContextFiles:         contextFiles,
		Skills:               skills,
	}
}

func renderProjectContext(contextFiles []ContextFile) string {
	parts := make([]string, 0, len(contextFiles)+1)
	parts = append(parts, "Project-specific instructions and guidelines:")
	for _, file := range contextFiles {
		parts = append(parts, "<project_instructions path=\""+file.Path+"\">\n"+file.Content+"\n</project_instructions>")
	}
	return strings.Join(parts, "\n\n")
}

func buildRules(selectedTools []string, toolGuidelines map[string][]string, promptGuidelines []string) string {
	rules := []string{}
	seen := map[string]bool{}
	addRule := func(rule string) {
		normalized := strings.TrimSpace(rule)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		rules = append(rules, normalized)
	}

	has := func(name string) bool {
		for _, tool := range selectedTools {
			if tool == name {
				return true
			}
		}
		return false
	}
	hasShell := has("bash") || has("powershell")
	if hasShell && !has("grep") && !has("find") && !has("ls") {
		if has("bash") && has("powershell") {
			addRule("Use bash or PowerShell for file operations like listing, searching, and finding files")
		} else if has("powershell") {
			addRule("Use PowerShell for file operations like listing, searching, and finding files")
		} else {
			addRule("Use bash for file operations like ls, rg, find")
		}
	}

	for _, name := range selectedTools {
		for _, rule := range toolGuidelines[name] {
			addRule(rule)
		}
	}
	for _, rule := range promptGuidelines {
		addRule(rule)
	}
	addRule("Be concise in your responses")
	addRule("Show file paths clearly when working with files")

	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	return strings.Join(lines, "\n")
}

// BuildSystemPromptSections builds the ordered, independently replaceable
// sections of the structured system prompt.
func BuildSystemPromptSections(input BuildSystemPromptOptions) (SystemPromptSections, error) {
	options := NormalizeBuildSystemPromptOptions(input)
	for name := range options.Sections {
		if !systemPromptSectionName.MatchString(name) || name == "preamble" {
			return nil, fmt.Errorf("invalid system prompt section name: %s", name)
		}
	}

	promptSections := map[string]string{}
	if options.CustomPrompt != "" {
		promptSections["preamble"] = options.CustomPrompt
	} else if options.ExplicitSystemPrompt {
		promptSections["preamble"] = ""
	} else {
		promptSections["preamble"] = DefaultSystemPromptPreamble
	}

	visibleTools := make([]string, 0, len(options.SelectedTools))
	for _, name := range options.SelectedTools {
		if options.ToolSnippets[name] != "" {
			visibleTools = append(visibleTools, name)
		}
	}
	if len(visibleTools) > 0 {
		lines := make([]string, 0, len(visibleTools))
		for _, name := range visibleTools {
			lines = append(lines, "- "+name+": "+options.ToolSnippets[name])
		}
		promptSections["tools"] = strings.Join(lines, "\n") +
			"\n\nIn addition to the tools above, you may have access to other custom tools depending on the project."
	}
	if rules := buildRules(options.SelectedTools, options.ToolGuidelines, options.PromptGuidelines); rules != "" {
		promptSections["rules"] = rules
	}

	if len(options.AppendSystemPrompt) > 0 {
		promptSections["addendum"] = strings.Join(options.AppendSystemPrompt, "\n\n")
	}
	if len(options.ContextFiles) > 0 {
		promptSections["project_context"] = renderProjectContext(options.ContextFiles)
	}
	if len(options.Skills) > 0 {
		if skillsPrompt := strings.TrimSpace(FormatSkillsForPrompt(options.Skills, "read")); skillsPrompt != "" {
			promptSections["skills"] = skillsPrompt
		}
	}
	if options.Cwd != "" {
		promptSections["cwd"] = strings.ReplaceAll(options.Cwd, "\\", "/")
	}
	for name, content := range options.Sections {
		if content != "" {
			promptSections[name] = content
		}
	}

	sections := SystemPromptSections{"preamble": promptSections["preamble"]}
	for name, content := range promptSections {
		if name == "preamble" {
			continue
		}
		sections[name] = "<" + name + ">\n" + content + "\n</" + name + ">"
	}
	return sections, nil
}

// BuildSystemPromptState returns the complete prompt state for input. A forced
// prompt is opaque; otherwise the structured sections carry the prompt.
func BuildSystemPromptState(input BuildSystemPromptOptions) (SystemPromptState, error) {
	if input.ForceSystemPrompt != nil {
		return SystemPromptState{Content: *input.ForceSystemPrompt}, nil
	}
	sections, err := BuildSystemPromptSections(input)
	if err != nil {
		return SystemPromptState{}, err
	}
	return SystemPromptState{Content: "", Sections: sections}, nil
}

// BuildSystemPrompt renders the system prompt exactly as the transcript's
// system message replays it.
func BuildSystemPrompt(input BuildSystemPromptOptions) (string, error) {
	state, err := BuildSystemPromptState(input)
	if err != nil {
		return "", err
	}
	if state.Sections == nil {
		return state.Content, nil
	}
	order := []string{"preamble", "tools", "rules", "addendum", "project_context", "skills", "cwd"}
	rendered := []string{}
	emitted := map[string]bool{}
	for _, name := range order {
		if content, ok := state.Sections[name]; ok {
			emitted[name] = true
			if name == "preamble" {
				if content != "" {
					rendered = append(rendered, content)
				}
			} else if content != "" {
				rendered = append(rendered, content)
			}
		}
	}
	for name, content := range state.Sections {
		if emitted[name] || name == "preamble" || content == "" {
			continue
		}
		rendered = append(rendered, content)
	}
	return strings.Join(rendered, "\n\n"), nil
}

// DiffSystemPromptSections diffs the sections the model currently has against
// the desired ones. It returns a patch of changed sections and nil when
// nothing changed. A nil pointer value removes the section.
func DiffSystemPromptSections(previous map[string]*string, current SystemPromptSections) map[string]*string {
	patch := map[string]*string{}
	for name, text := range current {
		prev, ok := previous[name]
		if !ok || prev == nil || *prev != text {
			value := text
			patch[name] = &value
		}
	}
	for name := range previous {
		if _, ok := current[name]; !ok {
			patch[name] = nil
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}
