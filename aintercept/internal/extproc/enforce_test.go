package extproc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// redactor replaces "clown" in every message, denies model "bad", denies a
// message containing "deny", and drops one containing "drop".
func redactor() *interceptortest.Fake {
	return &interceptortest.Fake{
		Start: func(s *pb.CallStart) *pb.Verdict {
			if s.GetRequest().GetModel() == "bad" {
				return &pb.Verdict{Action: &pb.Verdict_Deny{Deny: &pb.Deny{Reason: "bad model"}}}
			}
			return &pb.Verdict{}
		},
		Message: func(m *pb.Message) *pb.Verdict {
			p := m.GetPayload()
			switch {
			case bytes.Contains(p, []byte("deny")):
				return &pb.Verdict{Action: &pb.Verdict_Deny{Deny: &pb.Deny{Reason: "no"}}}
			case bytes.Contains(p, []byte("drop")):
				return &pb.Verdict{Action: &pb.Verdict_Drop{Drop: &pb.Drop{}}}
			case bytes.Contains(p, []byte("clown")):
				return &pb.Verdict{Action: &pb.Verdict_Replace{Replace: &pb.Replace{Payload: bytes.ReplaceAll(p, []byte("clown"), []byte("XX"))}}}
			}
			return &pb.Verdict{}
		},
	}
}

type enforceRig struct {
	t    *testing.T
	srv  *Server
	st   *streamState
	sink *memSink
	proc *events.Processor
}

func newRig(t *testing.T, content bool) *enforceRig {
	t.Helper()
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 1024})
	ic := interceptortest.NewClient(t, redactor(), interceptor.Config{Content: content, Deadline: 5e9}, nil)
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

// The request is held until its body is complete, then released, replaced.
func TestEnforceHoldsAndReplacesRequest(t *testing.T) {
	r := newRig(t, true)
	if out := r.send(eReqHeaders(false, anthropicCall...)); len(out) != 0 {
		t.Fatalf("headers answered before the body: %v", out)
	}
	if out := r.send(eReqBody(`{"model":"m","messages":[{"content":"a cl`, false)); len(out) != 0 {
		t.Fatalf("partial body answered: %v", out)
	}
	out := r.send(eReqBody(`own"}]}`, true))
	if len(out) != 2 || out[0].GetRequestHeaders() == nil {
		t.Fatalf("want headers then body, got %v", out)
	}
	if rm := strings.Join(removed(out[0]), ","); rm != "accept-encoding,content-length,content-encoding" {
		t.Fatalf("removed headers %q", rm)
	}
	if b, eos := streamed(out[1]); string(b) != `{"model":"m","messages":[{"content":"a XX"}]}` || !eos {
		t.Fatalf("forwarded %q eos=%v", b, eos)
	}
}

// Without content the body goes through unchanged, and so do responses.
func TestEnforceWithoutContentPassesThrough(t *testing.T) {
	r := newRig(t, false)
	r.send(eReqHeaders(false, anthropicCall...))
	out := r.send(eReqBody(`{"model":"m","x":"clown"}`, true))
	if len(out) != 2 || len(removed(out[0])) != 0 {
		t.Fatalf("got %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != `{"model":"m","x":"clown"}` {
		t.Fatalf("forwarded %q", b)
	}
	if out := r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream")); len(out) != 1 || len(removed(out[0])) != 0 {
		t.Fatalf("response headers %v", out)
	}
	if out := r.send(eRespBody([]byte("data: clown\n"), false)); len(out) != 1 {
		t.Fatalf("chunk not passed straight on: %v", out)
	} else if b, _ := streamed(out[0]); string(b) != "data: clown\n" {
		t.Fatalf("chunk %q", b)
	}
}

// OnCallStart denies with the provider's own error, and the call is recorded.
func TestEnforceDenyAtStart(t *testing.T) {
	r := newRig(t, false)
	r.send(eReqHeaders(false, anthropicCall...))
	out := r.send(eReqBody(`{"model":"bad"}`, true))
	im := out[0].GetImmediateResponse()
	if im == nil || im.GetStatus().GetCode() != 403 {
		t.Fatalf("want a 403 immediate response, got %v", out)
	}
	var body struct {
		Type  string `json:"type"`
		Error struct{ Type, Message string }
	}
	if err := json.Unmarshal(im.GetBody(), &body); err != nil || body.Type != "error" || body.Error.Type != "permission_error" || body.Error.Message != "bad model" {
		t.Fatalf("body %s", im.GetBody())
	}
	r.proc.Close()
	for _, e := range r.sink.ev {
		if e.Phase == events.PhaseComplete {
			if e.Status != 403 || !strings.Contains(e.Error, "bad model") || e.InterceptMode != "enforce" {
				t.Fatalf("complete row %+v", e)
			}
			return
		}
	}
	t.Fatal("denied call has no complete row")
}

// An event stream is decided event by event: replaced, dropped, allowed; a
// deny ends it with the provider's error event.
func TestEnforceEventStream(t *testing.T) {
	r := newRig(t, true)
	r.send(eReqHeaders(true, ":authority", "api.anthropic.com", ":method", "GET", ":path", "/v1/x"))
	out := r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream"))
	if len(out) != 1 || strings.Join(removed(out[0]), ",") != "content-length" {
		t.Fatalf("headers %v", out)
	}
	out = r.send(eRespBody([]byte("event: a\ndata: clown\n\ndata: drop me\n\ndata: ok\n\ndata: par"), false))
	b, eos := streamed(out[0])
	if string(b) != "event: a\ndata: XX\n\ndata: ok\n\n" || eos {
		t.Fatalf("got %q eos=%v", b, eos)
	}
	out = r.send(eRespBody([]byte("tial deny\n\ndata: after\n\n"), false))
	b, eos = streamed(out[0])
	if !eos || !strings.HasPrefix(string(b), "event: error\ndata: ") || strings.Contains(string(b), "after") {
		t.Fatalf("deny: got %q eos=%v", b, eos)
	}
	if out := r.send(eRespBody([]byte("data: more\n\n"), true)); len(out) != 0 {
		t.Fatalf("answered after the deny: %v", out)
	}
}

// A whole body is held, then released replaced (or denied).
func TestEnforceWholeResponse(t *testing.T) {
	r := newRig(t, true)
	r.send(eReqHeaders(true, ":authority", "api.openai.com", ":method", "GET", ":path", "/v1/x"))
	if out := r.send(eRespHeaders(":status", "200", "content-type", "application/json")); len(out) != 0 {
		t.Fatalf("headers released before the body: %v", out)
	}
	r.send(eRespBody([]byte(`{"a":"cl`), false))
	out := r.send(eRespBody([]byte(`own"}`), true))
	if len(out) != 2 || strings.Join(removed(out[0]), ",") != "content-length,content-encoding" {
		t.Fatalf("got %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != `{"a":"XX"}` {
		t.Fatalf("body %q", b)
	}

	r = newRig(t, true)
	r.send(eReqHeaders(true, ":authority", "api.openai.com", ":method", "GET", ":path", "/v1/x"))
	r.send(eRespHeaders(":status", "200", "content-type", "application/json"))
	out = r.send(eRespBody([]byte(`{"a":"deny"}`), true))
	if im := out[0].GetImmediateResponse(); im == nil || !strings.Contains(string(im.GetBody()), "blocked_by_policy") {
		t.Fatalf("want OpenAI's error format, got %v", out)
	}
}

// WebSocket messages are decided one by one and re-framed; compression is
// declined so frames never need re-compressing.
func TestEnforceWebSocket(t *testing.T) {
	r := newRig(t, true)
	out := r.send(eReqHeaders(false, ":authority", "chatgpt.com", ":method", "GET", ":path", "/ws",
		"upgrade", "websocket", "sec-websocket-extensions", "permessage-deflate"))
	if len(out) != 1 || strings.Join(removed(out[0]), ",") != "sec-websocket-extensions" {
		t.Fatalf("upgrade headers %v", out)
	}
	if out := r.send(eRespHeaders(":status", "101", "upgrade", "websocket")); len(out) != 1 {
		t.Fatalf("101 %v", out)
	}
	frames := append(wsframe.Encode(wsframe.OpText, []byte("hi clown"), true), wsframe.Encode(wsframe.OpText, []byte("drop"), true)...)
	out = r.send(eReqBody(string(frames[:5]), false))
	if b, _ := streamed(out[0]); len(b) != 0 {
		t.Fatalf("partial frame forwarded: %q", b)
	}
	out = r.send(eReqBody(string(frames[5:]), false))
	b, _ := streamed(out[0])
	msgs, err := wsframe.NewDecoder(false, false, 1<<20).Feed(b)
	if err != nil || len(msgs) != 1 || string(msgs[0].Payload) != "hi XX" || b[1]&0x80 == 0 {
		t.Fatalf("to server: %v %+v (masked=%v)", err, msgs, b[1]&0x80 != 0)
	}
	out = r.send(eRespBody(wsframe.Encode(wsframe.OpText, []byte("please deny"), false), false))
	if len(out) != 2 {
		t.Fatalf("deny: want close both ways, got %v", out)
	}
	toClient, _ := streamed(out[0])
	msgs, _ = wsframe.NewDecoder(false, false, 1<<20).Feed(toClient)
	if len(msgs) != 2 || !strings.Contains(string(msgs[0].Payload), "blocked_by_policy") || msgs[1].CloseCode != 1008 {
		t.Fatalf("to client: %+v", msgs)
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
