# Standalone native MCP client

Port packages/mcp completely as an embedded client (not an MCP server), including
protocol/content, transport-neutral client, in-memory testing, stdio, Streamable
HTTP and OAuth. Use standard-library transport/crypto/process support; retain
upstream protocol versions, pagination, result schemas and error semantics.

Public Go facade in packages/mcp:
* NewStreamableHTTPTransport(url string, options *HTTPOptions) Transport
* NewStdioTransport(command string, args []string, options *StdioOptions) Transport
* NewClient(transport Transport, options *ClientOptions) *Client
* Client.Connect(context.Context) error; ListTools(context.Context) ([]Tool,error)
* Client.CallTool(context.Context,name string,args map[string]any) (CallToolResult,error)
* Client.Close(context.Context) error
* ClientOptions{Name,Version string; RequestTimeout time.Duration; Roots []Root}
* HTTPOptions{Client *http.Client; Headers http.Header; ...}
* StdioOptions{Cwd string; Env []string; ...}; Env contains KEY=value pairs.
* CallToolResult has Content, StructuredContent json.RawMessage, IsError bool.
* ToAIContent(CallToolResult) []aitypes.ContentBlock; Tool uses JSON MCP names.

Native types may split into protocol/oauth/transports subpackages with root aliases.
Initialize negotiates protocol, then notifications/initialized. Correlate requests
under concurrency. ListTools fetches every cursor. Timeouts/abort notify cancelled,
progress may renew timeout. Errors carry JSON-RPC code/data. Session HTTP requests
send MCP-Session-Id and negotiated MCP-Protocol-Version; SSE reconnect preserves
Last-Event-ID and never duplicates tool execution. Standard HTTP JSON responses,
SSE POST and optional GET streams all work. Authenticate requests with supplied
credentials; stdio never leaks tokens. Close stdin/process groups with bounded
TERM/KILL fallback. No Node helper is required in product or tests.

Port OAuth protected-resource/server discovery, overrides, dynamic registration,
PKCE, browser/manual callbacks, RFC9207 issuer, saved token scope and concurrent
401 refresh. Insufficient-scope retries occur only as prescribed; credentials are
scoped by configured server identity and URL. Retain third-party MIT notices.

Content conversion preserves text/images/embedded resources; unsupported binary,
audio and links have readable placeholders. Structured-only tool output becomes
JSON text, while StructuredContent remains available for Codemode.

Independent local HTTP judges cover negotiation, pagination, session headers,
correlation under concurrency and structured-only result conversion. Candidate
self-tests additionally port upstream OAuth/SSE/stdio/progress/cancellation tests,
using the Go test binary as a helper process. Include shutdown of stubborn child,
no session header sent to another origin, 401 coalescing and resource-list change.
No live MCP server or paid model is involved.
