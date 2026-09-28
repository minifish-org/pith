/** Rebuild and audit the first real step's judges, without generating product Go code or calling a model. */
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { eventStreamOracle } from "../../portsmith/src/event-stream.ts";
import { copyFiles, hash, snapshotFiles } from "../../portsmith/src/files.ts";
import {
  executeGo,
  succeeded,
  testResults,
} from "../../portsmith/src/process.ts";

const m = fileURLToPath(new URL("./", import.meta.url)),
  project = path.dirname(m.replace(/\/$/, ""));
const p = JSON.parse(await readFile(path.join(m, "plan.json"), "utf8"));
const a = JSON.parse(await readFile(path.join(m, "analysis.json"), "utf8"));
const sourceName = "packages/ai/src/utils/event-stream.ts";
const source = await readFile(path.join(project, p.source, sourceName));
assert.equal(
  hash(source),
  a.files.find((f: { path: string }) => f.path === sourceName).sha256,
);
const temp = await mkdtemp(path.join(tmpdir(), "pith-starter-audit-"));
const packageName = "packages/ai/utils/eventstream",
  judgeRoot = path.join(m, "judges/ai-eventstream");
try {
  await copyFiles(temp, [{ name: `references/${sourceName}`, data: source }]);
  const golden = await eventStreamOracle(temp);
  const fixture = `${packageName}/testdata/eventstream-ts.json`;
  await writeFile(
    path.join(judgeRoot, fixture),
    JSON.stringify(golden, null, 2) + "\n",
  );
  const judges = await snapshotFiles(judgeRoot);
  const goRoot = path.join(temp, "go");
  await mkdir(goRoot);
  await copyFiles(goRoot, [
    {
      name: "go.mod",
      data: Buffer.from("module github.com/minifish-org/pith\n\ngo 1.24\n"),
    },
    ...judges,
  ]);
  const implementation = path.join(goRoot, packageName, "event_stream.go");
  const stub = `package eventstream
import "context"
type StreamItem[T any] struct{Value T;Done bool}
type EventStream[T any,R any]struct{}
func NewEventStream[T any,R any](func(T)bool,func(T)R)*EventStream[T,R]{return &EventStream[T,R]{}}
func(*EventStream[T,R])Push(T){}
func(*EventStream[T,R])End(*R){}
func(*EventStream[T,R])Next()<-chan StreamItem[T]{c:=make(chan StreamItem[T],1);c<-StreamItem[T]{Done:true};return c}
func(*EventStream[T,R])Result(context.Context)(R,error){var r R;return r,nil}
`;
  await writeFile(implementation, stub);
  const build = await executeGo(goRoot, ["-run", "^$"]);
  assert(succeeded(build), build.log);
  const negative = await executeGo(goRoot, [
    "-run",
    "^TestPortsmithJudgeAIEventStreamFIFO$",
  ]);
  assert(
    !negative.timedOut &&
      !negative.truncated &&
      testResults(negative).failed > 0,
    "known wrong implementation was not caught",
  );
  // This is ONLY a temporary positive control from Portsmith's old reference fixture.
  // It intentionally does not satisfy the new Go callback-reentrancy contract.
  const referencePath = path.join(
    project,
    "../portsmith/examples/event-stream/reference-go/event_stream.go",
  );
  const reference = (await readFile(referencePath, "utf8")).replace(
    "\npackage port\n",
    "\npackage eventstream\n",
  );
  await writeFile(implementation, reference);
  const preserved =
    "^TestPortsmithJudgeAIEventStream(FIFO|EndAndCancellation|FirstResult|TSParity|Generic|BulkFIFO)$";
  const positive = await executeGo(goRoot, ["-run", preserved], false, true);
  assert(succeeded(positive), positive.log);
  const mutation = reference.replace(
    "value := q.outgoing[n]",
    "value := q.outgoing[0]",
  );
  assert.notEqual(mutation, reference);
  await writeFile(implementation, mutation);
  const wrongFIFO = await executeGo(goRoot, [
    "-run",
    "^TestPortsmithJudgeAIEventStreamFIFO$",
  ]);
  assert(
    !wrongFIFO.timedOut &&
      !wrongFIFO.truncated &&
      testResults(wrongFIFO).failed > 0,
    "FIFO mutation was not caught",
  );
  const report = {
    status: "judge-ready",
    scope:
      "ai-foundation/eventstream only; no complete ai or Go candidate claim",
    upstream: p.revision,
    source: { name: sourceName, sha256: hash(source) },
    contractSha256: hash(
      await readFile(path.join(m, "contracts/ai-eventstream.md")),
    ),
    judges: judges.map((f) => ({ name: f.name, sha256: f.sha256 })),
    tsCases: golden.length,
    allJudgesCompile: true,
    knownWrongRejected: true,
    fifoMutationRejected: true,
    preservedPositiveControlWithRace: true,
    positiveControlExcludedTests: [
      "TestPortsmithJudgeAIEventStreamConcurrency",
    ],
    note: "Concurrency/reentrant callbacks are Go contract extensions; positive control is not accepted Pith code. It is never copied into product directories.",
    modelCalls: 0,
    goImplementationVerified: false,
  };
  await mkdir(path.join(m, "readiness"), { recursive: true });
  await writeFile(
    path.join(m, "readiness/ai-eventstream.json"),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(JSON.stringify(report, null, 2));
} finally {
  await rm(temp, { recursive: true, force: true });
}
