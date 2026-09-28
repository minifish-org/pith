import { build } from "../../../portsmith/node_modules/esbuild/lib/main.js";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { cases, ai, agent } from "./inputs.mts";
import "./protocols.mts";
import { projectRoot, migrationRoot, sha256 } from "../plan-lib.mts";
const pin=JSON.parse(await readFile(path.join(migrationRoot,"upstream.json"),"utf8"));
const source=path.join(projectRoot,pin.sourceDir), runtime=path.resolve(projectRoot,"../portsmith/node_modules");
const plan=JSON.parse(await readFile(path.join(migrationRoot,"plan.json"),"utf8"));
for(const file of plan.batches.find((b:any)=>b.id==="ai-providers").sources){
  const text=await readFile(path.join(source,file),"utf8");
  for(const match of text.matchAll(/export function (\w+Provider)\(/g))cases.push({batch:"ai-providers",id:match[1],input:{op:"provider",file,fn:match[1]}});
}
cases.push({batch:"ai-surface",id:"compat-providers",input:{op:"call",file:ai("compat"),fn:"getProviders",args:[]}});
for(const file of plan.batches.find((b:any)=>b.id==="ai-foundation").sources.filter((s:string)=>s.endsWith(".models.ts"))) {
 const item=JSON.parse(await readFile(path.join(migrationRoot,"inventory.json"),"utf8")).files.find((f:any)=>f.source===file);
 for(const symbol of item.exports)cases.push({batch:"ai-foundation",id:"catalog-"+symbol,input:{op:"catalog",file,symbol}});
}
const extra=[agent("harness/context"),agent("harness/env/nodejs"),agent("harness/session/memory"),ai("utils/event-stream"),ai("models"),ai("providers/faux")];
const files=[...new Set([...cases.map(c=>c.input.file),...extra])];
const cache=path.join(projectRoot,".cache/conformance");await mkdir(cache,{recursive:true});
const input=files.map((file,n)=>`import * as m${n} from ${JSON.stringify(path.join(source,file))};`).join("\n")+`\nexport const modules={${files.map((f,n)=>JSON.stringify(f)+`:m${n}`).join(",")}};\nexport { evaluate, encode } from ${JSON.stringify(path.join(migrationRoot,"conformance/evaluate.mts"))};`;
await build({stdin:{contents:input,resolveDir:migrationRoot,sourcefile:"pinned-oracle-entry.mts",loader:"ts"},bundle:true,treeShaking:false,platform:"node",format:"esm",target:"node22",outfile:path.join(cache,"oracle.mjs"),nodePaths:[path.join(migrationRoot,"node_modules"),runtime],alias:{"@earendil-works/chord/context":path.join(source,"packages/chord/src/context/index.ts")},plugins:[{name:"pinned-catalog",setup(b){b.onResolve({filter:/providers\/images\/register-builtins\.ts$/},args=>({path:path.resolve(args.resolveDir,args.path),sideEffects:true}));b.onResolve({filter:/data\/.*\.json$/},args=>({path:path.join(migrationRoot,"assets/catalog",path.basename(args.path))}));}}],packages:"bundle",banner:{js:"import {createRequire as __createRequire} from 'node:module';const require=__createRequire(import.meta.url);"},logLevel:"warning"});
const {modules,evaluate,encode}=await import(pathToFileURL(path.join(cache,"oracle.mjs")).href+`?t=${Date.now()}`);
const golden=[];
for(const c of cases){let expected;try{expected={ok:true,value:encode(await evaluate(modules,structuredClone(c.input)))};}catch(e){const message=e instanceof Error?e.message:String(e);if(/Invalid successful protocol fixture|is not a function|Unknown oracle operation/.test(message))throw e;expected={ok:false,error:message};}golden.push({...c,expected});}
await writeFile(path.join(migrationRoot,"conformance/golden.json"),JSON.stringify({revision:pin.commit,normalization:"Only explicit undefined marker, Map/Set serialization and temporary filesystem root. Errors retain TS text as evidence; Go must fail the same scenario, framework stack text is not compared.",files:await Promise.all(files.map(async sourceName=>({source:sourceName,sha256:sha256(await readFile(path.join(source,sourceName)))}))),cases:golden},null,2)+"\n");
console.log(JSON.stringify({cases:golden.length,byBatch:Object.fromEntries([...new Set(golden.map(c=>c.batch))].map(b=>[b,golden.filter(c=>c.batch===b).length])),errors:golden.filter(c=>!c.expected.ok).map(c=>({id:c.batch+"/"+c.id,error:c.expected.error}))},null,2));
