/** Compile fixed judge APIs and prove nonfunctional/incorrect samples are rejected. */
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import {
  executeGo,
  succeeded,
  testResults,
} from "../../portsmith/src/process.ts";
import { copyFiles, snapshotFiles } from "../../portsmith/src/files.ts";
const root = fileURLToPath(new URL("../", import.meta.url));
const config = JSON.parse(
  await readFile(new URL("workflow.json", import.meta.url), "utf8"),
);
const fixtures = JSON.parse(
  await readFile(new URL("audit/nonfunctional.json", import.meta.url), "utf8"),
);
await mkdir(path.join(root, ".cache"), { recursive: true });
const temp = await mkdtemp(path.join(root, ".cache", "judge-audit-"));
const records: unknown[] = [];
try {
  const files = [];
  for (const name of ["go.mod", "go.sum"])
    files.push({ name, data: await readFile(path.join(root, name)) });
  for (const [name, code] of Object.entries(fixtures.files))
    files.push({ name, data: Buffer.from(code as string) });
  for (const spec of Object.values(config.units) as { judge: string }[])
    files.push(
      ...(await snapshotFiles(path.join(root, "migration", spec.judge), 100)),
    );
  await copyFiles(temp, files);
  const compile = await executeGo(temp, ["-run", "^$"]);
  if (!succeeded(compile))
    throw Error(`Judge API compilation failed:\n${compile.log}`);
  for (const [id, spec] of Object.entries(config.units) as [
    string,
    { tests: string[] },
  ][]) {
    const run = await executeGo(temp, ["-run", "^" + spec.tests[0] + "$"]);
    if (
      run.code === 0 ||
      run.timedOut ||
      run.truncated ||
      !testResults(run, "TestPortsmithJudge").failed
    )
      throw Error(
        `Judge failed to reject nonfunctional sample: ${id}\n${run.log}`,
      );
    records.push({
      unit: id,
      nonfunctionalRejected: true,
      test: spec.tests[0],
    });
  }
  // An actual FIFO mutation, not a compile failure, must also be rejected.
  const original = (
    await readFile(
      path.join(
        root,
        "../portsmith/examples/event-stream/reference-go/event_stream.go",
      ),
      "utf8",
    )
  ).replace("package port\n", "package eventstream\n");
  const eventFile = path.join(temp, "internal/eventstream/stub.go");
  await writeFile(eventFile, original);
  const baseline = await executeGo(temp, [
    "-run",
    "^TestPortsmithJudgeP01_(01|02|03|05)$",
  ]);
  if (!succeeded(baseline))
    throw Error(`Existing EventStream baseline failed:\n${baseline.log}`);
  const race = await executeGo(
    temp,
    ["-run", "^TestPortsmithJudgeP01_(01|02|03|05)$"],
    false,
    true,
  );
  if (!succeeded(race))
    throw Error(`Race baseline/toolchain check failed:\n${race.log}`);
  const mutation = original.replace(
    "value := q.outgoing[n]",
    "value := q.outgoing[0]",
  );
  if (mutation === original) throw Error("Mutation site changed");
  await writeFile(eventFile, mutation);
  const mutated = await executeGo(temp, ["-run", "^TestPortsmithJudgeP01_01$"]);
  if (
    mutated.code === 0 ||
    mutated.timedOut ||
    mutated.truncated ||
    !testResults(mutated, "TestPortsmithJudgeP01_01").failed
  )
    throw Error("FIFO mutant escaped judge");
  const digest = (b: Buffer) => createHash("sha256").update(b).digest("hex");
  const evidence = {
    version: 1,
    kind: "judge-preparation-audit",
    goImplementationVerified: false,
    compiledAllJudges: true,
    negativeSamples: records,
    eventStream: {
      preservedCasesPassed: true,
      preservedCasesRacePassed: true,
      fifoMutationRejected: true,
      reentrancyCaseNotPartOfLegacyBaseline: true,
    },
    inputs: files.map((f) => ({ name: f.name, sha256: digest(f.data) })),
    limitations:
      "Compilation and negative tests do not prove test completeness or an implementation's correctness. Actual candidates must pass all required cases and cumulative integration checks.",
  };
  await writeFile(
    path.join(root, "migration/judge-audit.json"),
    JSON.stringify(evidence, null, 2) + "\n",
  );
  console.log(
    JSON.stringify(
      {
        compiledAllJudges: true,
        negativeUnits: records.length,
        fifoMutationRejected: true,
        implementationVerified: false,
      },
      null,
      2,
    ),
  );
} finally {
  await rm(temp, { recursive: true, force: true });
}
