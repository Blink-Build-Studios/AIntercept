package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Expiry: a call whose ending never comes is written without one once data
// time (the newest row anywhere, the rollup's clock) passes its start + 6 h (a
// WebSocket connection's: 7 d), as today's processor writes it (pendingTTL,
// canon.RequestFromPending). An ending that comes later replaces that row: the
// key ends as if the ending had come first, so expiry is order free like every
// other record, and a call never has two rows.

const (
	hourMS = int64(time.Hour / time.Millisecond)
	dayMS  = 24 * hourMS
	t0MS   = int64(1_000_000)
)

// expire expires every key's calls at clock (ns), as a rollup run does.
func (o *streamOut) expire(clock int64) {
	keys := make([]string, 0, len(o.states))
	for k := range o.states {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o.fold(k, o.states[k].Expire(clock))
	}
}

// wsConn is a Codex WebSocket connection's opening GET, as its start holds it.
func wsConn(id string, t int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: t * ms, Host: "chatgpt.com", Method: "GET",
		Path: "/backend-api/codex/responses", KeyHash: "k2", Transport: events.TransportWebSocket}
}

func TestExpireAtomic(t *testing.T) {
	probe := ccCall("p1", "s1", t0MS)
	probe.Synthetic = true
	cases := []struct {
		name     string
		before   func() []records.Record
		clockMS  int64    // data time, ms; the clock passes a deadline when it's after it
		expired  []string // the calls expiry writes
		deadline int64    // the key's earliest deadline afterwards (ms; 0: none)
	}{
		{"a call in flight at exactly 6 h: not yet",
			func() []records.Record { return []records.Record{callStart(ccCall("c1", "s1", t0MS))} },
			t0MS + 6*hourMS, nil, t0MS + 6*hourMS},
		{"a call in flight past 6 h: written without its ending",
			func() []records.Record { return []records.Record{callStart(ccCall("c1", "s1", t0MS))} },
			t0MS + 6*hourMS + 1, []string{"c1"}, 0},
		{"a WebSocket connection past 6 h: not yet (7 d)",
			func() []records.Record { return []records.Record{callStart(wsConn("w1", t0MS))} },
			t0MS + 6*hourMS + 1, nil, t0MS + 7*dayMS},
		{"a WebSocket connection past 7 d",
			func() []records.Record { return []records.Record{callStart(wsConn("w1", t0MS))} },
			t0MS + 7*dayMS + 1, []string{"w1"}, 0},
		{"a WebSocket turn: never in flight here (its connection is)",
			func() []records.Record { return []records.Record{callStart(ccCall("w1#1", "s1", t0MS))} },
			t0MS + 8*dayMS, nil, 0},
		{"an ended call: nothing",
			func() []records.Record {
				return []records.Record{callStart(ccCall("c1", "s1", t0MS)), callEnd(ccCall("c1", "s1", t0MS))}
			},
			t0MS + 8*dayMS, nil, 0},
		{"a start after its ending: nothing",
			func() []records.Record {
				return []records.Record{callEnd(ccCall("c1", "s1", t0MS)), callStart(ccCall("c1", "s1", t0MS))}
			},
			t0MS + 8*dayMS, nil, 0},
		{"an ending's copy under its start's key: nothing (the ending is under its own)",
			func() []records.Record {
				ended := keyedCall("c1", t0MS)
				ended.SessionID = "s1"
				return []records.Record{callStart(keyedCall("c1", t0MS)), endCopy(ended, startKeyOf(keyedCall("c1", t0MS)))}
			},
			t0MS + 8*dayMS, nil, 0},
		{"a probe (synthetic): expires too, into its key's probe times",
			func() []records.Record { return []records.Record{callStart(probe)} },
			t0MS + 6*hourMS + 1, []string{"p1"}, 0},
		{"two calls, the clock between their deadlines: the first only",
			func() []records.Record {
				return []records.Record{callStart(ccCall("c2", "s1", t0MS+hourMS)), callStart(ccCall("c1", "s1", t0MS))}
			},
			t0MS + 6*hourMS + 1, []string{"c1"}, t0MS + 7*hourMS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			batchN = map[string]int64{}
			before := tc.before()
			o := stream(before)
			if len(o.states) != 1 {
				t.Fatalf("want one key, got %d", len(o.states))
			}
			var key string
			var s *KeyState
			for k, st := range o.states {
				key, s = k, st
			}
			starts := map[string]*canon.Request{}
			for _, r := range before {
				if r.Kind == records.KindCallStart {
					starts[r.CallID] = r.Call
				}
			}
			o.expire(tc.clockMS * ms)
			for _, id := range tc.expired {
				if _, ok := o.rows[TableFactPending][key+"/"+id]; ok {
					t.Fatalf("%s is still pending", id)
				}
				if starts[id].Synthetic {
					if len(o.rows[TableFactProbes]) != 1 {
						t.Fatalf("probe rows %v, want the probe's times", o.rows[TableFactProbes])
					}
					continue
				}
				got, ok := o.rows[TableFactRequests][key+"/"+id]
				if !ok {
					t.Fatalf("%s has no request row", id)
				}
				if want := *starts[id]; !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: expired row\n got %+v\nwant %+v (its start)", id, got, want)
				}
			}
			if n := len(o.rows[TableFactRequests]) + len(o.rows[TableFactProbes]); tc.expired == nil && n != countEnded(before) {
				t.Fatalf("expiry wrote rows: %v", o.rows[TableFactRequests])
			}
			if got := s.Deadline(); got != tc.deadline*ms {
				t.Fatalf("deadline %d, want %d", got, tc.deadline*ms)
			}
			if d := s.Expire(tc.clockMS * ms); len(d.Changes) != 0 {
				t.Fatalf("expiring again changed %d rows", len(d.Changes))
			}
			// The expiry is in the key's checkpoint: a key loaded back is the same.
			b, err := Encode(s)
			if err != nil {
				t.Fatal(err)
			}
			s2, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s2.Rows(), s.Rows()) || s2.Deadline() != s.Deadline() {
				t.Fatalf("a key loaded back differs: deadline %d, want %d", s2.Deadline(), s.Deadline())
			}
		})
	}
}

func countEnded(rs []records.Record) int {
	n := 0
	for _, r := range rs {
		if r.Kind == records.KindCallEnd && r.RoutingKey == records.RoutingKey(&r) {
			n++
		}
	}
	return n
}

// The expired row is what today's processor writes for a call that never
// ended: canon.RequestFromPending of the transaction it holds, built here from
// the same raw rows the way it builds one (process shape.go, apply). The rows
// go through the observer's own assembler, as ingest does.
func TestExpiredRowIsTodaysRow(t *testing.T) {
	at := func(d time.Duration) int64 { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC).Add(d).UnixNano() }
	cc := map[string]string{"x-claude-code-session-id": "s1", "x-claude-code-prompt-id": "p1",
		"user-agent": "claude-cli/2.1.3 (external, cli)", "content-type": "application/json"}
	codex := map[string]string{"originator": "codex_cli_rs", "user-agent": "codex_cli_rs/0.164.0"}
	ws := map[string]string{"upgrade": "websocket", "originator": "codex_cli_rs", "user-agent": "codex_cli_rs/0.164.0"}
	hdr := func(id, host, method, path string, h map[string]string, eos bool) events.Event {
		return events.Event{CorrelationID: id, Seq: 1, TimestampNS: at(0), Phase: events.PhaseRequestHeaders, Direction: events.DirInbound,
			Host: host, Method: method, Path: path, KeyHash: "kh1", KeyHint: "hint1", IP: "10.0.0.1", Headers: h, EndOfStream: eos}
	}
	body := func(id string, n int) events.Event {
		return events.Event{CorrelationID: id, Seq: 2, TimestampNS: at(time.Millisecond), Phase: events.PhaseRequestBodyChunk,
			Direction: events.DirInbound, Bytes: n, EndOfStream: true}
	}
	cases := []struct {
		name string
		rows []events.Event
		ttl  time.Duration
	}{
		{"a Claude Code model call: headers, then its body", []events.Event{
			hdr("c1", "api.anthropic.com", "POST", "/v1/messages?beta=true", cc, false), body("c1", 2048)}, 6 * time.Hour},
		{"a Codex analytics call", []events.Event{
			hdr("a1", "chatgpt.com", "POST", "/backend-api/codex/analytics-events/events", codex, false), body("a1", 346)}, 6 * time.Hour},
		{"a GET its headers end", []events.Event{
			hdr("g1", "claude.ai", "GET", "/api/bootstrap", map[string]string{"user-agent": "Mozilla/5.0"}, true)}, 6 * time.Hour},
		{"a WebSocket connection (7 d)", []events.Event{
			hdr("w1", "chatgpt.com", "GET", "/backend-api/codex/responses", ws, true)}, 7 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := records.NewAssembler("inc")
			var recs []records.Record
			for i := range tc.rows {
				recs = append(recs, a.Apply(&tc.rows[i], nil, "f1")...)
			}
			if len(recs) != 1 || recs[0].Kind != records.KindCallStart {
				t.Fatalf("records %+v, want one call_start", recs)
			}
			// Today's processor's transaction for the same rows.
			h := tc.rows[0]
			p := canon.Pending{CorrelationID: h.CorrelationID, SourceFile: "f1", SeenNS: h.TimestampNS, StartNS: h.TimestampNS,
				Host: h.Host, Method: h.Method, Path: h.Path, Synthetic: h.Synthetic, IP: h.IP, KeyHash: h.KeyHash, KeyHint: h.KeyHint,
				WebSocket: h.Headers["upgrade"] == "websocket", ClientJSON: canon.Marshal(canon.Pick(h.Headers, canon.PendingClientHeaders))}
			for _, e := range tc.rows[1:] {
				p.RequestBytes += int64(e.Bytes)
			}
			want := canon.RequestFromPending(&p)

			s := New(recs[0].RoutingKey)
			s.Apply(recs[0])
			if d := s.Expire(h.TimestampNS + int64(tc.ttl)); len(d.Changes) != 0 {
				t.Fatalf("expired at exactly its wait: %+v", d.Changes)
			}
			s.Expire(h.TimestampNS + int64(tc.ttl) + 1)
			got, ok := s.Rows()[TableFactRequests][h.CorrelationID]
			if !ok {
				t.Fatalf("no request row; rows %v", s.Rows()[TableFactRequests])
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("expired row differs from today's\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// An ending that arrives after its call expired replaces the expired row: the
// key's rows (every table) are those of the ending alone, and the call has one
// row. A start or a duplicate that comes after changes nothing.
func TestExpireLateEndingReplacesTheExpiredRow(t *testing.T) {
	late := t0MS + 7*hourMS
	cases := []struct {
		name  string
		start func() records.Record
		end   func() records.Record
	}{
		{"its ending, within 6 h of its start (late to arrive)",
			func() records.Record { return callStart(ccCall("c1", "s1", t0MS)) },
			func() records.Record { return callEnd(ccCall("c1", "s1", t0MS)) }},
		{"its ending, more than 6 h after its start",
			func() records.Record { return callStart(ccCall("c1", "s1", t0MS)) },
			func() records.Record {
				c := ccCall("c1", "s1", t0MS)
				c.EndNS = late * ms
				return callEnd(c)
			}},
		{"its ending's copy (the ending went to the session's key)",
			func() records.Record { return callStart(keyedCall("c1", t0MS)) },
			func() records.Record {
				c := keyedCall("c1", t0MS)
				c.SessionID = "s1"
				return endCopy(c, startKeyOf(keyedCall("c1", t0MS)))
			}},
		{"a WebSocket connection's close",
			func() records.Record { return callStart(wsConn("w1", t0MS)) },
			func() records.Record {
				c := wsConn("w1", t0MS)
				c.EndNS = (t0MS + 8*dayMS) * ms
				return rec(records.Record{Kind: records.KindWSConnection, CallID: "w1", Call: c})
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			batchN = map[string]int64{}
			start, end := tc.start(), tc.end()
			want := stream([]records.Record{start, end})

			got := stream([]records.Record{start})
			got.expire((t0MS + 8*dayMS) * ms)
			if len(got.rows[TableFactRequests]) != 1 {
				t.Fatalf("no expired row: %v", got.rows[TableFactRequests])
			}
			got.apply(end)
			got.apply(start)
			got.apply(end)
			if diff := rowsDiff(got.rows, want.rows); diff != "" {
				t.Fatalf("rows differ from the ending alone:%s", diff)
			}
			for k, s := range got.states {
				if diff := rowsDiff(s.Rows(), want.states[k].Rows()); diff != "" {
					t.Fatalf("%s: state rows differ:%s", k, diff)
				}
			}
		})
	}
}

// expireCheck applies rs in order with an expiry at each clock in steps
// (after the record at that index), then one at final; the rows must equal
// one pass over rs, then one expiry at final.
func expireCheck(t *testing.T, label string, rs, in []records.Record, steps map[int]int64, final int64, reload bool) {
	t.Helper()
	want := stream(rs)
	want.expire(final)
	got := &streamOut{states: map[string]*KeyState{}, rows: map[string]map[string]any{}, cases: map[string]int{}}
	for i, r := range in {
		got.apply(r)
		if c, ok := steps[i]; ok {
			got.expire(c)
		}
		if reload && i == len(in)/2 {
			for k, s := range got.states {
				b, err := Encode(s)
				if err != nil {
					t.Fatal(err)
				}
				if got.states[k], err = Decode(b); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	got.expire(final)
	if diff := rowsDiff(got.rows, want.rows); diff != "" {
		t.Fatalf("%s: rows differ from one pass:%s", label, diff)
	}
}

// rowsDiff names the rows two streams disagree on.
func rowsDiff(got, want map[string]map[string]any) string {
	var out string
	for table := range mergeKeys(got, want) {
		for id := range mergeKeys(got[table], want[table]) {
			g, gok := got[table][id]
			w, wok := want[table][id]
			if gok != wok || !reflect.DeepEqual(g, w) {
				out += fmt.Sprintf("\n  %s %s:\n     got %+v\n    want %+v", table, id, g, w)
			}
		}
	}
	return out
}

func mergeKeys[V any](a, b map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// Short sequences: an abandoned call, a call ended in time, one ended past
// 6 h, and an expiry at a clock past all three, in every order of the records
// and every place for the expiry.
func TestExpireShortSequencesEveryOrder(t *testing.T) {
	mk := func() []records.Record {
		long := ccCall("c3", "s1", t0MS+2)
		long.EndNS = (t0MS + 7*hourMS) * ms
		return []records.Record{
			callStart(ccCall("c1", "s1", t0MS)),
			callStart(ccCall("c2", "s1", t0MS+1)), callEnd(ccCall("c2", "s1", t0MS+1)),
			callStart(ccCall("c3", "s1", t0MS+2)), callEnd(long),
		}
	}
	clock := (t0MS + 8*hourMS) * ms
	rs := mk()
	permute(len(rs), func(order []int) {
		in := make([]records.Record, len(order))
		for i, k := range order {
			in[i] = rs[k]
		}
		for at := 0; at < len(in); at++ {
			expireCheck(t, fmt.Sprintf("%v, expiry after %d", order, at), rs, in, map[int]int64{at: clock}, clock, false)
		}
	})
}

// generateExpiring is calls over a day of data time: some never end, some
// end within 6 h, some long after; under one key or two (the ending's copy);
// WebSocket connections and turns; events.
func generateExpiring(rng *rand.Rand, n int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		t := t0MS + int64(rng.Intn(24))*hourMS + int64(rng.Intn(1000))
		id := fmt.Sprintf("x-%d", i)
		dur := []int64{500, 5 * hourMS, 7 * hourMS, 2 * dayMS}[rng.Intn(4)]
		switch k := rng.Intn(6); {
		case k < 2:
			c := ccCall(id, fmt.Sprintf("s%d", rng.Intn(3)), t)
			c.Synthetic = rng.Intn(6) == 0
			out = append(out, callStart(c))
			if rng.Intn(3) > 0 {
				c.EndNS = (t + dur) * ms
				out = append(out, callEnd(c))
			}
		case k < 4:
			start, end := keyedCall(id, t), keyedCall(id, t)
			end.SessionID, end.EndNS = fmt.Sprintf("s%d", rng.Intn(3)), (t+dur)*ms
			out = append(out, callStart(start))
			if rng.Intn(3) > 0 {
				out = append(out, callEnd(end), endCopy(end, startKeyOf(start)))
			}
		case k < 5:
			c := wsConn(id, t)
			out = append(out, callStart(c), callStart(ccCall(id+"#1", "s1", t)), callEnd(ccCall(id+"#1", "s1", t)))
			if rng.Intn(2) == 0 {
				c.EndNS = (t + 8*dayMS) * ms
				out = append(out, rec(records.Record{Kind: records.KindWSConnection, CallID: id, Call: c}))
			}
		default:
			out = append(out, ccEvent(fmt.Sprintf("s%d", rng.Intn(3)), "tengu_api_query", t, `{}`))
		}
	}
	return out
}

// Long generated sequences: shuffled, duplicated, expired at rising clocks in
// between, reloaded from a checkpoint halfway: the rows equal one pass, then
// one expiry.
func TestExpireGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 20/seedFrac; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateExpiring(rng, 150)
		in := shuffled(rng, rs, 40)
		steps := map[int]int64{}
		clock := t0MS * ms
		for i := range in {
			if rng.Intn(10) == 0 {
				clock += int64(rng.Intn(int(4*hourMS))) * ms
				steps[i] = clock
			}
		}
		final := clock + 10*dayMS*ms
		expireCheck(t, fmt.Sprintf("seed %d", seed), rs, in, steps, final, true)
	}
}
