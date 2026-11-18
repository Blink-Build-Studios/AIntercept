package interceptor_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

type rows struct {
	mu sync.Mutex
	v  []events.Verdict
}

func (r *rows) add(_ string, v events.Verdict) { r.mu.Lock(); r.v = append(r.v, v); r.mu.Unlock() }
func (r *rows) all() []events.Verdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.Verdict(nil), r.v...)
}

func start(id string) *pb.CallStart {
	return &pb.CallStart{Call: &pb.Call{CallId: id}, Request: &pb.RequestInfo{Model: "m"}}
}

func msg(id string, seq int64, p string) *pb.Message {
	return &pb.Message{Call: &pb.Call{CallId: id}, Seq: seq, Payload: []byte(p)}
}

func TestDecideAppliesVerdicts(t *testing.T) {
	f := &interceptortest.Fake{
		Start: func(s *pb.CallStart) *pb.Verdict {
			return &pb.Verdict{Action: &pb.Verdict_Deny{Deny: &pb.Deny{Reason: "no", HttpStatus: 451}}}
		},
		Message: func(m *pb.Message) *pb.Verdict {
			return &pb.Verdict{Action: &pb.Verdict_Replace{Replace: &pb.Replace{Payload: []byte("new")}},
				Annotations: []*pb.Annotation{{Key: "k", Value: "v"}}}
		},
	}
	var r rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: time.Second}, r.add)
	defer c.Close()

	d := c.DecideStart(context.Background(), start("a"))
	if d.Action != interceptor.Deny || d.Reason != "no" || d.HTTPStatus != 451 {
		t.Fatalf("start: %+v", d)
	}
	d = c.DecideMessage(context.Background(), msg("a", 1, "old"))
	if d.Action != interceptor.Replace || string(d.Payload) != "new" {
		t.Fatalf("message: %+v", d)
	}
	got := r.all()
	if len(got) != 2 || !got[0].Applied || got[0].Action != "deny" || got[1].Action != "replace" || got[1].Annotations["k"] != "v" || got[1].Mode != "enforce" {
		t.Fatalf("verdict rows: %+v", got)
	}
}

// A missed deadline fails closed by default, and open with OnError allow.
// A Replace carries the whole rewritten body back, and a request body can be up
// to the enforce cap (32 MiB): more than gRPC's default 4 MiB receive limit.
func TestTakesALargeReplacement(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 30<<20)
	f := &interceptortest.Fake{Message: func(*pb.Message) *pb.Verdict {
		return &pb.Verdict{Action: &pb.Verdict_Replace{Replace: &pb.Replace{Payload: big}}}
	}}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	if d := c.DecideMessage(context.Background(), msg("a", 1, "old")); d.Action != interceptor.Replace || len(d.Payload) != len(big) {
		t.Fatalf("got %v (%d bytes), want the 30 MiB replacement", d.Action, len(d.Payload))
	}
}

func TestDeadlineFailsClosed(t *testing.T) {
	f := &interceptortest.Fake{Delay: 200 * time.Millisecond}
	for _, tc := range []struct {
		onError string
		want    interceptor.Action
	}{{"", interceptor.Deny}, {"allow", interceptor.Allow}} {
		var r rows
		c := interceptortest.NewClient(t, f, interceptor.Config{Deadline: 20 * time.Millisecond, OnError: tc.onError}, r.add)
		d := c.DecideStart(context.Background(), start("a"))
		c.Close()
		if d.Action != tc.want {
			t.Errorf("on-error %q: %v, want %v", tc.onError, d.Action, tc.want)
		}
		if got := r.all(); len(got) != 1 || got[0].Action != "error" || got[0].Applied != (tc.want == interceptor.Deny) {
			t.Errorf("on-error %q: rows %+v", tc.onError, got)
		}
	}
}

// Observe: callbacks arrive in order per call, the final record carries the
// call's verdicts, and they're released after it.
func TestObserveOrderAndRecord(t *testing.T) {
	f := &interceptortest.Fake{Message: func(m *pb.Message) *pb.Verdict {
		return &pb.Verdict{Action: &pb.Verdict_Drop{Drop: &pb.Drop{}}}
	}}
	var r rows
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true}, r.add)
	c.ObserveStart(start("a"))
	c.ObserveMessage(msg("a", 1, "x"))
	c.ObserveMessage(msg("a", 2, "y"))
	c.Complete(&pb.CallRecord{Call: &pb.Call{CallId: "a"}}, true)
	c.Complete(&pb.CallRecord{Call: &pb.Call{CallId: "a"}}, true)
	c.Close() // drains the queues

	calls, recs := f.Snapshot()
	want := []string{"start", "message", "message", "complete", "complete"}
	if len(calls) != len(want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls %v, want %v", calls, want)
		}
	}
	if n := len(recs[0].GetVerdicts()); n != 2 {
		t.Fatalf("first record has %d verdicts, want 2", n)
	}
	if v := recs[0].GetVerdicts()[0]; v.GetAction() != "drop" || v.GetApplied() {
		t.Fatalf("verdict %+v: want a recorded (not applied) drop", v)
	}
	if n := len(recs[1].GetVerdicts()); n != 0 {
		t.Fatalf("second record has %d verdicts; the final record should have released them", n)
	}
	if got := r.all(); len(got) != 2 || got[0].Mode != "observe" {
		t.Fatalf("rows %+v", got)
	}
}

// Without content, OnMessage is never called.
func TestObserveWithoutContent(t *testing.T) {
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{}, nil)
	c.ObserveMessage(msg("a", 1, "x"))
	c.Close()
	if calls, _ := f.Snapshot(); len(calls) != 0 {
		t.Fatalf("calls %v, want none", calls)
	}
}

// A stuck Interceptor never blocks the caller: a full queue drops and counts.
func TestObserveQueueFullDrops(t *testing.T) {
	f := &interceptortest.Fake{Block: make(chan struct{})}
	c := interceptortest.NewClient(t, f, interceptor.Config{QueueSize: 2, Workers: 1, ObserveTimeout: 2 * time.Second}, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			c.ObserveStart(start("a"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ObserveStart blocked")
	}
	if s := c.Stats(); s.Dropped == 0 {
		t.Fatalf("stats %+v: want drops", s)
	}
	close(f.Block)
	c.Close()
}

// The static token rides every callback: the request path's and the record's.
func TestSendsTheToken(t *testing.T) {
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: time.Second, Token: "static"}, nil)
	c.DecideStart(context.Background(), start("a"))
	c.DecideMessage(context.Background(), msg("a", 1, "x"))
	c.Complete(&pb.CallRecord{Call: start("a").GetCall()}, true)
	c.Close()
	got := f.Auths()
	if len(got) != 3 {
		t.Fatalf("calls: %v", got)
	}
	for _, a := range got {
		if a != "Bearer static" {
			t.Fatalf("authorization %q on a call, want Bearer static: %v", a, got)
		}
	}
}

// TokenFile follows the file as it's rewritten (a projected service-account
// token, replaced by an atomic rename), keeps the last token while the file is
// missing, and never sends an empty one.
func TestTokenFileFollowsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	write := func(v string) {
		tmp := filepath.Join(dir, "token.tmp")
		if err := os.WriteFile(tmp, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
	write("one")
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Deadline: time.Second, TokenFile: path, TokenFileRefresh: 10 * time.Millisecond}, nil)
	defer c.Close()
	last := func() string {
		c.DecideStart(context.Background(), start("a"))
		a := f.Auths()
		return a[len(a)-1]
	}
	if got := last(); got != "Bearer one" {
		t.Fatalf("first: %q", got)
	}
	write("two")
	time.Sleep(30 * time.Millisecond)
	if got := last(); got != "Bearer two" {
		t.Fatalf("after rotation: %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := last(); got != "Bearer two" {
		t.Fatalf("file missing: %q, want the last token", got)
	}
	write("")
	time.Sleep(30 * time.Millisecond)
	if got := last(); got != "Bearer two" {
		t.Fatalf("file empty: %q, want the last token", got)
	}
}

// One credential, chosen explicitly: both is a configuration error, and a
// token file that can't be read (or is empty) fails at start, not on the first
// call.
func TestTokenConfigErrors(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(good, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]interceptor.Config{
		"both":    {Endpoint: "x", Token: "t", TokenFile: good},
		"missing": {Endpoint: "x", TokenFile: filepath.Join(dir, "nope")},
		"empty":   {Endpoint: "x", TokenFile: empty},
	} {
		if c, err := interceptor.New(cfg, nil); err == nil {
			c.Close()
			t.Errorf("%s: New succeeded, want an error", name)
		}
	}
}
