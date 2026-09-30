import { mkdtemp, mkdir, readFile, writeFile, rm } from "node:fs/promises";
import path from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { inspectModules } from "../../../portsmith/dist/modules.js";
import { prepareTask, loadTask } from "../../../portsmith/dist/workspace.js";
import { verifyPort } from "../../../portsmith/dist/verify.js";

const exec = promisify(execFile);
const root = path.dirname(fileURLToPath(import.meta.url));
const inspected = await inspectModules(root);
const temp = await mkdtemp(path.join(tmpdir(), "pith-sdk-material-audit-"));
const put = async (name: string, data: string | Buffer) => {
  const target = path.join(temp, "compile", name);
  await mkdir(path.dirname(target), { recursive: true });
  await writeFile(target, data);
};
const started = new Date().toISOString();
try {
  await put("go.mod", inspected.mod);
  if (inspected.sum) await put("go.sum", inspected.sum);
  for (const file of inspected.baseline) await put(file.name, file.data);
  const judges: { name: string; data: Buffer }[] = [];
  const required: string[] = [];
  for (const [index, item] of inspected.items.entries()) {
    judges.push(...item.judge);
    required.push(...item.spec.tests);
    for (const file of item.judge) await put(file.name, file.data);
    const taskRoot = path.join(temp, "tasks", String(index));
    await prepareTask({
      source: inspected.source, out: taskRoot,
      files: item.spec.sources, revision: inspected.p.revision,
      goal: item.spec.goal, contract: item.contract,
      rules: path.join(root, "RULEBOOK.md"),
      goMod: path.join(inspected.project, "go.mod"),
      goSum: path.join(inspected.project, "go.sum"),
      judgeFiles: judges, requiredJudgeTests: required,
      seed: inspected.baseline, writableFiles: item.spec.outputs,
      moduleTask: true,
    });
    await loadTask(taskRoot);
  }
  await put("packages/coding-agent/contract.go", await readFile(path.join(root, "API.go.txt")));
  const options = { cwd: path.join(temp, "compile"), env: {...process.env, CGO_ENABLED: "0"}, maxBuffer: 16 * 1024 * 1024 };
  await exec("go", ["test", "-mod=readonly", "-run", "^$", "./internal/conformance/sdk_incremental"], options);
  let negative = "";
  try {
    await exec("go", ["test", "-json", "-mod=readonly", "-timeout=30s", "./internal/conformance/sdk_incremental"], options);
    throw new Error("Empty contract implementation unexpectedly passed");
  } catch (error: any) {
    if (error.code !== 1 || !error.stdout) throw error;
    negative = error.stdout;
  }
  const events = negative.split("\n").filter(Boolean).map(line => JSON.parse(line));
  const failed = events.filter(e => e.Action === "fail" && e.Test).map(e => e.Test);
  if (required.some(name => !failed.includes(name))) throw new Error("Not all independent judges reject the empty implementation: " + JSON.stringify(failed));
  const lastTask = path.join(temp, "tasks", String(inspected.items.length - 1));
  const stub = path.join(lastTask, "candidate/packages/coding-agent/doc.go");
  await mkdir(path.dirname(stub), { recursive: true });
  await writeFile(stub, await readFile(path.join(root, "API.go.txt")));
  const verification = await verifyPort(lastTask, false);
  if (verification.status !== "behavior_failed" || !verification.independent)
    throw new Error("Real verifier did not reach the negative behavior checks: " + JSON.stringify(verification));
  await mkdir(path.join(root, "readiness"), { recursive: true });
  await writeFile(path.join(root, "readiness", "audit.json"), JSON.stringify({
    started, finished: new Date().toISOString(),
    preparedSnapshots: inspected.items.length,
    baselineFiles: inspected.baseline.length,
    judgeCompile: "passed", negativeControl: "empty typed SDK",
    realVerifier: verification.status,
    rejectedBy: failed, paidModelCalls: 0,
    caveat: "Checks preparation and judge compilation/rejection. Does not prove a correct SDK, complete TS differential parity, or that a generated implementation will pass."
  }, null, 2) + "\n");
  console.log(`Prepared ${inspected.items.length} task snapshots; all ${required.length} judges compile and reject the empty SDK. No model calls.`);
} finally {
  await rm(temp, { recursive: true, force: true });
}
