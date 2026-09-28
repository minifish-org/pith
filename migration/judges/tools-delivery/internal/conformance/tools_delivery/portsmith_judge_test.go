package conformance

import (
 "bytes"
 "encoding/json"
 "go/ast"
 "go/parser"
 "go/token"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

type mapping struct { Source string `json:"source"`; Upstream string `json:"upstream"`; GoPackage string `json:"goPackage"`; GoSymbol string `json:"goSymbol"`; Reason string `json:"reason"` }
func projectRoot(t *testing.T)string {t.Helper();p,err:=os.Getwd();if err!=nil{t.Fatal(err)};for {if _,err=os.Stat(filepath.Join(p,"go.mod"));err==nil{return p};next:=filepath.Dir(p);if next==p{t.Fatal("go.mod not found")};p=next}}
func TestPortsmithJudgeToolsDeliverySurface(t *testing.T) {
 root:=projectRoot(t)
 frozen,err:=os.ReadFile("testdata/surface.json");if err!=nil{t.Fatal(err)}
 var spec struct{Outputs []string `json:"outputs"`; Symbols []mapping `json:"symbols"`; Map string `json:"map"`};if err=json.Unmarshal(frozen,&spec);err!=nil{t.Fatal(err)}
 symbols:=map[string]bool{}
 for _,out:=range spec.Outputs {
  raw,err:=os.ReadFile(filepath.Join(root,out));if err!=nil{t.Fatal(err)}
  if !strings.HasSuffix(out,".go")||strings.HasSuffix(out,"_test.go"){continue}
  if bytes.Contains(raw,[]byte("TODO(port)"))||bytes.Contains(raw,[]byte("panic(\"not implemented\"")){t.Fatalf("unimplemented output: %s",out)}
  f,err:=parser.ParseFile(token.NewFileSet(),out,raw,0);if err!=nil{t.Fatal(err)}
  for _,d:=range f.Decls {switch n:=d.(type){case *ast.FuncDecl:if n.Recv==nil&&ast.IsExported(n.Name.Name){symbols[filepath.ToSlash(filepath.Dir(out))+"."+n.Name.Name]=true};case *ast.GenDecl:for _,s:=range n.Specs{switch v:=s.(type){case *ast.TypeSpec:symbols[filepath.ToSlash(filepath.Dir(out))+"."+v.Name.Name]=true;case *ast.ValueSpec:for _,name:=range v.Names{symbols[filepath.ToSlash(filepath.Dir(out))+"."+name.Name]=true}}}}}
 }
 data,err:=os.ReadFile(filepath.Join(root,spec.Map));if err!=nil{t.Fatal(err)};var actual []mapping;if err=json.Unmarshal(data,&actual);err!=nil{t.Fatal(err)}
 if len(actual)!=len(spec.Symbols){t.Fatalf("source export mapping count: want %d, got %d",len(spec.Symbols),len(actual))}
 seen:=map[string]bool{}
 for _,want:=range spec.Symbols {
  found:=false
  for _,row:=range actual {if row.Source==want.Source&&row.Upstream==want.Upstream {
   key:=row.Source+"#"+row.Upstream;if seen[key]{t.Fatalf("duplicate symbol %s",key)};seen[key]=true
   if !ast.IsExported(row.GoSymbol)||!symbols[row.GoPackage+"."+row.GoSymbol] {t.Fatalf("missing exported Go declaration for %s -> %s.%s",key,row.GoPackage,row.GoSymbol)}
   if row.Reason=="" {t.Fatalf("missing mapping rationale for %s",key)};found=true
  }}
  if !found{t.Fatalf("unmapped source symbol %s#%s",want.Source,want.Upstream)}
 }
}
