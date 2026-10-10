# MCP

`packages/mcp` is a standalone, native Go client for the Model Context Protocol.
It is a Go port of Pi's `packages/mcp/src` and speaks the client half of the
protocol over pluggable transports. No Node.js helper, JavaScript engine or cgo
dependency is required.

The client speaks `2025-11-25` and still accepts the earlier revisions
`2025-06-18`, `2025-03-26` and `2024-11-05`, because a server built on an older
SDK answers `initialize` with its own latest version. `IsSupportedProtocolVersion`
is the guard.

Upstream: portions are adapted from
`modelcontextprotocol/typescript-sdk` v1.29.0 (MIT). The license text is kept at
`packages/mcp/LICENSES/modelcontextprotocol-typescript-sdk.txt`.

## Transports

| Transport | Constructor | Use |
| --- | --- | --- |
| stdio | `NewStdioTransport(command, args, *StdioOptions)` | Run an MCP server as a child process; newline-delimited JSON-RPC over stdin/stdout. |
| Streamable HTTP | `NewStreamableHTTPTransport(url, *HTTPOptions)` | HTTP POST plus optional server-to-client SSE stream, session headers and reconnection. |
| In-memory | `NewInMemoryTransport`, `CreateInMemoryTransportPair` | Pair a client with an in-process server for tests and embedding. |

`StdioTransport` layers environment variables, bounds stdout messages and
retained stderr, streams stderr through `OnStderr`, and escalates SIGTERM to
SIGKILL after `CloseTimeout`. `StreamableHTTPTransport` adds request headers,
bearer-token `AuthProvider`s with 401 refresh, bounded SSE events and resumable
GET streams. `ConsumeSSEStream` parses an SSE stream directly when a caller wants
to own the loop.

The default stdout JSON message / SSE event / HTTP JSON response ceiling is
**128 MiB** (`DefaultMaxMessageBytes`), shared with Codex WebSocket and agent
proxy defaults. `StdioOptions.MaxMessageBytes`, `HTTPOptions.MaxMessageBytes`
and `SSEStreamOptions.MaxEventBytes` let a host override it. SSE accumulation is
bounded before decoding, including a line that never supplies a newline.

## Client

```go
client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "app", Version: "1.0.0"})
defer client.Close(context.Background())

if err := client.Connect(ctx); err != nil { ... }
tools, err := client.ListTools(ctx)
result, err := client.CallTool(ctx, "search", map[string]any{"q": "x"})
blocks := mcp.ToAIContent(result) // []aitypes.ContentBlock for the model
```

- `Connect` negotiates the protocol version, advertises client capabilities and
  sends `notifications/initialized` before any other request.
- `ListTools`, `ListResources`, `ListResourceTemplates` follow `nextCursor`
  pagination and return the merged page.
- `CallTool`, `ReadResource`, `Request`, `Notify`, `Ping` are the remaining
  protocol calls. `RequestOptions.Timeout` and `RequestOptions.OnProgress`
  override per request; a progress notification renews the timeout.
  Initialization and discovery default to **30 seconds**. Tool calls default
  to a **10-minute inactivity window** and request progress even when the host
  supplies no callback. Valid progress renews the window; parent cancellation
  and explicit client/request timeouts still win. Continuous progress has no
  separate total deadline.
- `SetRequestHandler`, `OnNotification`, `OnError`, `OnClose` subscribe to
  server-initiated messages and transport lifecycle. `Close` is idempotent and
  reaps a stdio child tree.

`ToAIContent` converts an MCP tool result into model content: text and images
pass through, embedded text becomes text, audio and unknown binary are replaced
by an explicit omission note, and a structured-only result is rendered as a JSON
text fallback. `CallToolResult.StructuredContent` keeps the raw JSON for a
Codemode consumer.

## OAuth

The package ships the MCP authorization flow: protected-resource and
authorization-server discovery, dynamic client registration, PKCE, code
exchange, refresh, scope step-up and issuer validation. `AuthorizeMcp`,
`StartAuthorization`, `ExchangeAuthorizationCode`, `RefreshAuthorization` and
`RegisterClient` drive it; `McpOAuthProvider` with a pluggable state store is the
default provider, and `OAuthCallbackServer` runs a loopback redirect listener.
Errors are typed (`McpAuthRequiredError`, `McpSessionExpiredError`,
`OAuthIssuerMismatchError`, ...) so a caller can distinguish a login prompt from
a transport failure. The local callback server renders its own English pages and
is a development aid, not an authentication system.

## Session integration

`packages/coding-agent` resolves configured MCP servers into session tools:

- `MCPServerConfig` describes one server (`stdio` or `http`), its exposure and
  per-tool exposure overrides. `ValidateMCPServerConfig` rejects invalid
  configuration, and `MCPNamespace` gives a server's tools the `mcp__<server>`
  namespace.
- `MCPRuntime` owns the clients of one session. `Load` connects every enabled
  server and reports partial failures as `MCPDiagnostic`s instead of aborting.
  `ToolDefinitions` returns SDK `ToolDefinition`s; `Tools`, `Statuses`,
  `ListAllResources`, `ReadResource`, `Restart`, `Notify` and `Close` manage the
  lifetime.
- `MCPTransportFactory`, `AuthProvider`, `OnNotification` and `OnError` are the
  injection points for tests and embedding. `MCPContentText` renders a result
  for a prompt.

See `examples/mcp-client` for a runnable offline client and
`internal/conformance/pi_v1_sdk/integration_test.go` for an MCP tool used from an
embedded session.

## Limits and caveats

- Only the client half is implemented. Running an MCP server, npm package
  installation and remote service management are out of scope.
- The stdio transport executes the configured command; it is not a sandbox.
- Passing the MCP tests proves the covered protocol surface (initialize,
  pagination, correlated concurrent calls, cancellation, content conversion,
  OAuth and SSE). It does not claim parity with every MCP server extension.
