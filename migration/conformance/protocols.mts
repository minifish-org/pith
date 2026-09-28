import { add, ai } from "./inputs.mts";
const sse=(events:any[])=>events.map(e=>`event: ${e.type??"message"}\ndata: ${JSON.stringify(e)}\n\n`).join("");
const chat=(content:any)=>sse([{id:"r1",choices:[{index:0,delta:content,finish_reason:null}]},{id:"r1",choices:[{index:0,delta:{},finish_reason:content.tool_calls?"tool_calls":"stop"}],usage:{prompt_tokens:2,completion_tokens:1,total_tokens:3}}])+"data: [DONE]\n\n";
const message={id:"msg1",type:"message",role:"assistant",status:"completed",content:[{type:"output_text",text:"你好",annotations:[]}]};
const responses=sse([{type:"response.created",response:{id:"r1",model:"fixture",output:[],status:"in_progress"}},{type:"response.output_item.added",output_index:0,item:{...message,status:"in_progress",content:[]}},{type:"response.content_part.added",output_index:0,content_index:0,part:{type:"output_text",text:"",annotations:[]}},{type:"response.output_text.delta",output_index:0,content_index:0,delta:"你好"},{type:"response.output_text.done",output_index:0,content_index:0,text:"你好"},{type:"response.output_item.done",output_index:0,item:message},{type:"response.completed",response:{id:"r1",model:"fixture",status:"completed",output:[message],usage:{input_tokens:2,output_tokens:1,total_tokens:3,input_tokens_details:{cached_tokens:0}}}}]);
const anthropic=sse([{type:"message_start",message:{id:"r1",type:"message",role:"assistant",content:[],model:"fixture",usage:{input_tokens:2,output_tokens:0}}},{type:"content_block_start",index:0,content_block:{type:"text",text:""}},{type:"content_block_delta",index:0,delta:{type:"text_delta",text:"你好"}},{type:"content_block_stop",index:0},{type:"message_delta",delta:{stop_reason:"end_turn",stop_sequence:null},usage:{output_tokens:1}},{type:"message_stop"}]);
const gemini=sse([{candidates:[{index:0,content:{role:"model",parts:[{text:"你好"}]},finishReason:"STOP"}],usageMetadata:{promptTokenCount:2,candidatesTokenCount:1,totalTokenCount:3}}]).replaceAll("event: message\n", "");
const pi=sse([{type:"start"},{type:"text_start",contentIndex:0},{type:"text_delta",contentIndex:0,delta:"你好"},{type:"text_end",contentIndex:0,content:"你好",contentSignature:"sig"},{type:"done",reason:"stop",responseId:"r1",usage:{input:2,output:1,cacheRead:0,cacheWrite:0,totalTokens:3,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}}}]);
const apis:any={
  "openai-completions":["ai-openai",chat({content:"你好"})],
  "openai-responses":["ai-openai",responses],
  "azure-openai-responses":["ai-openai",responses],
  "openai-codex-responses":["ai-openai",responses],
  "anthropic-messages":["ai-anthropic",anthropic],
  "google-generative-ai":["ai-google",gemini],
  "google-vertex":["ai-google",gemini],
  "mistral-conversations":["ai-other-protocols",chat({content:"你好"})],
  "pi-messages":["ai-other-protocols",pi],
};
for(const [api,[batch,body]] of Object.entries(apis) as any){
  for(const mode of ["text","fragmented","http-error","cancelled"]){
    add(batch,`${api}-${mode}`,{op:"protocol",file:ai(`api/${api}`),api,mode,body:mode==="http-error"?JSON.stringify({error:{message:"fixture denied",type:"invalid_request_error",code:"bad_request"}}):body,status:mode==="http-error"?400:200});
  }
}
for(const api of ["openai-completions","mistral-conversations"])add(api==="openai-completions"?"ai-openai":"ai-other-protocols",`${api}-tool`,{op:"protocol",file:ai(`api/${api}`),api,mode:"text",body:chat({tool_calls:[{index:0,id:"call12345",type:"function",function:{name:"echo",arguments:'{"text":"你好"}'}}]}),status:200});
const bedrock=[{messageStart:{role:"assistant"}},{contentBlockStart:{contentBlockIndex:0,start:{}}},{contentBlockDelta:{contentBlockIndex:0,delta:{text:"你好"}}},{contentBlockStop:{contentBlockIndex:0}},{messageStop:{stopReason:"end_turn"}},{metadata:{usage:{inputTokens:2,outputTokens:1,totalTokens:3},metrics:{latencyMs:1}}}];
for(const mode of ["text","fragmented","http-error","cancelled"])add("ai-bedrock",`bedrock-${mode}`,{op:"protocol",file:ai("api/bedrock-converse-stream"),api:"bedrock-converse-stream",mode,status:mode==="http-error"?400:200,body:JSON.stringify({message:"fixture denied"}),bedrock});
// Catalog/facade behavior is tested against the pinned published catalog in Go.
