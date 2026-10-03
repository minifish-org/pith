// Diff computation utilities shared by the Durable edit tool.
//
// This is a Go port of packages/durable/src/tools/edit-diff.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The file reproduces the observable behavior of the pinned TypeScript
// implementation, including exact/fuzzy text matching, BOM and line-ending
// handling and the display/unified diff formats. The diff engine is a faithful
// port of the Myers algorithm used by the pinned `diff` dependency (line
// tokenization, component grouping and hunk formatting), so generated patches
// stay byte-for-byte compatible with the upstream tool output.
package tools

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

// DetectLineEnding returns the dominant newline spelling of content. It returns
// "\r\n" only when the first line ending is a CRLF pair, otherwise "\n".
func DetectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")
	if lfIdx == -1 {
		return "\n"
	}
	if crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

// NormalizeToLF converts every CRLF and lone CR line ending to LF.
func NormalizeToLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// RestoreLineEndings converts LF line endings back to the requested spelling.
func RestoreLineEndings(text string, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

var fuzzyReplacer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
	"\u201c", "\"", "\u201d", "\"", "\u201e", "\"", "\u201f", "\"",
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-",
	"\u2014", "-", "\u2015", "-", "\u2212", "-",
	"\u00a0", " ",
	"\u2002", " ", "\u2003", " ", "\u2004", " ", "\u2005", " ", "\u2006", " ",
	"\u2007", " ", "\u2008", " ", "\u2009", " ", "\u200a", " ",
	"\u202f", " ", "\u205f", " ", "\u3000", " ",
)

// isFuzzyTrimSpace reports whether r is removed by the upstream
// `String.prototype.trimEnd` call: ECMAScript WhiteSpace plus LineTerminator.
func isFuzzyTrimSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', ' ', '\r', '\n', '\u00a0', '\ufeff', '\u2028', '\u2029':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// NormalizeForFuzzyMatch applies the progressive fuzzy-matching transforms:
// NFKC normalization, trailing-whitespace stripping per line, smart
// quotes/dashes to ASCII and special Unicode spaces to a regular space.
func NormalizeForFuzzyMatch(text string) string {
	normalized := norm.NFKC.String(text)
	lines := strings.Split(normalized, "\n")
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], isFuzzyTrimSpace)
	}
	result := strings.Join(lines, "\n")
	return fuzzyReplacer.Replace(result)
}

// FuzzyMatchResult is the outcome of an exact-then-fuzzy text search.
type FuzzyMatchResult struct {
	// Found reports whether the text matched.
	Found bool
	// Index is the match offset in ContentForReplacement.
	Index int
	// MatchLength is the matched length in ContentForReplacement.
	MatchLength int
	// UsedFuzzyMatch is false for an exact match and true for a fuzzy match.
	UsedFuzzyMatch bool
	// ContentForReplacement is the original content for exact matches and the
	// fuzzy-normalized content for fuzzy matches.
	ContentForReplacement string
}

// Edit is one exact-text replacement request.
type Edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// AppliedEditsResult is the base and rewritten content produced by applying a
// set of edits.
type AppliedEditsResult struct {
	BaseContent string `json:"baseContent"`
	NewContent  string `json:"newContent"`
}

// TextReplacement is a matched replacement in a base-content coordinate space.
type TextReplacement struct {
	MatchIndex  int    `json:"matchIndex"`
	MatchLength int    `json:"matchLength"`
	NewText     string `json:"newText"`
}

// FuzzyFindText finds oldText in content, trying an exact match first and then
// a fuzzy match. The returned offsets index ContentForReplacement.
func FuzzyFindText(content string, oldText string) FuzzyMatchResult {
	exactIndex := strings.Index(content, oldText)
	if exactIndex != -1 {
		return FuzzyMatchResult{
			Found:                 true,
			Index:                 exactIndex,
			MatchLength:           len(oldText),
			UsedFuzzyMatch:        false,
			ContentForReplacement: content,
		}
	}

	fuzzyContent := NormalizeForFuzzyMatch(content)
	fuzzyOldText := NormalizeForFuzzyMatch(oldText)
	fuzzyIndex := strings.Index(fuzzyContent, fuzzyOldText)
	if fuzzyIndex == -1 {
		return FuzzyMatchResult{
			Found:                 false,
			Index:                 -1,
			MatchLength:           0,
			UsedFuzzyMatch:        false,
			ContentForReplacement: content,
		}
	}

	return FuzzyMatchResult{
		Found:                 true,
		Index:                 fuzzyIndex,
		MatchLength:           len(fuzzyOldText),
		UsedFuzzyMatch:        true,
		ContentForReplacement: fuzzyContent,
	}
}

// StripBom removes a leading UTF-8 BOM, returning the BOM (if any) separately.
func StripBom(content string) (bom string, text string) {
	if strings.HasPrefix(content, "\uFEFF") {
		return "\uFEFF", content[len("\uFEFF"):]
	}
	return "", content
}

// splitLinesWithEndings splits content into lines that keep their trailing
// newline. The final line has no newline unless the input ended with one.
func splitLinesWithEndings(content string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

type lineSpan struct {
	start int
	end   int
}

func getLineSpans(content string) []lineSpan {
	lines := splitLinesWithEndings(content)
	spans := make([]lineSpan, len(lines))
	offset := 0
	for i, line := range lines {
		span := lineSpan{start: offset, end: offset + len(line)}
		offset = span.end
		spans[i] = span
	}
	return spans
}

type replacementGroup struct {
	startLine    int
	endLine      int
	replacements []TextReplacement
}

func getReplacementLineRange(lines []lineSpan, replacement TextReplacement) (int, int, error) {
	replacementStart := replacement.MatchIndex
	replacementEnd := replacement.MatchIndex + replacement.MatchLength

	startLine := -1
	for i, line := range lines {
		if replacementStart >= line.start && replacementStart < line.end {
			startLine = i
			break
		}
	}
	if startLine == -1 {
		return 0, 0, fmt.Errorf("Replacement range is outside the base content.")
	}

	endLine := startLine
	for endLine < len(lines) && lines[endLine].end < replacementEnd {
		endLine++
	}
	if endLine >= len(lines) {
		return 0, 0, fmt.Errorf("Replacement range is outside the base content.")
	}

	return startLine, endLine + 1, nil
}

func applyReplacements(content string, replacements []TextReplacement, offset int) string {
	result := content
	for i := len(replacements) - 1; i >= 0; i-- {
		replacement := replacements[i]
		matchIndex := replacement.MatchIndex - offset
		result = result[:matchIndex] + replacement.NewText + result[matchIndex+replacement.MatchLength:]
	}
	return result
}

// ApplyReplacementsPreservingUnchangedLines applies replacements matched against
// baseContent to originalContent while preserving unchanged line blocks from the
// original. Each replacement is widened to the lines it touches; touched lines
// are rewritten from the normalized base and all other lines are copied back
// verbatim, so duplicate normalized lines cannot be aligned to the wrong
// occurrence.
func ApplyReplacementsPreservingUnchangedLines(originalContent string, baseContent string, replacements []TextReplacement) (string, error) {
	originalLines := splitLinesWithEndings(originalContent)
	baseLines := getLineSpans(baseContent)
	if len(originalLines) != len(baseLines) {
		return "", fmt.Errorf("Cannot preserve unchanged lines because the base content has a different line count.")
	}

	groups := make([]replacementGroup, 0, len(replacements))
	sorted := make([]TextReplacement, len(replacements))
	copy(sorted, replacements)
	sortReplacementsByIndex(sorted)
	for _, replacement := range sorted {
		startLine, endLine, err := getReplacementLineRange(baseLines, replacement)
		if err != nil {
			return "", err
		}
		if len(groups) > 0 {
			current := &groups[len(groups)-1]
			if startLine < current.endLine {
				if endLine > current.endLine {
					current.endLine = endLine
				}
				current.replacements = append(current.replacements, replacement)
				continue
			}
		}
		groups = append(groups, replacementGroup{startLine: startLine, endLine: endLine, replacements: []TextReplacement{replacement}})
	}

	var result strings.Builder
	originalLineIndex := 0
	for _, group := range groups {
		if group.startLine > originalLineIndex {
			result.WriteString(strings.Join(originalLines[originalLineIndex:group.startLine], ""))
		}
		groupStartOffset := baseLines[group.startLine].start
		groupEndOffset := baseLines[group.endLine-1].end
		result.WriteString(applyReplacements(baseContent[groupStartOffset:groupEndOffset], group.replacements, groupStartOffset))
		originalLineIndex = group.endLine
	}
	if originalLineIndex < len(originalLines) {
		result.WriteString(strings.Join(originalLines[originalLineIndex:], ""))
	}
	return result.String(), nil
}

func sortReplacementsByIndex(replacements []TextReplacement) {
	for i := 1; i < len(replacements); i++ {
		for j := i; j > 0 && replacements[j-1].MatchIndex > replacements[j].MatchIndex; j-- {
			replacements[j-1], replacements[j] = replacements[j], replacements[j-1]
		}
	}
}

func normalizeEdits(edits []Edit) []Edit {
	normalized := make([]Edit, len(edits))
	for i, edit := range edits {
		normalized[i] = Edit{OldText: NormalizeToLF(edit.OldText), NewText: NormalizeToLF(edit.NewText)}
	}
	return normalized
}

func countOccurrences(content string, oldText string) int {
	fuzzyContent := NormalizeForFuzzyMatch(content)
	fuzzyOldText := NormalizeForFuzzyMatch(oldText)
	if fuzzyOldText == "" {
		// Mirrors the upstream `content.split("").length - 1` behavior, which
		// counts UTF-16 code units rather than Unicode scalar values.
		return len(utf16.Encode([]rune(fuzzyContent))) - 1
	}
	return strings.Count(fuzzyContent, fuzzyOldText)
}

func getNotFoundError(path string, editIndex int, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
	}
	return fmt.Errorf("Could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines.", editIndex, path)
}

func getDuplicateError(path string, editIndex int, totalEdits int, occurrences int) error {
	if totalEdits == 1 {
		return fmt.Errorf("Found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.", occurrences, path)
	}
	return fmt.Errorf("Found %d occurrences of edits[%d] in %s. Each oldText must be unique. Please provide more context to make it unique.", occurrences, editIndex, path)
}

func getEmptyOldTextError(path string, editIndex int, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("oldText must not be empty in %s.", path)
	}
	return fmt.Errorf("edits[%d].oldText must not be empty in %s.", editIndex, path)
}

func getNoChangeError(path string, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("No changes made to %s. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected.", path)
	}
	return fmt.Errorf("No changes made to %s. The replacements produced identical content.", path)
}

type matchedEdit struct {
	editIndex   int
	matchIndex  int
	matchLength int
	newText     string
}

// ApplyEditsToNormalizedContent applies one or more exact-text replacements to
// LF-normalized content. Every edit is matched against the same base content;
// replacements are then applied in reverse order. If any edit needs fuzzy
// matching the operation runs in fuzzy-normalized space and overlays the
// changed lines onto the original so unchanged line blocks keep their bytes.
func ApplyEditsToNormalizedContent(normalizedContent string, edits []Edit, path string) (AppliedEditsResult, error) {
	normalizedEdits := normalizeEdits(edits)

	for i := range normalizedEdits {
		if len(normalizedEdits[i].OldText) == 0 {
			return AppliedEditsResult{}, getEmptyOldTextError(path, i, len(normalizedEdits))
		}
	}

	initialMatches := make([]FuzzyMatchResult, len(normalizedEdits))
	usedFuzzyMatch := false
	for i := range normalizedEdits {
		initialMatches[i] = FuzzyFindText(normalizedContent, normalizedEdits[i].OldText)
		if initialMatches[i].UsedFuzzyMatch {
			usedFuzzyMatch = true
		}
	}
	replacementBaseContent := normalizedContent
	if usedFuzzyMatch {
		replacementBaseContent = NormalizeForFuzzyMatch(normalizedContent)
	}

	matchedEdits := make([]matchedEdit, 0, len(normalizedEdits))
	for i := range normalizedEdits {
		edit := normalizedEdits[i]
		matchResult := FuzzyFindText(replacementBaseContent, edit.OldText)
		if !matchResult.Found {
			return AppliedEditsResult{}, getNotFoundError(path, i, len(normalizedEdits))
		}
		occurrences := countOccurrences(replacementBaseContent, edit.OldText)
		if occurrences > 1 {
			return AppliedEditsResult{}, getDuplicateError(path, i, len(normalizedEdits), occurrences)
		}
		matchedEdits = append(matchedEdits, matchedEdit{
			editIndex:   i,
			matchIndex:  matchResult.Index,
			matchLength: matchResult.MatchLength,
			newText:     edit.NewText,
		})
	}

	sortMatchedEdits(matchedEdits)
	for i := 1; i < len(matchedEdits); i++ {
		previous := matchedEdits[i-1]
		current := matchedEdits[i]
		if previous.matchIndex+previous.matchLength > current.matchIndex {
			return AppliedEditsResult{}, fmt.Errorf(
				"edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions.",
				previous.editIndex, current.editIndex, path,
			)
		}
	}

	baseContent := normalizedContent
	var newContent string
	if usedFuzzyMatch {
		replacements := make([]TextReplacement, len(matchedEdits))
		for i, edit := range matchedEdits {
			replacements[i] = TextReplacement{MatchIndex: edit.matchIndex, MatchLength: edit.matchLength, NewText: edit.newText}
		}
		preserved, err := ApplyReplacementsPreservingUnchangedLines(normalizedContent, replacementBaseContent, replacements)
		if err != nil {
			return AppliedEditsResult{}, err
		}
		newContent = preserved
	} else {
		replacements := make([]TextReplacement, len(matchedEdits))
		for i, edit := range matchedEdits {
			replacements[i] = TextReplacement{MatchIndex: edit.matchIndex, MatchLength: edit.matchLength, NewText: edit.newText}
		}
		newContent = applyReplacements(replacementBaseContent, replacements, 0)
	}

	if baseContent == newContent {
		return AppliedEditsResult{}, getNoChangeError(path, len(normalizedEdits))
	}

	return AppliedEditsResult{BaseContent: baseContent, NewContent: newContent}, nil
}

func sortMatchedEdits(edits []matchedEdit) {
	for i := 1; i < len(edits); i++ {
		for j := i; j > 0 && edits[j-1].matchIndex > edits[j].matchIndex; j-- {
			edits[j-1], edits[j] = edits[j], edits[j-1]
		}
	}
}

// diffChange is one grouped line-diff component.
type diffChange struct {
	value   string
	added   bool
	removed bool
	count   int
	lines   []string
}

// lineTokens mirrors the pinned `diff` library line tokenizer: each token is a
// line including its trailing newline, and empty tokens are dropped.
func lineTokens(value string) []string {
	tokens := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] == '\n' {
			token := value[start : i+1]
			if token != "" {
				tokens = append(tokens, token)
			}
			start = i + 1
		}
	}
	if start < len(value) {
		token := value[start:]
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

type diffComponent struct {
	count             int
	added             bool
	removed           bool
	previousComponent *diffComponent
	value             string
}

type pathState struct {
	oldPos        int
	lastComponent *diffComponent
}

func extractCommon(basePath *pathState, newTokens []string, oldTokens []string, diagonalPath int) int {
	oldPos := basePath.oldPos
	newPos := oldPos - diagonalPath
	commonCount := 0
	for newPos+1 < len(newTokens) && oldPos+1 < len(oldTokens) && oldTokens[oldPos+1] == newTokens[newPos+1] {
		newPos++
		oldPos++
		commonCount++
	}
	if commonCount > 0 {
		basePath.lastComponent = &diffComponent{count: commonCount, previousComponent: basePath.lastComponent}
	}
	basePath.oldPos = oldPos
	return newPos
}

func addToPath(path *pathState, added bool, removed bool, oldPosIncrease int) *pathState {
	last := path.lastComponent
	if last != nil && last.added == added && last.removed == removed {
		return &pathState{
			oldPos: path.oldPos + oldPosIncrease,
			lastComponent: &diffComponent{
				count:             last.count + 1,
				added:             added,
				removed:           removed,
				previousComponent: last.previousComponent,
			},
		}
	}
	return &pathState{
		oldPos: path.oldPos + oldPosIncrease,
		lastComponent: &diffComponent{
			count:             1,
			added:             added,
			removed:           removed,
			previousComponent: last,
		},
	}
}

// buildDiffValues walks the linked component list, assigns each component its
// joined token text and returns the components in forward order.
func buildDiffValues(lastComponent *diffComponent, newTokens []string, oldTokens []string) []*diffComponent {
	components := make([]*diffComponent, 0, 8)
	for component := lastComponent; component != nil; component = component.previousComponent {
		components = append(components, component)
	}
	for i, j := 0, len(components)-1; i < j; i, j = i+1, j-1 {
		components[i], components[j] = components[j], components[i]
	}

	newPos := 0
	oldPos := 0
	for _, component := range components {
		if !component.removed {
			component.value = strings.Join(newTokens[newPos:newPos+component.count], "")
			newPos += component.count
			if !component.added {
				oldPos += component.count
			}
		} else {
			component.value = strings.Join(oldTokens[oldPos:oldPos+component.count], "")
			oldPos += component.count
		}
	}
	return components
}

func computeDiffComponents(oldTokens []string, newTokens []string) []*diffComponent {
	oldLen := len(oldTokens)
	newLen := len(newTokens)
	bestPath := map[int]*pathState{0: {oldPos: -1}}
	newPos := extractCommon(bestPath[0], newTokens, oldTokens, 0)
	if bestPath[0].oldPos+1 >= oldLen && newPos+1 >= newLen {
		return buildDiffValues(bestPath[0].lastComponent, newTokens, oldTokens)
	}

	editLength := 1
	maxEditLength := newLen + oldLen
	minDiagonal := math.MinInt
	maxDiagonal := math.MaxInt
	for editLength <= maxEditLength {
		lo := minDiagonal
		if -editLength > lo {
			lo = -editLength
		}
		hi := maxDiagonal
		if editLength < hi {
			hi = editLength
		}
		for diagonalPath := lo; diagonalPath <= hi; diagonalPath += 2 {
			removePath := bestPath[diagonalPath-1]
			addPath := bestPath[diagonalPath+1]
			if removePath != nil {
				delete(bestPath, diagonalPath-1)
			}
			canAdd := false
			if addPath != nil {
				addPathNewPos := addPath.oldPos - diagonalPath
				canAdd = 0 <= addPathNewPos && addPathNewPos < newLen
			}
			canRemove := removePath != nil && removePath.oldPos+1 < oldLen
			if !canAdd && !canRemove {
				delete(bestPath, diagonalPath)
				continue
			}
			var basePath *pathState
			if !canRemove || (canAdd && removePath.oldPos < addPath.oldPos) {
				basePath = addToPath(addPath, true, false, 0)
			} else {
				basePath = addToPath(removePath, false, true, 1)
			}
			newPos = extractCommon(basePath, newTokens, oldTokens, diagonalPath)
			if basePath.oldPos+1 >= oldLen && newPos+1 >= newLen {
				return buildDiffValues(basePath.lastComponent, newTokens, oldTokens)
			}
			bestPath[diagonalPath] = basePath
			if basePath.oldPos+1 >= oldLen && diagonalPath-1 < maxDiagonal {
				maxDiagonal = diagonalPath - 1
			}
			if newPos+1 >= newLen && diagonalPath+1 > minDiagonal {
				minDiagonal = diagonalPath + 1
			}
		}
		editLength++
	}
	return nil
}

// diffLines ports the `diffLines` function of the pinned library.
func diffLines(oldStr string, newStr string) []diffChange {
	oldTokens := lineTokens(oldStr)
	newTokens := lineTokens(newStr)
	components := computeDiffComponents(oldTokens, newTokens)
	changes := make([]diffChange, len(components))
	for i, component := range components {
		changes[i] = diffChange{
			value:   component.value,
			added:   component.added,
			removed: component.removed,
			count:   component.count,
		}
	}
	return changes
}

func splitPatchLines(text string) []string {
	hasTrailingNewline := strings.HasSuffix(text, "\n")
	result := strings.Split(text, "\n")
	for i := range result {
		result[i] += "\n"
	}
	if hasTrailingNewline {
		return result[:len(result)-1]
	}
	last := result[len(result)-1]
	result = result[:len(result)-1]
	return append(result, last[:len(last)-1])
}

type diffHunk struct {
	oldStart int
	oldLines int
	newStart int
	newLines int
	lines    []string
}

func contextLines(lines []string) []string {
	context := make([]string, len(lines))
	for i, line := range lines {
		context[i] = " " + line
	}
	return context
}

func buildHunks(oldContent string, newContent string, context int) []diffHunk {
	changes := diffLines(oldContent, newContent)
	changes = append(changes, diffChange{value: "", lines: []string{}})

	hunks := make([]diffHunk, 0, 4)
	oldRangeStart := 0
	newRangeStart := 0
	var curRange []string
	oldLine := 1
	newLine := 1
	for i := 0; i < len(changes); i++ {
		current := &changes[i]
		lines := current.lines
		if lines == nil {
			lines = splitPatchLines(current.value)
			current.lines = lines
		}
		if current.added || current.removed {
			if oldRangeStart == 0 {
				var previous *diffChange
				if i-1 >= 0 {
					previous = &changes[i-1]
				}
				oldRangeStart = oldLine
				newRangeStart = newLine
				if previous != nil {
					if context > 0 {
						prevLines := previous.lines
						if len(prevLines) > context {
							prevLines = prevLines[len(prevLines)-context:]
						}
						curRange = contextLines(prevLines)
					} else {
						curRange = nil
					}
					oldRangeStart -= len(curRange)
					newRangeStart -= len(curRange)
				}
			}
			for _, line := range lines {
				if current.added {
					curRange = append(curRange, "+"+line)
				} else {
					curRange = append(curRange, "-"+line)
				}
			}
			if current.added {
				newLine += len(lines)
			} else {
				oldLine += len(lines)
			}
		} else {
			if oldRangeStart != 0 {
				if len(lines) <= context*2 && i < len(changes)-2 {
					curRange = append(curRange, contextLines(lines)...)
				} else {
					contextSize := len(lines)
					if contextSize > context {
						contextSize = context
					}
					curRange = append(curRange, contextLines(lines[:contextSize])...)
					hunks = append(hunks, diffHunk{
						oldStart: oldRangeStart,
						oldLines: oldLine - oldRangeStart + contextSize,
						newStart: newRangeStart,
						newLines: newLine - newRangeStart + contextSize,
						lines:    curRange,
					})
					oldRangeStart = 0
					newRangeStart = 0
					curRange = nil
				}
			}
			oldLine += len(lines)
			newLine += len(lines)
		}
	}

	for i := range hunks {
		var cleaned []string
		for _, line := range hunks[i].lines {
			if strings.HasSuffix(line, "\n") {
				cleaned = append(cleaned, line[:len(line)-1])
			} else {
				cleaned = append(cleaned, line, "\\ No newline at end of file")
			}
		}
		hunks[i].lines = cleaned
	}
	return hunks
}

func formatUnifiedPatch(path string, hunks []diffHunk) string {
	output := []string{"--- " + path, "+++ " + path}
	for _, hunk := range hunks {
		oldStart := hunk.oldStart
		newStart := hunk.newStart
		if hunk.oldLines == 0 {
			oldStart--
		}
		if hunk.newLines == 0 {
			newStart--
		}
		output = append(output, fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, hunk.oldLines, newStart, hunk.newLines))
		output = append(output, hunk.lines...)
	}
	return strings.Join(output, "\n") + "\n"
}

// GenerateUnifiedPatch generates a standard unified patch for oldContent and
// newContent. The optional contextLines argument defaults to 4.
func GenerateUnifiedPatch(path string, oldContent string, newContent string, contextLines ...int) string {
	context := 4
	if len(contextLines) > 0 {
		context = contextLines[0]
	}
	return formatUnifiedPatch(path, buildHunks(oldContent, newContent, context))
}

// GenerateDiffString generates the display-oriented diff with line numbers and
// context used by the edit tool. It also returns the first changed line in the
// new file (nil when there is no change).
func GenerateDiffString(oldContent string, newContent string, contextLines ...int) (string, *int) {
	context := 4
	if len(contextLines) > 0 {
		context = contextLines[0]
	}
	parts := diffLines(oldContent, newContent)
	output := make([]string, 0, 16)

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	lineNumWidth := len(strconv.Itoa(maxLineNum))

	oldLineNum := 1
	newLineNum := 1
	lastWasChange := false
	var firstChangedLine *int

	for i := 0; i < len(parts); i++ {
		part := parts[i]
		raw := strings.Split(part.value, "\n")
		if len(raw) > 0 && raw[len(raw)-1] == "" {
			raw = raw[:len(raw)-1]
		}

		if part.added || part.removed {
			if firstChangedLine == nil {
				line := newLineNum
				firstChangedLine = &line
			}
			for _, text := range raw {
				if part.added {
					lineNum := padStart(strconv.Itoa(newLineNum), lineNumWidth)
					output = append(output, "+"+lineNum+" "+text)
					newLineNum++
				} else {
					lineNum := padStart(strconv.Itoa(oldLineNum), lineNumWidth)
					output = append(output, "-"+lineNum+" "+text)
					oldLineNum++
				}
			}
			lastWasChange = true
			continue
		}

		nextPartIsChange := i < len(parts)-1 && (parts[i+1].added || parts[i+1].removed)
		hasLeadingChange := lastWasChange
		hasTrailingChange := nextPartIsChange

		switch {
		case hasLeadingChange && hasTrailingChange:
			if len(raw) <= context*2 {
				for _, text := range raw {
					lineNum := padStart(strconv.Itoa(oldLineNum), lineNumWidth)
					output = append(output, " "+lineNum+" "+text)
					oldLineNum++
					newLineNum++
				}
			} else {
				leadingLines := raw[:context]
				trailingLines := raw[len(raw)-context:]
				skippedLines := len(raw) - len(leadingLines) - len(trailingLines)

				for _, text := range leadingLines {
					lineNum := padStart(strconv.Itoa(oldLineNum), lineNumWidth)
					output = append(output, " "+lineNum+" "+text)
					oldLineNum++
					newLineNum++
				}
				output = append(output, " "+strings.Repeat(" ", lineNumWidth)+" ...")
				oldLineNum += skippedLines
				newLineNum += skippedLines
				for _, text := range trailingLines {
					lineNum := padStart(strconv.Itoa(oldLineNum), lineNumWidth)
					output = append(output, " "+lineNum+" "+text)
					oldLineNum++
					newLineNum++
				}
			}
		case hasLeadingChange:
			shownLines := raw
			if len(shownLines) > context {
				shownLines = shownLines[:context]
			}
			skippedLines := len(raw) - len(shownLines)
			for _, text := range shownLines {
				lineNum := padStart(strconv.Itoa(oldLineNum), lineNumWidth)
				output = append(output, " "+lineNum+" "+text)
				oldLineNum++
				newLineNum++
			}
			if skippedLines > 0 {
				output = append(output, " "+strings.Repeat(" ", lineNumWidth)+" ...")
				oldLineNum += skippedLines
				newLineNum += skippedLines
			}
		case hasTrailingChange:
			skippedLines := len(raw) - context
			if skippedLines < 0 {
				skippedLines = 0
			}
			if skippedLines > 0 {
				output = append(output, " "+strings.Repeat(" ", lineNumWidth)+" ...")
				oldLineNum += skippedLines
				newLineNum += skippedLines
			}
			for _, text := range raw[skippedLines:] {
				lineNum := padStart(strconv.Itoa(oldLineNum), lineNumWidth)
				output = append(output, " "+lineNum+" "+text)
				oldLineNum++
				newLineNum++
			}
		default:
			oldLineNum += len(raw)
			newLineNum += len(raw)
		}
		lastWasChange = false
	}

	return strings.Join(output, "\n"), firstChangedLine
}

func padStart(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return strings.Repeat(" ", width-len(value)) + value
}
