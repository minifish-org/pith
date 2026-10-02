# Preparation validation

Target: Pi v1.0.0, `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
Previous accepted upstream: `002fc8385268300ca91a5fc95f935c2afbbdac02`.

Completed before the implementation model runs:

1. Reviewed the complete cumulative Git path list (1,131 paths), including paths
   outside the previous native watch roots. The GitHub comparison file list is
   truncated and was not used as the complete inventory.
2. Fixed 9 sequential steps with 26 independent tests, explicit contracts,
   candidate self-test outputs, immutable assets and native source ownership.
3. Downloaded only exact npm tarballs, verified their registry SHA-512 integrity,
   and froze extracted provider data and QuickJS artifacts. Pi AI 1.0.0 reports
   the same gitHead as the selected source commit. No npm dependency tree was
   installed. See `review/release-artifacts.json`.
4. Instantiated pinned QuickJS WASM with wazero v1.9.0, CGO disabled, bounded
   memory/context and no filesystem/network exports; evaluated `6 * 7 == 42`.
   See `review/quickjs-probe.log` and the preserved probe source. This validates
   engine feasibility only, not the complete async sandbox.
5. Ran the existing complete Pith regression suite with
   `CGO_ENABLED=0 GOMAXPROCS=4 go test -p=4 ./...`: passed.
   See `review/baseline-tests.log`. This tests the old accepted implementation.
6. Compiled and executed the four new judges that use existing APIs against an
   isolated old baseline: all four failed for the intended missing behavior.
   See `review/negative-controls.log`: header merge, retry/overflow classification,
   Mistral empty deltas and mapped effort. The fixtures terminate successfully;
   their failures report behavior differences, not compiler or network failures.
7. Formatted every new independent Go judge and passed executor structural
   preflight. See `review/preflight.log`: 9 prepared steps, 0 verified steps.

Not yet completed: any model-generated Pi 1.0 implementation, acceptance of the
new APIs, full async QuickJS/MCP/SDK behavior, cross-platform delivery of the new
implementation or a live-provider test. New-API judges compile only after their
owning step introduces the frozen native interfaces. Historical judges, golden
fixtures and original receipts are unchanged. They must pass alongside new gates.

No paid model calls were made during preparation. A ready plan means materials
are executable; it does not mean the migration has passed. Durable remains outside
scope. Do not advance the upstream baseline until cumulative module acceptance.
