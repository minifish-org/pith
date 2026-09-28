/** Validate task preparation, not Go correctness; never calls an LLM. */
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
process.chdir(root); // Portsmith resolves plan.source relative to the working directory.
const { loadPlan, selectUnit, packageCycles } = await import(
  "../../portsmith/dist/plan.js"
);
const { prepareTask, loadTask } = await import(
  "../../portsmith/dist/workspace.js"
);
const planDir = path.join(root, "migration");
const pin = JSON.parse(
  await readFile(path.join(planDir, "upstream.json"), "utf8"),
);
const plan = await loadPlan(planDir);
if (plan.revision !== pin.commit || plan.source !== pin.sourceDir) {
  throw Error("Plan source/revision differs from the pinned upstream");
}
await mkdir(path.join(root, ".portsmith"), { recursive: true });
const temp = await mkdtemp(path.join(root, ".portsmith", "plan-check-"));
const units: { id: string; referenceFiles: number; frozenBytes: number }[] = [];
try {
  for (const item of plan.units) {
    const { plan, unit, planDigest } = await selectUnit(planDir, item.id);
    const taskRoot = await prepareTask({
      source: plan.source,
      out: path.join(temp, unit.id),
      files: [...unit.files, ...unit.references],
      revision: plan.revision,
      goal:
        unit.goal +
        "\n验收：\n" +
        unit.acceptance.join("\n") +
        "\nGo目标包：" +
        unit.targetPackage,
      rules: path.join(planDir, "RULEBOOK.md"),
      unit: unit.id,
      dependsOn: unit.dependsOn,
      planDigest,
    });
    const { task } = await loadTask(taskRoot);
    units.push({
      id: unit.id,
      referenceFiles: task.files.length,
      frozenBytes: task.files.reduce((n, f) => n + f.bytes, 0),
    });
  }
} finally {
  await rm(temp, { recursive: true, force: true });
}
const planData = await readFile(path.join(planDir, "plan.json"));
const report = {
  status: "preparation-verified",
  behaviorValidation:
    "not-run; independent judges and Go implementation are still planned",
  revision: plan.revision,
  planSha256: createHash("sha256").update(planData).digest("hex"),
  packageCycles: packageCycles(plan.units),
  units,
};
await mkdir(path.join(root, ".cache/pi"), { recursive: true });
await writeFile(
  path.join(root, ".cache/pi/plan-check.json"),
  JSON.stringify(report, null, 2) + "\n",
);
console.log(JSON.stringify(report, null, 2));
