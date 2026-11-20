package extproc

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/capture"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// Options and the Error answer (issue #118): per-person fast mode and
// priority tiers, decided at call start without content callbacks.

const fastNotEnabled = "Fast mode is not enabled for your organization."

// optionOf is a request's option, "" when it doesn't set it.
func optionOf(ri *pb.RequestInfo, k pb.Option) string {
	for _, o := range ri.GetOptions() {
		if o.GetKey() == k {
			return o.GetValue()
		}
	}
	return ""
}

// noFast refuses Anthropic's fast mode as Anthropic does for an organization
// without it, and a priority tier with a 403.
func noFast() *interceptortest.Fake {
	answer := func(ri *pb.RequestInfo) *pb.CallVerdict {
		switch {
		case optionOf(ri, pb.Option_OPTION_SPEED) == "fast":
			return &pb.CallVerdict{Action: &pb.CallVerdict_Error{Error: &pb.Error{HttpStatus: 400, Type: "invalid_request_error", Message: fastNotEnabled}}}
		case optionOf(ri, pb.Option_OPTION_SERVICE_TIER) == "priority":
			return &pb.CallVerdict{Action: &pb.CallVerdict_Error{Error: &pb.Error{HttpStatus: 403, Message: "No priority tier for you."}}}
		}
		return &pb.CallVerdict{}
	}
	return &interceptortest.Fake{
		Start: func(s *pb.CallStart) *pb.CallVerdict { return answer(s.GetRequest()) },
		Turn:  func(t *pb.TurnStart) *pb.CallVerdict { return answer(t.GetRequest()) },
	}
}

func rowsOf(rows *[]events.Verdict) func(string, events.Verdict) {
	return func(_ string, v events.Verdict) { *rows = append(*rows, v) }
}

const fastBeta = "fast-mode-2026-02-01"

// Enforce: a fast request is refused at call start with Anthropic's own 400
// (x-should-retry: false), and never goes upstream; a standard request from
// the same fast-capable client (the beta header, no speed) goes on.
func TestErrorRefusesFastMode(t *testing.T) {
	var rows []events.Verdict
	f := noFast()
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 1024})
	ic := interceptortest.NewClient(t, f, interceptor.Config{Deadline: 5e9}, rowsOf(&rows))
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	call := append(anthropicCall, "anthropic-beta", fastBeta)

	fast := &streamState{correlationID: "fast", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(fast, eReqHeaders(false, call...))
	out := srv.enforceHandle(fast, eReqBody(`{"model":"m","speed":"fast","output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, true))
	im := out[0].GetImmediateResponse()
	if len(out) != 1 || im == nil || im.GetStatus().GetCode() != 400 {
		t.Fatalf("want an immediate 400, got %v", out)
	}
	var body map[string]any
	if err := json.Unmarshal(im.GetBody(), &body); err != nil || js(body) != js(map[string]any{
		"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": fastNotEnabled}}) {
		t.Fatalf("body %s (%v)", im.GetBody(), err)
	}
	hdrs := map[string]string{}
	for _, h := range im.GetHeaders().GetSetHeaders() {
		hdrs[h.GetHeader().GetKey()] = string(h.GetHeader().GetRawValue())
	}
	if hdrs["x-should-retry"] != "false" || hdrs["content-type"] != "application/json" {
		t.Fatalf("headers %v", hdrs)
	}
	if calls, _, _ := f.Snapshot(); strings.Join(calls, ",") != "start" {
		t.Fatalf("callbacks %v", calls)
	}

	standard := &streamState{correlationID: "standard", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(standard, eReqHeaders(false, call...))
	body2 := `{"model":"m","output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`
	out = srv.enforceHandle(standard, eReqBody(body2, true))
	if len(out) != 2 || out[0].GetImmediateResponse() != nil {
		t.Fatalf("a standard request was refused: %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != body2 {
		t.Fatalf("forwarded %s", b)
	}

	if len(rows) != 1 || rows[0].Action != "refuse" || !rows[0].Applied || rows[0].Callback != "call_start" ||
		rows[0].Reason != "400 invalid_request_error: "+fastNotEnabled {
		t.Fatalf("rows %+v", rows)
	}
	proc.Close()
	for _, e := range sink.ev {
		if e.Phase == events.PhaseComplete && e.CorrelationID == "fast" {
			if e.Status != 400 || e.Error != "refused by interceptor: "+fastNotEnabled {
				t.Fatalf("complete row: status %d, error %q", e.Status, e.Error)
			}
			return
		}
	}
	t.Fatal("no complete row for the refused call")
}

// Observe: the same answer is recorded, never applied.
func TestErrorObservedNotApplied(t *testing.T) {
	var rows []events.Verdict
	f := noFast()
	ic := interceptortest.NewClient(t, f, interceptor.Config{}, rowsOf(&rows))
	proc := events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024})
	srv := New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	st := &streamState{correlationID: "c"}
	srv.handle(st, eReqHeaders(false, anthropicCall...))
	srv.handle(st, eReqBody(`{"model":"m","speed":"fast","messages":[{"role":"user","content":"hi"}]}`, true))
	ic.Close()
	if len(rows) != 1 || rows[0].Action != "refuse" || rows[0].Applied || rows[0].Mode != "observe" {
		t.Fatalf("rows %+v", rows)
	}
}

// OnRequest can answer Error too: the call is refused in its API's format.
func TestErrorFromOnRequest(t *testing.T) {
	var rows []events.Verdict
	f := &interceptortest.Fake{Request: func(*pb.RequestMessages) *pb.RequestVerdict {
		return &pb.RequestVerdict{Error: &pb.Error{HttpStatus: 403, Message: "Not this."}}
	}}
	ic := interceptortest.NewClient(t, f, interceptor.Config{Content: true, Deadline: 5e9}, rowsOf(&rows))
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024}), InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(st, eReqHeaders(false, ":authority", "api.openai.com", ":method", "POST", ":path", "/v1/responses", "content-type", "application/json"))
	out := srv.enforceHandle(st, eReqBody(`{"model":"m","input":"hi"}`, true))
	im := out[0].GetImmediateResponse()
	if im == nil || im.GetStatus().GetCode() != 403 ||
		string(im.GetBody()) != `{"error":{"code":null,"message":"Not this.","param":null,"type":"invalid_request_error"}}` {
		t.Fatalf("got %v", out)
	}
	if len(rows) != 1 || rows[0].Callback != "request" || rows[0].Action != "refuse" || !rows[0].Applied {
		t.Fatalf("rows %+v", rows)
	}
}

// An Error's status is 400 to 599: another is an invalid answer, onError.
func TestErrorStatusChecked(t *testing.T) {
	var rows []events.Verdict
	f := &interceptortest.Fake{Start: func(*pb.CallStart) *pb.CallVerdict {
		return &pb.CallVerdict{Action: &pb.CallVerdict_Error{Error: &pb.Error{HttpStatus: 200}}}
	}}
	ic := interceptortest.NewClient(t, f, interceptor.Config{Deadline: 5e9, OnError: "allow"}, rowsOf(&rows))
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024}), InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(st, eReqHeaders(false, anthropicCall...))
	if out := srv.enforceHandle(st, eReqBody(`{"model":"m"}`, true)); len(out) != 2 {
		t.Fatalf("an invalid Error was applied: %v", out)
	}
	if len(rows) != 1 || rows[0].Action != "error" || !strings.Contains(rows[0].Reason, "http_status is 200") {
		t.Fatalf("rows %+v", rows)
	}
}

// Codex's WebSocket: with turns on, each response.create goes to
// OnTurnStart, without content. A refused turn gets the API's error event and
// never goes upstream; the connection stays open, and the next turn goes on.
func TestErrorRefusesAWebSocketTurn(t *testing.T) {
	var rows []events.Verdict
	f := noFast()
	ic := interceptortest.NewClient(t, f, interceptor.Config{Turns: true, Deadline: 5e9}, rowsOf(&rows))
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024}), InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	out := srv.enforceHandle(st, eReqHeaders(false, ":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
		"upgrade", "websocket", "sec-websocket-extensions", "permessage-deflate"))
	if len(out) != 1 || strings.Join(removed(out[0]), ",") != "sec-websocket-extensions" {
		t.Fatalf("upgrade headers %v: its messages are re-framed for OnTurnStart", out)
	}
	srv.enforceHandle(st, eRespHeaders(":status", "101", "upgrade", "websocket"))

	priority := `{"type":"response.create","model":"m","service_tier":"priority","reasoning":{"effort":"high"},"input":[]}`
	out = srv.enforceHandle(st, eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(priority), true)), false))
	if len(out) != 2 {
		t.Fatalf("want nothing upstream and the error to the client, got %v", out)
	}
	if b, eos := streamed(out[0]); len(b) != 0 || eos {
		t.Fatalf("upstream got %q (end %v)", b, eos)
	}
	b, eos := streamed(out[1])
	msgs, err := wsframe.NewDecoder(false, false, 1<<20).Feed(b)
	if err != nil || eos || len(msgs) != 1 || msgs[0].Opcode != wsframe.OpText ||
		string(msgs[0].Payload) != `{"error":{"code":null,"message":"No priority tier for you.","type":"invalid_request_error"},"status":403,"type":"error"}` {
		t.Fatalf("to client: %v %+v (end %v)", err, msgs, eos)
	}

	standard := `{"type":"response.create","model":"m","reasoning":{"effort":"high"},"input":[]}`
	out = srv.enforceHandle(st, eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(standard), true)), false))
	b, _ = streamed(out[0])
	if msgs, err := wsframe.NewDecoder(false, false, 1<<20).Feed(b); err != nil || len(msgs) != 1 || string(msgs[0].Payload) != standard {
		t.Fatalf("the next turn: %v %+v", err, msgs)
	}
	turns := f.TurnsSeen()
	if len(turns) != 2 || turns[0].GetWsTurn() != 1 || turns[1].GetWsTurn() != 2 ||
		optionOf(turns[0].GetRequest(), pb.Option_OPTION_SERVICE_TIER) != "priority" || optionOf(turns[1].GetRequest(), pb.Option_OPTION_EFFORT) != "high" ||
		turns[0].GetCall().GetApi() != pb.Api_API_CHATGPT_BACKEND {
		t.Fatalf("OnTurnStart got %v", turns)
	}
	if len(rows) != 1 || rows[0].Callback != "turn_start" || rows[0].Action != "refuse" || rows[0].MessageID != "1" {
		t.Fatalf("rows %+v", rows)
	}
}

// Without content and without turns on, an enforced WebSocket is what it
// was: its frames go through untouched, compression and all, and nothing is
// asked per turn.
func TestWebSocketWithoutTurnsPassesThrough(t *testing.T) {
	f := noFast()
	ic := interceptortest.NewClient(t, f, interceptor.Config{Deadline: 5e9}, nil)
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024}), InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	out := srv.enforceHandle(st, eReqHeaders(false, ":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
		"upgrade", "websocket", "sec-websocket-extensions", "permessage-deflate"))
	if len(out) != 1 || len(removed(out[0])) != 0 {
		t.Fatalf("upgrade headers %v: compression must stay", out)
	}
	srv.enforceHandle(st, eRespHeaders(":status", "101", "upgrade", "websocket"))
	frame := wsframe.Encode(wsframe.OpText, []byte(`{"type":"response.create","model":"m","service_tier":"priority","input":[]}`), true)
	for _, chunk := range [][]byte{frame[:7], frame[7:]} { // forwarded as it comes, even mid-frame
		out = srv.enforceHandle(st, eReqBody(string(chunk), false))
		if b, _ := streamed(out[0]); len(out) != 1 || string(b) != string(chunk) {
			t.Fatalf("got %v, want the chunk as sent", out)
		}
	}
	if calls, _, _ := f.Snapshot(); strings.Join(calls, ",") != "start" {
		t.Fatalf("callbacks %v, want only OnCallStart", calls)
	}
}

// An Interceptor without OnTurnStart accepts every turn; AIntercept stops
// asking, and later connections keep their compression.
func TestTurnStartUnimplemented(t *testing.T) {
	ic := interceptortest.NewClient(t, &interceptortest.Fake{}, interceptor.Config{Turns: true, Deadline: 5e9}, nil)
	t.Cleanup(ic.Close)
	srv := New(Config{Processor: events.NewProcessor(&memSink{}, events.ProcessorConfig{BufferSize: 1024}), InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})
	upgrade := eReqHeaders(false, ":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
		"upgrade", "websocket", "sec-websocket-extensions", "permessage-deflate")
	st := &streamState{correlationID: "c", enforce: true, enf: &enforceState{}}
	srv.enforceHandle(st, upgrade)
	srv.enforceHandle(st, eRespHeaders(":status", "101", "upgrade", "websocket"))
	create := `{"type":"response.create","model":"m","input":[]}`
	out := srv.enforceHandle(st, eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(create), true)), false))
	b, _ := streamed(out[0])
	if msgs, _ := wsframe.NewDecoder(false, false, 1<<20).Feed(b); len(msgs) != 1 || string(msgs[0].Payload) != create {
		t.Fatalf("the turn wasn't accepted: %q", b)
	}
	if ic.Turns() {
		t.Fatal("still asking OnTurnStart")
	}
	next := &streamState{correlationID: "d", enforce: true, enf: &enforceState{}}
	if out := srv.enforceHandle(next, upgrade); len(removed(out[0])) != 0 {
		t.Fatalf("a later connection lost its compression: removed %v", removed(out[0]))
	}
}

// Each API's error, as the observer writes it, has the shape of that API's
// real errors in the capture corpus: the same fields, of the same types (a
// request's own id aside), and the same headers. An API with no captured
// error is listed in KNOWN_GAPS.
func TestErrorShapesMatchCorpus(t *testing.T) {
	gaps := readGaps(t, filepath.Join(corpusRoot, "KNOWN_GAPS"))
	dirs, err := capture.Cases(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	byAPI := map[string][]*capture.Case{}
	for _, dir := range dirs {
		c, err := capture.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if c.Response.Status >= 400 && json.Valid(c.RespBody) {
			byAPI[c.Meta.API] = append(byAPI[c.Meta.API], c)
		}
	}
	for _, api := range []string{apiAnthropic, apiResponses, apiChat, apiChatGPT, apiGemini} {
		t.Run(api, func(t *testing.T) {
			cases := byAPI[api]
			if len(cases) == 0 {
				if _, ok := gaps["field "+api+":row.error"]; !ok {
					t.Fatalf("no captured %s error to check its shape against; add one, or list `field %s:row.error` in KNOWN_GAPS", api, api)
				}
				return
			}
			for _, c := range cases {
				dn := denial{status: c.Response.Status, errType: "", host: c.Request.Host, path: c.Request.Path, reason: "x"}
				var real, ours any
				_ = json.Unmarshal(c.RespBody, &real)
				_ = json.Unmarshal(dn.body(), &ours)
				if d := shapeDiff("", ours, real); d != "" {
					t.Errorf("%s: %s\n ours %s\n real %s", c.Name(corpusRoot), d, dn.body(), c.RespBody)
				}
				for k, v := range dn.headers() {
					got := c.Response.Headers[k]
					if k == "content-type" && !strings.Contains(got, "json") {
						continue // a gateway's own error (text/plain); the API's are JSON
					}
					if got != v {
						t.Errorf("%s: header %s is %q, the real one %q", c.Name(corpusRoot), k, v, got)
					}
				}
				if c.Response.Headers["x-should-retry"] != "" && dn.headers()["x-should-retry"] == "" {
					t.Errorf("%s: the real error says x-should-retry, ours doesn't", c.Name(corpusRoot))
				}
			}
		})
	}
}

// providerOwn are fields of a real error that only the provider fills: the
// request's id, a gateway's echo of the status, and an error's own details
// (Anthropic's error_code on some errors).
var providerOwn = map[string]bool{"request_id": true, "status": true, ".error.details": true}

// shapeDiff says how ours differs in shape from real: a field one has and the
// other doesn't, or of another JSON type (null matches anything).
func shapeDiff(path string, ours, real any) string {
	om, ok1 := ours.(map[string]any)
	rm, ok2 := real.(map[string]any)
	if ok1 != ok2 {
		return fmt.Sprintf("%s: %T, the real one %T", path, ours, real)
	}
	if !ok1 {
		if ours == nil || real == nil || fmt.Sprintf("%T", ours) == fmt.Sprintf("%T", real) {
			return ""
		}
		return fmt.Sprintf("%s: %T, the real one %T", path, ours, real)
	}
	keys := map[string]bool{}
	for k := range om {
		keys[k] = true
	}
	for k := range rm {
		keys[k] = true
	}
	var ks []string
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		o, inO := om[k]
		r, inR := rm[k]
		switch {
		case !inO && (path == "" && providerOwn[k] || providerOwn[path+"."+k]):
		case !inO:
			return fmt.Sprintf("%s.%s: the real one has it, ours doesn't", path, k)
		case !inR:
			return fmt.Sprintf("%s.%s: ours has it, the real one doesn't", path, k)
		default:
			if d := shapeDiff(path+"."+k, o, r); d != "" {
				return d
			}
		}
	}
	return ""
}
