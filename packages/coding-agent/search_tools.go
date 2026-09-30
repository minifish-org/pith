// Search and path tools for the embedded SDK.
//
// This file adapts the coding-agent grep/find/ls tools and the path-utils
// helpers to Go without requiring bundled ripgrep/fd or Node. The default
// backend walks the local filesystem, respects .gitignore/.ignore/.fdignore
// rules, and returns bounded output with explicit truncation metadata. Callers
// may inject a custom operations backend for remote execution.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	gitignore "github.com/sabhiram/go-gitignore"
	"golang.org/x/text/unicode/norm"
)

// maxSearchLines disables the line limit for search output whose row count is
// already bounded by a match/result limit.
const maxSearchLines = int(^uint(0) >> 1)

// ---------------------------------------------------------------------------
// path utilities
// ---------------------------------------------------------------------------

var unicodeSpacePattern = regexp.MustCompile("[\u00a0\u2000-\u200a\u202f\u205f\u3000]")
var amPmPattern = regexp.MustCompile(`(?i) (AM|PM)\.`)

const narrowNoBreakSpace = "\u202f"

var searchIgnoreFileNames = []string{".gitignore", ".ignore", ".fdignore"}

// PathExists reports whether a path exists. It follows symlinks, matching the
// upstream access(F_OK) probe.
func PathExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// ExpandPath normalizes unicode spaces and strips a leading @ from a path.
func ExpandPath(filePath string) string {
	normalized := unicodeSpacePattern.ReplaceAllString(filePath, " ")
	if strings.HasPrefix(normalized, "@") {
		return normalized[1:]
	}
	return normalized
}

func homeDirectory() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return os.Getenv("HOME")
}

func fileURLToFilesystemPath(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "//" + parsed.Host + parsed.Path, true
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", false
	}
	if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' && decoded[2] == ':' {
		decoded = decoded[1:]
	}
	return decoded, true
}

func resolvePathAgainstCwd(filePath, cwd string) string {
	normalized := filePath
	switch {
	case normalized == "~":
		normalized = homeDirectory()
	case strings.HasPrefix(normalized, "~/"):
		normalized = filepath.Join(homeDirectory(), normalized[2:])
	case runtime.GOOS == "windows" && strings.HasPrefix(normalized, `~\`):
		normalized = filepath.Join(homeDirectory(), normalized[2:])
	case strings.HasPrefix(normalized, "file://"):
		if converted, ok := fileURLToFilesystemPath(normalized); ok {
			normalized = converted
		}
	}
	if filepath.IsAbs(normalized) {
		return filepath.Clean(normalized)
	}
	return filepath.Join(cwd, normalized)
}

// ResolveToCwd resolves a path against cwd, expanding ~ and file URLs.
func ResolveToCwd(filePath string, cwd string) string {
	return resolvePathAgainstCwd(ExpandPath(filePath), cwd)
}

func readPathVariants(resolved string) []string {
	amPmVariant := amPmPattern.ReplaceAllString(resolved, narrowNoBreakSpace+"$1.")
	nfdVariant := norm.NFD.String(resolved)
	curlyVariant := strings.ReplaceAll(resolved, "'", "\u2019")
	return []string{
		amPmVariant,
		nfdVariant,
		curlyVariant,
		norm.NFD.String(curlyVariant),
	}
}

// ResolveReadPath resolves a read path, probing the known textual variants
// produced by copy/paste on macOS. The first existing variant wins.
func ResolveReadPath(filePath string, cwd string) string {
	resolved := ResolveToCwd(filePath, cwd)
	if PathExists(resolved) {
		return resolved
	}
	for _, variant := range readPathVariants(resolved) {
		if variant != resolved && PathExists(variant) {
			return variant
		}
	}
	return resolved
}

// ResolveReadPathAsync is the context-aware form of ResolveReadPath.
func ResolveReadPathAsync(ctx context.Context, filePath string, cwd string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	resolved := ResolveToCwd(filePath, cwd)
	if PathExists(resolved) {
		return resolved, nil
	}
	for _, variant := range readPathVariants(resolved) {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		if variant != resolved && PathExists(variant) {
			return variant, nil
		}
	}
	return resolved, nil
}

// ---------------------------------------------------------------------------
// filesystem walk with ignore rules
// ---------------------------------------------------------------------------

type searchWalker struct {
	root     string
	patterns []string
	ignores  *gitignore.GitIgnore
}

func newSearchWalker(root string) *searchWalker {
	return &searchWalker{root: root, ignores: gitignore.CompileIgnoreLines()}
}

func normalizeIgnorePattern(line string, prefix string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
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

func (w *searchWalker) pushIgnoreRules(dir string) int {
	saved := len(w.patterns)
	relativeDir, err := filepath.Rel(w.root, dir)
	if err != nil || relativeDir == "." {
		relativeDir = ""
	}
	prefix := ""
	if relativeDir != "" {
		prefix = filepath.ToSlash(relativeDir) + "/"
	}
	for _, filename := range searchIgnoreFileNames {
		content, err := os.ReadFile(filepath.Join(dir, filename))
		if err != nil {
			continue
		}
		normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
		normalized = strings.ReplaceAll(normalized, "\r", "\n")
		for _, line := range strings.Split(normalized, "\n") {
			if pattern := normalizeIgnorePattern(line, prefix); pattern != "" {
				w.patterns = append(w.patterns, pattern)
			}
		}
	}
	w.ignores = gitignore.CompileIgnoreLines(w.patterns...)
	return saved
}

func (w *searchWalker) popIgnoreRules(saved int) {
	w.patterns = w.patterns[:saved]
	w.ignores = gitignore.CompileIgnoreLines(w.patterns...)
}

func (w *searchWalker) ignored(rel string, isDir bool) bool {
	if w.ignores == nil {
		return false
	}
	if isDir {
		return w.ignores.MatchesPath(rel) || w.ignores.MatchesPath(rel+"/")
	}
	return w.ignores.MatchesPath(rel)
}

// walk visits every regular file below dir, honoring nested ignore files. It
// does not follow directory symlinks, so symlink cycles terminate.
func (w *searchWalker) walk(dir string, fn func(path string) error) error {
	saved := w.pushIgnoreRules(dir)
	defer w.popIgnoreRules(saved)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		fullPath := filepath.Join(dir, name)
		relative, err := filepath.Rel(w.root, fullPath)
		if err != nil {
			relative = name
		}
		relative = filepath.ToSlash(relative)
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(fullPath)
			if statErr != nil {
				continue
			}
			if info.IsDir() {
				continue
			}
			isDir = false
		}
		if w.ignored(relative, isDir) {
			continue
		}
		if isDir {
			if walkErr := w.walk(fullPath, fn); walkErr != nil {
				return walkErr
			}
			continue
		}
		if err := fn(fullPath); err != nil {
			return err
		}
	}
	return nil
}

// walkFiles is the convenience entry point that accepts a file or directory.
func (w *searchWalker) walkFiles(root string, fn func(path string) error) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fn(root)
	}
	return w.walk(root, fn)
}

// ---------------------------------------------------------------------------
// glob support
// ---------------------------------------------------------------------------

func searchGlobToRegexp(pattern string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				j := i
				for j < len(pattern) && pattern[j] == '*' {
					j++
				}
				if j < len(pattern) && pattern[j] == '/' {
					builder.WriteString("(?:.*/)?")
					i = j + 1
				} else {
					builder.WriteString(".*")
					i = j
				}
			} else {
				builder.WriteString("[^/]*")
				i++
			}
		case '?':
			builder.WriteString("[^/]")
			i++
		case '[':
			j := i + 1
			if j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^') {
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j >= len(pattern) {
				builder.WriteString(`\[`)
				i++
				continue
			}
			class := pattern[i+1 : j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			builder.WriteString("[" + class + "]")
			i = j + 1
		case '{':
			j := strings.IndexByte(pattern[i:], '}')
			if j < 0 {
				builder.WriteString(`\{`)
				i++
				continue
			}
			options := strings.Split(pattern[i+1:i+j], ",")
			builder.WriteString("(?:")
			for k, option := range options {
				if k > 0 {
					builder.WriteString("|")
				}
				builder.WriteString(regexp.QuoteMeta(option))
			}
			builder.WriteString(")")
			i = i + j + 1
		default:
			builder.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

// matchGlob matches a relative path against a glob. Patterns without a slash
// match the basename; path patterns match the whole relative path.
func matchGlob(pattern, relativePath string) bool {
	relativePath = filepath.ToSlash(relativePath)
	if !strings.Contains(pattern, "/") {
		compiled, err := searchGlobToRegexp(pattern)
		if err != nil {
			return false
		}
		return compiled.MatchString(filepath.Base(relativePath))
	}
	effective := pattern
	if !strings.HasPrefix(effective, "/") && !strings.HasPrefix(effective, "**/") && effective != "**" {
		effective = "**/" + effective
	}
	compiled, err := searchGlobToRegexp(effective)
	if err != nil {
		return false
	}
	return compiled.MatchString(relativePath)
}

// RelativizeFindResultPath relativizes a find result against the search root
// and normalizes it to posix separators.
func RelativizeFindResultPath(resultPath string, searchPath string) string {
	hadTrailingSeparator := strings.HasSuffix(resultPath, string(filepath.Separator)) || strings.HasSuffix(resultPath, "/")
	relativePath := resultPath
	if filepath.IsAbs(resultPath) {
		if resolved, err := filepath.Rel(searchPath, resultPath); err == nil {
			relativePath = resolved
		}
	}
	posixPath := filepath.ToSlash(relativePath)
	if hadTrailingSeparator && !strings.HasSuffix(posixPath, "/") {
		posixPath += "/"
	}
	return posixPath
}

// ---------------------------------------------------------------------------
// grep
// ---------------------------------------------------------------------------

var grepToolSchema = json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Search pattern (regex or literal string)"},"path":{"type":"string","description":"Directory or file to search (default: current directory)"},"glob":{"type":"string","description":"Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"},"ignoreCase":{"type":"boolean","description":"Case-insensitive search (default: false)"},"literal":{"type":"boolean","description":"Treat pattern as literal string instead of regex (default: false)"},"context":{"type":"number","description":"Number of lines to show before and after each match (default: 0)"},"limit":{"type":"number","description":"Maximum number of matches to return (default: 100)"}},"required":["pattern"]}`)

// GrepToolInput is the parsed parameter object for the grep tool.
type GrepToolInput struct {
	Pattern    string  `json:"pattern"`
	Path       *string `json:"path,omitempty"`
	Glob       *string `json:"glob,omitempty"`
	IgnoreCase *bool   `json:"ignoreCase,omitempty"`
	Literal    *bool   `json:"literal,omitempty"`
	Context    *int    `json:"context,omitempty"`
	Limit      *int    `json:"limit,omitempty"`
}

// GrepToolDetails carries truncation and limit metadata for a grep result.
type GrepToolDetails struct {
	Truncation        *truncate.TruncationResult `json:"truncation,omitempty"`
	MatchLimitReached *int                       `json:"matchLimitReached,omitempty"`
	LinesTruncated    *bool                      `json:"linesTruncated,omitempty"`
}

// GrepOperations is a pluggable grep backend.
type GrepOperations interface {
	IsDirectory(absolutePath string) (bool, error)
	ReadFile(absolutePath string) (string, error)
}

// GrepToolOptions configures the grep tool.
type GrepToolOptions struct {
	Operations GrepOperations
}

// GrepToolSystemPromptContribution is the grep prompt metadata.
var GrepToolSystemPromptContribution = SystemPromptContribution{
	Snippet: "Search file contents for patterns (respects .gitignore)",
}

type localGrepOperations struct{}

func (localGrepOperations) IsDirectory(absolutePath string) (bool, error) {
	info, err := os.Stat(absolutePath)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (localGrepOperations) ReadFile(absolutePath string) (string, error) {
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

const defaultGrepLimit = 100

// CreateGrepToolDefinition builds the grep tool definition.
func CreateGrepToolDefinition(cwd string, options *GrepToolOptions) ToolDefinition {
	return ToolDefinition{
		Name:        "grep",
		Description: fmt.Sprintf("Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to %d matches or %dKB (whichever is hit first). Long lines are truncated to %d chars.", defaultGrepLimit, truncate.DefaultMaxBytes/1024, truncate.GrepMaxLineLength),
		Parameters:  grepToolSchema,
		Execute: func(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
			if ctx == nil {
				ctx = context.Background()
			}
			var params GrepToolInput
			if len(arguments) > 0 {
				if err := json.Unmarshal(arguments, &params); err != nil {
					return ToolResult{}, fmt.Errorf("grep: invalid arguments: %w", err)
				}
			}
			ops := GrepOperations(localGrepOperations{})
			if options != nil && options.Operations != nil {
				ops = options.Operations
			}
			return executeGrep(ctx, cwd, ops, params)
		},
	}
}

// CreateGrepTool builds the grep tool as an agent-runtime tool.
func CreateGrepTool(cwd string, options *GrepToolOptions) Tool {
	return WrapToolDefinition(CreateGrepToolDefinition(cwd, options))
}

type grepMatch struct {
	filePath   string
	lineNumber int
	lineText   string
	hasLine    bool
}

func executeGrep(ctx context.Context, cwd string, ops GrepOperations, params GrepToolInput) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	searchPath := cwd
	if params.Path != nil && *params.Path != "" {
		searchPath = ResolveToCwd(*params.Path, cwd)
	}
	isDirectory, err := ops.IsDirectory(searchPath)
	if err != nil {
		return ToolResult{}, fmt.Errorf("Path not found: %s", searchPath)
	}

	matcher, err := compileGrepMatcher(params.Pattern, params.IgnoreCase != nil && *params.IgnoreCase, params.Literal != nil && *params.Literal)
	if err != nil {
		return ToolResult{}, err
	}

	effectiveLimit := defaultGrepLimit
	if params.Limit != nil {
		if *params.Limit > 0 {
			effectiveLimit = *params.Limit
		}
	}
	contextValue := 0
	if params.Context != nil && *params.Context > 0 {
		contextValue = *params.Context
	}

	formatPath := func(filePath string) string {
		if isDirectory {
			if relative, err := filepath.Rel(searchPath, filePath); err == nil && relative != "." && !strings.HasPrefix(relative, "..") {
				return filepath.ToSlash(relative)
			}
		}
		return filepath.Base(filePath)
	}

	var matches []grepMatch
	matchLimitReached := false
	linesTruncated := false
	if params.Glob != nil && *params.Glob != "" {
		glob := *params.Glob
		err = newSearchWalker(searchPath).walkFiles(searchPath, func(path string) error {
			if len(matches) >= effectiveLimit {
				matchLimitReached = true
				return nil
			}
			relative, relErr := filepath.Rel(searchPath, path)
			if relErr != nil {
				relative = filepath.Base(path)
			}
			if !matchGlob(glob, relative) {
				return nil
			}
			return grepFile(ctx, ops, matcher, path, &matches, effectiveLimit, &matchLimitReached)
		})
	} else {
		err = newSearchWalker(searchPath).walkFiles(searchPath, func(path string) error {
			if len(matches) >= effectiveLimit {
				matchLimitReached = true
				return nil
			}
			return grepFile(ctx, ops, matcher, path, &matches, effectiveLimit, &matchLimitReached)
		})
	}
	if err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if len(matches) == 0 {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("No matches found")}}, nil
	}

	fileCache := map[string][]string{}
	getFileLines := func(filePath string) []string {
		if lines, ok := fileCache[filePath]; ok {
			return lines
		}
		content, err := ops.ReadFile(filePath)
		if err != nil {
			fileCache[filePath] = nil
			return nil
		}
		normalized := strings.ReplaceAll(content, "\r\n", "\n")
		normalized = strings.ReplaceAll(normalized, "\r", "\n")
		lines := strings.Split(normalized, "\n")
		fileCache[filePath] = lines
		return lines
	}

	outputLines := []string{}
	for _, match := range matches {
		relativePath := formatPath(match.filePath)
		lines := getFileLines(match.filePath)
		if len(lines) == 0 {
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: (unable to read file)", relativePath, match.lineNumber))
			continue
		}
		if contextValue == 0 {
			text := match.lineText
			if !match.hasLine && match.lineNumber-1 < len(lines) {
				text = lines[match.lineNumber-1]
			}
			text = strings.TrimSuffix(strings.ReplaceAll(text, "\r", ""), "\n")
			truncated, wasTruncated := truncate.TruncateLine(text, truncate.GrepMaxLineLength)
			if wasTruncated {
				linesTruncated = true
			}
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", relativePath, match.lineNumber, truncated))
			continue
		}
		start := match.lineNumber - contextValue
		if start < 1 {
			start = 1
		}
		end := match.lineNumber + contextValue
		if end > len(lines) {
			end = len(lines)
		}
		for current := start; current <= end; current++ {
			lineText := lines[current-1]
			truncated, wasTruncated := truncate.TruncateLine(lineText, truncate.GrepMaxLineLength)
			if wasTruncated {
				linesTruncated = true
			}
			if current == match.lineNumber {
				outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", relativePath, current, truncated))
			} else {
				outputLines = append(outputLines, fmt.Sprintf("%s-%d- %s", relativePath, current, truncated))
			}
		}
	}

	rawOutput := strings.Join(outputLines, "\n")
	truncation := truncate.TruncateHead(rawOutput, truncate.TruncationOptions{MaxLines: intPointer(maxSearchLines)})
	output := truncation.Content
	var details GrepToolDetails
	notices := []string{}
	if matchLimitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", effectiveLimit, effectiveLimit*2))
		details.MatchLimitReached = intPointer(effectiveLimit)
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", truncate.FormatSize(truncate.DefaultMaxBytes)))
		truncationCopy := truncation
		details.Truncation = &truncationCopy
	}
	if linesTruncated {
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", truncate.GrepMaxLineLength))
		details.LinesTruncated = boolPointer(true)
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return buildSearchResult(output, details)
}

func grepFile(ctx context.Context, ops GrepOperations, matcher *regexp.Regexp, path string, matches *[]grepMatch, limit int, limitReached *bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	content, err := ops.ReadFile(path)
	if err != nil {
		return nil
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for index, line := range strings.Split(normalized, "\n") {
		if len(*matches) >= limit {
			*limitReached = true
			return nil
		}
		if matcher.MatchString(line) {
			*matches = append(*matches, grepMatch{filePath: path, lineNumber: index + 1, lineText: line, hasLine: true})
		}
	}
	return nil
}

func compileGrepMatcher(pattern string, ignoreCase bool, literal bool) (*regexp.Regexp, error) {
	expression := pattern
	if literal {
		expression = regexp.QuoteMeta(expression)
	}
	if ignoreCase {
		expression = "(?i)" + expression
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("Invalid pattern: %v", err)
	}
	return compiled, nil
}

// ---------------------------------------------------------------------------
// find
// ---------------------------------------------------------------------------

var findToolSchema = json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"},"path":{"type":"string","description":"Directory to search in (default: current directory)"},"limit":{"type":"number","description":"Maximum number of results (default: 1000)"}},"required":["pattern"]}`)

// FindToolInput is the parsed parameter object for the find tool.
type FindToolInput struct {
	Pattern string  `json:"pattern"`
	Path    *string `json:"path,omitempty"`
	Limit   *int    `json:"limit,omitempty"`
}

// FindToolDetails carries truncation and limit metadata for a find result.
type FindToolDetails struct {
	Truncation         *truncate.TruncationResult `json:"truncation,omitempty"`
	ResultLimitReached *int                       `json:"resultLimitReached,omitempty"`
}

// FindGlobOptions are the options passed to a custom find glob backend.
type FindGlobOptions struct {
	Ignore []string
	Limit  int
}

// FindOperations is a pluggable find backend.
type FindOperations interface {
	Exists(absolutePath string) (bool, error)
	Glob(pattern string, cwd string, options FindGlobOptions) ([]string, error)
}

// FindToolOptions configures the find tool.
type FindToolOptions struct {
	Operations FindOperations
}

// FindToolSystemPromptContribution is the find prompt metadata.
var FindToolSystemPromptContribution = SystemPromptContribution{
	Snippet: "Find files by glob pattern (respects .gitignore)",
}

const defaultFindLimit = 1000

type localFindOperations struct{}

func (localFindOperations) Exists(absolutePath string) (bool, error) {
	return PathExists(absolutePath), nil
}

func (localFindOperations) Glob(pattern string, cwd string, options FindGlobOptions) ([]string, error) {
	results := []string{}
	walker := newSearchWalker(cwd)
	err := walker.walkFiles(cwd, func(path string) error {
		if len(results) >= options.Limit {
			return nil
		}
		relative, err := filepath.Rel(cwd, path)
		if err != nil {
			relative = filepath.Base(path)
		}
		if matchGlob(pattern, relative) {
			results = append(results, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// CreateFindToolDefinition builds the find tool definition.
func CreateFindToolDefinition(cwd string, options *FindToolOptions) ToolDefinition {
	return ToolDefinition{
		Name:        "find",
		Description: fmt.Sprintf("Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to %d results or %dKB (whichever is hit first).", defaultFindLimit, truncate.DefaultMaxBytes/1024),
		Parameters:  findToolSchema,
		Execute: func(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
			if ctx == nil {
				ctx = context.Background()
			}
			var params FindToolInput
			if len(arguments) > 0 {
				if err := json.Unmarshal(arguments, &params); err != nil {
					return ToolResult{}, fmt.Errorf("find: invalid arguments: %w", err)
				}
			}
			var ops FindOperations = localFindOperations{}
			if options != nil && options.Operations != nil {
				ops = options.Operations
			}
			return executeFind(ctx, cwd, ops, params)
		},
	}
}

// CreateFindTool builds the find tool as an agent-runtime tool.
func CreateFindTool(cwd string, options *FindToolOptions) Tool {
	return WrapToolDefinition(CreateFindToolDefinition(cwd, options))
}

func executeFind(ctx context.Context, cwd string, ops FindOperations, params FindToolInput) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	searchPath := cwd
	if params.Path != nil && *params.Path != "" {
		searchPath = ResolveToCwd(*params.Path, cwd)
	}
	exists, err := ops.Exists(searchPath)
	if err != nil {
		return ToolResult{}, err
	}
	if !exists {
		return ToolResult{}, fmt.Errorf("Path not found: %s", searchPath)
	}
	effectiveLimit := defaultFindLimit
	if params.Limit != nil && *params.Limit > 0 {
		effectiveLimit = *params.Limit
	}
	results, err := ops.Glob(params.Pattern, searchPath, FindGlobOptions{
		Ignore: []string{"**/node_modules/**", "**/.git/**"},
		Limit:  effectiveLimit,
	})
	if err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if len(results) == 0 {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("No files found matching pattern")}}, nil
	}
	relativized := make([]string, 0, len(results))
	for _, result := range results {
		relativized = append(relativized, RelativizeFindResultPath(result, searchPath))
	}
	resultLimitReached := len(relativized) >= effectiveLimit
	rawOutput := strings.Join(relativized, "\n")
	truncation := truncate.TruncateHead(rawOutput, truncate.TruncationOptions{MaxLines: intPointer(maxSearchLines)})
	output := truncation.Content
	var details FindToolDetails
	notices := []string{}
	if resultLimitReached {
		notices = append(notices, fmt.Sprintf("%d results limit reached", effectiveLimit))
		details.ResultLimitReached = intPointer(effectiveLimit)
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", truncate.FormatSize(truncate.DefaultMaxBytes)))
		truncationCopy := truncation
		details.Truncation = &truncationCopy
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return buildSearchResult(output, details)
}

// ---------------------------------------------------------------------------
// ls
// ---------------------------------------------------------------------------

var lsToolSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory to list (default: current directory)"},"limit":{"type":"number","description":"Maximum number of entries to return (default: 500)"}}}`)

// LsToolInput is the parsed parameter object for the ls tool.
type LsToolInput struct {
	Path  *string `json:"path,omitempty"`
	Limit *int    `json:"limit,omitempty"`
}

// LsToolDetails carries truncation and limit metadata for an ls result.
type LsToolDetails struct {
	Truncation        *truncate.TruncationResult `json:"truncation,omitempty"`
	EntryLimitReached *int                       `json:"entryLimitReached,omitempty"`
}

// LsStat is the file/directory metadata returned by an ls backend.
type LsStat struct {
	Directory bool
}

// IsDirectory reports whether the entry is a directory.
func (s LsStat) IsDirectory() bool { return s.Directory }

// LsOperations is a pluggable directory listing backend.
type LsOperations interface {
	Exists(absolutePath string) (bool, error)
	Stat(absolutePath string) (LsStat, error)
	ReadDir(absolutePath string) ([]string, error)
}

// LsToolOptions configures the ls tool.
type LsToolOptions struct {
	Operations LsOperations
}

// LsToolSystemPromptContribution is the ls prompt metadata.
var LsToolSystemPromptContribution = SystemPromptContribution{
	Snippet: "List directory contents",
}

const defaultLsLimit = 500

type localLsOperations struct{}

func (localLsOperations) Exists(absolutePath string) (bool, error) {
	return PathExists(absolutePath), nil
}

func (localLsOperations) Stat(absolutePath string) (LsStat, error) {
	info, err := os.Stat(absolutePath)
	if err != nil {
		return LsStat{}, err
	}
	return LsStat{Directory: info.IsDir()}, nil
}

func (localLsOperations) ReadDir(absolutePath string) ([]string, error) {
	entries, err := os.ReadDir(absolutePath)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// CreateLsToolDefinition builds the ls tool definition.
func CreateLsToolDefinition(cwd string, options *LsToolOptions) ToolDefinition {
	return ToolDefinition{
		Name:        "ls",
		Description: fmt.Sprintf("List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is truncated to %d entries or %dKB (whichever is hit first).", defaultLsLimit, truncate.DefaultMaxBytes/1024),
		Parameters:  lsToolSchema,
		Execute: func(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
			if ctx == nil {
				ctx = context.Background()
			}
			var params LsToolInput
			if len(arguments) > 0 {
				if err := json.Unmarshal(arguments, &params); err != nil {
					return ToolResult{}, fmt.Errorf("ls: invalid arguments: %w", err)
				}
			}
			var ops LsOperations = localLsOperations{}
			if options != nil && options.Operations != nil {
				ops = options.Operations
			}
			return executeLs(ctx, cwd, ops, params)
		},
	}
}

// CreateLsTool builds the ls tool as an agent-runtime tool.
func CreateLsTool(cwd string, options *LsToolOptions) Tool {
	return WrapToolDefinition(CreateLsToolDefinition(cwd, options))
}

func executeLs(ctx context.Context, cwd string, ops LsOperations, params LsToolInput) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	dirPath := cwd
	if params.Path != nil && *params.Path != "" {
		dirPath = ResolveToCwd(*params.Path, cwd)
	}
	exists, err := ops.Exists(dirPath)
	if err != nil {
		return ToolResult{}, err
	}
	if !exists {
		return ToolResult{}, fmt.Errorf("Path not found: %s", dirPath)
	}
	stat, err := ops.Stat(dirPath)
	if err != nil {
		return ToolResult{}, err
	}
	if !stat.IsDirectory() {
		return ToolResult{}, fmt.Errorf("Not a directory: %s", dirPath)
	}
	entries, err := ops.ReadDir(dirPath)
	if err != nil {
		return ToolResult{}, fmt.Errorf("Cannot read directory: %v", err)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i]) < strings.ToLower(entries[j])
	})

	effectiveLimit := defaultLsLimit
	if params.Limit != nil && *params.Limit > 0 {
		effectiveLimit = *params.Limit
	}
	results := []string{}
	entryLimitReached := false
	for _, entry := range entries {
		if len(results) >= effectiveLimit {
			entryLimitReached = true
			break
		}
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		fullPath := filepath.Join(dirPath, entry)
		entryStat, statErr := ops.Stat(fullPath)
		if statErr != nil {
			continue
		}
		suffix := ""
		if entryStat.IsDirectory() {
			suffix = "/"
		}
		results = append(results, entry+suffix)
	}
	if len(results) == 0 {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("(empty directory)")}}, nil
	}
	rawOutput := strings.Join(results, "\n")
	truncation := truncate.TruncateHead(rawOutput, truncate.TruncationOptions{MaxLines: intPointer(maxSearchLines)})
	output := truncation.Content
	var details LsToolDetails
	notices := []string{}
	if entryLimitReached {
		notices = append(notices, fmt.Sprintf("%d entries limit reached. Use limit=%d for more", effectiveLimit, effectiveLimit*2))
		details.EntryLimitReached = intPointer(effectiveLimit)
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", truncate.FormatSize(truncate.DefaultMaxBytes)))
		truncationCopy := truncation
		details.Truncation = &truncationCopy
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return buildSearchResult(output, details)
}

// buildSearchResult attaches typed details to a search/ls result.
func buildSearchResult(output string, details any) (ToolResult, error) {
	result := ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock(output)}}
	if details != nil {
		encoded, err := json.Marshal(details)
		if err != nil {
			return ToolResult{}, err
		}
		if string(encoded) != "{}" && string(encoded) != "null" {
			result.Details = encoded
		}
	}
	return result, nil
}

// errSearchUnsupported is reserved for backends that cannot implement an
// operation.
var errSearchUnsupported = errors.New("search operation is not supported")
