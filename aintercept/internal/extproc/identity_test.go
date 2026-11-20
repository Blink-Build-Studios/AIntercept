package extproc

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

// identityRig: a server whose credential lookups go to a fake profile
// endpoint answering after delay, and a fake Interceptor recording each
// start's identity.
func identityRig(t *testing.T, delay time.Duration) (*Server, *interceptor.Client, func() *pb.Identity) {
	t.Helper()
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		_, _ = w.Write([]byte(`{"account":{"uuid":"acc-1","email":"a@example.com","full_name":"Ada Example"}}`))
	}))
	t.Cleanup(p.Close)
	var mu sync.Mutex
	var got *pb.Identity
	f := &interceptortest.Fake{Start: func(s *pb.CallStart) *pb.CallVerdict {
		mu.Lock()
		got = s.GetCall().GetIdentity()
		mu.Unlock()
		return &pb.CallVerdict{}
	}}
	ic := interceptortest.NewClient(t, f, interceptor.Config{Deadline: 5 * time.Second}, nil)
	srv := New(Config{Processor: events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024}), InstanceID: "t", KeyHashSalt: "s",
		Interceptor: ic, Identities: identity.New(identity.Config{Lookup: true, Origin: p.URL, Client: p.Client()}),
		IdentityWait: time.Second, EnforceIdentityWait: time.Second})
	return srv, ic, func() *pb.Identity { mu.Lock(); defer mu.Unlock(); return got }
}

var oauthCall = append(append([]string{}, anthropicCall...), "authorization", "Bearer sk-ant-oat01-FAKEoauthTOKENfortests0001")

// Observe: the lookup starts at the request's headers; the stream is never
// held up for it, and OnCallStart carries the email.
func TestObserveLooksUpAtHeaders(t *testing.T) {
	srv, ic, start := identityRig(t, 100*time.Millisecond)
	st := &streamState{correlationID: "c"}
	t0 := time.Now()
	srv.handle(st, eReqHeaders(false, oauthCall...))
	srv.handle(st, eReqBody(`{"model":"m","messages":[]}`, true))
	if took := time.Since(t0); took > 50*time.Millisecond {
		t.Errorf("the stream took %v: it waited for the lookup", took)
	}
	if st.lookup == nil {
		t.Fatal("no lookup began at the headers")
	}
	ic.Close()
	if id := start(); id.GetEmail() != "a@example.com" || id.GetEmailSource() != pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL {
		t.Fatalf("start identity %v", id)
	}
}

// Enforce: the call waits for its credential's first lookup (bounded), and
// OnCallStart carries the email.
func TestEnforceWaitsForTheLookup(t *testing.T) {
	srv, _, start := identityRig(t, 30*time.Millisecond)
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(st, eReqHeaders(false, oauthCall...))
	srv.enforceHandle(st, eReqBody(`{"model":"m","messages":[]}`, true))
	if id := start(); id.GetEmail() != "a@example.com" || id.GetVerifiedAccountId() != "acc-1" || id.GetKeyHint() == "" {
		t.Fatalf("start identity %v", id)
	}
}
