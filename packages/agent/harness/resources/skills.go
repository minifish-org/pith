// Package resources is the Go port of the harness resource loaders from
// packages/agent/src/harness/prompt-templates.ts, skills.ts and
// system-prompt.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Skills are discovered by recursively walking input directories, honoring
// `.gitignore`/`.ignore`/`.fdignore` rules and loading `SKILL.md` files plus
// direct root `.md` files with skill frontmatter. Invalid metadata is reported
// as warnings rather than aborting the walk.
package resources

import (
	"strings"
	"unicode/utf8"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	gitignore "github.com/sabhiram/go-gitignore"
)

const (
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

var ignoreFileNames = []string{".gitignore", ".ignore", ".fdignore"}

// SkillDiagnosticCode is a stable skill diagnostic code.
type SkillDiagnosticCode string

// Skill diagnostic codes.
const (
	SkillDiagnosticFileInfoFailed  SkillDiagnosticCode = "file_info_failed"
	SkillDiagnosticListFailed      SkillDiagnosticCode = "list_failed"
	SkillDiagnosticReadFailed      SkillDiagnosticCode = "read_failed"
	SkillDiagnosticParseFailed     SkillDiagnosticCode = "parse_failed"
	SkillDiagnosticInvalidMetadata SkillDiagnosticCode = "invalid_metadata"
)

// SkillDiagnostic is a warning produced while loading skills.
type SkillDiagnostic struct {
	Type    string              `json:"type"`
	Code    SkillDiagnosticCode `json:"code"`
	Message string              `json:"message"`
	Path    string              `json:"path"`
}

// LoadedSkills is the result of loading skills.
type LoadedSkills struct {
	Skills      []harnesstypes.Skill `json:"skills"`
	Diagnostics []SkillDiagnostic    `json:"diagnostics"`
}

// SourcedSkillInput pairs a directory path with its provenance.
type SourcedSkillInput[TSource any] struct {
	Path   string
	Source TSource
}

// SourcedSkill attaches provenance to a loaded skill.
type SourcedSkill[TSource any, TSkill any] struct {
	Skill  TSkill  `json:"skill"`
	Source TSource `json:"source"`
}

// SourcedSkillDiagnostic attaches provenance to a diagnostic.
type SourcedSkillDiagnostic[TSource any] struct {
	SkillDiagnostic
	Source TSource `json:"source"`
}

// SourcedSkills is the result of loading source-tagged skills.
type SourcedSkills[TSource any, TSkill any] struct {
	Skills      []SourcedSkill[TSource, TSkill]   `json:"skills"`
	Diagnostics []SourcedSkillDiagnostic[TSource] `json:"diagnostics"`
}

// FormatSkillInvocation formats a skill invocation prompt, optionally appending
// additional user instructions.
func FormatSkillInvocation(skill harnesstypes.Skill, additionalInstructions *string) string {
	skillBlock := "<skill name=\"" + skill.Name + "\" location=\"" + skill.FilePath + "\">\nReferences are relative to " +
		dirnameEnvPath(skill.FilePath) + ".\n\n" + skill.Content + "\n</skill>"
	if additionalInstructions != nil && *additionalInstructions != "" {
		return skillBlock + "\n\n" + *additionalInstructions
	}
	return skillBlock
}

// LoadSkills loads skills from one or more directories.
func LoadSkills(
	env harnesstypes.ExecutionEnv,
	dirs []string,
	ctx harnesstypes.Context,
) (LoadedSkills, error) {
	skills := []harnesstypes.Skill{}
	diagnostics := []SkillDiagnostic{}
	for _, dir := range dirs {
		rootInfoResult := env.FileInfo(dir, ctx)
		if !rootInfoResult.OK {
			if rootInfoResult.Error.Code != harnesstypes.FileErrorNotFound {
				diagnostics = append(diagnostics, SkillDiagnostic{
					Type:    "warning",
					Code:    SkillDiagnosticFileInfoFailed,
					Message: skillErrorMessage(rootInfoResult.Error),
					Path:    dir,
				})
			}
			continue
		}
		rootInfo := rootInfoResult.Value
		kind, err := resolveKindForSkill(env, rootInfo, &diagnostics, ctx)
		if err != nil {
			return LoadedSkills{}, err
		}
		if kind != harnesstypes.FileKindDirectory {
			continue
		}
		result, err := loadSkillsFromDirInternal(env, rootInfo.Path, true, newIgnoreMatcher(), rootInfo.Path, ctx)
		if err != nil {
			return LoadedSkills{}, err
		}
		skills = append(skills, result.Skills...)
		diagnostics = append(diagnostics, result.Diagnostics...)
	}
	return LoadedSkills{Skills: skills, Diagnostics: diagnostics}, nil
}

// LoadSourcedSkills loads skills from source-tagged directories.
func LoadSourcedSkills[TSource any, TSkill any](
	env harnesstypes.ExecutionEnv,
	inputs []SourcedSkillInput[TSource],
	mapSkill func(harnesstypes.Skill, TSource, harnesstypes.Context) (TSkill, error),
	ctx harnesstypes.Context,
) (SourcedSkills[TSource, TSkill], error) {
	out := SourcedSkills[TSource, TSkill]{
		Skills:      []SourcedSkill[TSource, TSkill]{},
		Diagnostics: []SourcedSkillDiagnostic[TSource]{},
	}
	for _, input := range inputs {
		result, err := LoadSkills(env, []string{input.Path}, ctx)
		if err != nil {
			return SourcedSkills[TSource, TSkill]{}, err
		}
		for _, skill := range result.Skills {
			var mapped TSkill
			if mapSkill != nil {
				mapped, err = mapSkill(skill, input.Source, ctx)
				if err != nil {
					return SourcedSkills[TSource, TSkill]{}, err
				}
			} else {
				casted, ok := any(skill).(TSkill)
				if !ok {
					return SourcedSkills[TSource, TSkill]{}, errSkillTypeMismatch
				}
				mapped = casted
			}
			out.Skills = append(out.Skills, SourcedSkill[TSource, TSkill]{Skill: mapped, Source: input.Source})
		}
		for _, diagnostic := range result.Diagnostics {
			out.Diagnostics = append(out.Diagnostics, SourcedSkillDiagnostic[TSource]{
				SkillDiagnostic: diagnostic,
				Source:          input.Source,
			})
		}
	}
	return out, nil
}

func loadSkillsFromDirInternal(
	env harnesstypes.ExecutionEnv,
	dir string,
	includeRootFiles bool,
	matcher *ignoreMatcher,
	rootDir string,
	ctx harnesstypes.Context,
) (LoadedSkills, error) {
	skills := []harnesstypes.Skill{}
	diagnostics := []SkillDiagnostic{}

	dirInfoResult := env.FileInfo(dir, ctx)
	if !dirInfoResult.OK {
		if dirInfoResult.Error.Code != harnesstypes.FileErrorNotFound {
			diagnostics = append(diagnostics, SkillDiagnostic{
				Type:    "warning",
				Code:    SkillDiagnosticFileInfoFailed,
				Message: skillErrorMessage(dirInfoResult.Error),
				Path:    dir,
			})
		}
		return LoadedSkills{Skills: skills, Diagnostics: diagnostics}, nil
	}
	dirInfo := dirInfoResult.Value
	kind, err := resolveKindForSkill(env, dirInfo, &diagnostics, ctx)
	if err != nil {
		return LoadedSkills{}, err
	}
	if kind != harnesstypes.FileKindDirectory {
		return LoadedSkills{Skills: skills, Diagnostics: diagnostics}, nil
	}

	if err := addIgnoreRules(env, matcher, dir, rootDir, &diagnostics, ctx); err != nil {
		return LoadedSkills{}, err
	}

	entriesResult := env.ListDir(dir, ctx)
	if !entriesResult.OK {
		diagnostics = append(diagnostics, SkillDiagnostic{
			Type:    "warning",
			Code:    SkillDiagnosticListFailed,
			Message: skillErrorMessage(entriesResult.Error),
			Path:    dir,
		})
		return LoadedSkills{Skills: skills, Diagnostics: diagnostics}, nil
	}
	entries := append([]harnesstypes.FileInfo(nil), entriesResult.Value...)

	for _, entry := range entries {
		if entry.Name != "SKILL.md" {
			continue
		}
		fullPath := entry.Path
		entryKind, err := resolveKindForSkill(env, entry, &diagnostics, ctx)
		if err != nil {
			return LoadedSkills{}, err
		}
		if entryKind != harnesstypes.FileKindFile {
			continue
		}
		relPath := relativeEnvPath(rootDir, fullPath)
		if matcher.ignores(relPath) {
			continue
		}
		result, err := loadSkillFromFile(env, fullPath, dirInfo.Name, ctx)
		if err != nil {
			return LoadedSkills{}, err
		}
		if result.Skill != nil {
			skills = append(skills, *result.Skill)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
		return LoadedSkills{Skills: skills, Diagnostics: diagnostics}, nil
	}

	sortFileInfos(entries)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".") || entry.Name == "node_modules" {
			continue
		}
		fullPath := entry.Path
		entryKind, err := resolveKindForSkill(env, entry, &diagnostics, ctx)
		if err != nil {
			return LoadedSkills{}, err
		}
		if entryKind == "" {
			continue
		}
		relPath := relativeEnvPath(rootDir, fullPath)
		ignorePath := relPath
		if entryKind == harnesstypes.FileKindDirectory {
			ignorePath = relPath + "/"
		}
		if matcher.ignores(ignorePath) {
			continue
		}
		if entryKind == harnesstypes.FileKindDirectory {
			result, err := loadSkillsFromDirInternal(env, fullPath, false, matcher, rootDir, ctx)
			if err != nil {
				return LoadedSkills{}, err
			}
			skills = append(skills, result.Skills...)
			diagnostics = append(diagnostics, result.Diagnostics...)
			continue
		}
		if entryKind != harnesstypes.FileKindFile || !includeRootFiles || !hasMarkdownSuffix(entry.Name) {
			continue
		}
		result, err := loadSkillFromFile(env, fullPath, dirInfo.Name, ctx)
		if err != nil {
			return LoadedSkills{}, err
		}
		if result.Skill != nil {
			skills = append(skills, *result.Skill)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
	}

	return LoadedSkills{Skills: skills, Diagnostics: diagnostics}, nil
}

func addIgnoreRules(
	env harnesstypes.ExecutionEnv,
	matcher *ignoreMatcher,
	dir string,
	rootDir string,
	diagnostics *[]SkillDiagnostic,
	ctx harnesstypes.Context,
) error {
	relativeDir := relativeEnvPath(rootDir, dir)
	prefix := ""
	if relativeDir != "" {
		prefix = relativeDir + "/"
	}
	for _, filename := range ignoreFileNames {
		ignorePathResult := env.JoinPath([]string{dir, filename}, ctx)
		if !ignorePathResult.OK {
			*diagnostics = append(*diagnostics, SkillDiagnostic{
				Type:    "warning",
				Code:    SkillDiagnosticFileInfoFailed,
				Message: skillErrorMessage(ignorePathResult.Error),
				Path:    dir,
			})
			continue
		}
		ignorePath := ignorePathResult.Value
		info := env.FileInfo(ignorePath, ctx)
		if !info.OK {
			if info.Error.Code != harnesstypes.FileErrorNotFound {
				*diagnostics = append(*diagnostics, SkillDiagnostic{
					Type:    "warning",
					Code:    SkillDiagnosticFileInfoFailed,
					Message: skillErrorMessage(info.Error),
					Path:    ignorePath,
				})
			}
			continue
		}
		if info.Value.Kind != harnesstypes.FileKindFile {
			continue
		}
		content := env.ReadTextFile(ignorePath, ctx)
		if !content.OK {
			*diagnostics = append(*diagnostics, SkillDiagnostic{
				Type:    "warning",
				Code:    SkillDiagnosticReadFailed,
				Message: skillErrorMessage(content.Error),
				Path:    ignorePath,
			})
			continue
		}
		patterns := []string{}
		for _, line := range splitLines(content.Value) {
			pattern := prefixIgnorePattern(line, prefix)
			if pattern != "" {
				patterns = append(patterns, pattern)
			}
		}
		if len(patterns) > 0 {
			matcher.add(patterns)
		}
	}
	return nil
}

func prefixIgnorePattern(line string, prefix string) string {
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

type loadedSkillFile struct {
	Skill       *harnesstypes.Skill
	Diagnostics []SkillDiagnostic
}

func loadSkillFromFile(
	env harnesstypes.ExecutionEnv,
	filePath string,
	parentDirName string,
	ctx harnesstypes.Context,
) (loadedSkillFile, error) {
	diagnostics := []SkillDiagnostic{}
	isDeclaredSkill := lastPathComponent(filePath) == "SKILL.md"
	rawContent := env.ReadTextFile(filePath, ctx)
	if !rawContent.OK {
		diagnostics = append(diagnostics, SkillDiagnostic{
			Type:    "warning",
			Code:    SkillDiagnosticReadFailed,
			Message: skillErrorMessage(rawContent.Error),
			Path:    filePath,
		})
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}, nil
	}

	frontmatter, body, err := parseFrontmatter(rawContent.Value)
	if err != nil {
		if isDeclaredSkill {
			diagnostics = append(diagnostics, SkillDiagnostic{
				Type:    "warning",
				Code:    SkillDiagnosticParseFailed,
				Message: err.Error(),
				Path:    filePath,
			})
		}
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}, nil
	}

	var description string
	hasDescription := false
	if value, ok := frontmatter["description"].(string); ok {
		description = value
		hasDescription = true
	}
	if !isDeclaredSkill && (!hasDescription || strings.TrimSpace(description) == "") {
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}, nil
	}

	for _, message := range validateDescription(description, hasDescription) {
		diagnostics = append(diagnostics, SkillDiagnostic{
			Type:    "warning",
			Code:    SkillDiagnosticInvalidMetadata,
			Message: message,
			Path:    filePath,
		})
	}

	name := parentDirName
	if value, ok := frontmatter["name"].(string); ok && value != "" {
		name = value
	}
	for _, message := range validateName(name, parentDirName) {
		diagnostics = append(diagnostics, SkillDiagnostic{
			Type:    "warning",
			Code:    SkillDiagnosticInvalidMetadata,
			Message: message,
			Path:    filePath,
		})
	}

	if !hasDescription || strings.TrimSpace(description) == "" {
		return loadedSkillFile{Skill: nil, Diagnostics: diagnostics}, nil
	}

	skill := harnesstypes.Skill{
		Name:        name,
		Description: description,
		Content:     body,
		FilePath:    filePath,
	}
	if disabled, ok := frontmatter["disable-model-invocation"].(bool); ok && disabled {
		skill.DisableModelInvocation = boolPointer(true)
	}
	return loadedSkillFile{Skill: &skill, Diagnostics: diagnostics}, nil
}

func validateName(name string, parentDirName string) []string {
	errors := []string{}
	if name != parentDirName {
		errors = append(errors, `name "`+name+`" does not match parent directory "`+parentDirName+`"`)
	}
	if utf8.RuneCountInString(name) > maxSkillNameLength {
		errors = append(errors, "name exceeds 64 characters ("+itoa(utf8.RuneCountInString(name))+")")
	}
	if !isValidSkillName(name) {
		errors = append(errors, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errors = append(errors, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errors = append(errors, "name must not contain consecutive hyphens")
	}
	return errors
}

func validateDescription(description string, hasDescription bool) []string {
	errors := []string{}
	if !hasDescription || strings.TrimSpace(description) == "" {
		errors = append(errors, "description is required")
	} else if utf8.RuneCountInString(description) > maxSkillDescriptionLength {
		errors = append(errors, "description exceeds 1024 characters ("+itoa(utf8.RuneCountInString(description))+")")
	}
	return errors
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

func resolveKindForSkill(
	env harnesstypes.ExecutionEnv,
	info harnesstypes.FileInfo,
	diagnostics *[]SkillDiagnostic,
	ctx harnesstypes.Context,
) (harnesstypes.FileKind, error) {
	if info.Kind == harnesstypes.FileKindFile || info.Kind == harnesstypes.FileKindDirectory {
		return info.Kind, nil
	}
	canonicalPath := env.CanonicalPath(info.Path, ctx)
	if !canonicalPath.OK {
		if canonicalPath.Error.Code != harnesstypes.FileErrorNotFound {
			*diagnostics = append(*diagnostics, SkillDiagnostic{
				Type:    "warning",
				Code:    SkillDiagnosticFileInfoFailed,
				Message: skillErrorMessage(canonicalPath.Error),
				Path:    info.Path,
			})
		}
		return "", nil
	}
	target := env.FileInfo(canonicalPath.Value, ctx)
	if !target.OK {
		if target.Error.Code != harnesstypes.FileErrorNotFound {
			*diagnostics = append(*diagnostics, SkillDiagnostic{
				Type:    "warning",
				Code:    SkillDiagnosticFileInfoFailed,
				Message: skillErrorMessage(target.Error),
				Path:    info.Path,
			})
		}
		return "", nil
	}
	return target.Value.Kind, nil
}

func dirnameEnvPath(path string) string {
	normalized := strings.TrimRight(path, "/\\")
	separatorIndex := lastSeparator(normalized)
	if separatorIndex == 2 && len(normalized) > 1 && normalized[1] == ':' {
		return normalized[:3]
	}
	if separatorIndex <= 0 {
		return "/"
	}
	return normalized[:separatorIndex]
}

func relativeEnvPath(root string, path string) string {
	normalizedRoot := strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/")
	normalizedPath := strings.TrimRight(strings.ReplaceAll(path, `\`, "/"), "/")
	if normalizedPath == normalizedRoot {
		return ""
	}
	if strings.HasPrefix(normalizedPath, normalizedRoot+"/") {
		return normalizedPath[len(normalizedRoot)+1:]
	}
	return strings.TrimLeft(normalizedPath, "/")
}

func lastSeparator(value string) int {
	last := -1
	for index, char := range value {
		if char == '/' || char == '\\' {
			last = index
		}
	}
	return last
}

func lastPathComponent(path string) string {
	normalized := strings.TrimRight(path, "/\\")
	separatorIndex := lastSeparator(normalized)
	if separatorIndex < 0 {
		return normalized
	}
	return normalized[separatorIndex+1:]
}

func boolPointer(value bool) *bool { return &value }

// itoa avoids importing strconv in this file's hot path signature surface.
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

type ignoreMatcher struct {
	patterns []string
	compiled *gitignore.GitIgnore
}

func newIgnoreMatcher() *ignoreMatcher {
	return &ignoreMatcher{compiled: gitignore.CompileIgnoreLines()}
}

func (m *ignoreMatcher) add(patterns []string) {
	m.patterns = append(m.patterns, patterns...)
	m.compiled = gitignore.CompileIgnoreLines(m.patterns...)
}

func (m *ignoreMatcher) ignores(path string) bool {
	if m == nil || m.compiled == nil || path == "" {
		return false
	}
	return m.compiled.MatchesPath(path)
}

func splitLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func skillErrorMessage(err harnesstypes.FileError) string {
	return err.Message
}

var errSkillTypeMismatch = errorString("resources: skill type mismatch")

type errorString string

func (e errorString) Error() string { return string(e) }
