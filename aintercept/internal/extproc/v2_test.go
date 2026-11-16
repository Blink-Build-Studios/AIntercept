package extproc

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// rabbitV2 replaces "silly rabbit" in any text part of the new messages.
func rabbitV2() *interceptortest.FakeV2 {
	return &interceptortest.FakeV2{Request: func(q *pb2.RequestMessages) *pb2.RequestVerdict {
		v := &pb2.RequestVerdict{}
		for _, m := range q.GetMessages() {
			m := proto.Clone(m).(*pb2.ConvMessage)
			hit := false
			for _, p := range m.GetParts() {
				if t := p.GetText(); t != nil && strings.Contains(t.GetText(), "silly rabbit") {
					t.Text, hit = strings.ReplaceAll(t.GetText(), "silly rabbit", "tricks"), true
				}
			}
			if hit {
				v.Messages = append(v.Messages, &pb2.MessageVerdict{MessageId: m.GetId(), Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: m}}})
			}
		}
		return v
	}}
}

type v2Rig struct {
	enforceRig
	fake *interceptortest.FakeV2
	ic   *interceptor.Client
	rows *[]events.Verdict
}

func newV2Rig(t *testing.T, enforce bool) *v2Rig {
	t.Helper()
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 1024})
	f := rabbitV2()
	var rows []events.Verdict
	ic := interceptortest.NewClientV2(t, f, interceptor.Config{Content: true, Deadline: 5e9}, func(_ string, v events.Verdict) { rows = append(rows, v) })
	st := &streamState{correlationID: "c"}
	if enforce {
		st.enforce, st.enf = true, &enforceState{}
	}
	return &v2Rig{
		enforceRig: enforceRig{t: t, sink: sink, proc: proc, st: st,
			srv: New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})},
		fake: f, ic: ic, rows: &rows,
	}
}

const rabbitHistory = `{"model":"m","messages":[{"role":"user","content":"I'm a silly rabbit"},{"role":"assistant","content":"ha"},`

// Enforce, HTTP: the new prompt is replaced in place; the history (which
// says the same thing) and every other byte go as sent. The reply is asked
// for uncompressed (it goes to the Interceptor; this one has no OnReply, so
// its text goes on as it comes).
func TestV2EnforceReplacesTheNewPrompt(t *testing.T) {
	r := newV2Rig(t, true)
	body := rabbitHistory + `{"role":"user","content":[{"type":"text","text":"I'm a silly rabbit","cache_control":{"type":"ephemeral"}}]}]}`
	r.send(eReqHeaders(false, anthropicCall...))
	out := r.send(eReqBody(body, true))
	if len(out) != 2 || strings.Join(removed(out[0]), ",") != "content-length,content-encoding,accept-encoding" {
		t.Fatalf("got %v (removed %v)", out, removed(out[0]))
	}
	want := rabbitHistory + `{"role":"user","content":[{"type":"text","text":"I'm a tricks","cache_control":{"type":"ephemeral"}}]}]}`
	if b, eos := streamed(out[1]); string(b) != want || !eos {
		t.Fatalf("forwarded %s", b)
	}
	if _, reqs, _ := r.fake.Snapshot(); len(reqs) != 1 || len(reqs[0].GetMessages()) != 1 || reqs[0].GetTotalMessages() != 3 {
		t.Fatalf("OnRequest %v: want only the new message", reqs)
	}
	if out := r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream")); len(out) != 1 || strings.Join(removed(out[0]), ",") != "content-length" {
		t.Fatalf("response headers %v", out)
	}
	if out := r.send(eRespBody([]byte("data: silly rabbit\n\n"), false)); len(out) != 1 {
		t.Fatalf("event not passed straight on: %v", out)
	} else if b, _ := streamed(out[0]); string(b) != "data: silly rabbit\n\n" {
		t.Fatalf("event %q", b)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Action != "replace" || !rows[0].Applied || rows[0].MessageID == "" {
		t.Fatalf("rows %+v", rows)
	}
}

// Nothing to change: the body goes as sent, headers untouched but for
// accept-encoding (the reply's tool calls are held).
func TestV2EnforceAccept(t *testing.T) {
	r := newV2Rig(t, true)
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	r.send(eReqHeaders(false, anthropicCall...))
	out := r.send(eReqBody(body, true))
	if len(out) != 2 || strings.Join(removed(out[0]), ",") != "accept-encoding" {
		t.Fatalf("got %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != body {
		t.Fatalf("forwarded %s", b)
	}
}

// A request it can't read goes upstream untouched, without OnRequest, and is
// counted.
func TestV2EnforceUnparsedPassesAndCounts(t *testing.T) {
	r := newV2Rig(t, true)
	r.send(eReqHeaders(false, ":authority", "api.anthropic.com", ":method", "POST", ":path", "/v1/messages/count_tokens", "content-type", "application/json"))
	body := `{"messages":[{"role":"user","content":"I'm a silly rabbit"}]}`
	out := r.send(eReqBody(body, true))
	if b, _ := streamed(out[1]); string(b) != body {
		t.Fatalf("forwarded %s", b)
	}
	if calls, _, _ := r.fake.Snapshot(); strings.Join(calls, ",") != "start" {
		t.Fatalf("calls %v, want only OnCallStart", calls)
	}
	if n := r.ic.Stats().Unparsed; n != 1 {
		t.Fatalf("unparsed %d, want 1", n)
	}
}

// Enforce, Codex's WebSocket: a response.create is replaced and re-framed;
// the server's messages go to the client byte for byte.
func TestV2EnforceWebSocket(t *testing.T) {
	r := newV2Rig(t, true)
	out := r.send(eReqHeaders(false, ":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
		"upgrade", "websocket", "sec-websocket-extensions", "permessage-deflate"))
	if len(out) != 1 || strings.Join(removed(out[0]), ",") != "sec-websocket-extensions" {
		t.Fatalf("upgrade headers %v", out)
	}
	r.send(eRespHeaders(":status", "101", "upgrade", "websocket"))
	create := `{"type":"response.create","model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]}]}`
	out = r.send(eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(create), true)), false))
	b, _ := streamed(out[0])
	msgs, err := wsframe.NewDecoder(false, false, 1<<20).Feed(b)
	if err != nil || len(msgs) != 1 || string(msgs[0].Payload) != strings.Replace(create, "silly rabbit", "tricks", 1) {
		t.Fatalf("to server: %v %+v", err, msgs)
	}
	down := wsframe.Encode(wsframe.OpText, []byte(`{"type":"response.output_text.delta","delta":"silly rabbit"}`), false)
	out = r.send(eRespBody(down, false))
	if b, _ := streamed(out[0]); !bytes.Equal(b, down) {
		t.Fatalf("to client %q, want it as sent", b)
	}
}

// Observe: OnRequest is queued; its answer recorded, never applied.
func TestV2Observe(t *testing.T) {
	r := newV2Rig(t, false)
	body := rabbitHistory + `{"role":"user","content":"I'm a silly rabbit"}]}`
	r.srv.handle(r.st, eReqHeaders(false, anthropicCall...))
	r.srv.handle(r.st, eReqBody(body, true))
	r.ic.Close() // drains the queue
	calls, reqs, _ := r.fake.Snapshot()
	if strings.Join(calls, ",") != "start,request" || len(reqs[0].GetMessages()) != 1 {
		t.Fatalf("calls %v requests %v", calls, reqs)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Applied || rows[0].Mode != "observe" || rows[0].Action != "replace" {
		t.Fatalf("rows %+v", rows)
	}
}
