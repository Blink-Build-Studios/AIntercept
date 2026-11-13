package process

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The request rows keep the API's rules exactly (rows.py, person.py,
// claude_code.py); these cases are the ones a port gets wrong.

const msNS = 1_000_000

func buildRows(calls []rowCall, evs []eventCols, accounts []Account, people ...string) map[string][]RequestRow {
	part := newEventAgg()
	for _, e := range evs {
		part.add(e)
	}
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	d := newRowData(ptrs, []*eventAgg{part}, accounts)
	out := map[string][]RequestRow{}
	rows, _ := d.rows(people)
	for _, r := range rows {
		out[r.PersonID] = append(out[r.PersonID], r)
	}
	return out
}

// A Claude Code model call of a session, by an account, with a credential.
func ccCall(id, session, account, key string, ts int64) rowCall {
	return rowCall{id: id, corrID: id, ts: ts, end: ts + 1500*msNS, host: "api.anthropic.com", method: "POST",
		path: "/v1/messages?beta=true", status: 200, keyHash: key, session: session, person: account,
		product: productClaudeCode, class: "main", model: "claude-opus-5-5", stop: "end_turn", output: 10}
}

func ccEvent(corr, session, account, name string, ts int64, md string) eventCols {
	return eventCols{CorrelationID: corr, NS: ts, Source: productClaudeCode, Name: name, SessionID: session,
		PersonID: account, MetadataJSON: md}
}

func byID(rows []RequestRow) map[string]RequestRow {
	out := map[string]RequestRow{}
	for _, r := range rows {
		out[r.RequestID] = r
	}
	return out
}

func ids(rows []RequestRow) []string {
	return mapf(rows, func(r RequestRow) string { return r.RequestID })
}

// A person is every account with their email (any case); a session no account
// appears on is the unattributed person's. Their rows are every transaction made
// with their sessions' credentials, including ones with no session.
func TestRequestRowsPeople(t *testing.T) {
	calls := []rowCall{
		ccCall("a1", "s-a", "acct-a", "key-a", 1000),
		ccCall("a2", "s-a2", "acct-a2", "key-a2", 2000),
		ccCall("u1", "s-u", "", "key-u", 3000),
		// An account call: no session, but made with acct-a's credential.
		{id: "acct", corrID: "acct", ts: 4000, end: 5000, host: "api.anthropic.com", method: "GET",
			path: "/api/oauth/profile", status: 200, keyHash: "key-a", product: productClaudeCode},
		// A probe and someone else's call are nobody's here.
		{id: "probe", corrID: "probe", ts: 4500, host: "api.anthropic.com", path: "/v1/messages", synthetic: true, keyHash: "key-a"},
		ccCall("b1", "s-b", "acct-b", "key-b", 6000),
	}
	accounts := []Account{{AccountID: "acct-a", Email: "Pat@Example.com"}, {AccountID: "acct-a2", Email: "pat@example.com"},
		{AccountID: "acct-b", Email: "sam@example.com"}}
	got := buildRows(calls, nil, accounts, "acct-a", "acct-a2", "acct-b", unattributed)
	want := map[string][]string{
		"acct-a": {"a1", "a2", "acct"}, "acct-a2": {"a1", "a2", "acct"}, "acct-b": {"b1"}, unattributed: {"u1"},
	}
	for p, w := range want {
		if g := ids(got[p]); !reflect.DeepEqual(g, w) {
			t.Errorf("%s: rows %v, want %v", p, g, w)
		}
	}
	a := byID(got["acct-a"])["acct"]
	if a.Kind != "account" || a.Detail != "profile" || a.SessionID != nil || *a.Agent != "claude-code" || a.Background {
		t.Errorf("account call row: %+v", a)
	}
}

// Statuses, phrases and durations as Python shows them.
func TestRequestRowsStatusAndDuration(t *testing.T) {
	overloaded := ccCall("overloaded", "s", "acct", "k", 1000)
	overloaded.status, overloaded.output, overloaded.end = 529, 0, 1000+250_000 // 0.25 ms: a tie, to even
	tooLarge := ccCall("large", "s", "acct", "k", 2000)
	tooLarge.status, tooLarge.output, tooLarge.end = 413, 0, 2000+750_000
	reset := ccCall("reset", "s", "acct", "k", 3000)
	reset.status, reset.errText, reset.output, reset.down = 0, "connection reset", 0, 700*1024
	inFlight := callFromPending(Pending{CorrelationID: "flying", StartNS: 4000, Host: "api.anthropic.com", Method: "POST",
		Path: "/v1/messages", KeyHash: "k", ClientJSON: `{"x-claude-code-session-id":"s","x-claude-code-prompt-id":"p"}`})
	got := byID(buildRows([]rowCall{overloaded, tooLarge, reset, inFlight}, nil, nil, "acct")["acct"])

	o := got["overloaded"]
	if *o.Status != 529 || o.StatusText != nil || *o.ErrorMessage != "HTTP 529" || *o.DurationMS != 0.2 ||
		*o.ErrorAfterMS != 0.2 || *o.UsageMissing != "failed" {
		t.Errorf("529: %+v", o)
	}
	if l := got["large"]; *l.StatusText != "Content Too Large" || *l.DurationMS != 0.8 {
		t.Errorf("413: %+v", l)
	}
	r := got["reset"]
	if r.Status != nil || !r.StatusErr || *r.StatusText != "connection reset" || *r.ErrorMessage != "connection reset" ||
		*r.UsageMissing != "parseBuffer" || *r.ErrorBytesReceived != 700*1024 {
		t.Errorf("reset: %+v", r)
	}
	f := got["flying"]
	if f.Status != nil || f.StatusErr || f.DurationMS != nil || f.ErrorMessage != nil || *f.UsageMissing != "streamCut" ||
		*f.SessionID != "s" || f.Model != nil || *f.TokensAccuracy != "missing" || f.Client != nil {
		t.Errorf("in flight: %+v", f)
	}
}

// A failed call Claude Code retried carries its retry: the nearest tengu_api_retry
// of its prompt (the earliest of equals), and when the next attempt of the same
// prompt and class succeeded.
func TestRequestRowsRetries(t *testing.T) {
	failed := ccCall("try1", "s", "acct", "k", 1000*msNS)
	failed.status, failed.output, failed.prompt, failed.end = 529, 0, "p1", 2000*msNS
	other := ccCall("aux", "s", "acct", "k", 2500*msNS)
	other.prompt, other.class = "p1", "auxiliary" // another class: not the next attempt
	next := ccCall("try2", "s", "acct", "k", 3000*msNS)
	next.prompt = "p1"
	evs := []eventCols{
		ccEvent("b", "s", "acct", "tengu_api_retry", 2000*msNS+500*msNS, `{"cc_prompt_id":"p2","attempt":9}`), // another prompt
		ccEvent("b", "s", "acct", "tengu_api_retry", 2000*msNS-400*msNS, `{"cc_prompt_id":"p1","attempt":1}`),
		ccEvent("b", "s", "acct", "tengu_api_retry", 2000*msNS+400*msNS, `{"attempt":2}`), // as near: the earlier wins
	}
	got := byID(buildRows([]rowCall{failed, other, next}, evs, nil, "acct")["acct"])
	f := got["try1"]
	if f.Retries == nil || *f.Retries != 1 || *f.ErrorRetries != 1 || *f.ErrorRetrySucceededNS != 3000*msNS {
		t.Fatalf("retried call: %+v", f)
	}
	if got["try2"].Retries != nil || *got["try1"].StepIndex != 1 || *got["aux"].StepIndex != 2 || *got["try2"].StepIndex != 3 {
		t.Errorf("steps and retries: %+v", got)
	}
}

// What Claude Code reports: ttft and the cache split from the first
// tengu_api_success of a request id (by time), matched by request id, else client
// request id; a report no captured call matches is a not-captured row.
func TestRequestRowsReported(t *testing.T) {
	c := ccCall("c1", "s", "acct", "k", 1000)
	c.reqID, c.clientReqID = "req_1", "cr_1"
	c2 := ccCall("c2", "s", "acct", "k", 2000)
	c2.clientReqID = "cr_2"
	evs := []eventCols{
		ccEvent("b", "s", "acct", "tengu_api_success", 900, `{"requestId":"req_1","ttftMs":0,"cache_creation_5m_input_tokens":0,"cache_creation_1h_input_tokens":7}`),
		ccEvent("b", "s", "acct", "tengu_api_success", 800, `{"requestId":"req_1","ttftMs":512.5}`), // earlier: wins
		ccEvent("b", "s", "acct", "tengu_api_success", 1900, `{"clientRequestId":"cr_2","ttftMs":"33"}`),
		ccEvent("b", "s", "acct", "tengu_api_success", 5000, `{"requestId":"req_gone","ttftMs":40,"cache_creation_5m_input_tokens":3}`),
		ccEvent("b", "s", "acct", "tengu_api_success", 6000, `{"ttftMs":1}`), // no ids: named by its time
	}
	got := byID(buildRows([]rowCall{c, c2}, evs, nil, "acct")["acct"])
	if r := got["c1"]; *r.TTFTMS != 512.5 || r.TokensCacheWrite5m != nil || r.TokensCacheWrite1h != nil {
		t.Errorf("first report by time: %+v", r)
	}
	if r := got["c2"]; *r.TTFTMS != 33 {
		t.Errorf("by client request id: %+v", r)
	}
	m := got["reported:req_gone"]
	if m.Captured || m.Kind != "model" || *m.SessionID != "s" || *m.TTFTMS != 40 || *m.TokensCacheWrite5m != 3 ||
		*m.TokensAccuracy != "missing" || m.TokensOutput != nil || m.Detail != "reported by Claude Code · not captured" ||
		m.Status != nil || m.UsageMissing != nil || m.ToolCalls == nil {
		t.Errorf("not captured: %+v", m)
	}
	if _, ok := got["reported:6000"]; !ok {
		t.Errorf("a report with no ids is named by its time: %v", ids(mapValues(got)))
	}
}

// Codex: a turn's prompt number is its place in the thread (by first call, then
// id); an analytics batch is named by its event; a prewarm's output is 0.
func TestRequestRowsCodex(t *testing.T) {
	turn := func(id, prompt string, ts int64) rowCall {
		return rowCall{id: id, corrID: "ws", ts: ts, end: ts + 10, wsTurn: 1, host: "chatgpt.com", method: "GET",
			path: "/backend-api/codex/responses", status: 101, keyHash: "kc", session: "t1", person: "acct",
			prompt: prompt, product: productCodex, client: "exec", model: "gpt-6.1-sol", output: 5,
			tools: []string{"exec ×2"}}
	}
	prewarm := turn("ws~1", "", 500)
	prewarm.hidden, prewarm.output = "prewarm", 0
	calls := []rowCall{prewarm, turn("ws~3", "turn-b", 2000), turn("ws~2", "turn-a", 2000), turn("ws~4", "turn-c", 3000),
		// The connection itself isn't a row.
		{id: "ws", corrID: "ws", ts: 400, host: "chatgpt.com", method: "GET", path: "/backend-api/codex/responses",
			keyHash: "kc", product: productCodex},
		{id: "an", corrID: "an", ts: 3500, host: "chatgpt.com", method: "POST", path: "/backend-api/codex/analytics-events/events",
			status: 200, keyHash: "kc", product: productCodex},
	}
	evs := []eventCols{
		{CorrelationID: "an", NS: 3500, Source: productCodex, Name: "codex_turn_event", SessionID: "t1"},
		{CorrelationID: "an", NS: 3501, Source: productCodex, Name: "codex_app_used", SessionID: "t1"},
	}
	got := byID(buildRows(calls, evs, nil, "acct")["acct"])
	if _, ok := got["ws"]; ok || len(got) != 5 {
		t.Fatalf("rows: %v", ids(mapValues(got)))
	}
	for id, want := range map[string]int64{"ws~2": 1, "ws~3": 2, "ws~4": 3} {
		if r := got[id]; *r.PromptIndex != want || *r.Agent != "codex" || !reflect.DeepEqual(r.ToolCalls, []string{"exec ×2"}) {
			t.Errorf("%s: prompt %v, want %d: %+v", id, r.PromptIndex, want, r)
		}
	}
	if p := got["ws~1"]; *p.HiddenKind != "prewarm" || *p.TokensOutput != 0 || *p.TokensAccuracy != "exact" ||
		p.UsageMissing != nil || p.StepIndex != nil || p.PromptIndex != nil {
		t.Errorf("prewarm: %+v", p)
	}
	if a := got["an"]; a.Detail != "codex_app_used · 2 events" || !a.Background || *a.SessionID != "t1" {
		t.Errorf("analytics: %+v", a)
	}
}

// Values read from analytics metadata as DuckDB reads them: the first of a
// repeated key, numbers from strings, decimals rounded half away from zero,
// out of range is null.
func TestRequestRowsMetadataValues(t *testing.T) {
	md := jsonFields(`{"a":"x","a":"y","n":"2.5","m":-2.5,"big":3000000000,"b":true,"o":{"k": 1}}`, "a", "n", "m", "big", "b", "o")
	if s, _ := jsonString(md["a"]); s != "x" {
		t.Errorf("first key: %q", s)
	}
	if n := jsonInt(md["n"], -1<<31, 1<<31-1); n == nil || *n != 3 {
		t.Errorf("2.5: %v", n)
	}
	if n := jsonInt(md["m"], -1<<31, 1<<31-1); n == nil || *n != -3 {
		t.Errorf("-2.5: %v", n)
	}
	if n := jsonInt(md["big"], -1<<31, 1<<31-1); n != nil {
		t.Errorf("INT overflow: %v", *n)
	}
	if n := jsonInt(md["b"], -1<<31, 1<<31-1); n != nil {
		t.Errorf("bool: %v", *n)
	}
	if s, _ := jsonString(md["o"]); s != `{"k":1}` {
		t.Errorf("object: %q", s)
	}
	if _, ok := jsonString(md["missing"]); ok {
		t.Error("absent is null")
	}
}

func mapValues(m map[string]RequestRow) []RequestRow {
	out := make([]RequestRow, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	return out
}

// The table round-trips through Parquet with nulls and zeros kept apart.
func TestRequestRowsParquet(t *testing.T) {
	zero := 0.0
	rows := []RequestRow{{PersonID: "p", RequestID: "r", TTFTMS: &zero, TokensCacheWrite5m: ptr(int64(0)), ToolCalls: []string{}},
		{PersonID: "p", RequestID: "r2", ToolCalls: []string{"exec ×2"}}}
	got, err := readBack[RequestRow](EncodeParquet(rows))
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(rows)
	if string(gj) != string(wj) {
		t.Errorf("round trip:\n got %s\nwant %s", gj, wj)
	}
}

// Parts merge in table order: a batch's session is the first event's that names
// one (DuckDB's any_value), its count sums and its name is the least; a cached
// part is never changed by a merge.
func TestRequestRowsMergeParts(t *testing.T) {
	first, second := newEventAgg(), newEventAgg()
	first.add(eventCols{CorrelationID: "b", NS: 1, Source: productClaudeCode, Name: "tengu_z"})
	second.add(eventCols{CorrelationID: "b", NS: 2, Source: productClaudeCode, Name: "tengu_a", SessionID: "s2"})
	second.add(eventCols{CorrelationID: "b", NS: 3, Source: productClaudeCode, Name: "tengu_b", SessionID: "s3"})
	d := newRowData(nil, []*eventAgg{first, second}, nil)
	if b := d.batches["b"]; b.n != 3 || b.session != "s2" || b.name != "tengu_a" {
		t.Errorf("merged batch: %+v", *b)
	}
	newRowData(nil, []*eventAgg{first, second}, nil)
	if b := first.batches["b"]; b.n != 1 || b.session != "" || len(first.eventCorrs) != 0 {
		t.Errorf("a part changed by a merge: %+v", *b)
	}
}

// An analytics batch carrying several sessions' events names the earliest event's
// session, however its events are split into parts or ordered in them.
func TestBatchSessionOrderFree(t *testing.T) {
	late := eventCols{CorrelationID: "b", NS: 5, Source: productClaudeCode, Name: "tengu_a", SessionID: "s-late"}
	early := eventCols{CorrelationID: "b", NS: 1, Source: productClaudeCode, Name: "tengu_b", SessionID: "s-early"}
	tie := eventCols{CorrelationID: "b", NS: 1, Source: productClaudeCode, Name: "tengu_c", SessionID: "s-tie"}
	for _, parts := range [][][]eventCols{{{late, early, tie}}, {{tie}, {late}, {early}}, {{early}, {tie, late}}} {
		var aggs []*eventAgg
		for _, evs := range parts {
			a := newEventAgg()
			for _, e := range evs {
				a.add(e)
			}
			aggs = append(aggs, a)
		}
		if b := newRowData(nil, aggs, nil).batches["b"]; b.session != "s-early" || b.n != 3 {
			t.Errorf("%d parts: batch %+v", len(parts), *b)
		}
	}
}

// A person's newest data time is the latest of their sessions' visible model calls
// and Claude Code events (Session.last), across email-linked accounts; null with no session.
func TestPersonLast(t *testing.T) {
	calls := []rowCall{
		ccCall("a1", "s-a", "acct-a", "key-a", 1000),
		ccCall("a2", "s-a2", "acct-a2", "key-a2", 2000),
		ccCall("b1", "s-b", "acct-b", "key-b", 6000),
	}
	evs := []eventCols{
		ccEvent("batch-a", "s-a", "acct-a", "tengu_input_prompt", 9000, `{}`),
		ccEvent("batch-b", "s-b", "acct-b", "tengu_input_prompt", 5000, `{}`), // older than s-b's call
	}
	accounts := []Account{{AccountID: "acct-a", Email: "pat@example.com"}, {AccountID: "acct-a2", Email: "pat@example.com"},
		{AccountID: "acct-b"}, {AccountID: "acct-none"}}
	part := newEventAgg()
	for _, e := range evs {
		part.add(e)
	}
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	_, lasts := newRowData(ptrs, []*eventAgg{part}, accounts).rows([]string{"acct-a", "acct-a2", "acct-b", "acct-none"})
	got := map[string]any{}
	for _, l := range lasts {
		if l.LastNS == nil {
			got[l.PersonID] = nil
		} else {
			got[l.PersonID] = *l.LastNS
		}
	}
	want := map[string]any{"acct-a": int64(9000), "acct-a2": int64(9000), "acct-b": int64(6000), "acct-none": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("person last: %v, want %v", got, want)
	}
}
