package state

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The request log (request_rows, person_last, session_request_rows), rebuilt the
// way the query does from the keys' stored rows and the dimensions. This is the
// API's SQL (api/src/aintercept_api/stream_sql.py) step for step; both are held
// to the reference.

// personSessions is the person's sessions: their email group's, or for
// "unattributed" the visible sessions no account names.
func (v *rqView) personSessions(person string) []string {
	sids := map[string]bool{}
	if person == "unattributed" {
		for _, s := range v.visible {
			if !v.known[s] {
				sids[s] = true
			}
		}
	} else {
		accts := []string{person}
		if e := v.email[person]; e != "" {
			for a, x := range v.email {
				if a != person && x == e {
					accts = append(accts, a)
				}
			}
		}
		for _, a := range accts {
			for s := range v.sessOf[a] {
				sids[s] = true
			}
		}
	}
	out := make([]string, 0, len(sids))
	for s := range sids {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// personRows is a person's request log.
func (v *rqView) personRows(person string) []process.RequestRow {
	rows := v.scopeRows(v.personSessions(person), "")
	for i := range rows {
		rows[i].PersonID = person
	}
	sortRows(rows)
	return rows
}

// personLast is a person's newest data time: their sessions' newest.
func (v *rqView) personLast(person string) *int64 {
	var last *int64
	for _, s := range v.personSessions(person) {
		if t, ok := v.last[s]; ok && (last == nil || t > *last) {
			last = &t
		}
	}
	return last
}

// checkRequests holds the stream's request log to the reference's: every
// person's rows and newest time, and every session's Requests tab.
func checkRequests(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	ref := reference(rs)
	diffs := requestDiffs(ref, o, 3)
	for _, d := range diffs {
		t.Errorf("%s: %s", label, d)
	}
	if len(diffs) > 0 {
		t.FailNow()
	}
}

// requestDiffs is up to max differences between the stream's request log and the reference's.
func requestDiffs(ref *process.Derived, o *streamOut, max int) []string {
	v := viewOf(o.rows, ref.SessionRows)
	var out []string
	add := func(s string) bool {
		out = append(out, s)
		return len(out) >= max
	}
	byPerson := map[string][]process.RequestRow{}
	for _, r := range ref.RequestRows {
		byPerson[r.PersonID] = append(byPerson[r.PersonID], r)
	}
	for _, pl := range ref.PersonLast {
		p := pl.PersonID
		got, want := v.personRows(p), byPerson[p]
		if len(got) != len(want) {
			if add(fmt.Sprintf("person %s: %d rows, want %d%s", p, len(got), len(want), rowIDDiff(got, want))) {
				return out
			}
		} else {
			for i := range got {
				if !reflect.DeepEqual(got[i], want[i]) {
					if add(fmt.Sprintf("person %s row %d: %s", p, i, reqFieldDiff(got[i], want[i]))) {
						return out
					}
					break
				}
			}
		}
		if g := v.personLast(p); !reflect.DeepEqual(g, pl.LastNS) {
			if add(fmt.Sprintf("person %s: last %v, want %v", p, ptrStr(g), ptrStr(pl.LastNS))) {
				return out
			}
		}
	}
	byScope := map[string][]process.SessionRequestRow{}
	for _, r := range ref.SessionReqs {
		byScope[r.Scope] = append(byScope[r.Scope], r)
	}
	for _, sr := range ref.SessionRows {
		got, want := v.sessionRows(sr), byScope[sr.SessionID]
		if len(got) != len(want) {
			var g, w []process.RequestRow
			for _, x := range got {
				g = append(g, x.Row)
			}
			for _, x := range want {
				w = append(w, x.Row)
			}
			if add(fmt.Sprintf("session %s: %d rows, want %d%s", sr.SessionID, len(got), len(want), rowIDDiff(g, w))) {
				return out
			}
			continue
		}
		for i := range got {
			if !reflect.DeepEqual(got[i], want[i]) {
				if add(fmt.Sprintf("session %s row %d: %s", sr.SessionID, i, reqFieldDiff(got[i].Row, want[i].Row))) {
					return out
				}
				break
			}
		}
	}
	return out
}

func ptrStr[T any](p *T) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

func rowIDDiff(got, want []process.RequestRow) string {
	g, w := map[string]int{}, map[string]int{}
	for _, r := range got {
		g[r.RequestID]++
	}
	for _, r := range want {
		w[r.RequestID]++
	}
	var extra, missing []string
	for id, n := range g {
		if w[id] < n {
			extra = append(extra, id)
		}
	}
	for id, n := range w {
		if g[id] < n {
			missing = append(missing, id)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return fmt.Sprintf(" (extra %v, missing %v)", head(extra), head(missing))
}

func head(s []string) []string {
	if len(s) > 4 {
		return append(s[:4:4], "…")
	}
	return s
}

func reqFieldDiff(got, want process.RequestRow) string {
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	s := got.RequestID + ":"
	for f := 0; f < gv.NumField(); f++ {
		if !reflect.DeepEqual(gv.Field(f).Interface(), wv.Field(f).Interface()) {
			s += fmt.Sprintf(" %s got %s want %s;", gv.Type().Field(f).Name, show(gv.Field(f)), show(wv.Field(f)))
		}
	}
	return s
}

func show(v reflect.Value) string {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "nil"
		}
		return fmt.Sprint(v.Elem().Interface())
	}
	return fmt.Sprint(v.Interface())
}

// --- builders ---------------------------------------------------------------

// rqCall is a Claude Code model call with a prompt and request ids.
func rqCall(id, s string, t int64, prompt, rid string) *canon.Request {
	c := ccCall(id, s, t)
	c.PromptID, c.RequestID, c.ClientRequestID = prompt, rid, "cr-"+rid
	c.RequestClass, c.Model, c.StopReason, c.OutputTokens = "main", "claude-opus-4-1", "end_turn", 10
	return c
}

func failed(c *canon.Request, status int64) *canon.Request {
	c.Status, c.OutputTokens = status, 0
	if status == 0 {
		c.Error = "upstream reset"
	}
	return c
}

func reportEv(s string, t int64, rid string) records.Record {
	return ccEvent(s, "tengu_api_success", t, fmt.Sprintf(`{"requestId":%q,"ttftMs":%d,"cache_creation_5m_input_tokens":%d}`, rid, t%997, t%13))
}

func retryEv(s string, t int64, prompt string, attempt int) records.Record {
	md := fmt.Sprintf(`{"attempt":%d}`, attempt)
	if prompt != "" {
		md = fmt.Sprintf(`{"cc_prompt_id":%q,"attempt":%d}`, prompt, attempt)
	}
	return ccEvent(s, "tengu_api_retry", t, md)
}

// batchCall is an analytics request and its events: the call goes with the
// session its earliest event names, as ingest releases it.
func batchCall(id, keyHash string, t int64, evs ...*canon.Event) []records.Record {
	c := &canon.Request{CallID: id, CorrelationID: id, StartNS: t * ms, EndNS: (t + 50) * ms, Host: "api.anthropic.com",
		Method: "POST", Path: "/api/event_logging/batch", Status: 200, Product: canon.ProductClaudeCode, KeyHash: keyHash}
	var out []records.Record
	sess, sessNS := "", int64(0)
	ccn := int64(0) // a Claude Code event's rank among the batch's (records.Assembler.release)
	for i, e := range evs {
		e.CorrelationID = id
		if e.EventKey == "" {
			e.EventKey = fmt.Sprintf("%s/e%02d", id, i)
		}
		if e.Seq == 0 {
			e.Seq = int64(i + 1)
		}
		if e.RowNS == 0 {
			e.RowNS = t * ms
		}
		if e.SessionID != "" && (sess == "" || e.NS < sessNS || e.NS == sessNS && e.SessionID < sess) {
			sess, sessNS = e.SessionID, e.NS
		}
		r := records.Record{Kind: records.KindClientEvent, EventKey: e.EventKey, BatchID: id,
			BatchN: int64(i + 1), Event: e, Call: &canon.Request{KeyHash: keyHash}}
		if e.Source == canon.ProductClaudeCode {
			ccn++
			r.BatchCCN = ccn
		}
		out = append(out, rec(r))
	}
	out = append(out, rec(records.Record{Kind: records.KindCallEnd, CallID: id, Call: c, BatchSession: sess, BatchID: id}))
	return out
}

func batchEv(s, name string, t int64, md string) *canon.Event {
	return &canon.Event{NS: t * ms, Source: canon.ProductClaudeCode, Name: name, SessionID: s, MetadataJSON: md}
}

// flagsCall is a call with no session: it goes with its credential and day.
func flagsCall(id, keyHash string, t int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: t * ms, EndNS: (t + 20) * ms, Host: "api.anthropic.com",
		Method: "POST", Path: "/api/eval/sdk-x", Status: 200, Product: canon.ProductClaudeCode, KeyHash: keyHash}
}

// codexPoll is a Codex call no thread names (a usage check).
func codexPoll(id, keyHash, client string, t int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: t * ms, EndNS: (t + 20) * ms, Host: "chatgpt.com",
		Method: "GET", Path: "/backend-api/wham/usage", Status: 200, Product: canon.ProductCodex, KeyHash: keyHash, Client: client}
}

func withPerson(c *canon.Request, p string) *canon.Request { c.PersonID = p; return c }

// endCopy is a call's ending as ingest also writes it under its start's key.
func endCopy(c *canon.Request, startKey string) records.Record {
	r := callEnd(c)
	r.RecordID += "-copy"
	r.RoutingKey = startKey
	r.Bucket = int32(records.BucketOf(startKey, records.DefaultBucketBits))
	return r
}

// --- atomic cases -----------------------------------------------------------

var requestCases = []atomicCase{
	{"an ended call", nil,
		func() records.Record { return callEnd(rqCall("c1", "s1", 1000, "p1", "r1")) },
		[]string{CaseSessionLast, CaseTxEnd}},
	{"a call in flight", nil,
		func() records.Record { return callStart(rqCall("c1", "s1", 1000, "p1", "r1")) },
		[]string{CaseSessionLast, CaseTxStart}},
	{"its ending replaces its start's row",
		func() []records.Record { return []records.Record{callStart(rqCall("c1", "s1", 1000, "p1", "r1"))} },
		func() records.Record { return callEnd(rqCall("c1", "s1", 1000, "p1", "r1")) },
		[]string{CaseTxEnd}},
	{"a start after its ending has no row",
		func() []records.Record { return []records.Record{callEnd(rqCall("c1", "s1", 1000, "p1", "r1"))} },
		func() records.Record { return callStart(rqCall("c1", "s1", 1000, "p1", "r1")) },
		[]string{CaseTxNoRow}},
	{"an ending's copy under its start's key takes the start's row",
		func() []records.Record { return []records.Record{callStart(flagsCall("c1", "k1", 1000))} },
		func() records.Record {
			c := flagsCall("c1", "k1", 1000)
			c.SessionID = "s1"
			return endCopy(c, records.RoutingKey(&records.Record{Kind: records.KindCallStart, Call: flagsCall("c1", "k1", 1000)}))
		},
		[]string{CaseTxEndCopy}},
	{"a synthetic call has no row", nil,
		func() records.Record {
			c := rqCall("c1", "s1", 1000, "p1", "r1")
			c.Synthetic = true
			return callEnd(c)
		},
		[]string{CaseTxNoRow}},
	{"a WebSocket turn's start has no row", nil,
		func() records.Record { return callStart(codexCall("w1#1", "t1", "turn1", 1000)) },
		[]string{CaseTxNoRow}},
	{"a call with no session, under its credential", nil,
		func() records.Record { return callEnd(flagsCall("f1", "k1", 1000)) },
		[]string{CaseTxEnd}},
	{"a report no call matches", nil,
		func() records.Record { return reportEv("s1", 1000, "r1") },
		[]string{CaseBatchMember, CaseMissed, CaseReport, CaseSessionLast}},
	{"a call matching the report", func() []records.Record { return []records.Record{reportEv("s1", 1000, "r1")} },
		func() records.Record { return callEnd(rqCall("c1", "s1", 900, "p1", "r1")) },
		[]string{CaseMatched, CaseTxEnd}},
	{"a report its call already matches", func() []records.Record { return []records.Record{callEnd(rqCall("c1", "s1", 900, "p1", "r1"))} },
		func() records.Record { return reportEv("s1", 1000, "r1") },
		[]string{CaseBatchMember, CaseReport, CaseSessionLast}},
	{"a matching call that turns out hidden brings the not-captured row back",
		func() []records.Record {
			return []records.Record{reportEv("s1", 1000, "r1"), callStart(rqCall("c1", "s1", 900, "p1", "r1"))}
		},
		func() records.Record { return callEnd(hidden(rqCall("c1", "s1", 900, "p1", "r1"))) },
		[]string{CaseMissed, CaseTxEnd}},
	{"a retry after a failed call", func() []records.Record {
		return []records.Record{callEnd(failed(rqCall("c1", "s1", 1000, "p1", "r1"), 529))}
	},
		func() records.Record { return retryEv("s1", 3000, "p1", 1) },
		[]string{CaseBatchMember, CaseRetry, CaseRetryRow, CaseSessionLast}},
	{"a retry no failed call is near", func() []records.Record {
		return []records.Record{callEnd(failed(rqCall("c1", "s1", 1000, "p1", "r1"), 529))}
	},
		func() records.Record { return retryEv("s1", 60_000, "p1", 1) },
		[]string{CaseBatchMember, CaseRetry, CaseSessionLast}},
	{"the next attempt after a retried failure", func() []records.Record {
		return []records.Record{callEnd(failed(rqCall("c1", "s1", 1000, "p1", "r1"), 0)), retryEv("s1", 2000, "p1", 1)}
	},
		func() records.Record { return callEnd(rqCall("c2", "s1", 2500, "p1", "r2")) },
		[]string{CaseNeighbor, CaseSessionLast, CaseTxEnd}},
	{"an analytics request, its events and session", nil,
		func() records.Record {
			rs := batchCall("b1", "k1", 1000, batchEv("s1", "tengu_api_query", 990, `{}`))
			return rs[len(rs)-1]
		},
		[]string{CaseTxEnd}},
	{"an analytics event, under its batch", nil,
		func() records.Record {
			return batchCall("b1", "k1", 1000, batchEv("s1", "tengu_api_query", 990, `{}`))[0]
		},
		[]string{CaseBatchMember, CaseSessionLast}},
	{"a Codex event is a batch member", nil,
		func() records.Record {
			e := batchEv("t1", "codex_turn_event", 990, `{}`)
			e.Source = canon.ProductCodex
			return batchCall("b1", "k2", 1000, e)[0]
		},
		[]string{CaseBatchMember}},
}

var requestCaseSet = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range []string{CaseTxEnd, CaseTxEndCopy, CaseTxStart, CaseTxNoRow, CaseReport, CaseMissed, CaseMatched,
		CaseRetry, CaseRetryRow, CaseNeighbor, CaseBatchMember, CaseSessionLast} {
		m[c] = true
	}
	return m
}()

func sortedCases(cs []string) []string {
	out := append([]string(nil), cs...)
	sort.Strings(out)
	return out
}

func TestRequestsAtomic(t *testing.T) {
	for _, c := range requestCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkRequests(t, c.name, append(before, r), o)
			if got, want := sortedCases(only(d.Cases, requestCaseSet)), sortedCases(c.cases); !reflect.DeepEqual(got, want) {
				t.Fatalf("cases %v, want %v", got, want)
			}
			if d2 := o.apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

// Every order of a few records ends in the reference's request log.
func TestRequestsShortSequencesEveryOrder(t *testing.T) {
	heavy(t)
	sets := map[string]func() []records.Record{
		"start, end, report, a second report": func() []records.Record {
			c := rqCall("c1", "s1", 1000, "p1", "r1")
			return []records.Record{callStart(c), callEnd(rqCall("c1", "s1", 1000, "p1", "r1")), reportEv("s1", 1200, "r1"), reportEv("s1", 1300, "r9"),
				inputPrompt("s1", "p1", "2", 900)}
		},
		"a failure, its retry, the next attempt": func() []records.Record {
			return []records.Record{callEnd(failed(rqCall("c1", "s1", 1000, "p1", "r1"), 529)), retryEv("s1", 2000, "p1", 1),
				callStart(rqCall("c2", "s1", 2500, "p1", "r2")), callEnd(rqCall("c2", "s1", 2500, "p1", "r2")),
				callEnd(failed(rqCall("c3", "s1", 1500, "p1", "r3"), 500))}
		},
		"an analytics batch over two sessions, accounts": func() []records.Record {
			rs := batchCall("b1", "k1", 5000, batchEv("s2", "tengu_api_query", 4000, `{}`), batchEv("s1", "tengu_api_query", 4000, `{}`))
			return append(rs, callEnd(withPerson(rqCall("c1", "s1", 1000, "p1", "r1"), "acct-a")), account("acct-a", "A@x.io", "", "", 1),
				account("acct-b", "a@x.io", "", "", 2), callEnd(withPerson(rqCall("c2", "s2", 1100, "p2", "r2"), "acct-b")))
		},
		"a Codex thread and its client's polls": func() []records.Record {
			x := codexCall("x1", "t1", "turn1", 10_000)
			x.Client = "codex_cli_rs"
			x2 := codexCall("x2", "t1", "turn2", 20_000)
			x2.Client = "codex_cli_rs"
			return []records.Record{callEnd(x), callEnd(x2), callEnd(codexPoll("u1", "k2", "codex_cli_rs", 15_000)),
				callEnd(codexPoll("u2", "k2", "codex_cli_rs", 200_000)), callEnd(codexPoll("u3", "k2", "other", 16_000))}
		},
	}
	for name, mk := range sets {
		batchN = map[string]int64{}
		rs := mk()
		permute(len(rs), func(p []int) {
			ordered := make([]records.Record, len(rs))
			for i, j := range p {
				ordered[i] = rs[j]
			}
			checkRequests(t, fmt.Sprintf("%s %v", name, p), rs, stream(ordered))
		})
	}
}

// generateRequests is n people's sessions: calls in flight and ended, failures,
// retries, reports (some for calls the proxy never saw), hidden calls, analytics
// batches spanning sessions, calls with no session, Codex threads with their
// client's polls, accounts linked by email.
func generateRequests(rng *rand.Rand, n, per int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		acct := fmt.Sprintf("acct-%d", i)
		kh := fmt.Sprintf("kh-%d", i%3)
		out = append(out, account(acct, fmt.Sprintf("u%d@x.io", i%2), "", "", int64(rng.Intn(1000))))
		var sessions []string
		for k := 0; k < 2; k++ {
			sessions = append(sessions, fmt.Sprintf("rs-%d-%d", i, k))
		}
		t := int64(1_000_000 + rng.Intn(100_000))
		for j := 0; j < per; j++ {
			t += int64(rng.Intn(4000))
			s := sessions[rng.Intn(len(sessions))]
			pid := fmt.Sprintf("p-%d-%d", i, rng.Intn(6))
			rid := fmt.Sprintf("r-%d-%d", i, j)
			switch k := rng.Intn(12); {
			case k < 5:
				c := rqCall(fmt.Sprintf("c-%d-%d", i, j), s, t, pid, rid)
				c.KeyHash = kh
				c.RequestClass = []string{"main", "main", "auxiliary"}[rng.Intn(3)]
				if rng.Intn(3) == 0 {
					c.PersonID = acct
				}
				if rng.Intn(6) == 0 {
					failed(c, []int64{0, 529, 500, 429}[rng.Intn(4)])
				}
				if rng.Intn(8) == 0 {
					c.HiddenKind = []string{"prewarm", "title"}[rng.Intn(2)]
				}
				if rng.Intn(10) == 0 {
					c.RequestID = ""
				}
				if rng.Intn(3) > 0 {
					out = append(out, callStart(c))
				}
				if rng.Intn(6) > 0 {
					out = append(out, callEnd(c))
				}
				if rng.Intn(2) == 0 {
					out = append(out, reportEv(s, t+int64(rng.Intn(800)), rid))
				}
			case k < 6:
				out = append(out, reportEv(s, t, fmt.Sprintf("r-%d-gone-%d", i, j)))
			case k < 7:
				out = append(out, retryEv(s, t, []string{pid, ""}[rng.Intn(2)], 1+rng.Intn(3)))
			case k < 8:
				out = append(out, inputPrompt(s, pid, strconv.Itoa(rng.Intn(9)), t))
			case k < 9:
				evs := []*canon.Event{batchEv(s, "tengu_api_query", t-int64(rng.Intn(50)), `{}`)}
				if rng.Intn(2) == 0 {
					e := batchEv(sessions[rng.Intn(len(sessions))], "tengu_other", t-int64(rng.Intn(50)), `{}`)
					if rng.Intn(3) == 0 {
						e.PersonID = acct
					}
					evs = append(evs, e)
				}
				out = append(out, batchCall(fmt.Sprintf("b-%d-%d", i, j), kh, t, evs...)...)
			case k < 10:
				out = append(out, callEnd(flagsCall(fmt.Sprintf("f-%d-%d", i, j), kh, t)))
			default:
				th := fmt.Sprintf("th-%d", i)
				x := codexCall(fmt.Sprintf("x-%d-%d", i, j), th, fmt.Sprintf("turn%d", rng.Intn(4)), t)
				x.Client, x.KeyHash = "codex_cli_rs", "kx-"+strconv.Itoa(i)
				if rng.Intn(4) == 0 {
					x.PersonID = acct
				}
				out = append(out, callEnd(x))
				out = append(out, callEnd(codexPoll(fmt.Sprintf("u-%d-%d", i, j), "kx-"+strconv.Itoa(i), "codex_cli_rs", t+int64(rng.Intn(90_000)))))
			}
		}
	}
	return out
}

func TestRequestsGeneratedSequences(t *testing.T) {
	heavy(t)
	for seed := int64(1); seed <= 20/seedFrac; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateRequests(rng, 4, 150)
		in := shuffled(rng, rs, 40)
		o := stream(in[:len(in)/2])
		for k, s := range o.states {
			b, err := Encode(s)
			if err != nil {
				t.Fatal(err)
			}
			if o.states[k], err = Decode(b); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range in[len(in)/2:] {
			o.apply(r)
		}
		checkRequests(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

// A record's work doesn't grow with how many the session already holds.
func TestRequestsFlatCost(t *testing.T) {
	heavy(t)
	if testing.Short() {
		t.Skip("builds a 100,000-record session")
	}
	measure := func(n int) (float64, time.Duration) {
		s := New("s:big")
		for i := 0; i < n; i++ { // as real sessions are: mostly calls and reports, a failure and retry now and then
			at := int64(i) * 1000
			pid, rid := fmt.Sprintf("p%d", i/50), fmt.Sprintf("r%d", i)
			switch {
			case i%100 == 99:
				s.Apply(retryEv("big", at, pid, 1))
			case i%50 == 49:
				s.Apply(callEnd(failed(rqCall(fmt.Sprintf("c%d", i), "big", at, pid, rid), 529)))
			case i%3 == 0:
				s.Apply(callEnd(rqCall(fmt.Sprintf("c%d", i), "big", at, pid, rid)))
			case i%3 == 1:
				s.Apply(reportEv("big", at, fmt.Sprintf("r%d", i-1)))
			default:
				s.Apply(reportEv("big", at, fmt.Sprintf("gone%d", i)))
			}
		}
		pre := requestsDenseMix(n)
		k := 0
		allocs := testing.AllocsPerRun(1000, func() { s.Apply(pre[k]); k++ })
		start := time.Now()
		for ; k < len(pre); k++ {
			s.Apply(pre[k])
		}
		return allocs, time.Since(start) / time.Duration(len(pre)-1000)
	}
	a10, t10 := measure(10)
	a1k, t1k := measure(1_000)
	a100k, t100k := measure(flatBig)
	t.Logf("per record: n=10 %.1f allocs %v; n=1,000 %.1f allocs %v; n=100,000 %.1f allocs %v", a10, t10, a1k, t1k, a100k, t100k)
	if a100k > a10*1.5+4 {
		t.Errorf("allocations grow with the session: %.1f at 10, %.1f at 100,000", a10, a100k)
	}
	if t100k > 8*t10+20*time.Microsecond {
		t.Errorf("time grows with the session: %v at 10, %v at 100,000", t10, t100k)
	}
}

// Real data, local only (AINTERCEPT_RECORDS): the request log from the capture's
// records, shuffled with duplicates, equals the reference's.
func TestRealDataRequests(t *testing.T) {
	dir := os.Getenv("AINTERCEPT_RECORDS")
	if dir == "" {
		t.Skip("AINTERCEPT_RECORDS isn't set")
	}
	rs := loadRecords(t, dir)
	rng := rand.New(rand.NewSource(2))
	in := shuffled(rng, rs, len(rs)/50)
	o := stream(in)
	for _, c := range sortedCaseNames(o.cases) {
		if requestCaseSet[c] {
			t.Logf("case %-22s %9d", c, o.cases[c])
		}
	}
	ref := reference(rs)
	if diffs := requestDiffs(ref, o, 20); len(diffs) > 0 {
		for _, d := range diffs {
			t.Error(d)
		}
	}
	t.Logf("request rows %d, people %d, session request rows %d: compared", len(ref.RequestRows), len(ref.PersonLast), len(ref.SessionReqs))
}

func sortedCaseNames(m map[string]int) []string {
	var out []string
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func loadRecords(t *testing.T, dir string) []records.Record {
	t.Helper()
	var rs []records.Record
	err := filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() || !strings.HasSuffix(p, ".parquet") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		x, err := records.Decode(b)
		rs = append(rs, x...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// --- the SQL's fixtures -----------------------------------------------------

// The API's SQL is held to the reference by its own test
// (api/tests/test_stream_sql.py): it runs this with AINTERCEPT_SQL_FIXTURES set
// to a directory, and this writes, per scenario, the stored tables the SQL reads
// (folded from the stream) and the reference's request log.
func TestWriteRequestSQLFixtures(t *testing.T) {
	dir := os.Getenv("AINTERCEPT_SQL_FIXTURES")
	if dir == "" {
		t.Skip("AINTERCEPT_SQL_FIXTURES isn't set")
	}
	write := func(name string, rs []records.Record, in []records.Record) {
		o := stream(in)
		ref := reference(rs)
		writeSQLFixture(t, filepath.Join(dir, name), o, ref)
	}
	for seed := int64(1); seed <= 6; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateRequests(rng, 4, 120)
		write(fmt.Sprintf("seed%d", seed), rs, shuffled(rng, rs, 20))
	}
	if real := os.Getenv("AINTERCEPT_RECORDS"); real != "" {
		rs := loadRecords(t, real)
		write("real", rs, shuffled(rand.New(rand.NewSource(3)), rs, len(rs)/50))
	}
}

func writeSQLFixture(t *testing.T, dir string, o *streamOut, ref *process.Derived) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name+".parquet"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(TableRequestTxs, process.EncodeParquet(rowsOf[ReqTx](o, TableRequestTxs)))
	put(TableRequestReports, process.EncodeParquet(rowsOf[ReportRow](o, TableRequestReports)))
	put(TableRequestMissed, process.EncodeParquet(rowsOf[MissedRow](o, TableRequestMissed)))
	put(TableBatchMembers, process.EncodeParquet(rowsOf[BatchMember](o, TableBatchMembers)))
	put(TableSessionLast, process.EncodeParquet(rowsOf[SessionLast](o, TableSessionLast)))
	put(TableAccounts, process.EncodeParquet(rowsOf[process.Account](o, TableAccounts)))
	put(TableSessionAccounts, process.EncodeParquet(rowsOf[SessionAccount](o, TableSessionAccounts)))
	put(TableSessionVisible, process.EncodeParquet(rowsOf[SessionVisible](o, TableSessionVisible)))
	put(TableSessionCredential, process.EncodeParquet(rowsOf[SessionCredential](o, TableSessionCredential)))
	put(TablePromptIndex, process.EncodeParquet(rowsOf[PromptIndex](o, TablePromptIndex)))
	put(TableCodexTurns, process.EncodeParquet(rowsOf[CodexTurn](o, TableCodexTurns)))
	put("session_rows", process.EncodeParquet(ref.SessionRows))
	put("want_request_rows", process.EncodeParquet(ref.RequestRows))
	put("want_person_last", process.EncodeParquet(ref.PersonLast))
	put("want_session_request_rows", process.EncodeParquet(ref.SessionReqs))
	var people []string
	for _, p := range ref.PersonLast {
		people = append(people, p.PersonID)
	}
	b, _ := json.Marshal(people)
	if err := os.WriteFile(filepath.Join(dir, "people.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if diffs := requestDiffs(ref, o, 3); len(diffs) > 0 {
		t.Errorf("%s: the stored tables don't rebuild the reference: %v", dir, diffs)
	}
}

// requestsDenseMix is the flat-cost tests' new records after a session of n:
// call starts and ends, reports, a failure and a retry every ten records.
func requestsDenseMix(n int) []records.Record {
	pre := make([]records.Record, 3000)
	for k := range pre {
		at := int64(n)*1000 + int64(k)*10
		switch k % 10 {
		case 0:
			pre[k] = callStart(rqCall(fmt.Sprintf("cx%d", k), "big", at, "px", fmt.Sprintf("rx%d", k)))
		case 1:
			pre[k] = callEnd(rqCall(fmt.Sprintf("cx%d", k-1), "big", at-10, "px", fmt.Sprintf("rx%d", k-1)))
		case 2, 3:
			pre[k] = reportEv("big", at, fmt.Sprintf("rx%d", k-2))
		case 4:
			pre[k] = callEnd(failed(rqCall(fmt.Sprintf("cx%d", k), "big", at, "px", fmt.Sprintf("rx%d", k)), 529))
		case 5:
			pre[k] = retryEv("big", at, "px", 1)
		default:
			pre[k] = callEnd(rqCall(fmt.Sprintf("cx%d", k), "big", at, fmt.Sprintf("py%d", k), fmt.Sprintf("rx%d", k)))
		}
	}
	return pre
}
