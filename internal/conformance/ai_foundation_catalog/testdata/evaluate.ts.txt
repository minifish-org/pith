import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { gunzipSync, brotliDecompressSync, zstdDecompressSync } from "node:zlib";
// Generated bundle injects original, pinned TS modules into this map.
export async function evaluate(modules: Record<string, any>, input: any): Promise<any> {
  const m=modules[input.file];
  const ai=(p:string)=>modules[`packages/ai/src/${p}.ts`];
  const ag=(p:string)=>modules[`packages/agent/src/${p}.ts`];
  const ctx=modules["packages/agent/src/harness/context.ts"].BACKGROUND_CONTEXT;
  if(input.op==="catalog") return m[input.symbol];
  if(input.op==="provider") {
    const value=m[input.fn]();const p=value.provider??value;const models=await p.getModels();return {id:p.id,name:p.name,models:models.map((x:any)=>({id:x.id,api:x.api,provider:x.provider,reasoning:x.reasoning,input:x.input,cost:x.cost,contextWindow:x.contextWindow,maxTokens:x.maxTokens})).sort((a:any,b:any)=>a.id.localeCompare(b.id)),stream:typeof p.stream==="function",streamSimple:typeof p.streamSimple==="function"};
  }
  if(input.op==="protocol") {
    const requests:any[]=[]; let responseBytes=Buffer.from(input.body);
    if(input.api==="bedrock-converse-stream"&&input.status===200){
      // AWS eventstream framing with both CRCs, not an SSE substitute.
      const crc=(b:Uint8Array)=>{let c=0xffffffff;for(const x of b){c^=x;for(let i=0;i<8;i++)c=(c>>>1)^((c&1)?0xedb88320:0);}return (c^0xffffffff)>>>0;};
      const header=(name:string,value:string)=>{const n=Buffer.from(name),v=Buffer.from(value),b=Buffer.alloc(n.length+v.length+4);b[0]=n.length;n.copy(b,1);b[n.length+1]=7;b.writeUInt16BE(v.length,n.length+2);v.copy(b,n.length+4);return b;};
      responseBytes=Buffer.concat(input.bedrock.map((e:any)=>{const type=Object.keys(e)[0],body=Buffer.from(JSON.stringify(e[type])),headers=Buffer.concat([header(":message-type","event"),header(":event-type",type),header(":content-type","application/json")]),b=Buffer.alloc(16+headers.length+body.length);b.writeUInt32BE(b.length,0);b.writeUInt32BE(headers.length,4);b.writeUInt32BE(crc(b.subarray(0,8)),8);headers.copy(b,12);body.copy(b,12+headers.length);b.writeUInt32BE(crc(b.subarray(0,-4)),b.length-4);return b;}));
    }
    const server=createServer(async(req,res)=>{try{const chunks=[];for await(const b of req)chunks.push(b);let bytes=Buffer.concat(chunks);const encoding=req.headers["content-encoding"];if(encoding==="gzip")bytes=gunzipSync(bytes);if(encoding==="br")bytes=brotliDecompressSync(bytes);if(encoding==="zstd")bytes=zstdDecompressSync(bytes);const raw=bytes.toString();requests.push({method:req.method,path:req.url,body:raw?JSON.parse(raw):null});res.writeHead(input.status,{"content-type":input.status!==200?"application/json":input.api==="bedrock-converse-stream"?"application/vnd.amazon.eventstream":"text/event-stream"});if(input.mode==="fragmented"){for(let n=0;n<responseBytes.length;n+=3)res.write(responseBytes.subarray(n,n+3));res.end();}else res.end(responseBytes);}catch(error){res.writeHead(400);res.end(JSON.stringify({error:String(error)}));}});
    await new Promise<void>(r=>server.listen(0,"127.0.0.1",r));const address=server.address() as any,baseUrl=`http://127.0.0.1:${address.port}`;
    const model={id:"fixture",name:"Fixture",api:input.api,provider:input.api==="google-vertex"?"google-vertex":"fixture",baseUrl,reasoning:false,input:["text","image"],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:8192,maxTokens:1024};
    const controller=new AbortController();if(input.mode==="cancelled")controller.abort();const timeout=setTimeout(()=>controller.abort(),4000);
    const apiKey=input.api==="openai-codex-responses"?`x.${Buffer.from(JSON.stringify({"https://api.openai.com/auth":{chatgpt_account_id:"fixture"}})).toString("base64url")}.x`:"fixture-key";
    try{
      const stream=m.stream(model,{messages:[{role:"system",content:"system",timestamp:1},{role:"user",content:"hello",timestamp:2}]},{apiKey,bearerToken:"fixture-key",region:"us-east-1",project:"fixture",location:"us-central1",transport:"sse",signal:controller.signal,maxTokens:16,temperature:0,env:{AWS_BEDROCK_FORCE_HTTP1:"1",NO_PROXY:"*"},...(!input.api.startsWith("google-")&&input.api!=="bedrock-converse-stream"?{fetch:async(url:any,init:any)=>{if(!String(url instanceof Request?url.url:url).startsWith(baseUrl))throw Error("Oracle denied external URL");return fetch(url,init);}}:{})});
      const events=[];for await(const e of stream)events.push(e.type);const result=await stream.result();
      if(["text","fragmented"].includes(input.mode)&&!["stop","toolUse"].includes(result.stopReason))throw Error(`Invalid successful protocol fixture: ${input.api}: ${result.errorMessage}`);
      // Error spelling and generated wall-clock metadata differ across SDKs; preserve terminal classification and response data.
      return {requests:requests.map(r=>({...r,path:r.path.replace(/api_key=[^&]+/g,"api_key=fixture-key")})),events,content:result.content,usage:result.usage,stopReason:result.stopReason,responseId:result.responseId??null};
    } finally {clearTimeout(timeout);server.closeAllConnections();await new Promise<void>(r=>server.close(()=>r()));}
  }
  if(input.op==="call") {
    const args=structuredClone(input.args);
    if(input.fn==="detectSupportedImageMimeType")args[0]=new Uint8Array(args[0]);
    return await m[input.fn](...args);
  }
  if(input.op==="telemetry") {
    const telemetry=new m.InMemoryTelemetryContext();
    try { await telemetry.startSpan({name:"parent",attributes:{empty:"",zero:0,flag:false}},async(span:any)=>{
      span.addEvent("begin",{values:[1,2]});
      await span.startSpan({name:"child"},()=>{if(input.fail)throw Error("fixture error"); return 42;});
    }); } catch {}
    return telemetry.spans ?? telemetry.getSpans();
  }
  if(input.op==="assistant-stream") {
    const stream=new m.AssistantMessageEventStream();
    stream.push({type:"start",partial:input.message});
    stream.push(input.terminal==="done"?{type:"done",reason:"stop",message:input.message}:{type:"error",reason:"error",error:{...input.message,stopReason:"error",errorMessage:"fixture"}});
    const events=[];for await(const event of stream)events.push(event);
    return {events,result:await stream.result()};
  }
  if(input.op==="credentials") {
    const store=new m.InMemoryCredentialStore(); const signal=AbortSignal.abort();
    if(input.aborted) return await store.read("p",{signal});
    await store.modify("p",async()=>({type:"api_key",key:"first"}));
    await Promise.all([store.modify("p",async(c:any)=>({...c,key:c.key+"-second"})),store.modify("p",async(c:any)=>({...c,key:c.key+"-third"}))]);
    const read=await store.read("p"),list=await store.list(); await store.modify("p",async()=>undefined);
    const retained=await store.read("p");await store.delete("p");return {read,list,retained,deleted:await store.read("p")};
  }
  if(input.op==="pkce") {const a=await m.generatePKCE(),b=await m.generatePKCE();return {length:a.verifier.length,urlSafe:/^[A-Za-z0-9_-]+$/.test(a.verifier),challengeMatches:createHash("sha256").update(a.verifier).digest("base64url")===a.challenge,distinct:a.verifier!==b.verifier};}
  if(input.op==="model-store") {
    const s=new m.InMemoryModelsStore();if(input.aborted)return await s.read("p",{signal:AbortSignal.abort()});
    const v={models:[{id:"one"}],etag:'"v1"',checkedAt:0};await s.write("p",v);v.models[0].id="wrong";const a=await s.read("p");const original=structuredClone(a);a.models[0].id="wrong2";const b=await s.read("p");await s.delete("p");return {original,reread:b,deleted:await s.read("p")};
  }
  if(input.op==="agent-state") {
    const a=new m.Agent({streamFn:()=>{throw Error("unexpected model");},initialState:{systemPrompt:"system",thinkingLevel:"low",messages:[{role:"user",content:"hi",timestamp:1}]}});const before={messages:structuredClone(a.state.messages),thinkingLevel:a.state.thinkingLevel};a.steer({role:"user",content:"queued",timestamp:2});const queued=a.hasQueuedMessages();a.reset();return {before,queued,after:{messages:a.state.messages,isStreaming:a.state.isStreaming,pendingToolCalls:[...a.state.pendingToolCalls],queued:a.hasQueuedMessages()}};
  }
  if(input.op==="agent-loop") {
    const counts:string[]=[], events:string[]=[], streamMod=ai("utils/event-stream");let requests=0;
    const make=(content:any[],stopReason:string)=>({role:"assistant",api:"openai-completions",provider:"fixture",model:"fixture",content,usage:{input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}},stopReason,timestamp:1});
    const streamFn=()=>{const s=new streamMod.AssistantMessageEventStream();const msg=requests++===0?make([1,2].map(n=>({type:"toolCall",id:`c${n}`,name:"echo",arguments:{n}})),"toolUse"):make([{type:"text",text:"done"}],"stop");queueMicrotask(()=>s.push({type:"done",reason:msg.stopReason,message:msg}));return s;};
    const a=new m.Agent({streamFn,toolExecution:input.parallel?"parallel":"sequential",initialState:{tools:[{name:"echo",label:"echo",description:"echo",parameters:{type:"object",properties:{n:{type:"number"}},required:["n"]},execute:async(_id:string,args:any)=>{counts.push(String(args.n));return {content:[{type:"text",text:String(args.n)}],details:{}};}}]}});
    a.subscribe((e:any)=>events.push(e.type));await a.prompt({role:"user",content:"go",timestamp:1});
    return {requests,toolCalls:counts.sort(),roles:a.state.messages.map((x:any)=>x.role),results:a.state.messages.filter((x:any)=>x.role==="toolResult").map((x:any)=>({toolCallId:x.toolCallId,content:x.content,isError:x.isError})).sort((a:any,b:any)=>a.toolCallId.localeCompare(b.toolCallId)),ended:events.at(-1),streaming:a.state.isStreaming};
  }
  if(input.op==="session") {
    const repo=new m.MemorySessionRepo({now:()=>1});const s=await repo.create({id:"session"},ctx);
    try {
      await s.setName("demo",ctx);await s.createBranch("main",null,ctx);const name=await s.getName(ctx),branch=await s.branch("main",ctx);await s.setLabel("e1","label",ctx);const label=await s.getLabel("e1",ctx);await s.close(ctx);const reopened=await repo.open(s.metadata,ctx);const again=await reopened.getName(ctx);await reopened.close(ctx);await repo.delete(s.metadata,ctx);return {name,branch,label,again,remaining:await repo.list(undefined,ctx)};
    } finally {await repo.close(ctx);}
  }
  if(input.op==="reducer") {const snapshot=structuredClone(input.snapshot),reductions=[];for(const e of input.events)reductions.push(m.reduceLaneSnapshot(snapshot,structuredClone(e)));return {snapshot,reductions};}
  if(input.op==="tagged-error") {const e=new m[input.tag](input.props);return {tag:e._tag,name:e.name,message:e.message,json:e.toJSON(),matches:m[input.tag].is(e),rejectPlain:m[input.tag].is(input.props)};}
  if(input.op==="harness-lifecycle") {
    const repo=new (ag("harness/session/memory").MemorySessionRepo)({now:()=>1});const s=await repo.create({id:"session"},ctx);
    const provider=ai("providers/faux").fauxProvider(),models=ai("models").createModels();models.setProvider(provider.provider);
    const {harness}=await m.AgentHarness.create({session:s,models,model:provider.getModel(),activeToolNames:[],thinkingLevel:"low"},ctx);
    const before=await harness.lanes(ctx);const [a,b]=await Promise.all([harness.lane("main",ctx),harness.lane("main",ctx)]);
    const result={before,same:a===b,tip:await a.getTipId(ctx),thinking:await a.getThinkingLevel(ctx),tools:await a.getActiveTools(ctx),lanes:(await harness.lanes(ctx)).map((x:any)=>({name:x.name,tipId:x.tipId}))};await harness.close(ctx);await repo.close(ctx);return result;
  }
  if(input.op==="tool"||input.op==="env") {
    const root=await mkdtemp(path.join(tmpdir(),"pith-oracle-"));const env=new (ag("harness/env/nodejs").NodeExecutionEnv)({cwd:root});
    try {
      if(input.op==="env") {await mkdir(path.join(root,"nested"));const put=await env.writeFile("nested/f.txt",input.content,ctx),get=await env.readTextFile("nested/f.txt",ctx);return {writeOK:put.ok,read:get};}
      await writeFile(path.join(root,"file.txt"),input.bytes?Buffer.from(input.bytes):input.text??"");
      const invocation={invocationId:"i1",operationId:"r1",turnId:"t1",getMemo:async()=>undefined,setMemo:async()=>{}};
      const tool=m[input.fn]();const args=tool.prepareArguments?tool.prepareArguments(structuredClone(input.args)):input.args;
      const result=await tool.execute("c1",args,()=>{},{env},invocation,ctx);
      const after=input.inspect?await readFile(path.join(root,input.inspect),"utf8"):undefined;
      // Only the temporary root is normalized; text, truncation and order are preserved.
      return JSON.parse(JSON.stringify({result,after},(_k,v)=>typeof v==="string"?v.replaceAll(root,"$ROOT"):v));
    } finally {await rm(root,{recursive:true,force:true});}
  }
  throw Error(`Unknown oracle operation ${input.op}`);
}
export function encode(value:any):any {
  if(value===undefined)return {$undefined:true};
  if(Array.isArray(value))return value.map(encode);
  if(value instanceof Map)return [...value].map(([k,v])=>[encode(k),encode(v)]);
  if(value instanceof Set)return [...value].map(encode);
  if(value&&typeof value==="object")return Object.fromEntries(Object.entries(value).map(([k,v])=>[k,encode(v)]));
  return value;
}
