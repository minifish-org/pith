# Embedded SDK capability ledger

This ledger distinguishes existing lower-level code from the new application-facing SDK. Existing code is reusable evidence, not a claim that its behavior has already been connected to the new API. The accepted baseline stays immutable during this increment.

| Capability | Existing Pith basis | Incremental work | Acceptance |
| --- | --- | --- | --- |
| Model/provider calls | `packages/ai/api`, `providers`, `catalog`, `auth`, `types` | Caller-owned runtime, credentials, capacities and settings; provider routing without global cross-session state | Configuration and real local HTTP tests |
| Tool/model loop | `packages/agent/agent.go`, `agent_loop.go`, `types` | Compose the existing Agent into `CreateAgentSession`; preserve complete provider messages | Two-turn custom-tool session and HTTP tests |
| Durable conversation | `packages/agent/harness/session`, JSONL storage | Public manager, tree/branch/reopen and turn persistence; documented supported storage version | Storage and session restore tests |
| Resources | `packages/agent/harness/resources` | Explicit discovery roots; project instructions, skills, templates and reload | Resource tests |
| Execution tools | `packages/agent/harness/tools`, `env` | Bridge read/write/edit/bash; add coding-agent grep/find/ls behavior and file mutation ordering | Tool tests plus required candidate tests |
| Extension points | Core before/after tool and turn hooks | Go registry/custom tools, policy hooks, schema validation and active-tool selection | Custom-tool and before-hook tests; candidate after-hook tests |
| Events and host lifecycle | Core Agent subscriptions, abort, queues | SDK event/usage/results interface, ownership, concurrent-run rejection and cleanup | Session, cancellation and queue tests |
| Long context | Harness compaction implementations | Auto/manual trigger, capacity reserve, durable summary and failed-summary rollback | Compaction and rollback tests |
| Resilience | Core finish-turn and provider retry building blocks | Complete truncation continuation, transient retry policy and cancellation propagation | Resilience, exhaustion and cancellation tests |
| Distribution | Existing Go dependency graph | Pure-Go SDK and examples; no Node/Pi runtime required by embedding hosts | macOS/Linux/Windows no-CGO cross-build judge |
| Ongoing migration | Previous source/package/symbol maps | Add actual Go files and resolvable exported declarations to new mappings | Parser-based source-map judge |
| Application integration | Existing convenience `sdk.Run` and CLI | New public `packages/coding-agent` entry point; six English examples and guide | All-package compilation and delivery judges |

The baseline's small `packages/agent/sdk.Run` loop is not a substitute for this session layer. It remains unchanged for compatibility. The new API supports both a default real provider route and an injected stream for host adapters/tests.

Out of scope: interactive TUI, CLI UX, JS/TS execution plugins, package installer, experimental server backends, hosted Pi account services, export/sharing presentation and cache prewarming. Resource selection is explicit; arbitrary repository code is never executed during discovery. Browser OAuth UI is a host concern; provider credentials and refresh can be supplied through the existing auth layer and callbacks. Filesystem sandboxing, corporate RBAC, tenant authentication and MCP server management are separate host integrations, not implied by this SDK increment.

Frozen judges cover representative success and failure behavior; contracts also require candidate tests for schemas, tools, metadata, credential failures, failed saves, event lifetimes, non-retryable errors, active process cancellation and restart semantics. No finite set of these tests proves full Pi parity. After the migration completes, review the new compatibility guide and perform a separately authorized real-provider smoke run before customer deployment.
