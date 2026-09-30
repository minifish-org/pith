# Execution and acceptance overview

The original plan completed 3 modules, 24 batches and 26 automatic steps. The additive embedded SDK completed 7 steps in one additional module. Consult `results/ai.json`, `core.json`, `tools.json` and `sdk.json` for the actual recorded verification, and Git history for accepted commits.

Original preparation included behavioral contracts and export coverage, 226 TS behavioral comparisons, EventStream judges, native CLI/tool/session acceptance, 42 frozen catalog JSON assets with upstream license, and dependency probes. The 249 reference inputs were source material assigned to batches, not 249 independent passing Go tests. Preparation controls and generated-product acceptance must not be confused.

Each step goes through formatting, compile, vet, candidate tests, cumulative independent judges and applicable race checks. Entire modules are installed and committed only after project integration tests. The SDK addition keeps the accepted baseline immutable and uses a separate journal. Neither workflow automatically pushes.

Tests use temporary files and local fake services. Cross-building is not native platform execution. Passing the selected scenarios does not prove all protocol combinations, callback behavior or production readiness. Live provider smoke tests must be recorded separately. See [SDK capability scope](sdk/capabilities.md), [release evidence](../docs/evidence/2026-09-29.md), and the current [execution guide](start.md).
