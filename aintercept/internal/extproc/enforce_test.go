package extproc

import (
	"strings"
	"testing"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
)

type enforceRig struct {
	t    *testing.T
	srv  *Server
	st   *streamState
	sink *memSink
	proc *events.Processor
}

// newRig is an enforced stream whose Interceptor is f.
func newRig(t *testing.T, f *interceptortest.Fake, cfg interceptor.Config) *enforceRig {
	t.Helper()
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 1024})
	ic := interceptortest.NewClient(t, f, cfg, nil)
	t.Cleanup(ic.Close)
	return &enforceRig{t: t, sink: sink, proc: proc,
		srv: New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic}),
		st:  &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}}
}

func (r *enforceRig) send(req *extprocv3.ProcessingRequest) []*extprocv3.ProcessingResponse {
	return r.srv.enforceHandle(r.st, req)
}

func eReqHeaders(eos bool, kv ...string) *extprocv3.ProcessingRequest {
	h := headers(kv...)
	h.EndOfStream = eos
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: h}}
}

func eReqBody(b string, eos bool) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{Body: []byte(b), EndOfStream: eos}}}
}

func eRespHeaders(kv ...string) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{ResponseHeaders: headers(kv...)}}
}

func eRespBody(b []byte, eos bool) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: &extprocv3.HttpBody{Body: b, EndOfStream: eos}}}
}

func streamed(r *extprocv3.ProcessingResponse) ([]byte, bool) {
	var br *extprocv3.BodyResponse
	if v := r.GetRequestBody(); v != nil {
		br = v
	} else {
		br = r.GetResponseBody()
	}
	s := br.GetResponse().GetBodyMutation().GetStreamedResponse()
	return s.GetBody(), s.GetEndOfStream()
}

func removed(r *extprocv3.ProcessingResponse) []string {
	h := r.GetRequestHeaders()
	if h == nil {
		h = r.GetResponseHeaders()
	}
	return h.GetResponse().GetHeaderMutation().GetRemoveHeaders()
}

var anthropicCall = []string{":authority", "api.anthropic.com", ":method", "POST", ":path", "/v1/messages", "content-type", "application/json", "accept-encoding", "gzip"}

// The request is held until its body is complete, then released.
func TestEnforceHoldsTheRequest(t *testing.T) {
	r := newRig(t, rabbitV2(), interceptor.Config{Content: true, Deadline: 5e9})
	if out := r.send(eReqHeaders(false, anthropicCall...)); len(out) != 0 {
		t.Fatalf("headers answered before the body: %v", out)
	}
	if out := r.send(eReqBody(`{"model":"m","messages":[{"role":"user","content":"a silly`, false)); len(out) != 0 {
		t.Fatalf("partial body answered: %v", out)
	}
	out := r.send(eReqBody(` rabbit"}]}`, true))
	if len(out) != 2 || out[0].GetRequestHeaders() == nil {
		t.Fatalf("want headers then body, got %v", out)
	}
	if b, eos := streamed(out[1]); string(b) != `{"model":"m","messages":[{"role":"user","content":"a tricks"}]}` || !eos {
		t.Fatalf("forwarded %q eos=%v", b, eos)
	}
}

// Without content the body goes through unchanged, and so do responses.
func TestEnforceWithoutContentPassesThrough(t *testing.T) {
	r := newRig(t, rabbitV2(), interceptor.Config{Deadline: 5e9})
	r.send(eReqHeaders(false, anthropicCall...))
	body := `{"model":"m","messages":[{"role":"user","content":"silly rabbit"}]}`
	out := r.send(eReqBody(body, true))
	if len(out) != 2 || len(removed(out[0])) != 0 {
		t.Fatalf("got %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != body {
		t.Fatalf("forwarded %q", b)
	}
	if out := r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream")); len(out) != 1 || len(removed(out[0])) != 0 {
		t.Fatalf("response headers %v", out)
	}
	if out := r.send(eRespBody([]byte("data: rabbit\n"), false)); len(out) != 1 {
		t.Fatalf("chunk not passed straight on: %v", out)
	} else if b, _ := streamed(out[0]); string(b) != "data: rabbit\n" {
		t.Fatalf("chunk %q", b)
	}
}

// An Interceptor that misses the deadline at OnCallStart fails the call
// closed: the client gets a 503 in the provider's own error format, and the
// call is recorded with it.
func TestEnforceFailsClosedAtStart(t *testing.T) {
	for _, tc := range []struct {
		host, want string
	}{{"api.anthropic.com", `"type":"permission_error"`}, {"api.openai.com", `"code":"blocked_by_policy"`}} {
		t.Run(tc.host, func(t *testing.T) {
			r := newRig(t, &interceptortest.Fake{Delay: 200 * time.Millisecond}, interceptor.Config{Deadline: 20 * time.Millisecond})
			r.send(eReqHeaders(false, ":authority", tc.host, ":method", "POST", ":path", "/v1/messages", "content-type", "application/json"))
			out := r.send(eReqBody(`{"model":"m"}`, true))
			im := out[0].GetImmediateResponse()
			if im == nil || im.GetStatus().GetCode() != 503 || !strings.Contains(string(im.GetBody()), tc.want) ||
				!strings.Contains(string(im.GetBody()), "interceptor unavailable") {
				t.Fatalf("want a 503 in the provider's format, got %v", out)
			}
			r.proc.Close()
			for _, e := range r.sink.ev {
				if e.Phase == events.PhaseComplete {
					if e.Status != 503 || !strings.Contains(e.Error, "interceptor unavailable") || e.InterceptMode != "enforce" {
						t.Fatalf("complete row %+v", e)
					}
					return
				}
			}
			t.Fatal("refused call has no complete row")
		})
	}
}

// With no Interceptor, enforce forwards everything unchanged.
func TestEnforceWithoutInterceptor(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 64})
	srv := New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s"})
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(st, eReqHeaders(false, anthropicCall...))
	out := srv.enforceHandle(st, eReqBody(`{"model":"bad"}`, true))
	if len(out) != 2 || out[1].GetRequestBody() == nil {
		t.Fatalf("got %v", out)
	}
	proc.Close()
}
