// Package resources is the Go port of the harness resource loaders from
// packages/agent/src/harness/prompt-templates.ts, skills.ts and
// system-prompt.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package resources

import (
	"strings"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// FormatSkillsForSystemPrompt renders the model-visible skill list.
//
// Skills with DisableModelInvocation set to true are skipped. An empty visible
// list renders the empty string. All rendered fields are XML-escaped.
func FormatSkillsForSystemPrompt(skills []harnesstypes.Skill) string {
	visible := make([]harnesstypes.Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.DisableModelInvocation != nil && *skill.DisableModelInvocation {
			continue
		}
		visible = append(visible, skill)
	}
	if len(visible) == 0 {
		return ""
	}

	lines := []string{
		"The following skills provide specialized instructions for specific tasks.",
		"Read the full skill file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	for _, skill := range visible {
		lines = append(lines, "  <skill>")
		lines = append(lines, "    <name>"+escapeXML(skill.Name)+"</name>")
		lines = append(lines, "    <description>"+escapeXML(skill.Description)+"</description>")
		lines = append(lines, "    <location>"+escapeXML(skill.FilePath)+"</location>")
		lines = append(lines, "  </skill>")
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

func escapeXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}
