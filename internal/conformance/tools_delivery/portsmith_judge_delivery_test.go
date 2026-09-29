package conformance

import (
 "context"
 "encoding/json"
 "fmt"
 "net/http"
 "net/http/httptest"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "sync"
 "testing"
 "time"
)

// A real native binary must drive the real agent/tool SDK against this local model.
func TestPortsmithJudgeToolsDeliveryCLI(t *testing.T) {
 root:=projectRoot(t);dir:=t.TempDir();binary:=filepath.Join(dir,"pith")
 ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second);defer cancel()
 build:=exec.CommandContext(ctx,"go","build","-mod=readonly","-o",binary,"./cmd/pith");build.Dir=root
 if out,err:=build.CombinedOutput();err!=nil{t.Fatalf("build binary: %v\n%s",err,out)}
 run:=func(args ...string)(string,error){cmd:=exec.CommandContext(ctx,binary,args...);cmd.Dir=dir;cmd.Env=[]string{"HOME="+dir,"TMPDIR="+dir,"PATH=/usr/bin:/bin","LANG=en_US.UTF-8"};out,err:=cmd.CombinedOutput();return string(out),err}
 if out,err:=run("--help");err!=nil||!strings.Contains(out,"--prompt"){t.Fatalf("help: %v %s",err,out)}
 if out,err:=run("--prompt","hello");err==nil||strings.Contains(out,"panic:"){t.Fatalf("missing config must fail cleanly: %v %s",err,out)}
 if err:=os.WriteFile(filepath.Join(dir,"input.txt"),[]byte("alpha\n"),0600);err!=nil{t.Fatal(err)}
 var mu sync.Mutex;round:=0;var serverErrors []string
 names:=[]string{"read","write","edit","bash"}
 args:=[]any{map[string]any{"path":"input.txt"},map[string]any{"path":"output.txt","content":"beta\n"},map[string]any{"path":"output.txt","oldText":"beta","newText":"gamma"},map[string]any{"command":"printf 'shell-ok'"}}
 resumed:=false
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  mu.Lock();defer mu.Unlock()
  if r.URL.Path!="/v1/chat/completions"||r.Method!="POST"{serverErrors=append(serverErrors,"wrong model endpoint")}
  if r.Header.Get("Authorization")!="Bearer fixture-secret"{serverErrors=append(serverErrors,"missing bearer key")}
  var req struct{Messages []struct{Role string `json:"role"`;Content any `json:"content"`} `json:"messages"`;Tools []struct{Function struct{Name string `json:"name"`} `json:"function"`} `json:"tools"`}
  if err:=json.NewDecoder(r.Body).Decode(&req);err!=nil{serverErrors=append(serverErrors,err.Error())}
  declared:=map[string]bool{};for _,tool:=range req.Tools{declared[tool.Function.Name]=true};for _,name:=range names{if !declared[name]{serverErrors=append(serverErrors,"tool missing: "+name)}}
  count:=0;for _,msg:=range req.Messages{if msg.Role=="tool"{count++}}
  if round<5&&count!=round{serverErrors=append(serverErrors,fmt.Sprintf("tool conversation lost at %d: %d",round,count))}
  if round>=5{resumed=count>=4;if !resumed{serverErrors=append(serverErrors,"session history not restored")}}
  delta:=map[string]any{"role":"assistant","content":"all-done"};reason:="stop"
  if round<4{encoded,_:=json.Marshal(args[round]);delta=map[string]any{"role":"assistant","tool_calls":[]any{map[string]any{"index":0,"id":fmt.Sprintf("call-%d",round),"type":"function","function":map[string]any{"name":names[round],"arguments":string(encoded)}}}};reason="tool_calls"}
  round++;w.Header().Set("Content-Type","text/event-stream")
  chunk:=func(d any,finish any){encoded,_:=json.Marshal(map[string]any{"id":"fixture","object":"chat.completion.chunk","model":"fixture","choices":[]any{map[string]any{"index":0,"delta":d,"finish_reason":finish}}});fmt.Fprintf(w,"data: %s\n\n",encoded)}
  chunk(delta,nil);chunk(map[string]any{},reason);fmt.Fprint(w,"data: [DONE]\n\n")
 }));defer server.Close()
 session:=filepath.Join(dir,"session.jsonl")
 flags:=[]string{"--base-url",server.URL+"/v1","--model","fixture","--api-key","fixture-secret","--cwd",dir,"--session",session,"--prompt","Complete the local task"}
 out,err:=run(flags...);if err!=nil||!strings.Contains(out,"all-done"){t.Fatalf("tool loop: %v\n%s",err,out)}
 if strings.Contains(out,"fixture-secret"){t.Fatal("credential leaked to output")}
 data,err:=os.ReadFile(filepath.Join(dir,"output.txt"));if err!=nil||string(data)!="gamma\n"{t.Fatalf("write/edit not performed: %q %v",data,err)}
 if out,err=run(flags...);err!=nil||!strings.Contains(out,"all-done"){t.Fatalf("restart: %v\n%s",err,out)}
 mu.Lock();defer mu.Unlock();if round!=6||!resumed||len(serverErrors)>0{t.Fatalf("requests=%d restored=%v issues=%v",round,resumed,serverErrors)}
}
