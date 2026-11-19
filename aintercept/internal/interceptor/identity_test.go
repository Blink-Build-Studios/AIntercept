package interceptor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

// A fake Anthropic profile endpoint: it answers after delay, or once
// release is closed.
type profile struct {
	*httptest.Server
	delay   time.Duration
	release chan struct{}
}

func newProfile(t *testing.T, delay time.Duration, release chan struct{}) *profile {
	p := &profile{delay: delay, release: release}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.release != nil {
			<-p.release
		}
		time.Sleep(p.delay)
		_, _ = w.Write([]byte(`{"account":{"uuid":"acc-1","email":"a@example.com","full_name":"Ada Example"},"organization":{"uuid":"org-1","name":"Example Co"}}`))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *profile) cache() *identity.Cache {
	return identity.New(identity.Config{Lookup: true, Origin: p.URL, Client: p.Client()})
}

const oauth = "sk-ant-oat01-FAKEoauthTOKENfortests0001"

// info is a Claude Code call's start: its credential's lookup begun at its
// headers (as extproc does), and the account its body reports.
func info(id string, ids *identity.Cache, reported string) *interceptor.CallInfo {
	kh := "kh-" + id
	return &interceptor.CallInfo{CallID: id, Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages",
		KeyHash: kh, KeyHint: "sk-ant…0001", Summary: events.RequestSummary{AccountID: reported, Product: "claude_code"},
		Identities: ids, Lookup: ids.Begin(kh, "sk-ant…0001", "api.anthropic.com", map[string]string{"authorization": "Bearer " + oauth})}
}

// seen collects what a fake v1 Interceptor receives, by call.
type seen struct {
	mu     sync.Mutex
	starts map[string]*pb.Identity
	msgs   map[string][]int64
	at     map[string]time.Time
}

func newSeen(f *interceptortest.Fake) *seen {
	s := &seen{starts: map[string]*pb.Identity{}, msgs: map[string][]int64{}, at: map[string]time.Time{}}
	f.Start = func(c *pb.CallStart) *pb.Verdict {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.starts[c.GetCall().GetCallId()] = c.GetCall().GetIdentity()
		s.at[c.GetCall().GetCallId()] = time.Now()
		return &pb.Verdict{}
	}
	f.Message = func(m *pb.Message) *pb.Verdict {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := m.GetCall().GetCallId()
		if m.GetCall().GetIdentity().GetEmail() != s.starts[id].GetEmail() {
			s.msgs[id] = append(s.msgs[id], -1) // a message whose identity isn't its start's
		}
		s.msgs[id] = append(s.msgs[id], m.GetSeq())
		s.at[id+"/msg"] = time.Now()
		return &pb.Verdict{}
	}
	return s
}

func (s *seen) start(id string) *pb.Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts[id]
}

func waitUntil(t *testing.T, d time.Duration, ok func() bool) bool {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return ok()
}

// Observe, a token's first call: OnCallStart carries the email the lookup
// found, and the client isn't held up at all (ObserveCall returns at once).
func TestObserveFirstCallCarriesTheEmail(t *testing.T) {
	ids := newProfile(t, 50*time.Millisecond, nil).cache()
	f := &interceptortest.Fake{}
	s := newSeen(f)
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true}, nil)
	ci := info("a", ids, "acc-1")
	t0 := time.Now()
	call := c.ObserveCall(ci, 300*time.Millisecond)
	if took := time.Since(t0); took > 20*time.Millisecond {
		t.Errorf("ObserveCall took %v: the request path waited", took)
	}
	c.ObserveMessage(interceptor.Message(call, events.DirInbound, 1, pb.MessageKind_MESSAGE_KIND_HTTP_REQUEST_BODY, []byte("{}"), "", ""))
	rec, _ := interceptor.Record(&events.Event{CorrelationID: "a", KeyHash: "kh-a", Phase: events.PhaseComplete}, nil)
	c.Complete(rec, true)
	c.Close()
	id := s.start("a")
	if id.GetEmail() != "a@example.com" || id.GetName() != "Ada Example" || id.GetEmailSource() != pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL ||
		id.GetVerifiedAccountId() != "acc-1" || id.GetReportedAccountId() != "acc-1" || id.GetOrgId() != "org-1" ||
		id.GetAccountMismatch() || id.GetAccountId() != "" || id.GetKeyHash() != "kh-a" {
		t.Fatalf("start identity %v", id)
	}
	if m := s.msgs["a"]; len(m) != 1 || m[0] != 1 {
		t.Errorf("messages %v: want seq 1 with the start's identity", m)
	}
	_, recs := f.Snapshot()
	if len(recs) != 1 || recs[0].GetCall().GetIdentity().GetEmail() != "a@example.com" {
		t.Errorf("the record's identity isn't the start's: %v", recs)
	}
}

// Observe, a lookup slower than the wait: the callbacks go without the email,
// in order, and every later one carries the same identity (resolved once),
// even after the lookup ends.
func TestObserveSlowLookupGoesWithoutIt(t *testing.T) {
	release := make(chan struct{})
	ids := newProfile(t, 0, release).cache()
	f := &interceptortest.Fake{}
	s := newSeen(f)
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true}, nil)
	call := c.ObserveCall(info("a", ids, "acc-1"), 50*time.Millisecond)
	for seq := int64(1); seq <= 3; seq++ {
		c.ObserveMessage(interceptor.Message(call, events.DirInbound, seq, pb.MessageKind_MESSAGE_KIND_SSE_EVENT, []byte("x"), "", ""))
	}
	if !waitUntil(t, 2*time.Second, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.msgs["a"]) == 3 }) {
		t.Fatal("the held callbacks never went")
	}
	close(release)
	waitUntil(t, 2*time.Second, func() bool { _, ok := ids.Get("kh-a"); return ok })
	c.ObserveMessage(interceptor.Message(call, events.DirInbound, 4, pb.MessageKind_MESSAGE_KIND_SSE_EVENT, []byte("x"), "", ""))
	rec, _ := interceptor.Record(&events.Event{CorrelationID: "a", KeyHash: "kh-a", Phase: events.PhaseComplete}, ids)
	c.Complete(rec, true)
	c.Close()
	if id := s.start("a"); id.GetEmail() != "" || id.GetEmailSource() != pb.IdentitySource_IDENTITY_SOURCE_UNSPECIFIED || id.GetReportedAccountId() != "acc-1" {
		t.Errorf("start identity %v: want none (the lookup was late)", id)
	}
	if m := s.msgs["a"]; len(m) != 4 || m[0] != 1 || m[1] != 2 || m[2] != 3 || m[3] != 4 {
		t.Errorf("messages %v: want 1 2 3 4, each with the start's identity", m)
	}
	calls, recs := f.Snapshot()
	if calls[0] != "start" || len(recs) != 1 || recs[0].GetCall().GetIdentity().GetEmail() != "" {
		t.Errorf("calls %v; the record's identity isn't the start's: %v", calls, recs)
	}
	if st := c.Stats(); st.HeldForIdentity != 1 || st.Dropped != 0 {
		t.Errorf("stats %+v", st)
	}
}

// The hold is per call, before the shared queues: with one worker (every
// call in the same queue), a call waiting for its lookup doesn't delay
// another call's callbacks.
func TestHeldCallDoesntDelayItsQueue(t *testing.T) {
	release := make(chan struct{})
	ids := newProfile(t, 0, release).cache()
	f := &interceptortest.Fake{}
	s := newSeen(f)
	c := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Workers: 1}, nil)
	slow := c.ObserveCall(info("slow", ids, ""), 5*time.Second)
	c.ObserveMessage(interceptor.Message(slow, events.DirInbound, 1, pb.MessageKind_MESSAGE_KIND_SSE_EVENT, []byte("x"), "", ""))
	t0 := time.Now()
	other := c.ObserveCall(&interceptor.CallInfo{CallID: "other", Host: "api.anthropic.com", KeyHash: "kh-other", Identities: ids}, 5*time.Second)
	c.ObserveMessage(interceptor.Message(other, events.DirInbound, 1, pb.MessageKind_MESSAGE_KIND_SSE_EVENT, []byte("x"), "", ""))
	if !waitUntil(t, time.Second, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.msgs["other"]) == 1 }) {
		t.Fatal("the other call's callbacks were held behind the slow one's lookup")
	}
	if took := time.Since(t0); took > 200*time.Millisecond {
		t.Errorf("the other call's callbacks took %v", took)
	}
	if s.start("slow") != nil {
		t.Error("the slow call's start went before its lookup ended")
	}
	close(release)
	if !waitUntil(t, 2*time.Second, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.msgs["slow"]) == 1 }) {
		t.Fatal("the slow call's callbacks never went")
	}
	c.Close()
	if id := s.start("slow"); id.GetEmail() != "a@example.com" {
		t.Errorf("slow start identity %v", id)
	}
	if m := s.msgs["slow"]; len(m) != 1 || m[0] != 1 {
		t.Errorf("slow messages %v", m)
	}
}

// Enforce: DecideCall waits for a token's first lookup, at most the enforce
// wait.
func TestDecideWaitsAtMostTheEnforceWait(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
		block bool
		email string
	}{{"in time", 20 * time.Millisecond, false, "a@example.com"}, {"late", 0, true, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			var release chan struct{}
			if tc.block {
				release = make(chan struct{})
				defer close(release)
			}
			ids := newProfile(t, tc.delay, release).cache()
			f := &interceptortest.Fake{}
			s := newSeen(f)
			c := interceptortest.NewClient(t, f, interceptor.Config{Deadline: time.Second}, nil)
			defer c.Close()
			t0 := time.Now()
			call, d := c.DecideCall(context.Background(), info("a", ids, ""), 100*time.Millisecond)
			took := time.Since(t0)
			if d.Action != interceptor.Allow || call.GetIdentity().GetEmail() != tc.email || s.start("a").GetEmail() != tc.email {
				t.Errorf("decision %v, identity %v / %v", d.Action, call.GetIdentity(), s.start("a"))
			}
			if took > 300*time.Millisecond || tc.block && took < 100*time.Millisecond {
				t.Errorf("took %v, want at most the 100 ms wait (and all of it when the lookup is late)", took)
			}
			if st := ids.Stats(); st.WaitMS["enforce"] == nil || len(st.WaitMS["enforce"]) == 0 {
				t.Errorf("enforce wait not counted: %+v", st.WaitMS)
			}
		})
	}
}

// Spoofing: a body account other than the credential's verified one gives
// account_mismatch, an empty account_id and the verified email.
func TestAccountMismatch(t *testing.T) {
	ids := newProfile(t, 0, nil).cache()
	ids.Begin("kh-a", "", "api.anthropic.com", map[string]string{"authorization": "Bearer " + oauth}).Wait(2 * time.Second)
	f := &interceptortest.Fake{}
	s := newSeen(f)
	c := interceptortest.NewClient(t, f, interceptor.Config{Deadline: time.Second}, nil)
	defer c.Close()
	ci := info("a", ids, "acc-someone-else")
	ci.Auth = &events.AuthIdentity{Email: "spoof@example.com"}
	c.DecideCall(context.Background(), ci, 0)
	id := s.start("a")
	if !id.GetAccountMismatch() || id.GetAccountId() != "" || id.GetEmail() != "a@example.com" || id.GetVerifiedAccountId() != "acc-1" ||
		id.GetReportedAccountId() != "acc-someone-else" || id.GetEmailSource() != pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL {
		t.Fatalf("identity %v", id)
	}
	if st := ids.Stats(); st.AccountMismatch != 1 || st.Hits["credential"] != 1 {
		t.Errorf("stats %+v", st)
	}
}

// v2: a held call's OnRequest carries the identity resolved for its start.
func TestV2HeldRequestCarriesTheIdentity(t *testing.T) {
	ids := newProfile(t, 30*time.Millisecond, nil).cache()
	f := &interceptortest.FakeV2{}
	c := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: time.Second}, nil)
	call := c.ObserveCall(info("v", ids, ""), time.Second)
	c.ObserveRequest(call, parse(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	c.Close()
	_, reqs, _ := f.Snapshot()
	if len(reqs) != 1 || reqs[0].GetCall().GetIdentity().GetEmail() != "a@example.com" || reqs[0].GetCall().GetIdentity().GetVerifiedAccountId() != "acc-1" {
		t.Fatalf("requests %v", reqs)
	}
}
