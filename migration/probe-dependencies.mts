import assert from "node:assert/strict";
import {mkdtemp,readFile,writeFile,readdir,rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";
import {execFile} from "node:child_process";
import {promisify} from "node:util";
import {projectRoot,migrationRoot,sha256} from "./plan-lib.mts";
const exec=promisify(execFile), temp=await mkdtemp(path.join(tmpdir(),"pith-deps-"));
try {
 for(const name of ["go.mod","go.sum"])await writeFile(path.join(temp,name),await readFile(path.join(projectRoot,name)));
 await writeFile(path.join(temp,"dependencies_test.go"),await readFile(path.join(migrationRoot,"conformance/dependencies.go.txt")));
 const result=await exec("go",["test","-mod=readonly","-count=1","-race","-timeout=20s","./..."],{cwd:temp,timeout:180000,env:{...process.env,GOTOOLCHAIN:"local",GOPROXY:"off",GOSUMDB:"off",GOMAXPROCS:"2",GOMEMLIMIT:"512MiB"}});
 assert.match(result.stdout,/ok\s/);
 const {stdout}=await exec("go",["list","-m","-json","all"],{cwd:projectRoot});
 const mods=JSON.parse("["+stdout.trim().replace(/}\s*{/g,"},{")+"]").filter((m:any)=>!m.Main);
 const dependencies=[];
 for(const m of mods){
  const dir=m.Dir??JSON.parse((await exec("go",["mod","download","-json",m.Path+"@"+m.Version],{cwd:projectRoot})).stdout).Dir;
  const licenses=(await readdir(dir)).filter(n=>/^licen[cs]e(?:\.|$)/i.test(n));assert(licenses.length,"missing license for "+m.Path);
  dependencies.push({path:m.Path,version:m.Version,goVersion:m.GoVersion,licenses:await Promise.all(licenses.map(async file=>({file,sha256:sha256(await readFile(path.join(dir,file)))})))});
 }
 const report={status:"dependencies-probed",platform:process.platform,arch:process.arch,go:(await exec("go",["version"])).stdout.trim(),goModSha256:sha256(await readFile(path.join(projectRoot,"go.mod"))),goSumSha256:sha256(await readFile(path.join(projectRoot,"go.sum"))),probeSha256:sha256(await readFile(path.join(migrationRoot,"conformance/dependencies.go.txt"))),checks:["AWS client construction/credential injection/SigV4","binary eventstream roundtrip and corrupt CRC rejection","local WebSocket roundtrip","OAuth PKCE parameter","YAML false/unicode","gitignore negation","diff patch roundtrip","JSON Schema valid/invalid"],race:true,dependencies,note:"Compatibility probes only; they do not prove Pi protocol parity. oauth2 v0.28.0 retains Go 1.24 support; latest required Go 1.26. No live provider credentials used."};
 await writeFile(path.join(migrationRoot,"readiness/dependencies.json"),JSON.stringify(report,null,2)+"\n");console.log(JSON.stringify({status:report.status,dependencies:dependencies.length,checks:report.checks.length}));
} finally {await rm(temp,{recursive:true,force:true})}
