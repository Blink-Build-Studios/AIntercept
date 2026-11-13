package state

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The request drawer's inputs, precomputed per key (docs/STREAMING_PIPELINE.md
// §4.9): a drawer request reads its call's own rows, never its session's.
//
//   - drawer_locator (l: keys): the key a call or a reported call is under.
//   - drawer_calls: each call's request row (in flight until it ends).
//   - drawer_steps: each visible model call's place: its prompt, class, time,
//     and its neighbours by the message-id chain (prev_call, next_call).
//   - drawer_facts: the events about each call, by role: its tengu_api_success
//     (success), its stall summary (stall), its tool uses (tool), their Bash
//     commands (bash), the tengu_api_query before it (query), and for a failed
//     call the tengu_api_retry nearest its end (retry).
//   - drawer_sandbox: the session's sandbox setting where it changes.
//   - drawer_reports: each tengu_api_success, by the id a reported row opens.
//   - drawer_events: each Claude Code event, by its event id (batch:n).
//
// refDrawer is the single pass the per-record updates must equal: today's
// request_detail.py semantics, over every record at once.

const secNS = int64(1_000_000_000)

func drawerTables() map[string]bool {
	return map[string]bool{TableDrawerLocator: true, TableDrawerCalls: true, TableDrawerSteps: true, TableDrawerFacts: true,
		TableDrawerSandbox: true, TableDrawerReports: true, TableDrawerEvents: true}
}

// drawerRows is the stream's drawer rows, table → key/id → row.
func drawerRows(o *streamOut) map[string]map[string]any {
	out := map[string]map[string]any{}
	for t := range drawerTables() {
		if len(o.rows[t]) > 0 {
			out[t] = o.rows[t]
		}
	}
	return out
}

func mdOf(e *canon.Event) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(e.MetadataJSON), &m)
	return m
}

func mdStr(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case map[string]any, []any:
		return ""
	default:
		b, _ := json.Marshal(x)
		return strings.Trim(string(b), `"`)
	}
}

type refEv struct {
	r  records.Record
	e  *canon.Event
	md map[string]any
	id string // batch:n
}

// refDrawer is every key's drawer rows from all the records in one pass.
func refDrawer(rs []records.Record) map[string]map[string]any {
	out := map[string]map[string]any{}
	put := func(t, key, id string, row any) {
		if out[t] == nil {
			out[t] = map[string]any{}
		}
		out[t][key+"/"+id] = row
	}
	seen := map[string]bool{}
	byKey := map[string][]records.Record{}
	for _, r := range rs {
		if seen[r.RecordID] {
			continue
		}
		seen[r.RecordID] = true
		byKey[r.RoutingKey] = append(byKey[r.RoutingKey], r)
	}
	for key, krs := range byKey {
		if strings.HasPrefix(key, "l:") {
			for _, r := range krs {
				if r.Kind == records.KindLocator {
					t := records.LocatorTarget(&r)
					put(TableDrawerLocator, key, t, DrawerLocator{Target: t})
				}
			}
			continue
		}
		sid := sessionOf(key)
		calls := map[string]DrawerCall{}
		var evs []refEv
		for _, r := range krs {
			switch r.Kind {
			case records.KindCallEnd, records.KindWSConnection:
				calls[r.CallID] = DrawerCall{Request: *r.Call, BatchSession: r.BatchSession}
			case records.KindCallStart:
				if c, ok := calls[r.CallID]; !ok || c.InFlight {
					calls[r.CallID] = DrawerCall{Request: *r.Call, InFlight: true}
				}
			case records.KindClientEvent:
				if r.Event.Source == canon.ProductClaudeCode {
					evs = append(evs, refEv{r, r.Event, mdOf(r.Event), fmt.Sprintf("%s:%d", r.BatchID, r.BatchCCN)})
				}
			}
		}
		for id, c := range calls {
			put(TableDrawerCalls, key, id, c)
		}
		sort.Slice(evs, func(i, j int) bool {
			a, b := evs[i], evs[j]
			if a.e.NS != b.e.NS {
				return a.e.NS < b.e.NS
			}
			if a.e.CorrelationID != b.e.CorrelationID {
				return a.e.CorrelationID < b.e.CorrelationID
			}
			return a.r.BatchCCN < b.r.BatchCCN
		})
		for _, ev := range evs {
			put(TableDrawerEvents, key, ev.id, drawerEventOf(ev.id, ev.e))
			if ev.e.Name == "tengu_api_success" {
				rid := mdStr(ev.md, "requestId")
				if rid == "" {
					rid = mdStr(ev.md, "clientRequestId")
				}
				if rid == "" {
					rid = fmt.Sprint(ev.e.NS)
				}
				if _, ok := out[TableDrawerReports][key+"/"+rid]; !ok {
					put(TableDrawerReports, key, rid, DrawerReport{ReportID: rid, Event: drawerEventOf(ev.id, ev.e)})
				}
			}
		}
		// The sandbox setting where it changes.
		last := ""
		for _, ev := range evs {
			if ev.e.Name != "tengu_bash_tool_command_executed" {
				continue
			}
			v := mdStr(ev.md, "sandbox_enabled")
			if v == "" || v == last {
				continue
			}
			last = v
			put(TableDrawerSandbox, key, ev.id, DrawerSandbox{TS: ev.e.NS, EventID: ev.id, Value: v})
		}
		// Steps: the visible model calls, linked by the message-id chain.
		var steps []DrawerStep
		for id, c := range calls {
			if s, ok := stepOf(id, c, sid); ok {
				steps = append(steps, s)
			}
		}
		sort.Slice(steps, func(i, j int) bool { return lessStep(steps[i], steps[j]) })
		byMID, byPrev := map[string]string{}, map[string]string{}
		for _, s := range steps {
			if s.MessageID != "" {
				byMID[s.MessageID] = s.CallID
			}
			if s.PrevMessageID != "" {
				byPrev[s.PrevMessageID] = s.CallID
			}
		}
		for _, s := range steps {
			if s.PrevMessageID != "" {
				s.PrevCall = byMID[s.PrevMessageID]
			}
			if s.MessageID != "" {
				s.NextCall = byPrev[s.MessageID]
			}
			put(TableDrawerSteps, key, s.CallID, s)
		}
		// Facts, per call of the session.
		if sid == "" {
			continue
		}
		for id, c := range calls {
			for _, f := range refFacts(id, c, evs) {
				put(TableDrawerFacts, key, f.CallID+"/"+f.Role+"/"+f.Event.EventID, f)
			}
		}
	}
	return out
}

func refFacts(id string, c DrawerCall, evs []refEv) []DrawerFact {
	rid, crid, prompt := c.RequestID, c.ClientRequestID, c.PromptID
	ts := c.StartNS
	if ts == 0 {
		ts = c.EndNS
	}
	cid := strings.ReplaceAll(id, "#", "~")
	var out []DrawerFact
	add := func(role string, ev refEv) {
		out = append(out, DrawerFact{CallID: cid, Role: role, Event: drawerEventOf(ev.id, ev.e)})
	}
	first := func(match func(refEv) bool) *refEv {
		for i := range evs {
			if match(evs[i]) {
				return &evs[i]
			}
		}
		return nil
	}
	succ := func(x string) func(refEv) bool {
		return func(ev refEv) bool {
			return ev.e.Name == "tengu_api_success" && (mdStr(ev.md, "requestId") == x || mdStr(ev.md, "clientRequestId") == x)
		}
	}
	var s *refEv
	if rid != "" {
		s = first(succ(rid))
	}
	if s == nil && crid != "" {
		s = first(succ(crid))
	}
	if s != nil {
		add(RoleSuccess, *s)
	}
	if rid != "" {
		if st := first(func(ev refEv) bool {
			return ev.e.Name == "tengu_streaming_stall_summary" && mdStr(ev.md, "request_id") == rid
		}); st != nil {
			add(RoleStall, *st)
		}
		tools := map[string]bool{}
		for _, ev := range evs {
			if (ev.e.Name == "tengu_tool_use_success" || ev.e.Name == "tengu_tool_use_error") && mdStr(ev.md, "requestId") == rid {
				add(RoleTool, ev)
				if t := mdStr(ev.md, "toolUseID"); t != "" {
					tools[t] = true
				}
			}
		}
		for _, ev := range evs {
			if ev.e.Name == "tengu_bash_tool_command_executed" && tools[mdStr(ev.md, "tool_use_id")] {
				add(RoleBash, ev)
			}
		}
	}
	if prompt != "" {
		var q *refEv
		for i := range evs {
			ev := evs[i]
			if ev.e.Name == "tengu_api_query" && ev.e.PromptID == prompt && ts-60*secNS <= ev.e.NS && ev.e.NS <= ts+2*secNS {
				q = &evs[i]
			}
		}
		if q != nil {
			add(RoleQuery, *q)
		}
	}
	if callFailed(c) {
		var best *refEv
		var bestD, bestAt int64
		stop := c.EndNS
		if stop == 0 {
			stop = ts
		}
		for i := range evs {
			ev := evs[i]
			if ev.e.Name != "tengu_api_retry" {
				continue
			}
			// The retries the drawer sees: its prompt's, or naming its ids.
			if !(prompt != "" && ev.e.PromptID == prompt || rid != "" && ev.e.RequestID == rid || crid != "" && ev.e.ClientRequestID == crid) {
				continue
			}
			at := ev.e.NS
			if at < ts || at > stop+30*secNS || prompt != "" && ev.e.PromptID != "" && ev.e.PromptID != prompt {
				continue
			}
			d := at - stop
			if d < 0 {
				d = -d
			}
			if best == nil || d < bestD || d == bestD && at < bestAt {
				best, bestD, bestAt = &evs[i], d, at
			}
		}
		if best != nil {
			add(RoleRetry, *best)
		}
	}
	return out
}

// --- atomic --------------------------------------------------------------------

func withMD(r records.Record, md string, set func(*canon.Event)) records.Record {
	e := *r.Event
	e.MetadataJSON = md
	if set != nil {
		set(&e)
	}
	r.Event = &e
	return r
}

func drawerCall(id, s string, t int64, set func(*canon.Request)) *canon.Request {
	c := ccCall(id, s, t)
	c.PromptID, c.RequestID, c.MessageID = "p1", "req_"+id, "msg_"+id
	if set != nil {
		set(c)
	}
	return c
}

func locatorOf(id string, c *canon.Request) records.Record {
	return rec(records.Record{Kind: records.KindLocator, CallID: id, RoutingKey: "l:" + id, Call: c})
}

func TestDrawerAtomic(t *testing.T) {
	recSeq, batchN = 0, map[string]int64{}
	c1 := drawerCall("c1", "s1", 1000, nil)
	c2 := drawerCall("c2", "s1", 3000, func(c *canon.Request) { c.PreviousMessageID = "msg_c1" })
	failed := drawerCall("cf", "s1", 5000, func(c *canon.Request) { c.Status = 529 })
	succ := withMD(ccEvent("s1", "tengu_api_success", 1600, ""), `{"requestId":"req_c1","costUSD":0.01}`, nil)
	succCR := withMD(ccEvent("s1", "tengu_api_success", 1600, ""), `{"clientRequestId":"cr_1"}`, nil)
	stall := withMD(ccEvent("s1", "tengu_streaming_stall_summary", 1700, ""), `{"request_id":"req_c1","stall_count":2}`, nil)
	tool := withMD(ccEvent("s1", "tengu_tool_use_success", 1800, ""), `{"requestId":"req_c1","toolUseID":"tu1","toolName":"Bash"}`, nil)
	bash := withMD(ccEvent("s1", "tengu_bash_tool_command_executed", 1900, ""), `{"tool_use_id":"tu1","sandbox_enabled":"true"}`, nil)
	query := withMD(ccEvent("s1", "tengu_api_query", 990, ""), `{}`, func(e *canon.Event) { e.PromptID = "p1" })
	oldQuery := withMD(ccEvent("s1", "tengu_api_query", 900-61*1000, ""), `{}`, func(e *canon.Event) { e.PromptID = "p1" })
	retry := withMD(ccEvent("s1", "tengu_api_retry", 5600, ""), `{"attempt":1}`, func(e *canon.Event) { e.PromptID = "p1" })
	sand2 := withMD(ccEvent("s1", "tengu_bash_tool_command_executed", 2500, ""), `{"tool_use_id":"tu9","sandbox_enabled":"true"}`, nil)
	for _, c := range []struct {
		name  string
		prior []records.Record
		rec   records.Record
		cases []string
	}{
		{"locator: only the locator", nil, locatorOf("c1", c1), []string{CaseDrawerLocator}},
		{"call start: in flight", nil, callStart(c1), []string{CaseDrawerCallStart}},
		{"call end ends it", []records.Record{callStart(c1)}, callEnd(c1), []string{CaseDrawerCallEnd}},
		{"start after its end: ignored", []records.Record{callEnd(c1)}, callStart(c1), nil},
		{"step links its previous", []records.Record{callEnd(c1)}, callEnd(c2), []string{CaseDrawerCallEnd, CaseDrawerChain}},
		{"success: by request id", []records.Record{callEnd(c1)}, succ, []string{CaseDrawerEvent, CaseDrawerFact}},
		{"success before its call", []records.Record{succ}, callEnd(c1), []string{CaseDrawerCallEnd, CaseDrawerFact}},
		{"success: by client request id", []records.Record{callEnd(drawerCall("c1", "s1", 1000, func(c *canon.Request) { c.RequestID, c.ClientRequestID = "", "cr_1" }))}, succCR, []string{CaseDrawerEvent, CaseDrawerFact}},
		{"stall summary", []records.Record{callEnd(c1)}, stall, []string{CaseDrawerEvent, CaseDrawerFact}},
		{"tool use", []records.Record{callEnd(c1)}, tool, []string{CaseDrawerEvent, CaseDrawerFact}},
		{"bash of a tool use", []records.Record{callEnd(c1), tool}, bash, []string{CaseDrawerEvent, CaseDrawerFact, CaseDrawerSandbox}},
		{"query before the call", []records.Record{callEnd(c1)}, query, []string{CaseDrawerEvent, CaseDrawerFact}},
		{"query too early: none", []records.Record{callEnd(c1)}, oldQuery, []string{CaseDrawerEvent}},
		{"retry of a failed call", []records.Record{callEnd(failed)}, retry, []string{CaseDrawerEvent, CaseDrawerFact}},
		{"sandbox unchanged: no row", []records.Record{bash}, sand2, []string{CaseDrawerEvent}},
		{"report", nil, succ, []string{CaseDrawerEvent, CaseDrawerReport}},
	} {
		t.Run(c.name, func(t *testing.T) {
			all := append(append([]records.Record(nil), c.prior...), c.rec)
			o := stream(c.prior)
			d := o.apply(c.rec)
			got := drawerRows(o)
			want := refDrawer(all)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rows\n got %s\nwant %s", ddump(got), ddump(want))
			}
			if cs := only(d.Cases, drawerCaseSet()); !sameSet(cs, c.cases) {
				t.Fatalf("cases %v, want %v", cs, c.cases)
			}
			if again := o.apply(c.rec); len(again.Changes) != 0 {
				t.Fatalf("a duplicate changed %d rows", len(again.Changes))
			}
		})
	}
}

func drawerCaseSet() map[string]bool {
	m := map[string]bool{}
	for _, c := range drawerCases {
		m[c] = true
	}
	return m
}

func sameSet(a, b []string) bool {
	x, y := map[string]bool{}, map[string]bool{}
	for _, v := range a {
		x[v] = true
	}
	for _, v := range b {
		y[v] = true
	}
	return reflect.DeepEqual(x, y)
}

func ddump(m map[string]map[string]any) string {
	b, _ := json.MarshalIndent(m, "", " ")
	return string(b)
}

// --- every order ----------------------------------------------------------------

func TestDrawerEveryOrder(t *testing.T) {
	recSeq, batchN = 0, map[string]int64{}
	c1 := drawerCall("c1", "s1", 1000, nil)
	c2 := drawerCall("c2", "s1", 3000, func(c *canon.Request) { c.PreviousMessageID = "msg_c1" })
	rs := []records.Record{
		callStart(c1), callEnd(c1), callEnd(c2),
		withMD(ccEvent("s1", "tengu_api_success", 1600, ""), `{"requestId":"req_c1"}`, nil),
		withMD(ccEvent("s1", "tengu_tool_use_success", 1800, ""), `{"requestId":"req_c1","toolUseID":"tu1"}`, nil),
		withMD(ccEvent("s1", "tengu_bash_tool_command_executed", 1900, ""), `{"tool_use_id":"tu1","sandbox_enabled":"false"}`, nil),
		withMD(ccEvent("s1", "tengu_api_query", 990, ""), `{}`, func(e *canon.Event) { e.PromptID = "p1" }),
	}
	want := refDrawer(rs)
	permute(len(rs), func(order []int) {
		var in []records.Record
		for _, i := range order {
			in = append(in, rs[i])
		}
		if got := drawerRows(stream(in)); !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v:\n got %s\nwant %s", order, ddump(got), ddump(want))
		}
	})
}

// --- generated --------------------------------------------------------------------

func generateDrawer(rng *rand.Rand, sessions, calls int) []records.Record {
	names := []string{"tengu_api_success", "tengu_api_query", "tengu_api_retry", "tengu_tool_use_success",
		"tengu_tool_use_error", "tengu_bash_tool_command_executed", "tengu_streaming_stall_summary", "tengu_input_prompt"}
	var rs []records.Record
	for s := 0; s < sessions; s++ {
		sid := fmt.Sprintf("s%d", s)
		prev := ""
		for i := 0; i < calls; i++ {
			id := fmt.Sprintf("%s-c%d", sid, i)
			t := int64(i*4000 + rng.Intn(3000))
			p := fmt.Sprintf("p%d", i/3)
			c := drawerCall(id, sid, t, func(c *canon.Request) {
				c.PromptID, c.PreviousMessageID = p, prev
				if rng.Intn(4) == 0 {
					c.Status = 529
				}
				if rng.Intn(5) == 0 {
					c.ClientRequestID, c.RequestID = "cr_"+id, ""
				}
			})
			prev = c.MessageID
			rs = append(rs, callStart(c), callEnd(c), locatorOf(id, c))
			for k := 0; k < 4; k++ {
				name := names[rng.Intn(len(names))]
				at := t + int64(rng.Intn(70000)) - 62000
				tool := fmt.Sprintf("tu%d", rng.Intn(calls*2))
				md := fmt.Sprintf(`{"requestId":%q,"clientRequestId":%q,"request_id":%q,"toolUseID":%q,"tool_use_id":%q,"sandbox_enabled":%q}`,
					c.RequestID, c.ClientRequestID, c.RequestID, tool, tool, []string{"", "true", "false"}[rng.Intn(3)])
				rs = append(rs, withMD(ccEvent(sid, name, at, ""), md, func(e *canon.Event) { e.PromptID = p }))
			}
		}
	}
	return rs
}

func TestDrawerGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		recSeq, batchN = 0, map[string]int64{}
		rng := rand.New(rand.NewSource(seed))
		rs := generateDrawer(rng, 3, 12)
		want := refDrawer(rs)
		in := shuffled(rng, rs, len(rs)/20)
		o := stream(in[:len(in)/2])
		for k, s := range o.states { // a checkpoint midway
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
		if got := drawerRows(o); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: drawer rows differ\n got %s\nwant %s", seed, ddump(got), ddump(want))
		}
	}
}

// --- flat ----------------------------------------------------------------------

// A drawer request reads its call's own rows: as its session grows from 10 to
// 10,000 events about other calls, the rows naming the call stay the same.
func TestDrawerRowsPerCallStayFlat(t *testing.T) {
	count := func(n int) int {
		recSeq, batchN = 0, map[string]int64{}
		c1 := drawerCall("c1", "s1", 1000, nil)
		rs := []records.Record{callEnd(c1), withMD(ccEvent("s1", "tengu_api_success", 1600, ""), `{"requestId":"req_c1"}`, nil)}
		for i := 0; i < n; i++ {
			rs = append(rs, withMD(ccEvent("s1", "tengu_tool_use_success", int64(100000+i), ""),
				fmt.Sprintf(`{"requestId":"req_other%d","toolUseID":"t%d"}`, i, i), nil))
		}
		got := 0
		for k, row := range stream(rs).rows[TableDrawerFacts] {
			if f := row.(DrawerFact); f.CallID == "c1" && strings.HasPrefix(k, "s:s1/") {
				got++
			}
		}
		return got
	}
	if a, b := count(10), count(10000); a != b || a == 0 {
		t.Fatalf("rows for one call: %d at 10 events, %d at 10,000", a, b)
	}
}

func TestDrawerFlatCost(t *testing.T) {
	cost := func(n int) float64 {
		recSeq, batchN = 0, map[string]int64{}
		s := New("s:big")
		for i := 0; i < n; i++ {
			c := drawerCall(fmt.Sprintf("c%d", i), "big", int64(i*1000), func(c *canon.Request) { c.PromptID = fmt.Sprintf("p%d", i/5) })
			s.Apply(callEnd(c))
			s.Apply(withMD(ccEvent("big", "tengu_api_success", int64(i*1000+500), ""), fmt.Sprintf(`{"requestId":"req_c%d"}`, i), nil))
		}
		i := n
		return testingAllocs(func() {
			c := drawerCall(fmt.Sprintf("c%d", i), "big", int64(i*1000), func(c *canon.Request) { c.PromptID = fmt.Sprintf("p%d", i/5) })
			s.Apply(callEnd(c))
			s.Apply(withMD(ccEvent("big", "tengu_api_success", int64(i*1000+500), ""), fmt.Sprintf(`{"requestId":"req_c%d"}`, i), nil))
			i++
		})
	}
	small, big := cost(10), cost(1000)
	if big > small*1.5+8 {
		t.Fatalf("allocations per call: %.0f at 10 calls, %.0f at 1,000", small, big)
	}
}

func testingAllocs(f func()) float64 { return testing.AllocsPerRun(200, f) }
