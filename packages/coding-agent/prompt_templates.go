// Prompt template loading and expansion for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/prompt-templates.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Templates are plain markdown with optional YAML frontmatter. Loading only
// reads text; it never executes fenced commands. Argument substitution
// supports $1/$2, $@, $ARGUMENTS, ${N:-default}, ${@:N} and ${@:N:L} in a
// single non-recursive pass so argument values that look like patterns stay
// literal.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	yaml "github.com/goccy/go-yaml"
)

// PromptTemplate is a loaded prompt template. Body is the markdown after
// frontmatter.
type PromptTemplate struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	ArgumentHint string     `json:"argumentHint,omitempty"`
	File         string     `json:"file"`
	Body         string     `json:"body"`
	SourceInfo   SourceInfo `json:"sourceInfo,omitempty"`
}

// LoadPromptTemplatesOptions configures LoadPromptTemplates.
type LoadPromptTemplatesOptions struct {
	Cwd             string
	AgentDir        string
	PromptPaths     []string
	IncludeDefaults bool
}

// LoadPromptTemplatesResult is the result of loading prompt templates.
type LoadPromptTemplatesResult struct {
	Templates   []PromptTemplate     `json:"templates"`
	Diagnostics []ResourceDiagnostic `json:"diagnostics"`
}

// parseFrontmatter splits optional leading YAML frontmatter from a markdown
// body. It mirrors the upstream newline/BOM normalization and returns the body
// trimmed of surrounding whitespace.
func parseFrontmatter(content string) (map[string]any, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if !strings.HasPrefix(normalized, "---") {
		return map[string]any{}, normalized, nil
	}
	relative := strings.Index(normalized[3:], "\n---")
	if relative == -1 {
		return map[string]any{}, normalized, nil
	}
	endIndex := relative + 3
	yamlString := normalized[4:endIndex]
	body := strings.TrimSpace(normalized[endIndex+4:])
	frontmatter := map[string]any{}
	if strings.TrimSpace(yamlString) != "" {
		if err := yaml.Unmarshal([]byte(yamlString), &frontmatter); err != nil {
			return nil, "", err
		}
		if frontmatter == nil {
			frontmatter = map[string]any{}
		}
	}
	return frontmatter, body, nil
}

// ParseCommandArgs parses a command argument string using simple shell-style
// single and double quotes.
func ParseCommandArgs(argsString string) []string {
	args := []string{}
	current := strings.Builder{}
	inQuote := rune(0)
	for _, char := range argsString {
		switch {
		case inQuote != 0:
			if char == inQuote {
				inQuote = 0
			} else {
				current.WriteRune(char)
			}
		case char == '"' || char == '\'':
			inQuote = char
		case unicode.IsSpace(char):
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

var promptArgPattern = regexp.MustCompile(`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(?::(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

// SubstituteArgs substitutes argument placeholders in template content.
//
// Replacement is a single non-recursive pass: patterns inside argument or
// default values are never re-substituted.
func SubstituteArgs(content string, args []string) string {
	allArgs := strings.Join(args, " ")
	return promptArgPattern.ReplaceAllStringFunc(content, func(match string) string {
		groups := promptArgPattern.FindStringSubmatch(match)
		if groups == nil {
			return match
		}
		// Group 1: positional/wildcard target for ${...:-default}.
		if groups[1] != "" {
			value := ""
			if groups[1] == "@" || groups[1] == "ARGUMENTS" {
				value = allArgs
			} else if index, err := strconv.Atoi(groups[1]); err == nil {
				if index-1 >= 0 && index-1 < len(args) {
					value = args[index-1]
				}
			}
			if value != "" {
				return value
			}
			return groups[2]
		}
		// Groups 3/4: ${@:N} and ${@:N:L}.
		if groups[3] != "" {
			start, err := strconv.Atoi(groups[3])
			if err != nil {
				return ""
			}
			start--
			if start < 0 {
				start = 0
			}
			if groups[4] != "" {
				length, err := strconv.Atoi(groups[4])
				if err != nil {
					return ""
				}
				return strings.Join(slicePromptArgs(args, start, start+length), " ")
			}
			return strings.Join(slicePromptArgs(args, start, len(args)), " ")
		}
		// Group 5: $ARGUMENTS, $@ or $N.
		if groups[5] == "ARGUMENTS" || groups[5] == "@" {
			return allArgs
		}
		if index, err := strconv.Atoi(groups[5]); err == nil {
			if index-1 >= 0 && index-1 < len(args) {
				return args[index-1]
			}
		}
		return ""
	})
}

func slicePromptArgs(args []string, start, end int) []string {
	if start > len(args) {
		start = len(args)
	}
	if end > len(args) {
		end = len(args)
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	return args[start:end]
}

func promptTemplateSourceInfo(resolvedPath, globalDir, projectDir string) SourceInfo {
	if isUnderPath(resolvedPath, globalDir) && globalDir != "" {
		return CreateSyntheticSourceInfo(resolvedPath, SyntheticSourceInfoOptions{Source: "local", Scope: SourceScopeUser, BaseDir: globalDir})
	}
	if isUnderPath(resolvedPath, projectDir) && projectDir != "" {
		return CreateSyntheticSourceInfo(resolvedPath, SyntheticSourceInfoOptions{Source: "local", Scope: SourceScopeProject, BaseDir: projectDir})
	}
	baseDir := filepath.Dir(resolvedPath)
	if dirExists(resolvedPath) {
		baseDir = resolvedPath
	}
	return CreateSyntheticSourceInfo(resolvedPath, SyntheticSourceInfoOptions{Source: "local", BaseDir: baseDir})
}

type loadedTemplateFile struct {
	Template    *PromptTemplate
	Diagnostics []ResourceDiagnostic
}

func loadTemplateFromFile(filePath string, sourceInfo SourceInfo) loadedTemplateFile {
	diagnostics := []ResourceDiagnostic{}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err.Error(), Path: filePath})
		return loadedTemplateFile{Template: nil, Diagnostics: diagnostics}
	}

	frontmatter, body, err := parseFrontmatter(string(stripBOM(raw)))
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err.Error(), Path: filePath})
		return loadedTemplateFile{Template: nil, Diagnostics: diagnostics}
	}

	name := strings.TrimSuffix(filepath.Base(filePath), ".md")
	description := ""
	if value, ok := frontmatter["description"].(string); ok {
		description = value
	}
	if description == "" {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			description = line
			if len(line) > 60 {
				description = line[:60] + "..."
			}
			break
		}
	}
	argumentHint := ""
	if value, ok := frontmatter["argument-hint"].(string); ok {
		argumentHint = value
	}

	return loadedTemplateFile{
		Template: &PromptTemplate{
			Name:         name,
			Description:  description,
			ArgumentHint: argumentHint,
			File:         filePath,
			Body:         body,
			SourceInfo:   sourceInfo,
		},
		Diagnostics: diagnostics,
	}
}

func loadTemplatesFromDir(dir string, sourceInfo func(string) SourceInfo) LoadPromptTemplatesResult {
	templates := []PromptTemplate{}
	diagnostics := []ResourceDiagnostic{}
	if !dirExists(dir) {
		return LoadPromptTemplatesResult{Templates: templates, Diagnostics: diagnostics}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err.Error(), Path: dir})
		return LoadPromptTemplatesResult{Templates: templates, Diagnostics: diagnostics}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return compareSkillNames(entries[i].Name(), entries[j].Name()) < 0
	})
	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())
		isFile, _ := resolveEntryKind(fullPath, entry)
		if !isFile || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		result := loadTemplateFromFile(fullPath, sourceInfo(fullPath))
		if result.Template != nil {
			templates = append(templates, *result.Template)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
	}
	return LoadPromptTemplatesResult{Templates: templates, Diagnostics: diagnostics}
}

// LoadPromptTemplates loads prompt templates from the default locations
// (project before user) and the explicit paths. The first template with a name
// wins; later duplicates are reported as collisions.
func LoadPromptTemplates(options LoadPromptTemplatesOptions) LoadPromptTemplatesResult {
	templates := []PromptTemplate{}
	diagnostics := []ResourceDiagnostic{}
	addResult := func(result LoadPromptTemplatesResult) {
		templates = append(templates, result.Templates...)
		diagnostics = append(diagnostics, result.Diagnostics...)
	}

	resolvedCwd := resolveExistingPath(options.Cwd)
	resolvedAgentDir := resolveExistingPath(options.AgentDir)
	globalPromptsDir := filepath.Join(resolvedAgentDir, "prompts")
	projectPromptsDir := filepath.Join(resolvedCwd, ConfigDirName, "prompts")

	getSourceInfo := func(resolvedPath string) SourceInfo {
		return promptTemplateSourceInfo(resolvedPath, globalPromptsDir, projectPromptsDir)
	}

	if options.IncludeDefaults {
		addResult(loadTemplatesFromDir(projectPromptsDir, getSourceInfo))
		addResult(loadTemplatesFromDir(globalPromptsDir, getSourceInfo))
	}

	for _, rawPath := range options.PromptPaths {
		resolvedPath := resolveExistingPath(rawPath)
		if !fileExists(resolvedPath) && !dirExists(resolvedPath) {
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type:    "warning",
				Message: "prompt template path does not exist",
				Path:    resolvedPath,
			})
			continue
		}
		if dirExists(resolvedPath) {
			addResult(loadTemplatesFromDir(resolvedPath, getSourceInfo))
			continue
		}
		if strings.HasSuffix(resolvedPath, ".md") {
			result := loadTemplateFromFile(resolvedPath, getSourceInfo(resolvedPath))
			if result.Template != nil {
				templates = append(templates, *result.Template)
			}
			diagnostics = append(diagnostics, result.Diagnostics...)
		}
	}

	// Collision handling: first name wins.
	seen := map[string]PromptTemplate{}
	deduped := make([]PromptTemplate, 0, len(templates))
	collisions := []ResourceDiagnostic{}
	for _, template := range templates {
		if existing, ok := seen[template.Name]; ok {
			collisions = append(collisions, ResourceDiagnostic{
				Type:    "collision",
				Message: "name \"" + template.Name + "\" collision",
				Path:    template.File,
				Collision: &ResourceCollision{
					ResourceType: "prompt",
					Name:         template.Name,
					WinnerPath:   existing.File,
					LoserPath:    template.File,
				},
			})
			continue
		}
		seen[template.Name] = template
		deduped = append(deduped, template)
	}
	diagnostics = append(diagnostics, collisions...)
	return LoadPromptTemplatesResult{Templates: deduped, Diagnostics: diagnostics}
}

// ExpandPromptTemplate expands a `/name args` command when it matches a loaded
// template, otherwise it returns the input unchanged.
func ExpandPromptTemplate(text string, templates []PromptTemplate) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	trimmed := text[1:]
	name := trimmed
	argsString := ""
	if index := strings.IndexAny(trimmed, " \t\n"); index >= 0 {
		name = trimmed[:index]
		argsString = trimmed[index+1:]
	}
	for _, template := range templates {
		if template.Name == name {
			return SubstituteArgs(template.Body, ParseCommandArgs(argsString))
		}
	}
	return text
}

// ExpandTemplate expands a single prompt template with the supplied arguments.
func ExpandTemplate(template PromptTemplate, args []string) (string, error) {
	return SubstituteArgs(template.Body, args), nil
}
