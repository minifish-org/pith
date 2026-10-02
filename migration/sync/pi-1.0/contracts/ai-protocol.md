# Provider streaming behavior at Pi 1.0

Review every selected adapter's cumulative diff and upstream regression tests.
Port protocol changes, not .ts import/TS7 packaging changes. Preserve previously
accepted synchronous provider observers, original identity, ordering and observer
failure propagation. Existing observers must never be retried or replayed.

Mistral ignores empty string and empty typed-text deltas so a following thinking
block continues the same block. Reasoning effort uses the model thinkingLevelMap;
off uses its off mapping, supported requested levels use their actual mapping,
and source-prescribed fallback applies. Remove name-based effort heuristics.
OpenAI Responses rejects incomplete tool calls rather than yielding executable
partial args; grammar tool-call replay IDs are stable. Port session-id forwarding,
cache TTL/cost, fast-service-tier costs, OpenCode Qwen empty-signature behavior,
strict Anthropic tool schema rejection and new stop/error parsing from source.
Azure, Bedrock, Google, Codex, Pi Messages and compatible HTTP paths all preserve
sampling overrides and model-dependent metadata. Keep transport/websocket retry
semantics from upstream and cancellation without goroutine leaks.

The independent loopback Mistral judge tests both reasoning request payload and
stream block shape, using a synthetic model name to detect name-based heuristics.
Candidate self-tests must port every changed upstream protocol regression into
offline HTTP/WebSocket fixtures. Include terminal/incomplete responses, callback
failures, empty deltas, retry-after fallback, cache costs and malformed schemas.
No live provider calls are needed for acceptance.
