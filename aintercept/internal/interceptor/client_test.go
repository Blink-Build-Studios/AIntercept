package interceptor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
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

// A Replace carries the whole rewritten message back, and a request can be up
// to the enforce cap (32 MiB): more than gRPC's default 4 MiB receive limit.
func TestTakesALargeReplacement(t *testing.T) {
	big := strings.Repeat("x", 30<<20)
	f := &interceptortest.Fake{Request: replaceText("hello", big)}
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	defer c.Close()
	d := c.DecideRequest(context.Background(), call, parse(t, `{"messages":[{"role":"user","content":"hello"}]}`))
	if d.Action != interceptor.Replace || len(d.Payload) < len(big) {
		t.Fatalf("got %v (%d bytes), want the 30 MiB replacement", d.Action, len(d.Payload))
	}
}

// A missed deadline fails closed by default, and open with OnError allow.
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
		if tc.want == interceptor.Deny && (d.HTTPStatus != 503 || d.Reason != "interceptor unavailable") {
			t.Errorf("on-error %q: %+v, want a 503", tc.onError, d)
		}
		if got := r.all(); len(got) != 1 || got[0].Action != "error" || got[0].Callback != "call_start" || got[0].Applied != (tc.want == interceptor.Deny) {
			t.Errorf("on-error %q: rows %+v", tc.onError, got)
		}
	}
}

// Without content, OnRequest is never called (in either mode).
func TestWithoutContent(t *testing.T) {
	f := &interceptortest.Fake{}
	c := interceptortest.NewClient(t, f, interceptor.Config{Deadline: time.Second}, nil)
	r := parse(t, withHistory)
	c.ObserveRequest(call, r)
	if d := c.DecideRequest(context.Background(), call, r); d.Action != interceptor.Allow {
		t.Errorf("decision %+v, want Allow", d)
	}
	c.Close()
	if calls, _, _ := f.Snapshot(); len(calls) != 0 {
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
	c.DecideRequest(context.Background(), call, parse(t, withHistory))
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
