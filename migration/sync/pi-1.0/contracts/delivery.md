# Pi 1.0 SDK delivery and honest capability evidence

Document the supported native AI/core/MCP/Codemode/SDK APIs and their upstream
revision. Include runnable Go examples for mixed catalogs/classification, native
MCP, Codemode and virtual routing. Examples compile offline and use injected/fake
providers by default; paid credentials are explicit, never checked in.

Describe legacy catalog and removed harness compatibility, TS-extension/TUI/CLI
adaptation boundaries, no-CGO distribution and embedded QuickJS provenance/license.
This is SDK parity for the reviewed scope, not all Pi CLI or Durable functionality.
Update docs/sdk compatibility/source mapping and licenses; preserve historical
receipts and original claims rather than rewriting their dates/results.

Keep cmd/pith compatibility and test existing tools. Product/runtime diagnostics
and docs are English. Add integration tests proving all new packages compose with
CreateAgentSession and that builds use pure-Go dependencies (including sandbox).
Existing bootstrap manifests are immutable and already supplied by operator.
Compile native examples, run all acceptance/regression tests with CGO_ENABLED=0,
and check Linux/amd64, Linux/arm64, Darwin/arm64 and Windows/amd64 cross builds.
Race tests require the Go race runtime and are a separate local developer check;
they must not cause a no-CGO migration step to fail just because -race requires CGO.

Write docs/sdk/pi-1.0.md with mapping references and a precise feature/adaptation
table. Do not claim measured performance/live-provider behavior not tested. Do not
claim Durable support or advance migration/sync.json manually; sync advances only
after cumulative module acceptance and the recoverable metadata commit.
