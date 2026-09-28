/** Generate deterministic expected data by executing the pinned TS, never a model. */
import {
  mkdtemp,
  mkdir,
  readFile,
  writeFile,
  copyFile,
  rm,
} from "node:fs/promises";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
const root = fileURLToPath(new URL("../", import.meta.url));
const portsmith = path.resolve(root, "../portsmith");
const pin = JSON.parse(
  await readFile(new URL("upstream.json", import.meta.url), "utf8"),
);
const source = path.join(root, pin.sourceDir);
const digest = (b: Buffer | string) =>
  createHash("sha256").update(b).digest("hex");
const sourceFiles = [
  "packages/ai/src/utils/event-stream.ts",
  "packages/ai/src/utils/text.ts",
  "packages/ai/src/utils/transcript.ts",
  "packages/ai/src/utils/validation.ts",
  "packages/agent/src/agent-loop.ts",
  "packages/agent/src/stream-fn.ts",
  "packages/agent/src/harness/utils/truncate.ts",
];
const analysis = JSON.parse(
  await readFile(new URL("analysis.json", import.meta.url), "utf8"),
);
await mkdir(path.join(portsmith, ".portsmith"), { recursive: true });
const temp = await mkdtemp(path.join(portsmith, ".portsmith", "pith-oracle-"));
try {
  for (const name of sourceFiles) {
    const bytes = await readFile(path.join(source, name));
    if (
      digest(bytes) !==
      analysis.files.find((f: { path: string }) => f.path === name)?.sha256
    )
      throw Error(`Source drift: ${name}`);
    const dest = path.join(temp, "references", name);
    await mkdir(path.dirname(dest), { recursive: true });
    await writeFile(dest, bytes);
  }
  const runner = String.raw`
import {eventStreamOracle} from "../../src/event-stream.ts";
import * as transcript from "./references/packages/ai/src/utils/transcript.ts";
import {validateToolArguments} from "./references/packages/ai/src/utils/validation.ts";
import {agentLoop} from "./references/packages/agent/src/agent-loop.ts";
import {EventStream} from "./references/packages/ai/src/utils/event-stream.ts";
import {truncateHead} from "./references/packages/agent/src/harness/utils/truncate.ts";
const p01=await eventStreamOracle(import.meta.dirname);
const tool=(name)=>({name,description:"",parameters:{type:"object"}});
const messages=[{role:"system",content:"base",sections:{a:"A",b:"B"},toolsAdded:[tool("x"),tool("y")],timestamp:0},{role:"system",content:"more",sections:{a:"C",b:null},toolsRemoved:[{name:"x"}],toolsAdded:[tool("z"),tool("x")],timestamp:1}];
const p03={prompt:transcript.getCurrentSystemPrompt(messages),tools:transcript.getCurrentTools(messages).map(t=>t.name)};
const p04=[
 {schema:{type:"object",properties:{n:{type:"integer"}}},input:{n:"12"}},
 {schema:{type:"object",properties:{b:{type:"boolean"}}},input:{b:"false"}},
 {schema:{type:"object",properties:{n:{type:"number"}}},input:{n:"bad"}},
 {schema:{type:"object",properties:{n:{type:"integer"}}},input:{n:"1.5"}},
 {schema:{type:"object",properties:{s:{type:"string"}}},input:{s:null}},
 {schema:{type:"object",properties:{s:{type:["string","null"]}}},input:{s:null}},
].map(f=>{try{return {...f,ok:true,output:validateToolArguments({name:"t",description:"",parameters:f.schema},{type:"toolCall",id:"1",name:"t",arguments:f.input})}}catch{return {...f,ok:false}}});
let calls=0;const usage={input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
const stream=()=>{const s=new EventStream(e=>e.type==="done",e=>e.message);const first=calls++===0;queueMicrotask(()=>s.push({type:"done",reason:first?"toolUse":"stop",message:{role:"assistant",api:"openai-completions",provider:"fixture",model:"fixture",usage,stopReason:first?"toolUse":"stop",timestamp:0,content:first?[{type:"toolCall",id:"call-1",name:"echo",arguments:{value:"hello"}}]:[{type:"text",text:"done"}]}}));return s};
const echo={name:"echo",description:"",label:"Echo",parameters:{type:"object",properties:{value:{type:"string"}},required:["value"]},execute:async()=>({content:[{type:"text",text:"echoed: hello"}],details:{}})};
const config={model:{id:"fixture",name:"fixture",api:"openai-completions",provider:"fixture",baseUrl:"http://fixture.invalid",reasoning:false,input:["text"],cost:usage.cost,contextWindow:8000,maxTokens:1000},convertToLlm:m=>m,toolExecution:"sequential"};
const p05=[];for await(const event of agentLoop([{role:"user",content:"echo",timestamp:0}],{messages:[],tools:[echo]},config,undefined,stream)){p05.push(event.type)}
const p08=["", "one\ntwo", "一\r\n二\r\n三", "ROW\n".repeat(2001), "汉".repeat(20000)].map(input=>({input,...truncateHead(input)}));
console.log(JSON.stringify({p01,p03,p04,p05,p08}));
`;
  await writeFile(path.join(temp, "runner.mts"), runner);
  const { stdout } = await promisify(execFile)(
    process.execPath,
    [
      "--import",
      path.join(portsmith, "node_modules/tsx/dist/loader.mjs"),
      path.join(temp, "runner.mts"),
    ],
    { cwd: portsmith, timeout: 15000, maxBuffer: 1024 * 1024 },
  );
  const data = JSON.parse(stdout);
  const destinations = {
    p01: "P01-eventstream/internal/eventstream",
    p03: "P03-transcript/ai",
    p04: "P04-tool-arguments/ai",
    p05: "P05-agent-loop/agent",
    p08: "P08-read-text/tools/readfile",
  };
  const outputs = [];
  for (const [key, dir] of Object.entries(destinations)) {
    const name = `judges/${dir}/testdata/${key}-ts.json`;
    const text = JSON.stringify(data[key], null, 2) + "\n";
    await mkdir(path.dirname(path.join(root, "migration", name)), {
      recursive: true,
    });
    await writeFile(path.join(root, "migration", name), text);
    outputs.push({ name, sha256: digest(text) });
  }
  const report = {
    version: 1,
    commit: pin.commit,
    sourceFiles: await Promise.all(
      sourceFiles.map(async (name) => ({
        name,
        sha256: digest(await readFile(path.join(source, name))),
      })),
    ),
    outputs,
    modelCalls: 0,
    scope: "Selected deterministic TS cases; not full Pi parity",
  };
  await writeFile(
    path.join(root, "migration/oracle-evidence.json"),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(
    JSON.stringify(
      { sourceVerified: true, fixtures: outputs.map((f) => f.name) },
      null,
      2,
    ),
  );
} finally {
  await rm(temp, { recursive: true, force: true });
}
