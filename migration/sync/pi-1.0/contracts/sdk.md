# Pi 1.0 embedded SDK integration

Read all changed coding-agent core modules and builtin codemode/MCP/tool-search
extensions. Adapt extension callbacks into native Go APIs; do not add a TypeScript
extension loader, npm installation, CLI/TUI, llama UI/server downloads or Durable.
Retain existing SessionOptions/CreateAgentSession, ToolHooks, resources, model
configuration, disk session tree, retries, compaction, branch operations and events.

Add virtual routing with VirtualModelDefinition{Provider,ID,Name string;
ThinkingLevels []aitypes.ModelThinkingLevel; ContextWindow,MaxTokens int;
Route func(context.Context,ModelRouteRequest)(ModelRoute,error)}.
CreateVirtualModel(VirtualModelDefinition) aitypes.Model produces api pi-virtual.
SessionOptions.VirtualModels []VirtualModelDefinition registers per-session
routers. ModelRouteRequest{Model aitypes.Model; ThinkingLevel aitypes.ModelThinkingLevel;
Reason string; Previous,Failed *ModelRoutePrevious; State json.RawMessage;
Messages []aitypes.Message}. ModelRoute{Model aitypes.Model;
ThinkingLevel aitypes.ModelThinkingLevel; State json.RawMessage}.
ModelRoutePrevious carries physical model, thinking level and optional failed
AssistantMessage. Route before every logical provider request: user, continuation,
retry and direct. Virtual model never reaches provider; transcript records actual
physical model and thinking level while selection stays virtual. Persist changed
router state on the active session branch using pi.virtual-model-state custom
entries. Direct requests do not read/write branch state. Reject virtual-to-virtual
routing, id collisions and unavailable physical models. Fake injected StreamFn in
tests is a valid physical provider and does not require live credentials.

Native ToolDefinition gains Exposure string (direct/codemode/deferred), OutputSchema,
and ToolResult.StructuredContent. Default empty exposure is direct. NewCodemodeTool
(registry *ToolRegistry, options *codemode.SandboxOptions) (ToolDefinition,error)
creates a code-input tool. Every nested call re-enters registry schema/permission
hooks and core RunToolCall (never direct Execute bypass). Tool-level structured
data becomes script return value; usage/images/cost aggregate once. Successful
storeWrites persist on session branch using codemode-store; failures don't write.
Per-session close cancels MCP/sandbox activity. Deny list applies to all exposures,
including tool_search and nested scripts. Add CreateToolSearchTool or equivalent
registry method with source BM25 ranking/stable ties and bounded loading semantics.
Deferred tools are discoverable and callable nested but absent from provider tools
until loaded. Port codemode on/only presentation, schema/declaration budgeting and
tool load/unload result semantics. Preserve old explicit allow/deny API behavior.

Add native MCP server configuration/loading/runtime with stdio and HTTP transport,
client lifecycle, server identity/tool namespaces, auth callbacks, partial failure
diagnostics and exposure selection. Include tools, resource list/read, notifications
and session restart semantics from the exact builtin extension. Configuration is
caller-supplied/cwd-scoped, no forced global discovery or interactive prompt.

Port remaining source changes for resources/settings/model runtime/catalog refresh,
default-tools +/- grammar, cache warmer, credential resolution, session-first-user
persistence, continuation/model changes, branch context, compaction, trust and
usage metadata. Cache warmer is opt-in/bounded/cancellable with injected schedule;
it must not create unrequested paid requests during acceptance. Preserve native
compatibility wrappers for removed harness methods. No TS/Go file-name parity
requirement; exact source-to-Go ownership is recorded after acceptance.
All modern embedded-SDK builtin resolution uses the V1 mixed registry. Legacy
static lookup/no-argument provider factory catalogs are compatibility APIs only;
they must not keep the default embedded SDK on old model metadata.

Independent judges exercise per-session virtual routing/state and nested Codemode
permission enforcement. Candidate tests port all changed native SDK upstream tests
using fake streams/MCP servers; cover branch/retry/direct routing, store replay,
MCP start/close/reload, loading policy, denied tools, first-user disk persistence,
resource diagnostics, model changes in an active conversation and warmer shutdown.
