// Rebuild execution materials from the fixed inventory and source-derived oracle.
// Does not generate product Go, contact an LLM, commit, or advance an accepted baseline.
import { readFile, writeFile, mkdir } from "node:fs/promises";
import path from "node:path";
import { migrationRoot as root, projectRoot, sha256 } from "./plan-lib.mts";
const read = async (n:string)=>JSON.parse(await readFile(path.join(root,n),"utf8"));
const put = async (n:string,s:string)=>{await mkdir(path.dirname(path.join(root,n)),{recursive:true});await writeFile(path.join(root,n),s)};
const save = (n:string,v:any)=>put(n,JSON.stringify(v,null,2)+"\n");
const [p,w,inv,exp,analysis]=await Promise.all(["plan.json","workflow.json","inventory.json","exports.json","analysis.json"].map(read));
const source=path.resolve(projectRoot,p.source);
const bySource=new Map<string,any>(inv.files.map((f:any)=>[f.source,f]));
const byAnalysis=new Map<string,any>(analysis.files.map((f:any)=>[f.path,f]));
const byBatch=new Map<string,any>(p.batches.map((b:any)=>[b.id,b]));
const catalog=(f:any)=>f.source.endsWith(".models.ts")||["model-catalog.ts","models.generated.ts","image-models.generated.ts","providers/data-json.d.ts","providers/radius-config.ts"].some(n=>f.source==="packages/ai/src/"+n);
for(const f of inv.files){
 if(f.batch&&catalog(f)){
  f.batch="ai-foundation";
  f.targets=f.targets.map((n:string)=>n.replace("packages/ai/providers/","packages/ai/catalog/"));
  f.reason="静态目录提前到基础层，供 OAuth、模型集合和协议使用；catalog 不依赖 provider 工厂。";
 }
 if(f.source==="packages/agent/src/harness/result.ts"||f.source==="packages/agent/src/harness/execution/effect-gate.ts")f.batch="core-contracts";
 if(f.source==="packages/ai/src/utils/event-stream.ts")f.symbolSteps={EventStream:"ai-foundation/eventstream",AssistantMessageEventStream:"ai-foundation/types",createAssistantMessageEventStream:"ai-foundation/types"};
 if(f.source==="packages/agent/src/harness/session/testing/index.ts")f.selectedExports=f.exports.filter((n:string)=>!(/BENCHMARK|Benchmark/.test(n)));
}
const coreOrder=["core-contracts","core-loop","core-resources","core-session","core-compaction","core-runtime","core-harness"];
p.modules.find((m:any)=>m.id==="core").batches=coreOrder;
coreOrder.forEach((id,n)=>byBatch.get(id).dependsOn=n?[coreOrder[n-1]]:[]);
// Every watched reference is assigned once for translation/review. Direct imports win;
// test name matching is a fallback, not a claim that those tests have already passed in Go.
const referenceLedger=inv.referenceInputs.map((r:any)=>{
 const a=byAnalysis.get(r.source), scores=p.batches.map((b:any)=>({b,score:(a?.imports??[]).filter((e:any)=>bySource.get(e.target)?.batch===b.id).length*10}));
 scores.sort((a:any,b:any)=>b.score-a.score);
 let batch=scores[0].score?scores[0].b.id:r.source.includes("/ai/")?"ai-surface":r.source.includes("/telemetry/")?"ai-foundation":r.source.includes("/tools/")?"tools-env":"core-harness";
 return {...r,batch,classification:a?.test?"port-test-scenarios":"reference-context",status:"not-yet-run-against-go"};
});
for(const b of p.batches){
 b.sources=inv.files.filter((f:any)=>f.batch===b.id).map((f:any)=>f.source);
 b.references=[...new Set([...referenceLedger.filter((r:any)=>r.batch===b.id).map((r:any)=>r.source),...b.sources.flatMap((s:string)=>(byAnalysis.get(s)?.imports??[]).map((e:any)=>e.target).filter((t:string)=>bySource.has(t)&&!b.sources.includes(t)))])];
 b.outputs=[...new Set(inv.files.filter((f:any)=>f.batch===b.id).flatMap((f:any)=>f.targets))];
 if(b.id==="tools-delivery")b.outputs.push("cmd/pith/main.go","cmd/pith/main_test.go");
}
p.batches=p.modules.flatMap((m:any)=>m.batches.map((id:string)=>byBatch.get(id)));
for(const f of inv.files){
 const e=exp.files.find((e:any)=>e.source===f.source);e.batch=f.batch;e.targets=f.targets;
 for(const s of e.symbols){s.status=f.batch?(f.selectedExports&&!f.selectedExports.includes(s.upstream)?"excluded-by-explicit-scope":"behavior-contract-ready-signature-generated"):f.disposition;s.judge=f.batch?"judges/"+f.batch:null;}
 if(f.batch)f.mappingStatus="behavior-contract-ready-signature-generated";
}
await save("reference-ledger.json",referenceLedger);
await save("inventory.json",inv);await save("exports.json",exp);await save("plan.json",p);
if(process.argv.includes("--layout")){console.log("layout updated; regenerate TS oracles next");process.exit(0)}
const golden=await read("conformance/golden.json"), manifest=await read("assets/catalog-manifest.json");
const template=await readFile(path.join(root,"conformance/judge.go.txt"),"utf8");
const api=await readFile(path.join(root,"API.md"),"utf8");
const evaluate=await readFile(path.join(root,"conformance/evaluate.mts"),"utf8");
const assets=manifest.files.map((f:any)=>({source:"assets/catalog/"+f.file,target:f.target.replace("/.manifest.json","/manifest.json"),sha256:f.sha256}));
assets.push({source:"assets/catalog/LICENSE.txt",target:"packages/ai/catalog/data/LICENSE.txt",sha256:sha256(await readFile(path.join(root,"assets/catalog/LICENSE.txt")))});
const allOutputs:string[]=[];const materialSteps:any[]=[];
for(const b of p.batches){
 const savedEvent=w.batches["ai-foundation"].steps.find((s:any)=>s.id==="eventstream");
 const groups=b.id==="ai-foundation"?[
  {id:"types",sources:b.sources.filter((s:string)=>!catalog(bySource.get(s))),outputs:b.outputs.filter((s:string)=>!s.startsWith("packages/ai/catalog/")&&!s.includes("/eventstream/")),cases:golden.cases.filter((c:any)=>c.batch===b.id&&c.input.op!=="catalog")},
  {id:"catalog",sources:b.sources.filter((s:string)=>catalog(bySource.get(s))),outputs:b.outputs.filter((s:string)=>s.startsWith("packages/ai/catalog/")),cases:golden.cases.filter((c:any)=>c.batch===b.id&&c.input.op==="catalog")}
 ]:[{id:"port",sources:b.sources,outputs:[...b.outputs],cases:golden.cases.filter((c:any)=>c.batch===b.id)}];
 const steps:any[]=[];
 if(b.id==="ai-foundation") {if(!savedEvent)throw Error("missing original audited stream contract");steps.push(savedEvent);allOutputs.push(...savedEvent.outputs);b.outputs.push(...savedEvent.outputs.filter((n:string)=>!b.outputs.includes(n)));}
 for(const g of groups){
  const slug=b.id+(g.id==="port"?"":"-"+g.id),name=slug.split("-").map((s:string)=>s[0].toUpperCase()+s.slice(1)).join("");
  const prefix="internal/conformance/"+slug.replaceAll("-","_");
  const map=prefix+"/source_map.json";
  const delivery=b.id==="tools-delivery";
  const ownRefs=referenceLedger.filter((r:any)=>r.batch===b.id&&(g.id!=="catalog"));
  // Include all owned implementation files. Additional references remain readable as
  // frozen testdata if they do not fit the bounded source window.
  const extras=[...new Set([...b.references,...g.sources.flatMap((s:string)=>(byAnalysis.get(s)?.imports??[]).map((e:any)=>e.target).filter((t:string)=>bySource.has(t)))])].filter((s:string)=>!g.sources.includes(s));
  const selected=[...g.sources,...extras.slice(0,Math.max(0,79-g.sources.length))];
  b.references=[...new Set([...b.references,...selected.filter((s:string)=>!b.sources.includes(s))])];
  const out=[...g.outputs,map];
  if(!delivery)out.push(prefix+"/adapter_test.go");
  for(const dir of new Set(g.outputs.filter((s:string)=>s.endsWith(".go")&&!s.endsWith("_test.go")).map((s:string)=>path.posix.dirname(s))))out.push(dir+"/"+slug.replaceAll("-","_")+"_test.go");
  const outputs=[...new Set(out)];
  b.outputs=[...new Set([...b.outputs,...outputs,...(g.id==="catalog"?assets.map((a:any)=>a.target):[])])];
  allOutputs.push(...outputs);
  const symbols=g.sources.flatMap((s:string)=>{const f=bySource.get(s);return (f.selectedExports??f.exports).map((upstream:string)=>({source:s,upstream}))});
  const judge="judges/"+slug;
  const tests=["TestPortsmithJudge"+name+"Surface"];
  let code=template.replaceAll("__NAME__",name);
  if(delivery){
   code=code.slice(0,code.indexOf("// RunCase"))+code.slice(code.indexOf("type mapping"),code.indexOf("func TestPortsmithJudge"+name+"Adapter"));
   code=code.replace(' "context"\n',"").replace(' "reflect"\n',"").replace(' "strconv"\n',"").replace(' "time"\n',"");
   await put(judge+"/"+prefix+"/portsmith_judge_delivery_test.go",await readFile(path.join(root,"conformance/delivery.go.txt"),"utf8"));tests.push("TestPortsmithJudgeToolsDeliveryCLI");
  }else {if(!g.cases.length)throw Error("missing behavior cases: "+slug);tests.push("TestPortsmithJudge"+name+"Cases","TestPortsmithJudge"+name+"Adapter");}
  await put(judge+"/"+prefix+"/portsmith_judge_test.go",code);
  for(const [index,c] of g.cases.entries())await save(judge+"/"+prefix+"/testdata/case-"+String(index).padStart(3,"0")+".json",c);
  await save(judge+"/"+prefix+"/testdata/surface.json",{outputs:[...new Set(allOutputs)],symbols,map});
  await put(judge+"/"+prefix+"/testdata/evaluate.ts.txt",evaluate);
  await save(judge+"/"+prefix+"/testdata/reference-ledger.json",ownRefs);
  for(const r of ownRefs)await put(judge+"/"+prefix+"/testdata/upstream/"+r.source+".txt",await readFile(path.join(source,r.source),"utf8"));
  const contract="contracts/"+slug+".md";
  const requirements=delivery?`Implement cmd/pith as a native CLI using the completed Go SDK. Flags: --help, --prompt, --base-url, --model, --api-key, --cwd, --session. Missing model/base URL/key fails cleanly (nonzero). The supplied local OpenAI Chat Completions service requests read, write, edit and bash in successive turns. Persist and reload the full conversation through --session. Print assistant text, never credentials. No Node, npm or TS runtime. --api-key may also come from PITH_API_KEY. No input or interactive prompt is required for this command. Read the frozen delivery test for the exact wire protocol.`:
`Generate ${prefix}/adapter_test.go in package conformance. Its ONLY test bridge is:\n\nfunc RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error)\n\nTranslate each input operation into calls to the REAL exported Go SDK; do not implement the SDK behavior in this bridge. Use test-local HTTP servers, filesystem dirs and fixed clocks as in testdata/evaluate.ts.txt. Input.op=call invokes the corresponding source function with input.args; catalog reads its actual embedded data; protocol invokes the real streaming protocol and normalizes only fields stated by the evaluator. Go errors need not match JS framework wording, but successful/error classification must match. Undefined fields use {"$undefined":true}, distinct from null. Never read expected results, golden files, TS source or invoke Node in the adapter. Do not hard-code vectors or provider/model results. Use read_judge to inspect operation evaluator and cases; it is read-only.\n\nThe production API is typed and idiomatic Go, not a JSON dispatch interpreter. The bridge is test-only and never ships in the binary.`;
  await put(contract,`# ${slug}: executable behavior contract\n\nPinned source: ${p.revision}. Entire batch behavior: ${b.behaviors.join("; ")}.\nAcceptance scope: ${b.acceptance.join("; ")}.\n\n${requirements}\n\n## Complete implementation and export mapping\n\nPort ALL in-scope behavior in the listed sources, not just the fixture examples. Source of truth is the pinned TS implementation and its tests. Translate the upstream tests in testdata/reference-ledger.json into Go self-tests; reference-context entries are context, not executable tests. Live credentials/cloud hardware tests become deterministic fake HTTP/env scenarios with the limitation recorded. Keep the exact enum/wire JSON semantics.\n\nFill ${map} with one row per frozen source symbol in testdata/surface.json: {source, upstream, goPackage, goSymbol, reason}. goPackage is repo-relative (packages/...), goSymbol is an actual exported top-level declaration. Generic types and function aliases are valid; empty marker declarations are not substitutes for functionality. Barrel symbols map to their real public equivalent. Prefix colliding protocol stream names and OAuth login/refresh names by provider. Do not create import cycles. Exact Go function signatures are selected during translation under the type rules below and compiled by Go; they are not falsely claimed to have been precompiled now. Fields/methods remain part of their enclosing type's complete behavior.\n\n## Read and write scope\n\nPrimary sources:\n${g.sources.map((s:string)=>"- "+s).join("\n")}\n\nAllowed new outputs:\n${outputs.map((s:string)=>"- "+s).join("\n")}\n\nCurrent-module earlier candidates may be fixed for integration. Prior modules are immutable. Public option/compaction/session DTOs referenced by later implementations belong in the early shared types packages now. Read direct type references before selecting their shape. Go compile failures are repairable candidate failures, not instructions to drop a later API.\n\nDependencies are pinned in candidate go.mod/go.sum. No additional dependency installation by the generator. Use net/http, encoding/json and standard crypto for HTTP/SSE/OAuth; AWS SDK v2 for Bedrock, coder/websocket for WS, jsonschema/v6 for schema, goccy/go-yaml, go-diff and go-gitignore for resource/tool behavior. Preserve explicit coercion/repair logic that these libraries do not supply.\n\n${g.id==="catalog"?"Catalog JSON and MIT license are frozen read-only seed assets in packages/ai/catalog/data. Embed them with go:embed and expose all source catalog symbols; never rewrite the JSON. The publication .manifest.json is shipped as manifest.json because outputs exclude dotfiles.\n":""}\n## Architecture and conversion rules\n\n${api}\n`);
  const step:any={id:g.id,sources:selected,goal:`完整移植 ${slug}，按冻结行为契约生成所有来源导出、生产实现、测试适配器和自测。逐段读取源码与 read_judge 中的独立测试/上游测试；不能只实现 fixtures 中出现的路径。`,contract,judge,outputs,tests,race:true};
  if(g.id==="catalog")step.assets=assets;
  steps.push(step);materialSteps.push({batch:b.id,step:g.id,contract,judge,sources:g.sources.length,sourceReadFiles:selected.length,cases:g.cases.length,tests,outputs:outputs.length});
 }
 w.batches[b.id]={status:"ready",steps};
}
p.status="execution-materials-prepared";p.planRevision="full-2";
p.execution={...p.execution,ready:true,canStart:true,status:"prepared-not-migrated",startPolicy:"all-prepared",blockers:p.execution.blockers.map((b:any)=>({...b,status:"resolved",description:({E01:"v2 executor, cumulative verification, module commits/recovery, read-only assets and judges",E02:"Frozen behavior/test bridge, typed Go API transformation rules and complete source symbol mapping gates; concrete SDK signatures chosen and compiled during migration",E03:"Pinned TS golden cases, per-step independent judges, final native CLI tool-loop/session gate; readiness/audit.json records judge controls",E04:"pi-ai 0.87.1 npm archive verified by integrity; static catalogs embedded with hashes and MIT license",E05:"Pinned Go dependencies, compatibility probes and license evidence in readiness/dependencies.json"} as any)[b.id]}))};
w.status="prepared-not-migrated";w.executionReady=true;w.canStart=true;w.blockers=[];w.startPolicy="all-prepared";
await save("plan.json",p);await save("workflow.json",w);
await save("readiness/materials.json",{revision:p.revision,modules:3,batches:p.batches.length,steps:materialSteps,tsCases:golden.cases.length,productGoGenerated:false,fullParityProven:false});
console.log(JSON.stringify({batches:p.batches.length,steps:materialSteps.length+1,cases:golden.cases.length,assets:assets.length}));
