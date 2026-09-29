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
// Prompt templates are loaded from explicit `.md` files or direct `.md`
// children of directories. Missing paths and non-markdown files are skipped,
// while read and parse failures are reported as warnings. Frontmatter parsing
// and argument substitution preserve the upstream line and offset rules.
package resources

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	yaml "github.com/goccy/go-yaml"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// PromptTemplateDiagnosticCode is a stable prompt-template diagnostic code.
type PromptTemplateDiagnosticCode string

// Prompt template diagnostic codes.
const (
	PromptTemplateDiagnosticFileInfoFailed PromptTemplateDiagnosticCode = "file_info_failed"
	PromptTemplateDiagnosticListFailed     PromptTemplateDiagnosticCode = "list_failed"
	PromptTemplateDiagnosticReadFailed     PromptTemplateDiagnosticCode = "read_failed"
	PromptTemplateDiagnosticParseFailed    PromptTemplateDiagnosticCode = "parse_failed"
)

// PromptTemplateDiagnostic is a warning produced while loading prompt templates.
type PromptTemplateDiagnostic struct {
	Type    string                       `json:"type"`
	Code    PromptTemplateDiagnosticCode `json:"code"`
	Message string                       `json:"message"`
	Path    string                       `json:"path"`
}

// LoadedPromptTemplates is the result of loading prompt templates.
type LoadedPromptTemplates struct {
	PromptTemplates []harnesstypes.PromptTemplate `json:"promptTemplates"`
	Diagnostics     []PromptTemplateDiagnostic    `json:"diagnostics"`
}

// SourcedPromptInput pairs a path with its application-defined provenance.
type SourcedPromptInput[TSource any] struct {
	Path   string
	Source TSource
}

// SourcedPromptTemplate attaches provenance to a loaded prompt template.
type SourcedPromptTemplate[TSource any, TPromptTemplate any] struct {
	PromptTemplate TPromptTemplate `json:"promptTemplate"`
	Source         TSource         `json:"source"`
}

// SourcedPromptTemplateDiagnostic attaches provenance to a diagnostic.
type SourcedPromptTemplateDiagnostic[TSource any] struct {
	PromptTemplateDiagnostic
	Source TSource `json:"source"`
}

// SourcedPromptTemplates is the result of loading source-tagged templates.
type SourcedPromptTemplates[TSource any, TPromptTemplate any] struct {
	PromptTemplates []SourcedPromptTemplate[TSource, TPromptTemplate] `json:"promptTemplates"`
	Diagnostics     []SourcedPromptTemplateDiagnostic[TSource]        `json:"diagnostics"`
}

// LoadPromptTemplates loads prompt templates from explicit paths.
func LoadPromptTemplates(
	env harnesstypes.ExecutionEnv,
	paths []string,
	ctx harnesstypes.Context,
) (LoadedPromptTemplates, error) {
	promptTemplates := []harnesstypes.PromptTemplate{}
	diagnostics := []PromptTemplateDiagnostic{}
	for _, path := range paths {
		infoResult := env.FileInfo(path, ctx)
		if !infoResult.OK {
			if infoResult.Error.Code != harnesstypes.FileErrorNotFound {
				diagnostics = append(diagnostics, PromptTemplateDiagnostic{
					Type:    "warning",
					Code:    PromptTemplateDiagnosticFileInfoFailed,
					Message: errorMessage(infoResult.Error),
					Path:    path,
				})
			}
			continue
		}
		info := infoResult.Value
		kind, kindErr := resolveKindForTemplate(env, info, &diagnostics, ctx)
		if kindErr != nil {
			return LoadedPromptTemplates{}, kindErr
		}
		if kind == harnesstypes.FileKindDirectory {
			result, err := loadTemplatesFromDir(env, info.Path, ctx)
			if err != nil {
				return LoadedPromptTemplates{}, err
			}
			promptTemplates = append(promptTemplates, result.PromptTemplates...)
			diagnostics = append(diagnostics, result.Diagnostics...)
		} else if kind == harnesstypes.FileKindFile && hasMarkdownSuffix(info.Name) {
			result, err := loadTemplateFromFile(env, info.Path, info.Name, ctx)
			if err != nil {
				return LoadedPromptTemplates{}, err
			}
			if result.PromptTemplate != nil {
				promptTemplates = append(promptTemplates, *result.PromptTemplate)
			}
			diagnostics = append(diagnostics, result.Diagnostics...)
		}
	}
	return LoadedPromptTemplates{PromptTemplates: promptTemplates, Diagnostics: diagnostics}, nil
}

// LoadSourcedPromptTemplates loads prompt templates from source-tagged paths.
func LoadSourcedPromptTemplates[TSource any, TPromptTemplate any](
	env harnesstypes.ExecutionEnv,
	inputs []SourcedPromptInput[TSource],
	mapPromptTemplate func(harnesstypes.PromptTemplate, TSource, harnesstypes.Context) (TPromptTemplate, error),
	ctx harnesstypes.Context,
) (SourcedPromptTemplates[TSource, TPromptTemplate], error) {
	out := SourcedPromptTemplates[TSource, TPromptTemplate]{
		PromptTemplates: []SourcedPromptTemplate[TSource, TPromptTemplate]{},
		Diagnostics:     []SourcedPromptTemplateDiagnostic[TSource]{},
	}
	for _, input := range inputs {
		result, err := LoadPromptTemplates(env, []string{input.Path}, ctx)
		if err != nil {
			return SourcedPromptTemplates[TSource, TPromptTemplate]{}, err
		}
		for _, promptTemplate := range result.PromptTemplates {
			var mapped TPromptTemplate
			if mapPromptTemplate != nil {
				mapped, err = mapPromptTemplate(promptTemplate, input.Source, ctx)
				if err != nil {
					return SourcedPromptTemplates[TSource, TPromptTemplate]{}, err
				}
			} else {
				casted, ok := any(promptTemplate).(TPromptTemplate)
				if !ok {
					return SourcedPromptTemplates[TSource, TPromptTemplate]{}, errors.New("resources: prompt template type mismatch")
				}
				mapped = casted
			}
			out.PromptTemplates = append(out.PromptTemplates, SourcedPromptTemplate[TSource, TPromptTemplate]{
				PromptTemplate: mapped,
				Source:         input.Source,
			})
		}
		for _, diagnostic := range result.Diagnostics {
			out.Diagnostics = append(out.Diagnostics, SourcedPromptTemplateDiagnostic[TSource]{
				PromptTemplateDiagnostic: diagnostic,
				Source:                   input.Source,
			})
		}
	}
	return out, nil
}

type loadedTemplateFile struct {
	PromptTemplate *harnesstypes.PromptTemplate
	Diagnostics    []PromptTemplateDiagnostic
}

func loadTemplatesFromDir(
	env harnesstypes.ExecutionEnv,
	dir string,
	ctx harnesstypes.Context,
) (LoadedPromptTemplates, error) {
	promptTemplates := []harnesstypes.PromptTemplate{}
	diagnostics := []PromptTemplateDiagnostic{}
	entriesResult := env.ListDir(dir, ctx)
	if !entriesResult.OK {
		diagnostics = append(diagnostics, PromptTemplateDiagnostic{
			Type:    "warning",
			Code:    PromptTemplateDiagnosticListFailed,
			Message: errorMessage(entriesResult.Error),
			Path:    dir,
		})
		return LoadedPromptTemplates{PromptTemplates: promptTemplates, Diagnostics: diagnostics}, nil
	}
	entries := append([]harnesstypes.FileInfo(nil), entriesResult.Value...)
	sortFileInfos(entries)

	for _, entry := range entries {
		kind, err := resolveKindForTemplate(env, entry, &diagnostics, ctx)
		if err != nil {
			return LoadedPromptTemplates{}, err
		}
		if kind != harnesstypes.FileKindFile || !hasMarkdownSuffix(entry.Name) {
			continue
		}
		result, err := loadTemplateFromFile(env, entry.Path, entry.Name, ctx)
		if err != nil {
			return LoadedPromptTemplates{}, err
		}
		if result.PromptTemplate != nil {
			promptTemplates = append(promptTemplates, *result.PromptTemplate)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
	}
	return LoadedPromptTemplates{PromptTemplates: promptTemplates, Diagnostics: diagnostics}, nil
}

func loadTemplateFromFile(
	env harnesstypes.ExecutionEnv,
	filePath string,
	fileName string,
	ctx harnesstypes.Context,
) (loadedTemplateFile, error) {
	diagnostics := []PromptTemplateDiagnostic{}
	rawContent := env.ReadTextFile(filePath, ctx)
	if !rawContent.OK {
		diagnostics = append(diagnostics, PromptTemplateDiagnostic{
			Type:    "warning",
			Code:    PromptTemplateDiagnosticReadFailed,
			Message: errorMessage(rawContent.Error),
			Path:    filePath,
		})
		return loadedTemplateFile{PromptTemplate: nil, Diagnostics: diagnostics}, nil
	}

	frontmatter, body, err := parseFrontmatter(rawContent.Value)
	if err != nil {
		diagnostics = append(diagnostics, PromptTemplateDiagnostic{
			Type:    "warning",
			Code:    PromptTemplateDiagnosticParseFailed,
			Message: err.Error(),
			Path:    filePath,
		})
		return loadedTemplateFile{PromptTemplate: nil, Diagnostics: diagnostics}, nil
	}

	var firstLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			firstLine = line
			break
		}
	}
	description := ""
	if value, ok := frontmatter["description"].(string); ok {
		description = value
	}
	if description == "" && firstLine != "" {
		description = firstLine
		if len(firstLine) > 60 {
			description = firstLine[:60] + "..."
		}
	}
	template := harnesstypes.PromptTemplate{
		Name:    stripMarkdownSuffix(fileName),
		Content: body,
	}
	descriptionValue := description
	template.Description = &descriptionValue
	return loadedTemplateFile{PromptTemplate: &template, Diagnostics: diagnostics}, nil
}

func resolveKindForTemplate(
	env harnesstypes.ExecutionEnv,
	info harnesstypes.FileInfo,
	diagnostics *[]PromptTemplateDiagnostic,
	ctx harnesstypes.Context,
) (harnesstypes.FileKind, error) {
	if info.Kind == harnesstypes.FileKindFile || info.Kind == harnesstypes.FileKindDirectory {
		return info.Kind, nil
	}
	canonicalPath := env.CanonicalPath(info.Path, ctx)
	if !canonicalPath.OK {
		if canonicalPath.Error.Code != harnesstypes.FileErrorNotFound {
			*diagnostics = append(*diagnostics, PromptTemplateDiagnostic{
				Type:    "warning",
				Code:    PromptTemplateDiagnosticFileInfoFailed,
				Message: errorMessage(canonicalPath.Error),
				Path:    info.Path,
			})
		}
		return "", nil
	}
	target := env.FileInfo(canonicalPath.Value, ctx)
	if !target.OK {
		if target.Error.Code != harnesstypes.FileErrorNotFound {
			*diagnostics = append(*diagnostics, PromptTemplateDiagnostic{
				Type:    "warning",
				Code:    PromptTemplateDiagnosticFileInfoFailed,
				Message: errorMessage(target.Error),
				Path:    info.Path,
			})
		}
		return "", nil
	}
	return target.Value.Kind, nil
}

// ParseCommandArgs parses an argument string using simple shell-style single
// and double quotes.
func ParseCommandArgs(argsString string) []string {
	args := []string{}
	current := ""
	inQuote := rune(0)
	for _, char := range argsString {
		switch {
		case inQuote != 0:
			if char == inQuote {
				inQuote = 0
			} else {
				current += string(char)
			}
		case char == '"' || char == '\'':
			inQuote = char
		case char == ' ' || char == '\t':
			if current != "" {
				args = append(args, current)
				current = ""
			}
		default:
			current += string(char)
		}
	}
	if current != "" {
		args = append(args, current)
	}
	return args
}

var positionalArgPattern = regexp.MustCompile(`\$(\d+)`)
var slicedArgsPattern = regexp.MustCompile(`\$\{@:(\d+)(?::(\d+))?\}`)

// SubstituteArgs substitutes `$1`, `$@`, `$ARGUMENTS`, `${@:N}` and `${@:N:L}`
// placeholders with command arguments.
func SubstituteArgs(content string, args []string) string {
	result := positionalArgPattern.ReplaceAllStringFunc(content, func(match string) string {
		num, err := strconv.Atoi(match[1:])
		if err != nil {
			return ""
		}
		index := num - 1
		if index < 0 || index >= len(args) {
			return ""
		}
		return args[index]
	})
	result = slicedArgsPattern.ReplaceAllStringFunc(result, func(match string) string {
		groups := slicedArgsPattern.FindStringSubmatch(match)
		start, err := strconv.Atoi(groups[1])
		if err != nil {
			return ""
		}
		start--
		if start < 0 {
			start = 0
		}
		if groups[2] != "" {
			length, err := strconv.Atoi(groups[2])
			if err != nil {
				return ""
			}
			return strings.Join(sliceArgs(args, start, start+length), " ")
		}
		return strings.Join(sliceArgs(args, start, len(args)), " ")
	})
	allArgs := strings.Join(args, " ")
	result = strings.ReplaceAll(result, "$ARGUMENTS", allArgs)
	result = strings.ReplaceAll(result, "$@", allArgs)
	return result
}

func sliceArgs(args []string, start, end int) []string {
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

// FormatPromptTemplateInvocation formats a prompt template invocation with
// positional arguments.
func FormatPromptTemplateInvocation(template harnesstypes.PromptTemplate, args []string) string {
	return SubstituteArgs(template.Content, args)
}

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

func hasMarkdownSuffix(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".md")
}

func stripMarkdownSuffix(name string) string {
	if hasMarkdownSuffix(name) {
		return name[:len(name)-3]
	}
	return name
}

func sortFileInfos(entries []harnesstypes.FileInfo) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && compareNames(entries[j-1].Name, entries[j].Name) > 0; j-- {
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}

// compareNames approximates JavaScript localeCompare for the file ordering
// used by the loaders: case-insensitive primary ordering with a stable
// case-sensitive tie-break.
func compareNames(left, right string) int {
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

func errorMessage(err harnesstypes.FileError) string {
	return err.Message
}
