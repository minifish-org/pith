/** Compile and test verifier controls in temporary directories. Never emit Pith implementations. */
import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile, rm, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { inspectModules } from "../../portsmith/src/modules.ts";
import { copyFiles, hash } from "../../portsmith/src/files.ts";
import { executeGo, succeeded, testResults } from "../../portsmith/src/process.ts";
import { projectRoot, migrationRoot } from "./plan-lib.mts";
const exec=promisify(execFile),i=await inspectModules(migrationRoot);
const items=i.items.filter(s=>s.spec.id!=="eventstream");
const temp=await mkdtemp(path.join(tmpdir(),"pith-full-audit-"));
const put=async(name:string,text:string)=>{await mkdir(path.dirname(path.join(temp,name)),{recursive:true});await writeFile(path.join(temp,name),text)};
const stub=`package conformance\nimport("context";"encoding/json")\nfunc RunCase(context.Context,json.RawMessage)(json.RawMessage,error){return json.RawMessage("null"),nil}\n`;
try {
 await put("go.mod","module github.com/minifish-org/pith\n\ngo 1.24\n");
 const adapters:string[]=[];
 for(const item of items){
  await copyFiles(temp,item.judge);
  if(item.batch==="tools-delivery")continue;
  const file=item.spec.outputs.find(n=>n.endsWith("/adapter_test.go"))!;assert(file);
  adapters.push(file);await put(file,stub);
 }
 const compile=await executeGo(temp,["-run","^$"]);assert(succeeded(compile),compile.log);
 const wrong=await executeGo(temp,["-run","Cases$"]);if(wrong.timedOut||wrong.truncated){await writeFile(path.join(migrationRoot,"readiness/audit-failure.log"),wrong.log);throw Error(JSON.stringify({code:wrong.code,timedOut:wrong.timedOut,truncated:wrong.truncated,tail:wrong.log.slice(-1000)}))};
 for(const item of items.filter(s=>s.batch!=="tools-delivery"))assert(testResults(wrong).failed>0&&wrong.log.includes(item.spec.tests.find(n=>n.endsWith("Cases"))!),"missing negative control: "+item.key);
 // Positive comparator control deliberately reads fixtures; it is NOT an SDK
 // implementation, is never copied to a candidate, and must fail the Adapter gate.
 const positive=`package conformance
import("context";"encoding/json";"os";"path/filepath";"errors";"reflect")
func RunCase(_ context.Context,input json.RawMessage)(json.RawMessage,error){
 var wanted any;if err:=json.Unmarshal(input,&wanted);err!=nil{return nil,err}
 files,_:=filepath.Glob("testdata/case-*.json");for _,f:=range files{data,_:=os.ReadFile(f);var c struct{Input any;Expected struct{OK bool;Value json.RawMessage}};_ = json.Unmarshal(data,&c);if reflect.DeepEqual(wanted,c.Input){if !c.Expected.OK{return nil,errors.New("expected failure")};return c.Expected.Value,nil}};return nil,errors.New("unknown control")}
`;
 for(const file of adapters)await put(file,positive);
 const good=await executeGo(temp,["-run","Cases$"],false,true);assert(succeeded(good),good.log);
 const rejectLookup=await executeGo(temp,["-run","Adapter$"]);assert(!succeeded(rejectLookup)&&testResults(rejectLookup).failed===adapters.length,rejectLookup.log);
 // Corrupt one successful expected return in every comparator control.
 for(const file of adapters)await put(file,positive.replace("return c.Expected.Value,nil","return json.RawMessage(`{\"audit_mutation\":true}`),nil"));
 const mutations=await executeGo(temp,["-run","Cases$"]);assert(!mutations.timedOut&&!mutations.truncated);
 const mutationFailures=testResults(mutations);
 for(const item of items.filter(s=>s.batch!=="tools-delivery")){
  const name=item.spec.tests.find(n=>n.endsWith("Cases"))!;
  assert(mutations.log.split("\n").some(l=>{try{const e=JSON.parse(l);return e.Test===name&&e.Action==="fail"}catch{return false}}),"mutation not detected: "+name);
 }
 // Exercise source-surface validators with a small valid structural control,
 // then remove its export. This checks the gate, not the full future Go API.
 await put("packages/audit/control.go","package audit\nconst Value=1\n");
 for(const item of items){
  const surface=item.judge.find(f=>f.name.endsWith("/testdata/surface.json"))!;
  const spec=JSON.parse(surface.data.toString());
  const rows=[{source:"control.ts",upstream:"value",goPackage:"packages/audit",goSymbol:"Value",reason:"temporary positive structural control"}];
  await put(spec.map,JSON.stringify(rows));
  await put(surface.name,JSON.stringify({outputs:["packages/audit/control.go"],symbols:rows,map:spec.map}));
 }
 for(const file of adapters)await put(file,`package conformance\nimport("context";"encoding/json";"github.com/minifish-org/pith/packages/audit")\nfunc RunCase(context.Context,json.RawMessage)(json.RawMessage,error){_ = audit.Value;return json.RawMessage("null"),nil}\n`);
 const structures=await executeGo(temp,["-run","(Surface|Adapter)$"]);assert(succeeded(structures),structures.log);
 await put("packages/audit/control.go","package audit\nconst Missing=1\n");
 // Keep bridge compilable so failure proves the surface check, not a compiler error.
 for(const file of adapters)await put(file,stub);
 const missing=await executeGo(temp,["-run","Surface$"]);assert(testResults(missing).failed===items.length,missing.log);
 await put("cmd/pith/main.go","package main\nfunc main(){}\n");
 const cliWrong=await executeGo(temp,["-run","ToolsDeliveryCLI$"]);assert(cliWrong.log.includes("help:")&&testResults(cliWrong).failed===1,cliWrong.log);
 // Prepare every actual task snapshot to check size limits, fixture isolation and
 // required source presence without generating or calling any model.
 const {prepareTask}=await import("../../portsmith/src/workspace.ts");
 let judges:any[]=[],assets:any[]=[];
 for(const [n,item]of i.items.entries()){
  judges.push(...item.judge);assets.push(...item.assets);
  await prepareTask({source:i.source,out:path.join(temp,"snapshots",String(n)),files:item.spec.sources,revision:i.p.revision,goal:item.spec.goal,rules:path.join(i.root,"RULEBOOK.md"),goMod:path.join(i.project,"go.mod"),goSum:path.join(i.project,"go.sum"),contract:item.contract,judgeFiles:judges,requiredJudgeTests:item.spec.tests,moduleTask:true,seed:assets,writableFiles:item.spec.outputs});
 }
 const report={status:"execution-materials-audited",revision:i.p.revision,steps:i.items.length,batches:i.p.batches.length,judgePackages:items.length,behaviorPackages:adapters.length,independentCases:JSON.parse(await readFile(path.join(migrationRoot,"conformance/golden.json"),"utf8")).cases.length,allJudgesCompile:true,comparatorPositiveControlsWithRace:true,knownNullResultsRejected:true,wrongValuesRejected:true,fixtureLookupAdaptersRejected:true,exportSurfacePositiveAndMissingControls:true,deliveryJudgeCompilesAndRejectsEmptyBinary:true,fullTaskSnapshotsPrepared:i.items.length,modelCalls:0,productGoGenerated:false,fullParityProven:false,limits:"Controls test verifier mechanics, not a correct port. CLI full success, Go behavioral parity and platform runtime checks require the generated SDK. Original EventStream has its separate audit.",materials:i.items.map(s=>({key:s.key,seal:s.seal,judgeFiles:s.judge.map(f=>({name:f.name,sha256:hash(f.data)})),contractSha256:hash(s.contract)}))};
 await writeFile(path.join(migrationRoot,"readiness/audit.json"),JSON.stringify(report,null,2)+"\n");
 console.log(JSON.stringify({...report,materials:undefined},null,2));
}finally {await rm(temp,{recursive:true,force:true})}
