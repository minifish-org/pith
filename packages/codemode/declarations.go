package codemode

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DefaultInputSchemaMaxChars is the largest rendered input type, in
// characters, before it becomes `unknown`.
const DefaultInputSchemaMaxChars = 16000

// indent is the declaration indentation unit.
const indent = "  "

// maxRefExpansions bounds local $ref expansions per rendered schema.
const maxRefExpansions = 32

// MCPTypescriptPreamble contains the TypeScript types for MCP results so
// `CallToolResult<T>` declarations can refer to them.
const MCPTypescriptPreamble = `type Role = "user" | "assistant";
type MetaObject = Record<string, unknown>;
type Annotations = {
  audience?: Role[];
  priority?: number;
  lastModified?: string;
};
type Icon = {
  src: string;
  mimeType?: string;
  sizes?: string[];
  theme?: "light" | "dark";
};
type TextResourceContents = {
  uri: string;
  mimeType?: string;
  _meta?: MetaObject;
  text: string;
};
type BlobResourceContents = {
  uri: string;
  mimeType?: string;
  _meta?: MetaObject;
  blob: string;
};
type TextContent = {
  type: "text";
  text: string;
  annotations?: Annotations;
  _meta?: MetaObject;
};
type ImageContent = {
  type: "image";
  data: string;
  mimeType: string;
  annotations?: Annotations;
  _meta?: MetaObject;
};
type AudioContent = {
  type: "audio";
  data: string;
  mimeType: string;
  annotations?: Annotations;
  _meta?: MetaObject;
};
type ResourceLink = {
  icons?: Icon[];
  name: string;
  title?: string;
  uri: string;
  description?: string;
  mimeType?: string;
  annotations?: Annotations;
  size?: number;
  _meta?: MetaObject;
  type: "resource_link";
};
type EmbeddedResource = {
  type: "resource";
  resource: TextResourceContents | BlobResourceContents;
  annotations?: Annotations;
  _meta?: MetaObject;
};
type ContentBlock =
  | TextContent
  | ImageContent
  | AudioContent
  | ResourceLink
  | EmbeddedResource;
type CallToolResult<TStructured = { [key: string]: unknown }> = {
  _meta?: MetaObject;
  content: ContentBlock[];
  isError?: boolean;
  structuredContent?: TStructured;
  [key: string]: unknown;
};`

// RenderDeclarations renders TypeScript declarations for the script-visible
// tools. Tools become members of `declare const tools`.
func RenderDeclarations(tools []Tool) (string, error) {
	return renderDeclarations(tools, nil), nil
}

// RenderDeclarationsWithGlobals renders tools and globals. Globals with a
// `namespace.member` name are grouped into a namespace object.
func RenderDeclarationsWithGlobals(tools, globals []Tool) (string, error) {
	return renderDeclarations(tools, globals), nil
}

func renderDeclarations(tools, globals []Tool) string {
	var sections []string
	if len(tools) > 0 {
		members := make([]string, 0, len(tools))
		for _, tool := range tools {
			members = append(members, docComment(tool.Description, indent)+indent+renderToolSignature(tool))
		}
		sections = append(sections, "declare const tools: {\n"+strings.Join(members, "\n")+"\n};")
	}
	namespaceOrder := []string{}
	namespaces := map[string][]string{}
	for _, g := range globals {
		dot := strings.Index(g.Name, ".")
		if dot == -1 {
			sections = append(sections, renderGlobal("declare function "+g.Name, g, ""))
			continue
		}
		namespace := g.Name[:dot]
		members, ok := namespaces[namespace]
		if !ok {
			namespaceOrder = append(namespaceOrder, namespace)
		}
		members = append(members, renderGlobal(g.Name[dot+1:], g, indent))
		namespaces[namespace] = members
	}
	for _, namespace := range namespaceOrder {
		members := namespaces[namespace]
		sections = append(sections, "declare const "+namespace+": {\n"+strings.Join(members, "\n")+"\n};")
	}
	return strings.Join(sections, "\n\n")
}

// RenderToolSignature renders one tool as a member of the `tools` object.
// Input types longer than the default budget render as `unknown`.
func RenderToolSignature(tool Tool) string {
	return renderToolSignature(tool)
}

func renderToolSignature(tool Tool) string {
	input := "unknown"
	if len(tool.InputSchema) > 0 {
		input = SchemaToType(tool.InputSchema, DefaultInputSchemaMaxChars)
	}
	return ToIdentifier(tool.Name) + "(args: " + input + "): Promise<" + renderToolOutputType(tool.OutputSchema) + ">;"
}

// RenderToolSample returns the description followed by the tool's declaration.
func RenderToolSample(tool Tool) string {
	declaration := "declare const tools: { " + renderToolSignature(tool) + " };"
	return strings.TrimSpace(tool.Description) + "\n\ncodemode tool declaration:\n```ts\n" + declaration + "\n```"
}

// RenderToolOutputType returns the type a tool call resolves to.
func RenderToolOutputType(schema json.RawMessage) string {
	return renderToolOutputType(schema)
}

func renderToolOutputType(schema json.RawMessage) string {
	structured, ok := mcpStructuredContentSchemaRaw(schema)
	if ok {
		typ := schemaToTypeAny(structured, nil)
		if typ == "unknown" {
			return "CallToolResult"
		}
		return "CallToolResult<" + typ + ">"
	}
	if len(schema) == 0 {
		return "unknown"
	}
	return SchemaToType(schema, 0)
}

// MCPStructuredContentSchema returns the `structuredContent` schema of an MCP
// `CallToolResult` output schema, `true` when it declares none, and ok=false
// when the schema is not a `CallToolResult`.
func MCPStructuredContentSchema(schema json.RawMessage) (json.RawMessage, bool) {
	structured, ok := mcpStructuredContentSchemaRaw(schema)
	if !ok {
		return nil, false
	}
	b, _ := json.Marshal(structured)
	return b, true
}

func mcpStructuredContentSchemaRaw(schema json.RawMessage) (any, bool) {
	obj := parseSchema(schema)
	if obj == nil {
		return nil, false
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, false
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	content, _ := props["content"].(map[string]any)
	if content == nil || content["type"] != "array" {
		return nil, false
	}
	items, _ := content["items"].(map[string]any)
	if items == nil || items["type"] != "object" {
		return nil, false
	}
	isError, _ := props["isError"].(map[string]any)
	if isError == nil || isError["type"] != "boolean" {
		return nil, false
	}
	meta, _ := props["_meta"].(map[string]any)
	if meta == nil || meta["type"] != "object" {
		return nil, false
	}
	if sc, exists := props["structuredContent"]; exists {
		if _, isObj := sc.(map[string]any); isObj {
			return sc, true
		}
		if _, isBool := sc.(bool); isBool {
			return sc, true
		}
	}
	return true, true
}

func renderGlobal(head string, global Tool, indentStr string) string {
	if global.Signature != "" {
		return docComment(global.Description, indentStr) + indentStr + head + global.Signature + ";"
	}
	input := "unknown"
	if len(global.InputSchema) > 0 {
		input = SchemaToType(global.InputSchema, 0)
	}
	output := "unknown"
	if len(global.OutputSchema) > 0 {
		output = SchemaToType(global.OutputSchema, 0)
	}
	return docComment(global.Description, indentStr) + indentStr + head + "(args: " + input + "): Promise<" + output + ">;"
}

func docComment(description, indentStr string) string {
	text := strings.TrimSpace(description)
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "*/", "*\\/")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 1 {
		return indentStr + "/** " + lines[0] + " */\n"
	}
	var sb strings.Builder
	sb.WriteString(indentStr + "/**\n")
	for _, line := range lines {
		if line != "" {
			sb.WriteString(indentStr + " * " + line + "\n")
		} else {
			sb.WriteString(indentStr + " *\n")
		}
	}
	sb.WriteString(indentStr + " */\n")
	return sb.String()
}

// SchemaToType converts a JSON Schema to a TypeScript type expression. A
// maxChars of 0 disables the length budget.
func SchemaToType(schema json.RawMessage, maxChars int) string {
	typ := schemaToTypeAny(parseSchema(schema), nil)
	if maxChars > 0 && len(typ) > maxChars {
		return "unknown"
	}
	return typ
}

func parseSchema(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return normalizeJSON(v)
}

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeJSON(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeJSON(val)
		}
		return t
	default:
		return v
	}
}

type schemaContext struct {
	root       any
	resolving  map[string]bool
	expansions int
}

func schemaToTypeAny(schema any, ctx *schemaContext) string {
	if ctx == nil {
		ctx = &schemaContext{root: schema, resolving: map[string]bool{}}
	}
	return toType(schema, ctx)
}

func toType(schema any, ctx *schemaContext) string {
	switch s := schema.(type) {
	case bool:
		if s {
			return "unknown"
		}
		return "never"
	case nil:
		return "unknown"
	case map[string]any:
		return objectSchemaToType(s, ctx)
	default:
		return "unknown"
	}
}

func objectSchemaToType(schema map[string]any, ctx *schemaContext) string {
	if ref, ok := schema["$ref"].(string); ok {
		if ctx.resolving[ref] || ctx.expansions >= maxRefExpansions {
			return "unknown"
		}
		target := resolveRef(ref, ctx.root)
		if target == nil {
			return "unknown"
		}
		ctx.expansions++
		ctx.resolving[ref] = true
		defer delete(ctx.resolving, ref)
		return toType(target, ctx)
	}

	if c, ok := schema["const"]; ok {
		return jsStringify(c)
	}
	if enum, ok := schema["enum"].([]any); ok {
		parts := make([]string, 0, len(enum))
		for _, v := range enum {
			parts = append(parts, jsStringify(v))
		}
		return union(parts)
	}

	if variants, ok := schema["anyOf"].([]any); ok {
		return unionTypes(variants, ctx)
	}
	if variants, ok := schema["oneOf"].([]any); ok {
		return unionTypes(variants, ctx)
	}
	if parts, ok := schema["allOf"].([]any); ok {
		rendered := []string{}
		for _, part := range parts {
			t := toType(part, ctx)
			if t != "unknown" {
				rendered = append(rendered, t)
			}
		}
		if len(rendered) == 0 {
			return "unknown"
		}
		for i, p := range rendered {
			if strings.Contains(p, " | ") {
				rendered[i] = "(" + p + ")"
			}
		}
		return strings.Join(rendered, " & ")
	}

	typ, hasType := schema["type"]
	if arr, ok := typ.([]any); ok {
		parts := make([]string, 0, len(arr))
		for _, entry := range arr {
			copied := cloneMap(schema)
			copied["type"] = entry
			parts = append(parts, toType(copied, ctx))
		}
		return union(parts)
	}
	typeName, _ := typ.(string)
	switch typeName {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "null":
		return "null"
	case "array":
		return arrayType(schema, ctx)
	case "object":
		return objectType(schema, ctx)
	case "":
		if !hasType {
			if _, ok := schema["properties"]; ok {
				return objectType(schema, ctx)
			}
			if _, ok := schema["additionalProperties"]; ok {
				return objectType(schema, ctx)
			}
			if _, ok := schema["required"]; ok {
				return objectType(schema, ctx)
			}
			if _, ok := schema["items"]; ok {
				return arrayType(schema, ctx)
			}
			if _, ok := schema["prefixItems"]; ok {
				return arrayType(schema, ctx)
			}
			return "unknown"
		}
		return "unknown"
	default:
		return "unknown"
	}
}

func unionTypes(variants []any, ctx *schemaContext) string {
	parts := make([]string, 0, len(variants))
	for _, v := range variants {
		parts = append(parts, toType(v, ctx))
	}
	return union(parts)
}

func arrayType(schema map[string]any, ctx *schemaContext) string {
	if items, ok := schema["items"]; ok {
		if _, isArr := items.([]any); !isArr {
			return "Array<" + toType(items, ctx) + ">"
		}
	}
	var tuple []any
	if t, ok := schema["prefixItems"].([]any); ok {
		tuple = t
	} else if t, ok := schema["items"].([]any); ok {
		tuple = t
	}
	if len(tuple) > 0 {
		parts := make([]string, 0, len(tuple))
		for _, item := range tuple {
			parts = append(parts, toType(item, ctx))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return "unknown[]"
}

func descriptionOf(property any) string {
	if m, ok := property.(map[string]any); ok {
		if d, ok := m["description"].(string); ok {
			return strings.TrimSpace(d)
		}
	}
	return ""
}

func objectType(schema map[string]any, ctx *schemaContext) string {
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	required := map[string]bool{}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sortStrings(names)
	members := make([]string, 0, len(names)+1)
	for _, name := range names {
		optional := "?"
		if required[name] {
			optional = ""
		}
		members = append(members, propertyKey(name)+optional+": "+toType(properties[name], ctx)+";")
	}
	additional, hasAdditional := schema["additionalProperties"]
	if hasAdditional && additional != false {
		var typ string
		if b, ok := additional.(bool); ok && b {
			typ = "unknown"
		} else {
			typ = toType(additional, ctx)
		}
		members = append(members, "[key: string]: "+typ+";")
	} else if !hasAdditional && len(names) == 0 {
		members = append(members, "[key: string]: unknown;")
	}
	if len(members) == 0 {
		return "{}"
	}
	hasDescription := false
	for _, name := range names {
		if descriptionOf(properties[name]) != "" {
			hasDescription = true
			break
		}
	}
	if !hasDescription {
		return "{ " + strings.Join(members, " ") + " }"
	}
	lines := []string{"{"}
	for i, name := range names {
		desc := descriptionOf(properties[name])
		for _, line := range strings.Split(strings.ReplaceAll(desc, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, indent+"// "+strings.TrimSpace(line))
			}
		}
		lines = append(lines, indent+strings.ReplaceAll(members[i], "\n", "\n"+indent))
	}
	for _, member := range members[len(names):] {
		lines = append(lines, indent+member)
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n")
}

func propertyKey(name string) string {
	if isIdentifier(name) {
		return name
	}
	return jsStringify(name)
}

func union(types []string) string {
	seen := map[string]bool{}
	unique := []string{}
	for _, t := range types {
		if seen[t] {
			continue
		}
		seen[t] = true
		unique = append(unique, t)
	}
	for _, t := range unique {
		if t == "unknown" {
			return "unknown"
		}
	}
	if len(unique) == 0 {
		return "never"
	}
	return strings.Join(unique, " | ")
}

func resolveRef(ref string, root any) any {
	if ref != "#" && !strings.HasPrefix(ref, "#/") {
		return nil
	}
	current := root
	rest := strings.TrimPrefix(ref, "#")
	rest = strings.TrimPrefix(rest, "/")
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" {
			continue
		}
		key := decodeRefSegment(segment)
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		val, ok := m[key]
		if !ok {
			return nil
		}
		current = val
	}
	switch current.(type) {
	case bool, map[string]any:
		return current
	default:
		return nil
	}
}

func decodeRefSegment(segment string) string {
	decoded := decodeURIComponent(segment)
	decoded = strings.ReplaceAll(decoded, "~1", "/")
	decoded = strings.ReplaceAll(decoded, "~0", "~")
	return decoded
}

func decodeURIComponent(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := hexVal(s[i+1])
			lo, ok2 := hexVal(s[i+2])
			if ok1 && ok2 {
				sb.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func jsStringify(v any) string {
	if v == nil {
		return "null"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "undefined"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
