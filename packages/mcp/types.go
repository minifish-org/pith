// Package mcp is a standalone, native Go client for the Model Context Protocol.
//
// It is a port of packages/mcp/src from Pi. The package is an embedded client
// only: it speaks the client half of the protocol over pluggable transports
// (stdio, Streamable HTTP, in-memory) and converts tool results into model
// content. No Node.js helper, JavaScript engine or CGO dependency is required.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License; portions adapted
// from modelcontextprotocol/typescript-sdk v1.29.0, Copyright (c) 2024
// Anthropic, PBC, MIT License. See LICENSES/ for the third-party notice.
package mcp

// LatestProtocolVersion is the newest MCP revision this client speaks.
const LatestProtocolVersion = "2025-11-25"

// SupportedProtocolVersions lists the versions the client accepts from a
// server. Servers built on older SDKs answer initialize with their own latest
// version, so older revisions stay accepted.
var SupportedProtocolVersions = []string{
	LatestProtocolVersion,
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

// IsSupportedProtocolVersion reports whether a server-selected version is one
// this client understands.
func IsSupportedProtocolVersion(version string) bool {
	for _, supported := range SupportedProtocolVersions {
		if version == supported {
			return true
		}
	}
	return false
}

// Implementation identifies a client or server by name and version.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// Root is a filesystem root a client exposes to a server.
type Root struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

// RootsListResult is the answer to a server's `roots/list` request.
type RootsListResult struct {
	Roots []Root `json:"roots"`
}

// RootsCapability advertises the roots capability to a server.
type RootsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ClientCapabilities describes optional client features.
type ClientCapabilities struct {
	Experimental map[string]any   `json:"experimental,omitempty"`
	Roots        *RootsCapability `json:"roots,omitempty"`
	Sampling     map[string]any   `json:"sampling,omitempty"`
	Elicitation  map[string]any   `json:"elicitation,omitempty"`
}

// ServerCapabilities describes optional server features.
type ServerCapabilities struct {
	Experimental map[string]any  `json:"experimental,omitempty"`
	Logging      map[string]any  `json:"logging,omitempty"`
	Prompts      *ListChangedCap `json:"prompts,omitempty"`
	Resources    *ResourcesCap   `json:"resources,omitempty"`
	Tools        *ListChangedCap `json:"tools,omitempty"`
	Completions  map[string]any  `json:"completions,omitempty"`
}

// ListChangedCap is the capability shape for list-based features.
type ListChangedCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
	Subscribe   bool `json:"subscribe,omitempty"`
}

// ResourcesCap advertises the resources capability.
type ResourcesCap struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeParams is the `initialize` request payload.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// InitializeResult is the `initialize` response payload.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ProgressNotification is a `notifications/progress` payload.
type ProgressNotification struct {
	ProgressToken JsonRpcId `json:"progressToken"`
	Progress      float64   `json:"progress"`
	Total         *float64  `json:"total,omitempty"`
	Message       string    `json:"message,omitempty"`
}

// CancelledNotification is a `notifications/cancelled` payload.
type CancelledNotification struct {
	RequestID JsonRpcId `json:"requestId"`
	Reason    string    `json:"reason,omitempty"`
}

// ToolAnnotations carries human- and model-facing tool hints.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// ToolExecution describes how a tool may be executed as a task.
type ToolExecution struct {
	TaskSupport string `json:"taskSupport,omitempty"`
}

// Tool is a server tool definition as returned by `tools/list`. Field names
// follow the MCP JSON schema (`inputSchema`, `outputSchema`, `_meta`).
type Tool struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description,omitempty"`
	InputSchema  map[string]any   `json:"inputSchema"`
	OutputSchema map[string]any   `json:"outputSchema,omitempty"`
	Annotations  *ToolAnnotations `json:"annotations,omitempty"`
	Execution    *ToolExecution   `json:"execution,omitempty"`
	Meta         map[string]any   `json:"_meta,omitempty"`
}

// ListToolsResult is one page of `tools/list`.
type ListToolsResult struct {
	Tools      []Tool         `json:"tools"`
	NextCursor string         `json:"nextCursor,omitempty"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

// Resource is a resource a server lists in `resources/list`.
type Resource struct {
	URI         string              `json:"uri"`
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	MimeType    string              `json:"mimeType,omitempty"`
	Size        *int64              `json:"size,omitempty"`
	Annotations *ContentAnnotations `json:"annotations,omitempty"`
	Meta        map[string]any      `json:"_meta,omitempty"`
}

// ResourceTemplate is a family of resources addressed by an RFC 6570 URI
// template, from `resources/templates/list`.
type ResourceTemplate struct {
	URITemplate string              `json:"uriTemplate"`
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	MimeType    string              `json:"mimeType,omitempty"`
	Annotations *ContentAnnotations `json:"annotations,omitempty"`
	Meta        map[string]any      `json:"_meta,omitempty"`
}

// ListResourcesResult is one page of `resources/list`.
type ListResourcesResult struct {
	Resources  []Resource     `json:"resources"`
	NextCursor string         `json:"nextCursor,omitempty"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

// ListResourceTemplatesResult is one page of `resources/templates/list`.
type ListResourceTemplatesResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	NextCursor        string             `json:"nextCursor,omitempty"`
	Meta              map[string]any     `json:"_meta,omitempty"`
}

// ReadResourceResult is the `resources/read` response payload.
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
	Meta     map[string]any     `json:"_meta,omitempty"`
}
