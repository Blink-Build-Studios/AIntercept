package extproc

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/capture"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/identity"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// The capture corpus (docs/PROVIDERS.md, "The test rule"): every traffic
// shape we've seen, as an anonymized real call, replayed through the
// observer's own code (this server in observe mode, the processor's
// extraction, the records assembler, the Interceptor's callbacks and the
// identity resolvers). Each case's expect.json lists what that shape means,
// field by field; each field is its own test.
//
//	go test ./internal/extproc -run TestCorpus -update   rewrites every expect.json from the code (review the diff)

var updateCorpus = flag.Bool("update", false, "rewrite each corpus case's expect.json from what the code reads")

const corpusRoot = "../events/testdata/captures"

func TestCorpus(t *testing.T) {
	dirs, err := capture.Cases(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no cases in the corpus")
	}
	for _, dir := range dirs {
		c, err := capture.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(c.Name(corpusRoot), func(t *testing.T) {
			got := replayCase(t, c)
			path := filepath.Join(dir, "expect.json")
			if *updateCorpus {
				if err := capture.WriteJSON(path, got); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := readExpect(path)
			if err != nil {
				t.Fatalf("%v (write it, or run with -update and review it)", err)
			}
			for _, k := range unionKeys(want, got) {
				t.Run(k, func(t *testing.T) {
					w, inWant := want[k]
					g, inGot := got[k]
					switch {
					case !inGot:
						t.Errorf("the code no longer reads it; want %s", js(w))
					case !inWant:
						t.Errorf("not in expect.json; the code reads %s", js(g))
					case js(w) != js(g):
						t.Errorf("got  %s\nwant %s", js(g), js(w))
					}
				})
			}
		})
	}
}

func readExpect(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// js is a value as compact JSON with sorted keys (the comparison's form).
func js(v any) string {
	b, _ := json.Marshal(normalize(v))
	return string(b)
}

func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// ── The replay ───────────────────────────────────────────────────────────────

const corpusSalt = "corpus-salt"

// replayCase runs a case through the observer and reads off every field the
// code produces.
func replayCase(t *testing.T, c *capture.Case) map[string]any {
	t.Helper()
	// Each new credential is looked up as the observer does, at the
	// provider's account endpoint; only a case that is that endpoint's own
	// call answers it (with its response), the rest are refused.
	path, _, _ := strings.Cut(c.Request.Path, "?")
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || c.Response.Status == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(c.Response.Status)
		w.Write(c.RespBody)
	}))
	defer provider.Close()
	var mu sync.Mutex
	var learned []identity.Learned
	ids := identity.New(identity.Config{Lookup: true, Origin: provider.URL, Client: provider.Client(),
		Learned: func(l identity.Learned) { mu.Lock(); learned = append(learned, l); mu.Unlock() }})
	defer ids.Close()
	var starts []*pb2.CallStart
	fake := &interceptortest.Fake{
		Start: func(s *pb2.CallStart) *pb2.CallVerdict { starts = append(starts, s); return &pb2.CallVerdict{} },
		Reply: func(*pb2.ResponseMessage) *pb2.MessageVerdict { return &pb2.MessageVerdict{} },
		Turn:  func(*pb2.TurnStart) *pb2.CallVerdict { return &pb2.CallVerdict{} },
	}
	ic := interceptortest.NewClient(t, fake, interceptor.Config{Content: true, Deadline: 5 * time.Second}, nil)
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 1 << 14, OnComplete: func(e *events.Event) {
		ids.Observe(e)
		rec, final := interceptor.Record(e, ids)
		ic.Complete(rec, final)
	}})
	srv := New(Config{Processor: proc, InstanceID: "corpus", KeyHashSalt: corpusSalt, CaptureFullBodies: true,
		Interceptor: ic, Identities: ids, IdentityWait: 5 * time.Second})
	st := &streamState{correlationID: "corpus-call"}

	hdrs := requestHeaders(c)
	ws := len(c.WS) > 0
	reqHeaders := &extprocv3.ProcessingRequest{
		Request:    &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: headerMap(hdrs, len(c.ReqBody) == 0 && !ws)},
		Attributes: map[string]*structpb.Struct{extProcAttributesKey: {Fields: map[string]*structpb.Value{"source.address": structpb.NewStringValue(c.Request.IP)}}},
	}
	srv.handle(st, reqHeaders)
	if len(c.ReqBody) > 0 {
		for _, ch := range chunks(t, c.ReqBody, c.Request.Encoding, c.Request.Chunks) {
			srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: ch}})
		}
	}
	if c.Response.Headers != nil {
		srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
			ResponseHeaders: headerMap(c.Response.Headers, len(c.RespBody) == 0 && !ws)}})
	}
	switch {
	case ws:
		replayWS(t, srv, st, c)
		srv.wsClose(st)
	case len(c.RespBody) > 0:
		for _, ch := range chunks(t, c.RespBody, c.Response.Encoding, c.Response.Chunks) {
			srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: ch}})
		}
	case c.Response.Error != "":
		srv.offer(st, events.Event{Phase: events.PhaseDisconnect, Error: c.Response.Error})
	}
	ic.Ended(st.correlationID)
	proc.Close()
	ic.Close()

	// The rows, through the assembler the observer writes its raw files with.
	asm := records.NewAssembler("corpus")
	var recs []records.Record
	for i := range sink.ev {
		line, _ := json.Marshal(sink.ev[i])
		recs = append(recs, asm.Apply(&sink.ev[i], line, "")...)
	}
	recs = append(recs, asm.Flush()...)

	out := map[string]any{}
	observeRows(out, sink.ev)
	observeRecords(out, recs)
	observeConvo(out, c)
	observeInterceptor(out, fake, starts, ic.Stats())
	mu.Lock()
	observeLearned(out, learned)
	mu.Unlock()
	return shorten(out).(map[string]any)
}

// requestHeaders are the case's request headers with a stand-in for its
// redacted credential.
func requestHeaders(c *capture.Case) map[string]string {
	h := map[string]string{}
	for k, v := range c.Request.Headers {
		h[k] = v
	}
	if cr := c.Request.Credential; cr != nil {
		tok := standIn(cr)
		if cr.Scheme != "" {
			tok = cr.Scheme + " " + tok
		}
		h[cr.Header] = tok
	}
	return h
}

// standIn is a fake credential of the case's kind, built here so no
// credential-shaped string is committed.
func standIn(cr *capture.Credential) string {
	fill := strings.Repeat("0", 80)
	switch cr.Kind {
	case capture.CredAnthropicOAuth:
		return "sk-ant-oat01-corpus" + fill + "OAAA"
	case capture.CredAnthropicKey:
		return "sk-ant-api03-corpus" + fill + "KAAA"
	case capture.CredOpenAIKey:
		return "sk-proj-corpus" + fill + "OPEN"
	case capture.CredGeminiKey:
		return "AIza" + "corpus" + fill[:29]
	case capture.CredChatGPT:
		return capture.JWT(cr.Claims)
	}
	return "corpus-credential"
}

func headerMap(h map[string]string, eos bool) *extprocv3.HttpHeaders {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	hm := &corev3.HeaderMap{}
	for _, k := range keys {
		hm.Headers = append(hm.Headers, &corev3.HeaderValue{Key: k, RawValue: []byte(h[k])})
	}
	return &extprocv3.HttpHeaders{Headers: hm, EndOfStream: eos}
}

// chunks are a body as sent: encoded as it was on the wire, cut where it was.
func chunks(t *testing.T, plain []byte, enc string, sizes []int) []*extprocv3.HttpBody {
	t.Helper()
	b := encodeBody(t, plain, enc)
	var out []*extprocv3.HttpBody
	for i, n := range sizes {
		if i == len(sizes)-1 || n > len(b) {
			break
		}
		out = append(out, &extprocv3.HttpBody{Body: b[:n]})
		b = b[n:]
	}
	out = append(out, &extprocv3.HttpBody{Body: b, EndOfStream: true})
	return out
}

func encodeBody(t *testing.T, b []byte, enc string) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch e := strings.ToLower(strings.TrimSpace(enc)); {
	case e == "" || e == "identity":
		return b
	case strings.Contains(e, "gzip"):
		w := gzip.NewWriter(&buf)
		w.Write(b)
		w.Close()
	case e == "br":
		w := brotli.NewWriter(&buf)
		w.Write(b)
		w.Close()
	case e == "zstd":
		w, _ := zstd.NewWriter(nil)
		return w.EncodeAll(b, nil)
	default:
		t.Fatalf("encoding %q: add it to the corpus replay", enc)
	}
	return buf.Bytes()
}

// replayWS sends a case's WebSocket messages as frames (compressed when the
// connection negotiated it), each direction's in order.
func replayWS(t *testing.T, srv *Server, st *streamState, c *capture.Case) {
	t.Helper()
	ext := wsframe.ParseExtensions(c.Response.Headers["sec-websocket-extensions"])
	up := &wsDeflater{on: ext.Deflate, reset: ext.ClientNoContextTakeover}
	down := &wsDeflater{on: ext.Deflate, reset: ext.ServerNoContextTakeover}
	for i, m := range c.WS {
		isUp := m.Dir == "up"
		var frame []byte
		switch {
		case m.Close != nil:
			frame = wsframe.CloseFrame(*m.Close, "", isUp)
		default:
			payload, op := []byte(m.Msg), byte(wsframe.OpText)
			switch {
			case m.Text != "":
				payload = []byte(m.Text)
			case m.Binary != "":
				payload, _ = base64.StdEncoding.DecodeString(m.Binary)
				op = wsframe.OpBinary
			}
			d := down
			if isUp {
				d = up
			}
			frame = d.frame(t, op, payload, isUp)
		}
		body := &extprocv3.HttpBody{Body: frame}
		if isUp {
			srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: body}})
		} else {
			srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: body}})
		}
		_ = i
	}
}

// wsDeflater frames one direction's messages, per-message-deflate when on.
type wsDeflater struct {
	on, reset bool
	buf       bytes.Buffer
	w         *flate.Writer
}

func (d *wsDeflater) frame(t *testing.T, op byte, p []byte, mask bool) []byte {
	if !d.on {
		return wsframe.Encode(op, p, mask)
	}
	if d.w == nil || d.reset {
		d.buf.Reset()
		d.w, _ = flate.NewWriter(&d.buf, flate.DefaultCompression)
	}
	start := d.buf.Len()
	d.w.Write(p)
	d.w.Flush()
	z := append([]byte(nil), d.buf.Bytes()[start:]...)
	z = z[:len(z)-4] // the sync flush's tail, per RFC 7692
	f := wsframe.Encode(op, z, mask)
	f[0] |= 0x40 // RSV1: compressed
	return f
}

// ── What the code read ───────────────────────────────────────────────────────

// Fields that are measured or made up by the replay, not read from the traffic.
var rowSkip = corpusSet("correlation_id", "seq", "timestamp_ns", "request_start_ns", "response_start_ns", "request_bytes",
	"response_bytes", "bytes", "body_b64", "body_preview", "observer_instance", "headers", "end_of_stream", "phase",
	"direction")

var callSkip = corpusSet("call_id", "correlation_id", "source_file", "first_file", "start_ns", "response_start_ns", "end_ns",
	"request_bytes", "response_bytes", "detail_json", "client_json", "provider_json", "tool_uses_json", "ext_json", "tools_offered")

func corpusSet(ks ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range ks {
		m[k] = true
	}
	return m
}

// observeRows reads the processor's rows: the call's complete row (or each
// WebSocket turn's, and the connection's), and its analytics events.
func observeRows(out map[string]any, rows []events.Event) {
	var turns []events.Event
	var evs []any
	for _, e := range rows {
		switch {
		case e.Phase == events.PhaseComplete && e.Transport == events.TransportWebSocket:
			turns = append(turns, e)
		case e.Phase == events.PhaseComplete:
			flatInto(out, "row", rowMap(e))
		case e.Phase == events.PhaseWSConnection:
			m := rowMap(e)
			if w, ok := m["websocket"].(map[string]any); ok {
				delete(w, "duration_ms")
				delete(w, "bytes_up")
				delete(w, "bytes_down")
			}
			flatInto(out, "ws_connection", m)
		case e.Phase == events.PhaseClientEvent && e.ClientEvent != nil:
			ce := *e.ClientEvent
			ce.Data = nil // the event's own fields; its payload is the client's
			evs = append(evs, ce)
		case e.Phase == events.PhaseDisconnect:
			out["disconnect.error"] = e.Error
		}
	}
	for i, e := range turns {
		flatInto(out, fmt.Sprintf("turn%d.row", i+1), rowMap(e))
	}
	if len(evs) > 0 {
		out["client_events"] = evs
	}
}

func rowMap(e events.Event) map[string]any {
	m := toMap(e)
	for k := range m {
		if rowSkip[k] {
			delete(m, k)
		}
	}
	return m
}

// observeRecords reads the records the raw files hold: each call_end's
// request row (what the workers load), its kind and detail.
func observeRecords(out map[string]any, recs []records.Record) {
	ends := map[string]canon.Request{}
	var order []string
	for _, r := range recs {
		if (r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection) && r.Call != nil {
			if _, ok := ends[r.CallID]; !ok {
				order = append(order, r.CallID)
			}
			ends[r.CallID] = *r.Call
		}
		if r.Kind == records.KindAccount && r.Account != nil {
			out["record.account"] = map[string]any{"account_id": r.Account.AccountID, "email": r.Account.Email,
				"name": r.Account.Name, "org": r.Account.Org}
		}
	}
	for _, id := range order {
		r := ends[id]
		prefix := "call"
		if r.WSTurn > 0 {
			prefix = fmt.Sprintf("turn%d.call", r.WSTurn)
		} else if r.Transport == events.TransportWebSocket {
			prefix = "ws_connection.call"
		}
		m := map[string]any{}
		rv := reflect.ValueOf(r)
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Type().Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("parquet"), ",")
			if callSkip[name] || rv.Field(i).IsZero() {
				continue
			}
			m[name] = rv.Field(i).Interface()
		}
		// Its client and provider maps are the complete row's, copied.
		for name, s := range map[string]string{"tool_uses": r.ToolUsesJSON, "ext": r.ExtJSON, "tools_offered": r.ToolsOffered} {
			if s != "" {
				var v any
				_ = json.Unmarshal([]byte(s), &v)
				m[name] = v
			}
		}
		m["kind"] = process.RequestKind(r.Path)
		if canon.IsModelCall(&r) {
			m["model_call"] = true
		}
		flatInto(out, prefix, m)
		if r.DetailJSON != "" {
			var d map[string]any
			_ = json.Unmarshal([]byte(r.DetailJSON), &d)
			flatInto(out, strings.TrimSuffix(prefix, "call")+"detail", d)
		}
	}
}

// observeConvo reads the request as the Interceptor's parser does: every
// message (role, parts' kinds and sizes) and which are new.
func observeConvo(out map[string]any, c *capture.Case) {
	var reqs []convo.Request
	var why []string
	if len(c.WS) > 0 {
		for _, m := range c.WS {
			if m.Dir == "up" {
				if r, ok := convo.ParseWebSocket(c.Request.Host, c.Request.Path, m.Msg); ok {
					reqs = append(reqs, r)
				}
			}
		}
	} else if len(c.ReqBody) > 0 {
		r, reason := convo.ParseReason(c.Request.Host, c.Request.Path, c.ReqBody)
		if reason == "" {
			reqs = append(reqs, r)
		} else if reason != "not a model endpoint" {
			why = append(why, reason)
		}
	}
	if len(why) > 0 {
		out["convo.unreadable"] = why[0]
	}
	for i, r := range reqs {
		prefix := "convo"
		if len(reqs) > 1 {
			prefix = fmt.Sprintf("turn%d.convo", i+1)
		}
		var msgs []any
		for _, m := range r.Messages {
			var parts []any
			for _, p := range m.Parts {
				pm := map[string]any{"kind": p.Kind, "size": p.Size}
				for k, v := range map[string]any{"name": p.Name, "call_id": p.CallID, "media_type": p.MediaType, "url": p.URL,
					"is_error": p.IsError, "ref": p.Ref} {
					if !reflect.ValueOf(v).IsZero() {
						pm[k] = v
					}
				}
				parts = append(parts, pm)
			}
			msgs = append(msgs, map[string]any{"role": m.Role, "parts": parts})
		}
		m := map[string]any{"format": r.Format, "messages": msgs, "new": r.New}
		if r.Chained {
			m["chained"], m["prev"] = true, r.Prev
		}
		flatInto(out, prefix, m)
	}
}

// observeInterceptor reads the callbacks the Interceptor got, in order.
func observeInterceptor(out map[string]any, f *interceptortest.Fake, starts []*pb2.CallStart, s interceptor.Stats) {
	calls, reqs, recs := f.Snapshot()
	out["interceptor.callbacks"] = calls
	if s.Unparsed > 0 {
		out["interceptor.unparsed"] = s.UnparsedReasons
	}
	if s.NoNewMessages > 0 {
		out["interceptor.no_new_messages"] = s.NoNewMessages
	}
	list := func(prefix string, ms []proto.Message, drop ...string) {
		for i, m := range ms {
			p := prefix
			if len(ms) > 1 {
				p = fmt.Sprintf("%s.%d", prefix, i+1)
			}
			v := protoMap(m)
			for _, d := range drop {
				dropPath(v, d)
			}
			flatInto(out, p, v)
		}
	}
	list("on_call_start", protos(starts), "call.call_id", "call.start_unix_nano", "headers", "request.request_bytes")
	// A turn's call is its connection's (OnCallStart's).
	list("on_turn_start", protos(f.TurnsSeen()), "call", "request.request_bytes")
	list("on_request", protos(reqs), "call")
	list("on_tool_call", protos(f.ToolCallsSeen()), "call")
	list("on_reply", protos(f.RepliesSeen()), "call")
	// A record's client headers are OnCallStart's.
	list("on_call_complete", protos(recs), "call.call_id", "call.start_unix_nano", "call.client.headers", "end_unix_nano",
		"request.request_bytes", "response.response_bytes", "response.ttfb_ms", "response.duration_ms")
}

func protos[T proto.Message](ms []T) []proto.Message {
	out := make([]proto.Message, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

func protoMap(m proto.Message) map[string]any {
	b, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v
}

func dropPath(m map[string]any, path string) {
	head, rest, more := strings.Cut(path, ".")
	if !more {
		delete(m, head)
		return
	}
	if sub, ok := m[head].(map[string]any); ok {
		dropPath(sub, rest)
	}
}

// observeLearned reads who the identity cache learned a credential belongs
// to: from its lookup, or the provider's own account response.
func observeLearned(out map[string]any, ls []identity.Learned) {
	for _, l := range ls {
		out["identity.learned"] = map[string]any{"how": l.How, "account_id": l.AccountID, "email": l.Email, "name": l.Name,
			"org_id": l.OrgID, "org": l.Org, "key_hint": l.KeyHint}
	}
}

// ── Flattening ───────────────────────────────────────────────────────────────

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// flatInto adds an object's fields under prefix: one field per key, and a
// nested object of plain values one field per key in turn.
func flatInto(out map[string]any, prefix string, m map[string]any) {
	for k, v := range m {
		if isZero(v) {
			continue
		}
		if sub, ok := v.(map[string]any); ok && !nested(sub) {
			for sk, sv := range sub {
				if !isZero(sv) {
					out[prefix+"."+k+"."+sk] = sv
				}
			}
			continue
		}
		if sub, ok := v.(map[string]any); ok && (k == "call" || k == "request" || k == "response" || k == "identity" || k == "client") {
			flatInto(out, prefix+"."+k, sub)
			continue
		}
		out[prefix+"."+k] = v
	}
}

func nested(m map[string]any) bool {
	for _, v := range m {
		switch v.(type) {
		case map[string]any, []any:
			return true
		}
	}
	return false
}

func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// shorten cuts long strings (placeholder prompt and reply text) to their
// start and their size, so expect.json stays readable.
func shorten(v any) any {
	v = normalize(v)
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if utf8.RuneCountInString(x) > 120 {
				r := []rune(x)
				return fmt.Sprintf("%s…(%d bytes)", string(r[:100]), len(x))
			}
			return x
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
		}
		return v
	}
	return walk(v)
}
