// Tool discovery for the embedded SDK.
//
// This file ports packages/coding-agent/src/extensions/tool-search/tool.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe: a BM25 ranker over
// tool metadata plus the optional `tool_search` tool. The tool searches the
// codemode and deferred tools that are not active yet and activates the
// matches, so the next model call declares them. Ties keep registration order.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// ToolSearchToolName is the registered name of the discovery tool.
const ToolSearchToolName = "tool_search"

// DefaultToolSearchLimit is the default number of matches.
const DefaultToolSearchLimit = 8

// toolSearchSchema is the parameter schema of the discovery tool.
var toolSearchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": { "type": "string", "description": "Search query for deferred tools." },
    "limit": { "type": "number", "description": "Maximum number of tools to return. Defaults to 8." }
  },
  "required": ["query"]
}`)

// StopWords are dropped during tokenization.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "in": true, "is": true,
	"it": true, "of": true, "on": true, "or": true, "that": true, "the": true,
	"this": true, "to": true, "with": true,
}

// ToolSearchDocument is a tool as the ranker sees it.
type ToolSearchDocument struct {
	Name string
	Text string
}

// ToolSearchMatch is one ranked match.
type ToolSearchMatch struct {
	Name  string
	Score float64
}

// ToolRanker ranks documents for a query.
type ToolRanker interface {
	Rank(query string, documents []ToolSearchDocument, limit int) []ToolSearchMatch
}

// stem reduces a term to a naive singular form.
func stem(term string) string {
	if len(term) > 4 && strings.HasSuffix(term, "ies") {
		return term[:len(term)-3] + "y"
	}
	if len(term) > 4 {
		for _, suffix := range []string{"ches", "shes", "sses", "xes", "zes"} {
			if strings.HasSuffix(term, suffix) {
				return term[:len(term)-2]
			}
		}
	}
	if len(term) > 3 && strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss") {
		return term[:len(term)-1]
	}
	return term
}

// Tokenize lowercases terms, splits camelCase and non-alphanumerics, drops stop
// words and stems the remainder.
func Tokenize(text string) []string {
	split := splitCamel(text)
	split = strings.ToLower(split)
	fields := strings.FieldsFunc(split, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" || stopWords[field] {
			continue
		}
		terms = append(terms, stem(field))
	}
	return terms
}

// splitCamel inserts a space at camelCase and acronym boundaries.
func splitCamel(text string) string {
	var builder strings.Builder
	runes := []rune(text)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := runes[i-1]
			prevLowerOrDigit := (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if prevLowerOrDigit || (prev >= 'A' && prev <= 'Z' && nextLower) {
				builder.WriteRune(' ')
			}
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// CreateToolSearchDocument builds the search text of a tool: its name, the name
// with underscores as spaces, the description and the schema descriptions and
// property names, recursively.
func CreateToolSearchDocument(definition ToolDefinition) ToolSearchDocument {
	parts := []string{definition.Name, strings.ReplaceAll(definition.Name, "_", " "), definition.Description}
	schemaText(definition.Parameters, &parts)
	joined := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			joined = append(joined, part)
		}
	}
	return ToolSearchDocument{Name: definition.Name, Text: strings.Join(joined, " ")}
}

func schemaText(schema json.RawMessage, parts *[]string) {
	if len(schema) == 0 {
		return
	}
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return
	}
	schemaValueText(value, parts)
}

func schemaValueText(value any, parts *[]string) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if description, ok := object["description"].(string); ok {
		*parts = append(*parts, description)
	}
	if properties, ok := object["properties"].(map[string]any); ok {
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			*parts = append(*parts, name)
			schemaValueText(properties[name], parts)
		}
	}
	if items, ok := object["items"]; ok {
		schemaValueText(items, parts)
	}
	if items, ok := object["items"].([]any); ok {
		for _, item := range items {
			schemaValueText(item, parts)
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if variants, ok := object[key].([]any); ok {
			for _, variant := range variants {
				schemaValueText(variant, parts)
			}
		}
	}
}

// Bm25Ranker is the Okapi BM25 ranker. Zero value uses k1=1.2, b=0.75.
type Bm25Ranker struct {
	K1 float64
	B  float64
}

// Rank returns the matching documents, best score first, with stable ties.
func (r Bm25Ranker) Rank(query string, documents []ToolSearchDocument, limit int) []ToolSearchMatch {
	k1 := r.K1
	if k1 == 0 {
		k1 = 1.2
	}
	b := r.B
	if b == 0 {
		b = 0.75
	}
	if limit <= 0 || len(documents) == 0 {
		return nil
	}
	queryTerms := unique(Tokenize(query))
	if len(queryTerms) == 0 {
		return nil
	}
	termCounts := make([]map[string]int, len(documents))
	lengths := make([]int, len(documents))
	totalLength := 0
	for index, document := range documents {
		counts := map[string]int{}
		for _, term := range Tokenize(document.Text) {
			counts[term]++
		}
		termCounts[index] = counts
		length := 0
		for _, count := range counts {
			length += count
		}
		lengths[index] = length
		totalLength += length
	}
	averageLength := float64(totalLength) / float64(len(documents))
	if averageLength == 0 {
		averageLength = 1
	}
	idf := map[string]float64{}
	for _, term := range queryTerms {
		frequency := 0
		for _, counts := range termCounts {
			if counts[term] > 0 {
				frequency++
			}
		}
		idf[term] = math.Log(1 + (float64(len(documents))-float64(frequency)+0.5)/(float64(frequency)+0.5))
	}
	matches := make([]ToolSearchMatch, 0, len(documents))
	for index, document := range documents {
		score := 0.0
		for _, term := range queryTerms {
			count := termCounts[index][term]
			if count == 0 {
				continue
			}
			norm := k1 * (1 - b + (b * float64(lengths[index]) / averageLength))
			score += idf[term] * ((float64(count) * (k1 + 1)) / (float64(count) + norm))
		}
		if score > 0 {
			matches = append(matches, ToolSearchMatch{Name: document.Name, Score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// searchableDefinitions returns the codemode and deferred tools that are not
// active yet, in registration order.
func (r *ToolRegistry) searchableDefinitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		if r.active[name] {
			continue
		}
		definition := r.tools[name]
		switch definition.exposure() {
		case ExposureCodemode, ExposureDeferred:
			out = append(out, definition)
		}
	}
	return out
}

// ToolSearchResultTool is one loaded tool in the result details.
type ToolSearchResultTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CreateToolSearchTool builds the `tool_search` definition bound to a registry.
// Matches are activated so the next model call declares them.
func CreateToolSearchTool(registry *ToolRegistry) (ToolDefinition, error) {
	if registry == nil {
		return ToolDefinition{}, fmt.Errorf("tool_search requires a tool registry")
	}
	return ToolDefinition{
		Name:        ToolSearchToolName,
		Description: toolSearchDescription,
		Parameters:  toolSearchSchema,
		Exposure:    ExposureDirect,
		Execute: func(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
			return executeToolSearch(ctx, registry, arguments)
		},
	}, nil
}

// CreateToolSearchTool builds the `tool_search` definition for this registry.
func (r *ToolRegistry) CreateToolSearchTool() (ToolDefinition, error) {
	return CreateToolSearchTool(r)
}

// SearchTools ranks the searchable, not-yet-active tools and activates the
// matches. It returns the loaded tools in rank order.
func (r *ToolRegistry) SearchTools(query string, limit int) []ToolSearchResultTool {
	if limit <= 0 {
		limit = DefaultToolSearchLimit
	}
	candidates := r.searchableDefinitions()
	documents := make([]ToolSearchDocument, 0, len(candidates))
	for _, candidate := range candidates {
		documents = append(documents, CreateToolSearchDocument(candidate))
	}
	matches := Bm25Ranker{}.Rank(query, documents, limit)
	if len(matches) == 0 {
		return nil
	}
	descriptions := map[string]string{}
	for _, candidate := range candidates {
		descriptions[candidate.Name] = candidate.Description
	}
	loaded := make([]ToolSearchResultTool, 0, len(matches))
	names := append([]string(nil), r.Names()...)
	for _, match := range matches {
		loaded = append(loaded, ToolSearchResultTool{Name: match.Name, Description: descriptions[match.Name]})
		names = append(names, match.Name)
	}
	if err := r.SetActive(names); err != nil {
		return nil
	}
	return loaded
}

func executeToolSearch(ctx context.Context, registry *ToolRegistry, arguments json.RawMessage) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	var input struct {
		Query string   `json:"query"`
		Limit *float64 `json:"limit"`
	}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &input); err != nil {
			return ToolResult{}, fmt.Errorf("tool_search arguments: %w", err)
		}
	}
	if strings.TrimSpace(input.Query) == "" {
		return ToolResult{}, fmt.Errorf("query must not be empty")
	}
	limit := DefaultToolSearchLimit
	if input.Limit != nil {
		if *input.Limit != math.Trunc(*input.Limit) || *input.Limit <= 0 {
			return ToolResult{}, fmt.Errorf("limit must be a positive integer")
		}
		limit = int(*input.Limit)
	}
	loaded := registry.SearchTools(input.Query, limit)

	var text string
	if len(loaded) == 0 {
		text = "No matching tools found."
	} else {
		var builder strings.Builder
		fmt.Fprintf(&builder, "Loaded %d tool(s). They are available from your next call:", len(loaded))
		for _, tool := range loaded {
			firstLine := strings.Split(strings.TrimSpace(tool.Description), "\n")[0]
			fmt.Fprintf(&builder, "\n- %s: %s", tool.Name, firstLine)
		}
		text = builder.String()
	}
	details, _ := json.Marshal(map[string]any{"loaded": loaded})
	return ToolResult{
		Content:           []aitypes.ContentBlock{aitypes.TextBlock(text)},
		Details:           details,
		StructuredContent: details,
	}, nil
}

const toolSearchDescription = "# Tool discovery\n\nSearches over deferred tool metadata with BM25 and exposes matching tools for the next model call.\n\nSome of the tools, such as tools of MCP servers, may not have been provided to you upfront, and you should use this tool (`tool_search`) to search for the required tools. For MCP tool discovery, always use `tool_search`."
