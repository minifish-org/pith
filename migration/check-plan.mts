import assert from "node:assert/strict";
import path from "node:path";
import {
  baselineEntries,
  diffInputs,
  loadBundle,
  projectRoot,
  scanInputs,
  validateStructure,
} from "./plan-lib.mts";

const args = process.argv.slice(2);
assert(
  args.length === 0 || (args.length === 1 && args[0] === "--self-test"),
  "用法：check-plan.mts [--self-test]",
);
const bundle = await loadBundle();
const stats = validateStructure(bundle);
const actual = await scanInputs(
  path.resolve(projectRoot, bundle.plan.source),
  bundle.inventory,
);
const delta = diffInputs(bundle, actual);
assert.equal(
  delta.status,
  "no-watched-input-changes",
  `锁定源码有变化：${JSON.stringify(delta)}`,
);
let selfTests = 0;
if (args[0] === "--self-test") {
  const cases = [
    (b: any) => b.inventory.files.pop(),
    (b: any) => b.plan.modules[0].dependsOn.push("tools"),
    (b: any) => b.plan.batches[0].dependsOn.push(b.plan.batches[0].id),
    (b: any) =>
      (b.inventory.files.find((f: any) => f.batch).sha256 = "0".repeat(64)),
    (b: any) => b.plan.batches[1].outputs.push(b.plan.batches[0].outputs[0]),
    (b: any) => (b.plan.execution.ready = !b.plan.execution.ready),
    (b: any) => b.exports.files.pop(),
  ];
  for (const mutate of cases) {
    const copy = {
      ...structuredClone({ ...bundle, analysisBytes: undefined }),
      analysisBytes: bundle.analysisBytes,
    };
    mutate(copy);
    assert.throws(() => validateStructure(copy));
    selfTests++;
  }
  const baseline = new Map<string, string>(
    baselineEntries(bundle.inventory).map((e: any) => [e.source, e.sha256]),
  );
  const changed = new Map(baseline);
  changed.set("packages/ai/src/types.ts", "f".repeat(64));
  assert.deepEqual(diffInputs(bundle, changed).regressionModules, [
    "ai",
    "core",
    "tools",
  ]);
  selfTests++;
  const moved = new Map(baseline);
  const source = "packages/agent/src/harness/tools/write.ts";
  moved.delete(source);
  moved.set(
    "packages/agent/src/harness/tools/renamed.ts",
    baseline.get(source)!,
  );
  assert(
    diffInputs(bundle, moved).possibleRenames.some((p) => p.from === source),
  );
  selfTests++;
  const fresh = new Map(baseline);
  fresh.set("packages/ai/src/providers/new.ts", "f".repeat(64));
  assert.deepEqual(diffInputs(bundle, fresh).unclassified, [
    "packages/ai/src/providers/new.ts",
  ]);
  selfTests++;
  const data = new Map(baseline);
  data.set("packages/ai/src/providers/data/deepseek.json", "f".repeat(64));
  assert.equal(diffInputs(bundle, data).generatedCatalogChanges.length, 1);
  selfTests++;
  const meta = new Map(baseline);
  meta.set("package.json", "f".repeat(64));
  assert.deepEqual(diffInputs(bundle, meta).regressionModules, [
    "ai",
    "core",
    "tools",
  ]);
  selfTests++;
}
console.log(
  JSON.stringify(
    {
      status: "planning-verified",
      revision: bundle.plan.revision,
      modules: ["ai", "core", "tools"],
      ...stats,
      checkedInputHashes: actual.size,
      selfTests,
      executionReady: bundle.plan.execution.ready,
      blockers: bundle.plan.execution.blockers,
      goImplementationVerified: false,
    },
    null,
    2,
  ),
);
