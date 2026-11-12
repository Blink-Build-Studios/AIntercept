package records_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Long generated sequences: many streams of every kind, interleaved at random
// (each stream's own rows in order), must give exactly the request and event
// rows today's processor shapes from the same rows. The processor is the
// single-pass reference.
func TestGeneratedSequencesMatchTheProcessor(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		rows := generate(rand.New(rand.NewSource(seed)))
		var raw []process.RawRow
		a := records.NewAssembler("inc")
		var got []records.Record
		for i := range rows {
			l, _ := json.Marshal(rows[i])
			raw = append(raw, process.RawRow{Event: rows[i], Line: l, File: "f"})
			got = append(got, a.Apply(&rows[i], l, "f")...)
		}
		got = append(got, a.Flush()...)
		wantReq, wantEv := process.Shape(raw)
		gotReq, gotEv := callsAndEvents(got)
		compare(t, seed, "requests", keyed(wantReq, func(r canon.Request) string { return r.CallID }), keyed(gotReq, func(r canon.Request) string { return r.CallID }))
		compare(t, seed, "events", keyed(wantEv, func(e canon.Event) string { return e.EventKey }), keyed(gotEv, func(e canon.Event) string { return e.EventKey }))
	}
}

// callsAndEvents is the records' request rows (one per call: a call written to
// two keys is one row) and event rows, without the raw-file provenance the
// processor names and a live observer can't.
func callsAndEvents(rs []records.Record) ([]canon.Request, []canon.Event) {
	var reqs []canon.Request
	var evs []canon.Event
	seen := map[string]bool{}
	for _, r := range rs {
		switch r.Kind {
		case records.KindCallEnd, records.KindWSConnection:
			if !seen[r.CallID] {
				seen[r.CallID] = true
				reqs = append(reqs, *r.Call)
			}
		case records.KindClientEvent:
			evs = append(evs, *r.Event)
		}
	}
	return reqs, evs
}

func keyed[T any](xs []T, key func(T) string) map[string]T {
	m := map[string]T{}
	for _, x := range xs {
		m[key(x)] = x
	}
	return m
}

func compare[T any](t *testing.T, seed int64, what string, want, got map[string]T) {
	t.Helper()
	strip := func(v T) T {
		switch x := any(&v).(type) {
		case *canon.Request:
			x.SourceFile, x.FirstFile = "", ""
		case *canon.Event:
			x.SourceFile = ""
		}
		return v
	}
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	bad := 0
	for _, k := range sorted {
		w, okW := want[k]
		g, okG := got[k]
		if okW != okG || !reflect.DeepEqual(strip(w), strip(g)) {
			bad++
			if bad <= 3 {
				t.Errorf("seed %d %s %s:\n got %+v\nwant %+v", seed, what, k, strip(g), strip(w))
			}
		}
	}
	if bad > 0 {
		t.Errorf("seed %d %s: %d of %d differ", seed, what, bad, len(sorted))
	}
	if len(want) == 0 {
		t.Errorf("seed %d %s: nothing generated", seed, what)
	}
}

// generate is a random mix of the streams a pod sees, interleaved.
func generate(r *rand.Rand) []events.Event {
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	ns := func(d time.Duration) int64 { return t0.Add(d).UnixNano() }
	var streams [][][]events.Event // each stream's steps: a row, or rows always emitted together
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("pod-%d-%d", r.Intn(3), i)
		start := time.Duration(r.Intn(3600)) * time.Second
		sess := fmt.Sprintf("s%d", r.Intn(5))
		kh := fmt.Sprintf("kh%d", r.Intn(3))
		var s [][]events.Event
		seq := uint64(0)
		stamp := func(e events.Event) events.Event {
			seq++
			e.CorrelationID, e.Seq = id, seq
			if e.TimestampNS == 0 {
				e.TimestampNS = ns(start + time.Duration(seq)*time.Second)
			}
			return e
		}
		add := func(e events.Event) { s = append(s, []events.Event{stamp(e)}) }
		with := func(e events.Event) { // emitted with the step before it (a batch's events, after its request)
			s[len(s)-1] = append(s[len(s)-1], stamp(e))
		}
		switch r.Intn(5) {
		case 0, 1: // a Claude Code model call
			client := map[string]string{"x-claude-code-session-id": sess, "x-claude-code-prompt-id": fmt.Sprintf("p%d", r.Intn(4)),
				"user-agent": "claude-cli/2.1.3 (external, cli)"}
			body := r.Intn(2) == 0
			add(events.Event{Phase: events.PhaseRequestHeaders, Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages",
				KeyHash: kh, IP: "10.0.0.1", Headers: map[string]string{"x-claude-code-session-id": sess, "user-agent": client["user-agent"]},
				EndOfStream: !body})
			if body {
				for k := r.Intn(3); k >= 0; k-- {
					add(events.Event{Phase: events.PhaseRequestBodyChunk, Direction: events.DirInbound, Bytes: 100 + r.Intn(100), EndOfStream: k == 0})
				}
			}
			switch r.Intn(4) {
			case 0: // dropped before the response
				add(events.Event{Phase: events.PhaseDisconnect, Error: "context canceled"})
				streams = append(streams, s)
				continue
			}
			add(events.Event{Phase: events.PhaseResponseHeaders, Status: 200, Headers: map[string]string{"request-id": "req_" + id}})
			for k := r.Intn(3); k > 0; k-- {
				add(events.Event{Phase: events.PhaseResponseBodyChunk, Direction: events.DirOutbound, Bytes: 10 + r.Intn(50)})
			}
			c := events.Event{Phase: events.PhaseComplete, Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Status: 200,
				KeyHash: kh, RequestStartNS: ns(start), Model: "claude-opus-5-5", OutputTokens: r.Intn(50), SessionID: sess,
				AccountUUID: "acct-" + kh, Client: client, Provider: map[string]string{"request-id": "req_" + id}}
			if r.Intn(5) == 0 {
				c.Auth = &events.AuthIdentity{AccountID: "acct-" + kh, Email: kh + "@example.com"}
			}
			add(c)
			if r.Intn(4) == 0 { // a disconnect after completing adds nothing
				add(events.Event{Phase: events.PhaseDisconnect, Error: "stream reset"})
			}
		case 2: // an analytics batch
			add(events.Event{Phase: events.PhaseRequestHeaders, Host: "api.anthropic.com", Method: "POST",
				Path: "/api/event_logging/v2/batch", KeyHash: kh})
			add(events.Event{Phase: events.PhaseRequestBodyChunk, Direction: events.DirInbound, Bytes: 500, EndOfStream: true})
			add(events.Event{Phase: events.PhaseComplete, Host: "api.anthropic.com", Method: "POST", Path: "/api/event_logging/v2/batch",
				Status: 200, KeyHash: kh, RequestStartNS: ns(start)})
			n := 1 + r.Intn(4)
			var last *events.ClientEvent
			for k := 0; k < n; k++ {
				ce := &events.ClientEvent{Source: "claude_code", Name: "tengu_api_success", SessionID: fmt.Sprintf("s%d", r.Intn(5)),
					Timestamp: t0.Add(start + time.Duration(r.Intn(60))*time.Second).Format(time.RFC3339Nano), AccountUUID: "acct-" + kh,
					Data: map[string]any{"additional_metadata": map[string]any{"requestId": fmt.Sprintf("req_x%d", r.Intn(9))}}}
				if r.Intn(2) == 0 {
					ce.ID = fmt.Sprintf("ev-%s-%d", id, k)
				}
				if last != nil && r.Intn(4) == 0 { // a client that sent the same event twice
					c := *last
					ce = &c
				}
				last = ce
				with(events.Event{Phase: events.PhaseClientEvent, Host: "api.anthropic.com", Path: "/api/event_logging/v2/batch",
					KeyHash: kh, TimestampNS: ns(start + 3*time.Second), ClientEvent: ce})
			}
		case 3: // a Codex WebSocket with turns
			add(events.Event{Phase: events.PhaseRequestHeaders, Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses",
				KeyHash: kh, Headers: map[string]string{"upgrade": "websocket", "originator": "codex_cli_rs"}, EndOfStream: true})
			client := map[string]string{"originator": "codex_cli_rs", "user-agent": "codex_cli_rs/0.164.0"}
			for turn := 1; turn <= 1+r.Intn(3); turn++ {
				md := map[string]any{"thread_id": "t" + sess, "turn_id": fmt.Sprintf("u%d", turn)}
				if r.Intn(5) > 0 {
					add(events.Event{Phase: events.PhaseWSTurnStart, Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses",
						KeyHash: kh, Transport: events.TransportWebSocket, WSTurn: turn, Client: client, ClientMetadata: md})
				}
				add(events.Event{Phase: events.PhaseComplete, Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/responses",
					KeyHash: kh, Transport: events.TransportWebSocket, WSTurn: turn, Client: client, ClientMetadata: md,
					Model: "gpt-6.1-sol", OutputTokens: 7})
			}
			if r.Intn(4) > 0 { // still open at the end: no connection row yet
				add(events.Event{Phase: events.PhaseWSConnection, Host: "chatgpt.com", KeyHash: kh, Transport: events.TransportWebSocket})
			}
		case 4: // anything else: a claude.ai page, a GET with no session
			add(events.Event{Phase: events.PhaseRequestHeaders, Host: "claude.ai", Method: "GET", Path: "/api/bootstrap",
				KeyHash: kh, EndOfStream: true})
			add(events.Event{Phase: events.PhaseResponseHeaders, Status: 200 + 100*r.Intn(3)})
			add(events.Event{Phase: events.PhaseComplete, Host: "claude.ai", Method: "GET", Path: "/api/bootstrap", Status: 200, KeyHash: kh})
		}
		streams = append(streams, s)
	}
	// Interleave, keeping each stream's order.
	var out []events.Event
	for {
		var live []int
		for i, s := range streams {
			if len(s) > 0 {
				live = append(live, i)
			}
		}
		if len(live) == 0 {
			return out
		}
		i := live[r.Intn(len(live))]
		out = append(out, streams[i][0]...)
		streams[i] = streams[i][1:]
	}
}

// Every generated batch's Claude Code events are ranked exactly as the event
// rows number them.
func TestGeneratedRanksMatchTheEventRows(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		rows := generate(rand.New(rand.NewSource(seed)))
		a := records.NewAssembler("inc")
		x := process.NewShaper()
		var got []records.Record
		for i := range rows {
			l, _ := json.Marshal(rows[i])
			x.Apply(&rows[i], l, "f")
			got = append(got, a.Apply(&rows[i], l, "f")...)
		}
		got = append(got, a.Flush()...)
		_, evs := x.Rows()
		want := process.EventNumbers(evs)
		n := 0
		for _, r := range got {
			if r.Kind != records.KindClientEvent {
				continue
			}
			if w, ok := want[r.Event.EventKey]; ok {
				n++
				if int64(w) != r.BatchCCN {
					t.Errorf("seed %d %s: rank %d, event rows number it %d", seed, r.Event.EventKey, r.BatchCCN, w)
				}
			}
		}
		if n == 0 {
			t.Fatalf("seed %d: no numbered event", seed)
		}
	}
}
