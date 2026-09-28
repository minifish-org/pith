package eventstream

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func p01Next(t *testing.T, ch <-chan StreamItem[int]) StreamItem[int] {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("Next closed without item")
		}
		return v
	case <-time.After(time.Second):
		t.Fatal("Next blocked")
		return StreamItem[int]{}
	}
}
func p01Stream() *EventStream[int, int] {
	return NewEventStream(func(n int) bool { return n == 3 }, func(n int) int { return n })
}
func TestPortsmithJudgeAIEventStreamFIFO(t *testing.T) {
	s := p01Stream()
	s.Push(1)
	s.Push(2)
	s.Push(3)
	s.Push(4)
	for _, want := range []int{1, 2, 3} {
		if got := p01Next(t, s.Next()); got.Done || got.Value != want {
			t.Fatal(got, want)
		}
	}
	if !p01Next(t, s.Next()).Done {
		t.Fatal("no terminal")
	}
	s = p01Stream()
	a, b := s.Next(), s.Next()
	s.Push(7)
	s.Push(8)
	if p01Next(t, a).Value != 7 || p01Next(t, b).Value != 8 {
		t.Fatal("waiter order")
	}
	s.End(nil)
}
func TestPortsmithJudgeAIEventStreamEndAndCancellation(t *testing.T) {
	s := p01Stream()
	s.Push(9)
	s.End(nil)
	if p01Next(t, s.Next()).Value != 9 || !p01Next(t, s.Next()).Done {
		t.Fatal("drain")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Result(ctx); err == nil {
		t.Fatal("absent result resolved")
	}
	z := 0
	s.End(&z)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	v, e := s.Result(ctx2)
	if e != nil || v != 0 {
		t.Fatal(v, e)
	}
	s = p01Stream()
	a, b := s.Next(), s.Next()
	s.End(nil)
	if !p01Next(t, a).Done || !p01Next(t, b).Done {
		t.Fatal("waiters not released")
	}
}
func TestPortsmithJudgeAIEventStreamFirstResult(t *testing.T) {
	s := p01Stream()
	s.Push(3)
	v := 99
	s.End(&v)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 3; i++ {
		v, e := s.Result(ctx)
		if e != nil || v != 3 {
			t.Fatal("first result/cancel", v, e)
		}
	}
}
func TestPortsmithJudgeAIEventStreamConcurrency(t *testing.T) {
	var s *EventStream[int, int]
	var waiting <-chan StreamItem[int]
	s = NewEventStream(func(n int) bool { waiting = s.Next(); return true }, func(n int) int { return n })
	done := make(chan struct{})
	go func() { s.Push(5); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback lock deadlock")
	}
	if p01Next(t, waiting).Value != 5 {
		t.Fatal("reentrant Next")
	}
	s = NewEventStream(func(n int) bool {
		if n < 0 {
			panic("fixture")
		}
		return false
	}, func(n int) int { return n })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic suppressed")
			}
		}()
		s.Push(-1)
	}()
	done = make(chan struct{})
	go func() { s.Push(7); s.End(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panic retained lock")
	}
	s = NewEventStream(func(int) bool { return false }, func(n int) int { return n })
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); s.Push(n) }(i)
	}
	wg.Wait()
	s.End(nil)
	seen := map[int]bool{}
	for {
		v := p01Next(t, s.Next())
		if v.Done {
			break
		}
		if seen[v.Value] {
			t.Fatal("duplicate")
		}
		seen[v.Value] = true
	}
	if len(seen) != 100 {
		t.Fatal(len(seen))
	}
}
func TestPortsmithJudgeAIEventStreamTSParity(t *testing.T) {
	var cases []struct {
		Name     string
		Complete int
		Steps    []struct {
			Op    string
			Value *int
		}
		Expected []any
	}
	data, e := os.ReadFile("testdata/eventstream-ts.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	if len(cases) != 7 {
		t.Fatal("missing TS cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			s := NewEventStream(func(n int) bool { return n == c.Complete }, func(n int) int { return n })
			wait := []<-chan StreamItem[int]{}
			trace := []any{}
			receive := func(ch <-chan StreamItem[int]) {
				v := p01Next(t, ch)
				if v.Done {
					v.Value = 0
				}
				trace = append(trace, map[string]any{"done": v.Done, "value": v.Value})
			}
			for _, step := range c.Steps {
				switch step.Op {
				case "push":
					s.Push(*step.Value)
				case "end":
					s.End(step.Value)
				case "next":
					receive(s.Next())
				case "wait":
					wait = append(wait, s.Next())
				case "take":
					receive(wait[0])
					wait = wait[1:]
				case "result":
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					v, e := s.Result(ctx)
					cancel()
					if e != nil {
						t.Fatal(e)
					}
					trace = append(trace, map[string]any{"result": v})
				}
			}
			raw, _ := json.Marshal(trace)
			var got []any
			json.Unmarshal(raw, &got)
			if !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("%s != TS %v", raw, c.Expected)
			}
		})
	}
}

func TestPortsmithJudgeAIEventStreamGeneric(t *testing.T) {
	s := NewEventStream(func(string) bool { return false }, func(v string) string { return v })
	s.Push("")
	s.Push("你好")
	value := ""
	s.End(&value)
	if (<-s.Next()).Value != "" || (<-s.Next()).Value != "你好" || !(<-s.Next()).Done {
		t.Fatal("generic FIFO")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := s.Result(ctx); err != nil || got != "" {
		t.Fatal(got, err)
	}
	p := NewEventStream(func(*int) bool { return false }, func(v *int) *int { return v })
	var z *int
	p.End(&z)
	if got, err := p.Result(ctx); err != nil || got != nil {
		t.Fatal("nil result", got, err)
	}
}
func TestPortsmithJudgeAIEventStreamBulkFIFO(t *testing.T) {
	s := NewEventStream(func(int) bool { return false }, func(v int) int { return v })
	for k := 0; k < 4000; k++ {
		s.Push(k)
	}
	for k := 0; k < 2000; k++ {
		if got := p01Next(t, s.Next()); got.Done || got.Value != k {
			t.Fatal(k, got)
		}
	}
	for k := 4000; k < 6000; k++ {
		s.Push(k)
	}
	s.End(nil)
	for k := 2000; k < 6000; k++ {
		if got := p01Next(t, s.Next()); got.Done || got.Value != k {
			t.Fatal(k, got)
		}
	}
	if !p01Next(t, s.Next()).Done {
		t.Fatal("missing end")
	}
}
