package records

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// --- row builders -------------------------------------------------------------------

var t1 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) int64 { return t1.Add(d).UnixNano() }

const kh = "kh1"

var ccHeaders = map[string]string{"x-claude-code-session-id": "s1", "user-agent": "claude-cli/2.1.3 (external, cli)"}
var ccClient = map[string]string{"x-claude-code-session-id": "s1", "x-claude-code-prompt-id": "p1", "user-agent": "claude-cli/2.1.3 (external, cli)"}

func hdr(id string, eos bool, h map[string]string) events.Event {
	return events.Event{CorrelationID: id, Seq: 1, TimestampNS: at(0), Phase: events.PhaseRequestHeaders, Direction: events.DirInbound,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages?beta=true", KeyHash: kh, IP: "10.0.0.1",
		Headers: h, EndOfStream: eos}
}

func reqChunk(id string, n int, eos bool) events.Event {
	return events.Event{CorrelationID: id, Seq: 2, TimestampNS: at(time.Millisecond), Phase: events.PhaseRequestBodyChunk,
		Direction: events.DirInbound, Bytes: n, EndOfStream: eos}
}

func respHdr(id, reqID string) events.Event {
	return events.Event{CorrelationID: id, Seq: 3, TimestampNS: at(800 * time.Millisecond), Phase: events.PhaseResponseHeaders,
		Direction: events.DirOutbound, Status: 200, Headers: map[string]string{"request-id": reqID}}
}

func respChunk(id string, n int) events.Event {
	return events.Event{CorrelationID: id, Seq: 4, TimestampNS: at(900 * time.Millisecond), Phase: events.PhaseResponseBodyChunk,
		Direction: events.DirOutbound, Bytes: n}
}

func complete(id string, client map[string]string) events.Event {
	return events.Event{CorrelationID: id, Seq: 5, TimestampNS: at(5 * time.Second), Phase: events.PhaseComplete,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages?beta=true", Status: 200, KeyHash: kh,
		RequestStartNS: at(0), ResponseStartNS: at(800 * time.Millisecond), Model: "claude-opus-5-5", StopReason: "end_turn",
		InputTokens: 100, OutputTokens: 10, AccountUUID: "acct-1", DeviceID: "dev-1", SessionID: "s1", Client: client,
		Provider: map[string]string{"request-id": "req_1"}}
}

func disconnect(id string) events.Event {
	return events.Event{CorrelationID: id, Seq: 6, TimestampNS: at(3 * time.Second), Phase: events.PhaseDisconnect, Error: "context canceled"}
}

func wsHdr(id string) events.Event {
	return events.Event{CorrelationID: id, Seq: 1, TimestampNS: at(0), Phase: events.PhaseRequestHeaders, Direction: events.DirInbound,
		Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses", KeyHash: kh,
		Headers: map[string]string{"upgrade": "websocket", "originator": "codex_cli_rs", "user-agent": "codex_cli_rs/0.164.0"}, EndOfStream: true}
}

var codexClient = map[string]string{"originator": "codex_cli_rs", "user-agent": "codex_cli_rs/0.164.0"}

func wsTurnStart(id string, turn int, thread string) events.Event {
	return events.Event{CorrelationID: id, Seq: uint64(10 + turn), TimestampNS: at(time.Duration(turn) * time.Minute), Phase: events.PhaseWSTurnStart,
		Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses", KeyHash: kh, Transport: events.TransportWebSocket,
		WSTurn: turn, Client: codexClient, ClientMetadata: map[string]any{"thread_id": thread, "turn_id": "u" + string(rune('0'+turn))}}
}

func wsTurn(id string, turn int, thread string) events.Event {
	e := wsTurnStart(id, turn, thread)
	e.Phase, e.Seq, e.TimestampNS = events.PhaseComplete, uint64(20+turn), at(time.Duration(turn)*time.Minute+30*time.Second)
	e.Model, e.OutputTokens = "gpt-6.1-sol", 7
	return e
}

func wsConn(id string) events.Event {
	return events.Event{CorrelationID: id, Seq: 99, TimestampNS: at(time.Hour), Phase: events.PhaseWSConnection,
		Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses", KeyHash: kh, Transport: events.TransportWebSocket}
}

func analytics(id string) []events.Event {
	h := hdr(id, false, nil)
	h.Path = "/api/event_logging/v2/batch"
	c := events.Event{CorrelationID: id, Seq: 5, TimestampNS: at(6 * time.Second), Phase: events.PhaseComplete,
		Host: "api.anthropic.com", Method: "POST", Path: "/api/event_logging/v2/batch", Status: 200, KeyHash: kh,
		RequestStartNS: at(0)}
	return []events.Event{h, reqChunk(id, 300, true), c}
}

func clientEvent(id, evID, session string, ts time.Duration) events.Event {
	return events.Event{CorrelationID: id, Seq: 5, TimestampNS: at(6 * time.Second), Phase: events.PhaseClientEvent,
		Host: "api.anthropic.com", Path: "/api/event_logging/v2/batch", KeyHash: kh,
		ClientEvent: &events.ClientEvent{Source: "claude_code", Name: "tengu_api_success", ID: evID, SessionID: session,
			Timestamp: t1.Add(ts).Format(time.RFC3339Nano), AccountUUID: "acct-1",
			Data: map[string]any{"additional_metadata": map[string]any{"requestId": "req_1"}}}}
}

func line(e events.Event) []byte {
	b, _ := json.Marshal(e)
	return b
}

// fullModeCall is a model call's rows with its bodies recorded (full interception).
func fullModeCall() []events.Event {
	resp := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Running the tests.\"}}\n\n"
	return bodiesOf("f1", `{"messages":[{"role":"user","content":"hi"}]}`, resp)
}

func bodiesOf(id, req, resp string) []events.Event {
	q := reqChunk(id, len(req), true)
	q.BodyB64 = base64.StdEncoding.EncodeToString([]byte(req))
	r := respChunk(id, len(resp))
	r.BodyB64 = base64.StdEncoding.EncodeToString([]byte(resp))
	return []events.Event{hdr(id, false, ccHeaders), q, respHdr(id, "req_1"), r}
}

// --- atomic cases ---------------------------------------------------------------------

// want is one expected record: its kind, call id and routing key, plus any
// further check on its content.
type want struct {
	kind, callID, key string
	check             func(t *testing.T, r Record)
}

// An atomic case is one row applied to a known state (the prior rows), or a
// flush, and exactly the records it must produce. Every case label the
// assembler declares has one here (TestEveryCaseHasAnAtomicTest).
type atomicCase struct {
	label string
	prior []events.Event
	row   *events.Event // nil: Flush
	want  []want
	// flush also flushes after the row: an analytics batch's records come out
	// when it's released. only compares just the records of that kind.
	flush bool
	only  string
}

func row(e events.Event) *events.Event { return &e }

var day = "2026-11-12"

func atomicCases() []atomicCase {
	ana := analytics("a1")
	withAuth := hdr("c1", true, ccHeaders)
	withAuth.Auth = &events.AuthIdentity{AccountID: "acct-9", Email: "x@example.com", Name: "X"}
	codexHTTP := hdr("cx", true, map[string]string{"originator": "codex_exec", "user-agent": "codex_exec/0.164.0"})
	codexHTTP.Host, codexHTTP.Path = "chatgpt.com", "/backend-api/codex/responses"
	codexEnd := complete("cx", map[string]string{"originator": "codex_exec", "user-agent": "codex_exec/0.164.0"})
	codexEnd.Host, codexEnd.Path, codexEnd.SessionID = "chatgpt.com", "/backend-api/codex/responses", ""
	codexEnd.ClientMetadata = map[string]any{"thread_id": "t9", "turn_id": "u9"}
	codexHdrMD := hdr("cy", true, map[string]string{"originator": "codex_exec", "user-agent": "codex_exec/0.164.0",
		"x-codex-turn-metadata": `{"thread_id":"t8","turn_id":"u8"}`})
	codexHdrMD.Host, codexHdrMD.Path = "chatgpt.com", "/backend-api/codex/responses"
	noPayload := clientEvent("a1", "e1", "s1", 0)
	noPayload.ClientEvent = nil
	unknown := hdr("c1", false, nil)
	unknown.Phase = "something_new"
	verdict := hdr("c1", false, nil)
	verdict.Phase = events.PhaseVerdict

	return []atomicCase{
		{label: CasePendingHeaders, row: row(hdr("c1", false, ccHeaders))},
		{label: CaseStartHeadersEOS, row: row(hdr("c1", true, ccHeaders)), want: []want{{KindCallStart, "c1", "s:s1", func(t *testing.T, r Record) {
			if r.Call.SessionID != "s1" || r.Call.Product != "claude_code" || r.Call.EndNS != 0 {
				t.Errorf("call_start: %+v", r.Call)
			}
		}}}},
		{label: CaseStartHeadersEOS + "/codex header", row: row(codexHdrMD), want: []want{{KindCallStart, "cy", "s:t8", nil}}},
		{label: CasePendingRequestChunk, prior: []events.Event{hdr("c1", false, ccHeaders)}, row: row(reqChunk("c1", 40, false))},
		{label: CaseStartBodyEOS, prior: []events.Event{hdr("c1", false, ccHeaders), reqChunk("c1", 40, false)}, row: row(reqChunk("c1", 60, true)),
			want: []want{{KindCallStart, "c1", "s:s1", func(t *testing.T, r Record) {
				if r.Call.RequestBytes != 100 {
					t.Errorf("request bytes %d", r.Call.RequestBytes)
				}
			}}}},
		{label: CasePendingResponseHeaders, prior: []events.Event{hdr("c1", true, ccHeaders)}, row: row(respHdr("c1", "req_1"))},
		{label: CasePendingResponseChunk, prior: []events.Event{hdr("c1", true, ccHeaders), respHdr("c1", "req_1")}, row: row(respChunk("c1", 50))},
		{label: CaseEndComplete, prior: []events.Event{hdr("c1", true, ccHeaders), respHdr("c1", "req_1"), respChunk("c1", 50)},
			row: row(complete("c1", ccClient)), want: []want{{KindCallEnd, "c1", "s:s1", func(t *testing.T, r Record) {
				c := r.Call
				if c.Model != "claude-opus-5-5" || c.Status != 200 || c.RequestID != "req_1" || c.ResponseBytes != 50 ||
					c.PromptID != "p1" || c.PersonID != "acct-1" || c.EndNS != at(5*time.Second) {
					t.Errorf("call_end: %+v", c)
				}
			}}}},
		{label: CaseEndCompleteNoHeaders, row: row(complete("c1", ccClient)), want: []want{{KindCallEnd, "c1", "s:s1", nil}}},
		{label: CaseStartWSUpgrade, row: row(func() events.Event { h := wsHdr("w3"); h.EndOfStream = false; return h }()),
			want: []want{{KindCallStart, "w3", "c:" + kh + ":" + day, func(t *testing.T, r Record) {
				if r.Call.Transport != events.TransportWebSocket {
					t.Errorf("upgrade start: %+v", r.Call)
				}
			}}}},
		{label: CaseStartWSTurn, prior: []events.Event{wsHdr("w1")}, row: row(wsTurnStart("w1", 1, "t1")),
			want: []want{{KindCallStart, "w1#1", "s:t1", func(t *testing.T, r Record) {
				if r.Call.WSTurn != 1 || r.Call.Transport != events.TransportWebSocket || r.Call.PromptID != "u1" {
					t.Errorf("turn start: %+v", r.Call)
				}
			}}}},
		{label: CaseEndWSTurn, prior: []events.Event{wsHdr("w1"), wsTurnStart("w1", 1, "t1")}, row: row(wsTurn("w1", 1, "t1")),
			want: []want{{KindCallEnd, "w1#1", "s:t1", func(t *testing.T, r Record) {
				if r.Call.Model != "gpt-6.1-sol" || r.Call.OutputTokens != 7 {
					t.Errorf("turn end: %+v", r.Call)
				}
			}}}},
		{label: CaseEndWSConnection, prior: []events.Event{wsHdr("w1"), wsTurn("w1", 1, "t1")}, row: row(wsConn("w1")),
			want: []want{{KindWSConnection, "w1", "c:" + kh + ":" + day, nil}}},
		{label: CaseEndDisconnect, prior: []events.Event{hdr("c1", true, ccHeaders)}, row: row(disconnect("c1")),
			want: []want{{KindCallEnd, "c1", "s:s1", func(t *testing.T, r Record) {
				if r.Call.Error != "context canceled" || r.Call.EndNS != at(3*time.Second) {
					t.Errorf("disconnect: %+v", r.Call)
				}
			}}}},
		{label: CaseEndDisconnectEarly, row: row(disconnect("c1"))},
		{label: CaseEndDisconnectThenHeaders, prior: []events.Event{disconnect("c1")}, row: row(hdr("c1", true, ccHeaders)),
			want: []want{{KindCallEnd, "c1", "s:s1", func(t *testing.T, r Record) {
				if r.Call.Error == "" {
					t.Errorf("want the early disconnect's error: %+v", r.Call)
				}
			}}}},
		{label: CaseLateAfterDone, prior: []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)}, row: row(respChunk("c1", 9))},
		{label: CaseEndSplitKey, prior: []events.Event{codexHTTP}, row: row(codexEnd), want: []want{
			{KindCallEnd, "cx", "s:t9", nil},
			{KindCallEnd, "cx", "c:" + kh + ":" + day, nil},
		}},
		{flush: true, only: KindClientEvent, label: CaseEventWithID, prior: ana, row: row(clientEvent("a1", "e1", "s1", 0)),
			want: []want{{KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if r.BatchID != "a1" || r.BatchN != 1 || r.Event.EventKey != "a1/e1" || r.EventKey != r.RecordID {
					t.Errorf("event: %+v / %+v", r, r.Event)
				}
			}}}},
		{flush: true, only: KindClientEvent, label: CaseEventNoID, prior: ana, row: row(clientEvent("a1", "", "s1", 0)),
			want: []want{{KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if !strings.HasPrefix(r.Event.EventKey, "a1/h") || strings.Count(r.Event.EventKey, "-") != 0 {
					t.Errorf("hashed key %q", r.Event.EventKey)
				}
			}}}},
		{flush: true, only: KindClientEvent, label: CaseEventRepeat, prior: append(append([]events.Event{}, ana...), clientEvent("a1", "", "s1", 0)), row: row(clientEvent("a1", "", "s1", 0)),
			want: []want{{KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if strings.HasSuffix(r.Event.EventKey, "-2") || r.BatchN != 1 {
					t.Errorf("first: key %q n %d", r.Event.EventKey, r.BatchN)
				}
			}}, {KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if !strings.HasSuffix(r.Event.EventKey, "-2") || r.BatchN != 2 {
					t.Errorf("repeat: key %q n %d", r.Event.EventKey, r.BatchN)
				}
			}}}},
		{label: CaseEventNoPayload, prior: ana, row: row(noPayload)},
		{only: KindCallEnd, label: CaseBatchSession, prior: append(append([]events.Event{}, ana...),
			clientEvent("a1", "e1", "s2", 5*time.Second), clientEvent("a1", "e2", "s1", 3*time.Second)),
			row: row(hdr("c2", false, ccHeaders)),
			// Its start named no session (the batch hadn't been read): its end goes
			// to the session and to the start's credential key, which closes it there.
			want: []want{{KindCallEnd, "a1", "s:s1", func(t *testing.T, r Record) {
				if r.BatchSession != "s1" || r.BatchID != "a1" {
					t.Errorf("batch call: session %q id %q", r.BatchSession, r.BatchID)
				}
			}}, {KindCallEnd, "a1", "c:" + kh + ":" + day, nil}}},
		{only: KindCallEnd, label: CaseBatchNoSession, prior: append(append([]events.Event{}, ana...), clientEvent("a1", "e1", "", 0)),
			want: []want{{KindCallEnd, "a1", "c:" + kh + ":" + day, nil}}},
		{label: CaseAccountSighting, row: row(withAuth), want: []want{
			{KindAccount, "", "a:acct-9", func(t *testing.T, r Record) {
				if r.Account.Email != "x@example.com" || r.Account.Name != "X" || r.Account.NS != at(0) {
					t.Errorf("account: %+v", r.Account)
				}
			}},
			{KindCallStart, "c1", "s:s1", nil},
		}},
		{label: CaseEndWSConnection + "/no headers seen", row: row(wsConn("w2")), want: []want{{KindWSConnection, "w2", "c:" + kh + ":" + day, nil}}},
		{label: CaseEndWSTurn + "/after the connection closed", prior: []events.Event{wsHdr("w1"), wsConn("w1")}, row: row(wsTurn("w1", 2, "t1")),
			want: []want{{KindCallEnd, "w1#2", "s:t1", nil}}},
		{flush: true, only: KindClientEvent, label: CaseEventWithID + "/no request seen", row: row(clientEvent("a9", "e9", "s1", 0)), want: []want{{KindClientEvent, "", "s:s1", nil}}},
		{label: CaseEndComplete + "/recorded bodies", prior: fullModeCall(), row: row(complete("f1", ccClient)),
			want: []want{{KindCallEnd, "f1", "s:s1", func(t *testing.T, r Record) {
				if !strings.Contains(r.Call.DetailJSON, "Running the tests.") || r.Call.RequestBytes == 0 {
					t.Errorf("detail from the recorded bodies: %q", r.Call.DetailJSON)
				}
			}}}},
		{label: CaseEndComplete + "/bodies that show nothing", prior: bodiesOf("f2", `{}`, "x"), row: row(complete("f2", ccClient)),
			want: []want{{KindCallEnd, "f2", "s:s1", func(t *testing.T, r Record) {
				if r.Call.DetailJSON != "" {
					t.Errorf("detail %q", r.Call.DetailJSON)
				}
			}}}},
		{label: CasePendingRequestChunk + "/a body that isn't base64", prior: []events.Event{hdr("f3", false, ccHeaders)},
			row: row(func() events.Event { e := reqChunk("f3", 3, false); e.BodyB64 = "%%%"; return e }())},
		{label: CasePendingRequestChunk + "/a body that isn't a model call's", prior: []events.Event{func() events.Event {
			h := hdr("f4", false, ccHeaders)
			h.Path = "/api/oauth/profile"
			return h
		}()}, row: row(func() events.Event { e := reqChunk("f4", 3, false); e.BodyB64 = "YWJj"; return e }())},
		{label: CaseVerdict, row: row(verdict)},
		{label: CaseUnknownPhase, row: row(unknown)},
	}
}

func TestAtomicCases(t *testing.T) {
	for _, c := range atomicCases() {
		t.Run(c.label, func(t *testing.T) {
			a := NewAssembler("inc")
			for i := range c.prior {
				a.Apply(&c.prior[i], line(c.prior[i]), "f")
			}
			before := a.Cases()
			var got []Record
			if c.row == nil {
				got = a.Flush()
			} else {
				got = a.Apply(c.row, line(*c.row), "f")
				if c.flush {
					got = append(got, a.Flush()...)
				}
			}
			if c.only != "" {
				var kept []Record
				for _, r := range got {
					if r.Kind == c.only {
						kept = append(kept, r)
					}
				}
				got = kept
			}
			label, _, _ := strings.Cut(c.label, "/")
			if a.Cases()[label] <= before[label] {
				t.Errorf("case %s didn't fire (histogram %v)", label, a.Cases())
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d records, want %d: %s", len(got), len(c.want), summary(got))
			}
			for i, w := range c.want {
				r := got[i]
				if r.Kind != w.kind || r.CallID != w.callID || r.RoutingKey != w.key {
					t.Fatalf("record %d: %s %s %s, want %s %s %s", i, r.Kind, r.CallID, r.RoutingKey, w.kind, w.callID, w.key)
				}
				if r.Bucket != int32(BucketOf(r.RoutingKey, DefaultBucketBits)) || r.Version != Version || r.RecordID == "" {
					t.Fatalf("record %d header: %+v", i, r)
				}
				if w.check != nil {
					w.check(t, r)
				}
			}
		})
	}
}

func summary(rs []Record) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.Kind+":"+r.CallID+":"+r.RoutingKey)
	}
	return strings.Join(s, ", ")
}

func TestEveryCaseHasAnAtomicTest(t *testing.T) {
	tested := map[string]bool{}
	for _, c := range atomicCases() {
		label, _, _ := strings.Cut(c.label, "/")
		tested[label] = true
	}
	for _, c := range AllCases {
		if !tested[c] {
			t.Errorf("case %q has no atomic test", c)
		}
	}
	if len(AllCases) == 0 {
		t.Fatal("no cases declared")
	}
}

// --- short sequences: every interleaving of a few streams -------------------------------

// Rows of one stream keep their order (one ext_proc stream, one goroutine), but
// streams interleave arbitrarily. Every interleaving must give the same records.
func TestInterleavingsGiveTheSameRecords(t *testing.T) {
	one := func(rows ...events.Event) [][]events.Event {
		var out [][]events.Event
		for _, r := range rows {
			out = append(out, []events.Event{r})
		}
		return out
	}
	ana := analytics("a1")
	streams := [][][]events.Event{
		one(hdr("c1", false, ccHeaders), reqChunk("c1", 10, true), complete("c1", ccClient)),
		one(wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"), wsConn("w1")),
		// A batch's events are emitted right after its request's row, by the
		// same drain goroutine: nothing comes between them.
		append(one(ana[0], ana[1]), []events.Event{ana[2], clientEvent("a1", "e1", "s1", 0), clientEvent("a1", "e2", "s2", 0)}),
	}
	lines := map[string][]byte{} // each row's line, marshalled once
	for _, st := range streams {
		for _, step := range st {
			for _, r := range step {
				lines[rowKey(r)] = line(r)
			}
		}
	}
	var want []string
	n := 0
	interleave(streams, nil, func(rows []events.Event) {
		n++
		a := NewAssembler("inc")
		var got []Record
		for i := range rows {
			r := rows[i]
			got = append(got, a.Apply(&r, lines[rowKey(r)], "f")...)
		}
		got = append(got, a.Flush()...)
		s := canonicalSet(got)
		if want == nil {
			want = s
			return
		}
		if strings.Join(s, "\n") != strings.Join(want, "\n") {
			t.Fatalf("interleaving %d differs:\n%s\nwant\n%s", n, strings.Join(s, "\n"), strings.Join(want, "\n"))
		}
	})
	if n != 4200 { // 10! / (3! 4! 3!)
		t.Fatalf("%d interleavings", n)
	}
}

// rowKey names a test row (its stream, sequence, phase and event).
func rowKey(r events.Event) string {
	k := r.CorrelationID + "/" + strconv.FormatUint(r.Seq, 10) + "/" + string(r.Phase)
	if r.ClientEvent != nil {
		k += "/" + r.ClientEvent.ID
	}
	return k
}

// interleave calls f with every interleaving of the streams' steps (a step is
// one row, or rows that are always emitted together), each stream in order.
func interleave(streams [][][]events.Event, prefix []events.Event, f func([]events.Event)) {
	done := true
	for i, s := range streams {
		if len(s) == 0 {
			continue
		}
		done = false
		rest := append([][][]events.Event{}, streams...)
		rest[i] = s[1:]
		interleave(rest, append(append([]events.Event{}, prefix...), s[0]...), f)
	}
	if done {
		f(prefix)
	}
}

// canonicalSet is records as comparable strings, without what depends on
// emission order (record ids).
func canonicalSet(rs []Record) []string {
	var out []string
	for _, r := range rs {
		r.RecordID, r.EventKey = "", ""
		b, _ := json.Marshal(r)
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

// --- where a call's session is known: at its start? ----------------------------------

// Each client names its session at the call's start, so the call_start and
// call_end route to the same key. Codex HTTP without its turn metadata header
// is the exception: the end is written to both keys (CaseEndSplitKey).
func TestSessionIsKnownAtTheStart(t *testing.T) {
	codexHdr := hdr("cy", true, map[string]string{"originator": "codex_exec", "user-agent": "codex_exec/0.164.0",
		"x-codex-turn-metadata": `{"thread_id":"t8","turn_id":"u8"}`})
	codexHdr.Host, codexHdr.Path = "chatgpt.com", "/backend-api/codex/responses"
	codexEnd := complete("cy", map[string]string{"originator": "codex_exec", "user-agent": "codex_exec/0.164.0"})
	codexEnd.Host, codexEnd.Path, codexEnd.SessionID = "chatgpt.com", "/backend-api/codex/responses", ""
	codexEnd.ClientMetadata = map[string]any{"thread_id": "t8", "turn_id": "u8"}
	chat := hdr("ch", true, nil)
	chat.Host, chat.Path = "claude.ai", "/api/organizations/o/chat_conversations/c/completion"
	chatEnd := complete("ch", nil)
	chatEnd.Host, chatEnd.Path, chatEnd.SessionID = "claude.ai", chat.Path, ""
	for name, rows := range map[string][]events.Event{
		"claude code":    {hdr("c1", true, ccHeaders), complete("c1", ccClient)},
		"codex http":     {codexHdr, codexEnd},
		"codex ws turn":  {wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1")},
		"claude.ai chat": {chat, chatEnd},
	} {
		a := NewAssembler("inc")
		var recs []Record
		for i := range rows {
			recs = append(recs, a.Apply(&rows[i], line(rows[i]), "f")...)
		}
		recs = append(recs, a.Flush()...)
		start, end := map[string]string{}, map[string][]string{}
		for _, r := range recs {
			switch r.Kind {
			case KindCallStart:
				start[r.CallID] = r.RoutingKey
			case KindCallEnd:
				end[r.CallID] = append(end[r.CallID], r.RoutingKey)
			}
		}
		checked := 0
		for id, keys := range end {
			k, ok := start[id]
			if !ok {
				t.Errorf("%s: %s ended without a start", name, id)
				continue
			}
			checked++
			if len(keys) != 1 || keys[0] != k {
				t.Errorf("%s: %s started under %s, ended under %v", name, id, k, keys)
			}
		}
		if checked == 0 {
			t.Errorf("%s: no call checked", name)
		}
	}
}

// A body past the cap stops growing; the call's detail is from what came before.
func TestBodiesStopAtTheCap(t *testing.T) {
	a := NewAssembler("inc")
	h := hdr("b1", false, ccHeaders)
	a.Apply(&h, line(h), "f")
	big := reqChunk("b1", 1, false)
	big.BodyB64 = base64.StdEncoding.EncodeToString(make([]byte, maxCallBody))
	a.Apply(&big, nil, "f")
	more := reqChunk("b1", 1, true)
	more.BodyB64 = base64.StdEncoding.EncodeToString([]byte("x"))
	a.Apply(&more, nil, "f")
	if n := len(a.calls["b1"].bodies.req); n != maxCallBody {
		t.Fatalf("held %d bytes, want the cap %d", n, maxCallBody)
	}
}

// Calls are forgotten once past their time, in data time: an ended call's
// marker after doneTTL, an open HTTP call after pendingTTL, a WebSocket after
// pendingTTLWS. Memory follows the calls in flight, not history.
func TestSweepForgetsCallsPastTheirTime(t *testing.T) {
	a := NewAssembler("inc")
	open, ended, ws := hdr("open", false, ccHeaders), hdr("ended", true, ccHeaders), wsHdr("ws")
	end := complete("ended", ccClient)
	for _, e := range []events.Event{open, ended, end, ws} {
		a.Apply(&e, line(e), "f")
	}
	tick := func(d time.Duration) {
		for i := 0; i < sweepEvery; i++ {
			e := events.Event{CorrelationID: "tick", Phase: events.PhaseVerdict, TimestampNS: at(d)}
			a.Apply(&e, nil, "f")
		}
	}
	tick(time.Hour)
	if a.calls["ended"] != nil || a.calls["open"] == nil || a.calls["ws"] == nil {
		t.Fatalf("after an hour: ended %v open %v ws %v", a.calls["ended"] != nil, a.calls["open"] != nil, a.calls["ws"] != nil)
	}
	tick(7 * time.Hour)
	if a.calls["open"] != nil || a.calls["ws"] == nil {
		t.Fatalf("after 7 hours: open %v ws %v", a.calls["open"] != nil, a.calls["ws"] != nil)
	}
	tick(8 * 24 * time.Hour)
	if a.calls["ws"] != nil {
		t.Fatal("a WebSocket past a week is forgotten")
	}
}

// A held analytics request survives a sweep: it's released, not forgotten.
func TestSweepKeepsTheHeldCall(t *testing.T) {
	a := NewAssembler("inc")
	for _, e := range analytics("a1") {
		a.Apply(&e, line(e), "f")
	}
	a.newestNS = at(30 * 24 * time.Hour)
	a.sweep()
	if rs := a.Flush(); len(rs) != 1 || rs[0].CallID != "a1" {
		t.Fatalf("held call lost: %s", summary(rs))
	}
}
