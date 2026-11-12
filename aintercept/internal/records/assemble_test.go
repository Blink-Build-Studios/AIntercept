package records

import (
	"encoding/json"
	"sort"
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
		{label: CaseEventWithID, prior: ana, row: row(clientEvent("a1", "e1", "s1", 0)),
			want: []want{{KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if r.BatchID != "a1" || r.BatchN != 1 || r.Event.EventKey != "a1/e1" || r.EventKey != r.RecordID {
					t.Errorf("event: %+v / %+v", r, r.Event)
				}
			}}}},
		{label: CaseEventNoID, prior: ana, row: row(clientEvent("a1", "", "s1", 0)),
			want: []want{{KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if !strings.HasPrefix(r.Event.EventKey, "a1/h") || strings.Count(r.Event.EventKey, "-") != 0 {
					t.Errorf("hashed key %q", r.Event.EventKey)
				}
			}}}},
		{label: CaseEventRepeat, prior: append(append([]events.Event{}, ana...), clientEvent("a1", "", "s1", 0)), row: row(clientEvent("a1", "", "s1", 0)),
			want: []want{{KindClientEvent, "", "s:s1", func(t *testing.T, r Record) {
				if !strings.HasSuffix(r.Event.EventKey, "-2") || r.BatchN != 2 {
					t.Errorf("repeat: key %q n %d", r.Event.EventKey, r.BatchN)
				}
			}}}},
		{label: CaseEventNoPayload, prior: ana, row: row(noPayload)},
		{label: CaseBatchSession, prior: append(append([]events.Event{}, ana...),
			clientEvent("a1", "e1", "s2", 5*time.Second), clientEvent("a1", "e2", "s1", 3*time.Second)),
			row: row(hdr("c2", false, ccHeaders)),
			want: []want{{KindCallEnd, "a1", "s:s1", func(t *testing.T, r Record) {
				if r.BatchSession != "s1" || r.BatchID != "a1" {
					t.Errorf("batch call: session %q id %q", r.BatchSession, r.BatchID)
				}
			}}}},
		{label: CaseBatchNoSession, prior: append(append([]events.Event{}, ana...), clientEvent("a1", "e1", "", 0)),
			want: []want{{KindCallEnd, "a1", "c:" + kh + ":" + day, nil}}},
		{label: CaseAccountSighting, row: row(withAuth), want: []want{
			{KindAccount, "", "a:acct-9", func(t *testing.T, r Record) {
				if r.Account.Email != "x@example.com" || r.Account.Name != "X" || r.Account.NS != at(0) {
					t.Errorf("account: %+v", r.Account)
				}
			}},
			{KindCallStart, "c1", "s:s1", nil},
		}},
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
	streams := [][]events.Event{
		{hdr("c1", false, ccHeaders), reqChunk("c1", 10, true), complete("c1", ccClient)},
		{wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"), wsConn("w1")},
		append(analytics("a1"), clientEvent("a1", "e1", "s1", 0)),
		{hdr("c2", true, ccHeaders), disconnect("c2")},
	}
	var want []string
	n := 0
	interleave(streams, nil, func(rows []events.Event) {
		n++
		a := NewAssembler("inc")
		var got []Record
		for i := range rows {
			got = append(got, a.Apply(&rows[i], line(rows[i]), "f")...)
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
	if n < 1000 {
		t.Fatalf("only %d interleavings", n)
	}
}

func interleave(streams [][]events.Event, prefix []events.Event, f func([]events.Event)) {
	done := true
	for i, s := range streams {
		if len(s) == 0 {
			continue
		}
		done = false
		rest := append([][]events.Event{}, streams...)
		rest[i] = s[1:]
		interleave(rest, append(append([]events.Event{}, prefix...), s[0]), f)
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
		keys := map[string]map[string]bool{}
		for i := range rows {
			for _, r := range a.Apply(&rows[i], line(rows[i]), "f") {
				if r.Kind == KindCallStart || r.Kind == KindCallEnd {
					if keys[r.Kind] == nil {
						keys[r.Kind] = map[string]bool{}
					}
					keys[r.Kind][r.RoutingKey] = true
				}
			}
		}
		for _, r := range a.Flush() {
			if r.Kind == KindCallEnd {
				if keys[r.Kind] == nil {
					keys[r.Kind] = map[string]bool{}
				}
				keys[r.Kind][r.RoutingKey] = true
			}
		}
		if len(keys[KindCallStart]) != 1 || len(keys[KindCallEnd]) != 1 {
			t.Errorf("%s: start keys %v, end keys %v", name, keys[KindCallStart], keys[KindCallEnd])
			continue
		}
		for k := range keys[KindCallStart] {
			if !keys[KindCallEnd][k] {
				t.Errorf("%s: start key %s, end keys %v", name, k, keys[KindCallEnd])
			}
		}
	}
}
