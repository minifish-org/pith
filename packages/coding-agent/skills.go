// Skill discovery for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/skills.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Discovery rules (unchanged from the source): a directory that contains a
// SKILL.md is a skill root and is not recursed into; otherwise direct root .md
// children with a description are loaded and subdirectories are searched
// recursively for SKILL.md. `.gitignore`/`.ignore`/`.fdignore` rules are
// honored. Duplicate names are reported as collision diagnostics and the first
// skill wins. Skill bodies are plain text: discovery never executes fenced
// commands or any other content.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	gitignore "github.com/sabhiram/go-gitignore"
)

const (
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

var skillIgnoreFileNames = []string{".gitignore", ".ignore", ".fdignore"}

// SkillFrontmatter is the parsed frontmatter of a skill file.
type SkillFrontmatter struct {
	Name                   string
	Description            string
	DisableModelInvocation bool
	Extra                  map[string]any
}

// Skill is a discovered skill. Body is the markdown body after frontmatter and
// is only loaded as data; it is never executed at discovery time.
type Skill struct {
	Name                   string     `json:"name"`
	Description            string     `json:"description"`
	File                   string     `json:"file"`
	Body                   string     `json:"body"`
	BaseDir                string     `json:"baseDir,omitempty"`
	DisableModelInvocation bool       `json:"disableModelInvocation,omitempty"`
	SourceInfo             SourceInfo `json:"sourceInfo,omitempty"`
}

// LoadSkillsResult is the result of loading skills.
type LoadSkillsResult struct {
	Skills      []Skill              `json:"skills"`
	Diagnostics []ResourceDiagnostic `json:"diagnostics"`
}

// LoadSkillsFromDirOptions configures LoadSkillsFromDir.
type LoadSkillsFromDirOptions struct {
	Dir    string
	Source string
}

// LoadSkillsOptions configures LoadSkills.
type LoadSkillsOptions struct {
	Cwd             string
	AgentDir        string
	SkillPaths      []string
	IncludeDefaults bool
}

func validateSkillName(name string) []string {
	errs := []string{}
	if utf8.RuneCountInString(name) > maxSkillNameLength {
		errs = append(errs, "name exceeds 64 characters ("+itoa(utf8.RuneCountInString(name))+")")
	}
	if !isValidSkillName(name) {
		errs = append(errs, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errs = append(errs, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errs = append(errs, "name must not contain consecutive hyphens")
	}
	return errs
}

func validateSkillDescription(description string, hasDescription bool) []string {
	errs := []string{}
	if !hasDescription || strings.TrimSpace(description) == "" {
		errs = append(errs, "description is required")
	} else if utf8.RuneCountInString(description) > maxSkillDescriptionLength {
		errs = append(errs, "description exceeds 1024 characters ("+itoa(utf8.RuneCountInString(description))+")")
	}
	return errs
}

func isValidSkillName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if char >= '0' && char <= '9' {
			continue
		}
		if char == '-' {
			continue
		}
		return false
	}
	return true
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// FormatSkillsForPrompt renders the Agent Skills XML block for a system
// prompt. Skills with DisableModelInvocation are omitted. The returned string
// keeps the source's leading blank lines; callers trim as needed.
func FormatSkillsForPrompt(skills []Skill, fileReadTool string) string {
	visible := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.DisableModelInvocation {
			continue
		}
		visible = append(visible, skill)
	}
	if len(visible) == 0 {
		return ""
	}
	if fileReadTool == "" {
		fileReadTool = "read"
	}

	lines := []string{
		"",
		"",
		"The following skills provide specialized instructions for specific tasks.",
	}
	if fileReadTool == "read" {
		lines = append(lines, "Use the read tool to load a skill's file when the task matches its description.")
	} else {
		lines = append(lines, "Use bash to load a skill's file when the task matches its description.")
	}
	lines = append(lines,
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	)
	for _, skill := range visible {
		lines = append(lines, "  <skill>")
		lines = append(lines, "    <name>"+escapeSkillXML(skill.Name)+"</name>")
		lines = append(lines, "    <description>"+escapeSkillXML(skill.Description)+"</description>")
		lines = append(lines, "    <location>"+escapeSkillXML(skill.File)+"</location>")
		lines = append(lines, "  </skill>")
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

func escapeSkillXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

type skillWalkState struct {
	patterns []string
	ignores  *gitignore.GitIgnore
	visited  map[string]bool
}

func newSkillWalkState() *skillWalkState {
	return &skillWalkState{
		ignores: gitignore.CompileIgnoreLines(),
		visited: map[string]bool{},
	}
}

func (s *skillWalkState) addIgnorePatterns(patterns []string) {
	if len(patterns) == 0 {
		return
	}
	s.patterns = append(s.patterns, patterns...)
	s.ignores = gitignore.CompileIgnoreLines(s.patterns...)
}

func skillIgnorePattern(line string, prefix string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, `\#`) {
		return ""
	}
	pattern := line
	negated := false
	if strings.HasPrefix(pattern, "!") {
		negated = true
		pattern = pattern[1:]
	} else if strings.HasPrefix(pattern, `\!`) {
		pattern = pattern[1:]
	}
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
	}
	prefixed := pattern
	if prefix != "" {
		prefixed = prefix + pattern
	}
	if negated {
		return "!" + prefixed
	}
	return prefixed
}

func addSkillIgnoreRules(state *skillWalkState, dir, rootDir string, diagnostics *[]ResourceDiagnostic) {
	relativeDir, err := filepath.Rel(rootDir, dir)
	if err != nil {
		relativeDir = ""
	}
	if relativeDir == "." {
		relativeDir = ""
	}
	prefix := ""
	if relativeDir != "" {
		prefix = filepath.ToSlash(relativeDir) + "/"
	}
	for _, filename := range skillIgnoreFileNames {
		ignorePath := filepath.Join(dir, filename)
		if !fileExists(ignorePath) {
			continue
		}
		content, err := os.ReadFile(ignorePath)
		if err != nil {
			*diagnostics = append(*diagnostics, ResourceDiagnostic{
				Type:    "warning",
				Message: err.Error(),
				Path:    ignorePath,
			})
			continue
		}
		patterns := []string{}
		for _, line := range splitNormalizedLines(string(stripBOM(content))) {
			if pattern := skillIgnorePattern(line, prefix); pattern != "" {
				patterns = append(patterns, pattern)
			}
		}
		if len(patterns) > 0 {
			state.addIgnorePatterns(patterns)
		}
	}
}

func splitNormalizedLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func resolveEntryKind(path string, entry os.DirEntry) (isFile, isDir bool) {
	if entry.Type()&os.ModeSymlink != 0 {
		info, err := os.Stat(path)
		if err != nil {
			return false, false
		}
		return info.Mode().IsRegular(), info.IsDir()
	}
	if entry.IsDir() {
		return false, true
	}
	return entry.Type().IsRegular(), false
}

// LoadSkillsFromDir loads skills from a directory using the recursive SKILL.md
// discovery rules.
func LoadSkillsFromDir(options LoadSkillsFromDirOptions) LoadSkillsResult {
	return loadSkillsFromDirInternal(options.Dir, options.Source, true, newSkillWalkState(), options.Dir)
}

func loadSkillsFromDirInternal(dir, source string, includeRootFiles bool, state *skillWalkState, rootDir string) LoadSkillsResult {
	skills := []Skill{}
	diagnostics := []ResourceDiagnostic{}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	// Avoid symlink loops: never walk the same real directory twice.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		if state.visited[resolved] {
			return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
		}
		state.visited[resolved] = true
	}

	addSkillIgnoreRules(state, dir, rootDir, &diagnostics)

	entries, err := os.ReadDir(dir)
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err.Error(), Path: dir})
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	for _, entry := range entries {
		if entry.Name() != "SKILL.md" {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		isFile, _ := resolveEntryKind(fullPath, entry)
		if !isFile {
			continue
		}
		relativePath, _ := filepath.Rel(rootDir, fullPath)
		if state.ignores.MatchesPath(filepath.ToSlash(relativePath)) {
			continue
		}
		result := loadSkillFromFile(fullPath, source)
		if result.Skill != nil {
			skills = append(skills, *result.Skill)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return compareSkillNames(entries[i].Name(), entries[j].Name()) < 0
	})

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		isFile, isDir := resolveEntryKind(fullPath, entry)
		if !isFile && !isDir {
			continue
		}
		relativePath, _ := filepath.Rel(rootDir, fullPath)
		relativePath = filepath.ToSlash(relativePath)
		ignorePath := relativePath
		if isDir {
			ignorePath = relativePath + "/"
		}
		if state.ignores.MatchesPath(ignorePath) {
			continue
		}
		if isDir {
			sub := loadSkillsFromDirInternal(fullPath, source, false, state, rootDir)
			skills = append(skills, sub.Skills...)
			diagnostics = append(diagnostics, sub.Diagnostics...)
			continue
		}
		if !isFile || !includeRootFiles || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		result := loadSkillFromFile(fullPath, source)
		if result.Skill != nil {
			skills = append(skills, *result.Skill)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
	}

	return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
}

func compareSkillNames(left, right string) int {
	lowerLeft := strings.ToLower(left)
	lowerRight := strings.ToLower(right)
	if lowerLeft < lowerRight {
		return -1
	}
	if lowerLeft > lowerRight {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func createSkillSourceInfo(filePath, baseDir, source string) SourceInfo {
	switch source {
	case "user":
		return CreateSyntheticSourceInfo(filePath, SyntheticSourceInfoOptions{Source: "local", Scope: SourceScopeUser, BaseDir: baseDir})
	case "project":
		return CreateSyntheticSourceInfo(filePath, SyntheticSourceInfoOptions{Source: "local", Scope: SourceScopeProject, BaseDir: baseDir})
	case "path":
		return CreateSyntheticSourceInfo(filePath, SyntheticSourceInfoOptions{Source: "local", BaseDir: baseDir})
	default:
		return CreateSyntheticSourceInfo(filePath, SyntheticSourceInfoOptions{Source: source, BaseDir: baseDir})
	}
}

type loadedSkillFile struct {
	Skill       *Skill
	Diagnostics []ResourceDiagnostic
}

func loadSkillFromFile(filePath, source string) loadedSkillFile {
	diagnostics := []ResourceDiagnostic{}
	isDeclaredSkill := filepath.Base(filePath) == "SKILL.md"

	raw, err := os.ReadFile(filePath)
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err.Error(), Path: filePath})
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}
	}

	frontmatter, body, err := parseFrontmatter(string(stripBOM(raw)))
	if err != nil {
		if isDeclaredSkill {
			diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err.Error(), Path: filePath})
		}
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}
	}

	description := ""
	hasDescription := false
	if value, ok := frontmatter["description"].(string); ok {
		description = value
		hasDescription = true
	}
	if !isDeclaredSkill && (!hasDescription || strings.TrimSpace(description) == "") {
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}
	}

	for _, message := range validateSkillDescription(description, hasDescription) {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: message, Path: filePath})
	}

	name := filepath.Base(filepath.Dir(filePath))
	if value, ok := frontmatter["name"].(string); ok && value != "" {
		name = value
	}
	for _, message := range validateSkillName(name) {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: message, Path: filePath})
	}

	if !hasDescription || strings.TrimSpace(description) == "" {
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}
	}

	skill := Skill{
		Name:        name,
		Description: description,
		File:        filePath,
		Body:        body,
		BaseDir:     filepath.Dir(filePath),
		SourceInfo:  createSkillSourceInfo(filePath, filepath.Dir(filePath), source),
	}
	if disabled, ok := frontmatter["disable-model-invocation"].(bool); ok && disabled {
		skill.DisableModelInvocation = true
	}
	return loadedSkillFile{Skill: &skill, Diagnostics: diagnostics}
}

// LoadSkills loads skills from the default locations (project before user) and
// the explicit paths. The first skill with a name wins; later duplicates are
// reported as collisions.
func LoadSkills(options LoadSkillsOptions) LoadSkillsResult {
	allDiagnostics := []ResourceDiagnostic{}
	collisionDiagnostics := []ResourceDiagnostic{}
	byName := map[string]Skill{}
	realPaths := map[string]bool{}

	addSkills := func(result LoadSkillsResult) {
		allDiagnostics = append(allDiagnostics, result.Diagnostics...)
		for _, skill := range result.Skills {
			realPath := skill.File
			if resolved, err := filepath.EvalSymlinks(skill.File); err == nil {
				realPath = resolved
			}
			if realPaths[realPath] {
				continue
			}
			if existing, ok := byName[skill.Name]; ok {
				collisionDiagnostics = append(collisionDiagnostics, ResourceDiagnostic{
					Type:    "collision",
					Message: "name \"" + skill.Name + "\" collision",
					Path:    skill.File,
					Collision: &ResourceCollision{
						ResourceType: "skill",
						Name:         skill.Name,
						WinnerPath:   existing.File,
						LoserPath:    skill.File,
					},
				})
				continue
			}
			byName[skill.Name] = skill
			realPaths[realPath] = true
		}
	}

	resolvedCwd := resolveExistingPath(options.Cwd)
	resolvedAgentDir := resolveExistingPath(options.AgentDir)

	if options.IncludeDefaults {
		// Project first so project resources win over user resources.
		if resolvedCwd != "" {
			addSkills(loadSkillsFromDirInternal(filepath.Join(resolvedCwd, ConfigDirName, "skills"), "project", true, newSkillWalkState(), filepath.Join(resolvedCwd, ConfigDirName, "skills")))
		}
		if resolvedAgentDir != "" {
			addSkills(loadSkillsFromDirInternal(filepath.Join(resolvedAgentDir, "skills"), "user", true, newSkillWalkState(), filepath.Join(resolvedAgentDir, "skills")))
		}
	}

	userSkillsDir := filepath.Join(resolvedAgentDir, "skills")
	projectSkillsDir := filepath.Join(resolvedCwd, ConfigDirName, "skills")

	getSource := func(resolvedPath string) string {
		if isUnderPath(resolvedPath, userSkillsDir) {
			return "user"
		}
		if isUnderPath(resolvedPath, projectSkillsDir) {
			return "project"
		}
		return "path"
	}

	for _, rawPath := range options.SkillPaths {
		resolvedPath := resolveExistingPath(rawPath)
		if !fileExists(resolvedPath) && !dirExists(resolvedPath) {
			allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
				Type:    "warning",
				Message: "skill path does not exist",
				Path:    resolvedPath,
			})
			continue
		}
		source := getSource(resolvedPath)
		if dirExists(resolvedPath) {
			addSkills(loadSkillsFromDirInternal(resolvedPath, source, true, newSkillWalkState(), resolvedPath))
			continue
		}
		if strings.HasSuffix(resolvedPath, ".md") {
			result := loadSkillFromFile(resolvedPath, source)
			if result.Skill != nil {
				addSkills(LoadSkillsResult{Skills: []Skill{*result.Skill}, Diagnostics: result.Diagnostics})
			} else {
				allDiagnostics = append(allDiagnostics, result.Diagnostics...)
			}
			continue
		}
		allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
			Type:    "warning",
			Message: "skill path is not a markdown file",
			Path:    resolvedPath,
		})
	}

	ordered := make([]Skill, 0, len(byName))
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ordered = append(ordered, byName[name])
	}

	allDiagnostics = append(allDiagnostics, collisionDiagnostics...)
	return LoadSkillsResult{Skills: ordered, Diagnostics: allDiagnostics}
}

func isUnderPath(target, root string) bool {
	if root == "" || target == "" {
		return false
	}
	normalizedRoot := filepath.Clean(root)
	if target == normalizedRoot {
		return true
	}
	return strings.HasPrefix(target, normalizedRoot+string(os.PathSeparator))
}
