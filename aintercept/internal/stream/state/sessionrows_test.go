package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Session rows: one stored row per session key plus its list rows (prompt ids,
// devices, call accounts, missed reports), a child's edge to its parent, and a
// title thread's title calls. sessionRowsOf rebuilds today's session_rows from
// them, step for step as the API's SQL does (api/src/aintercept_api/sql/
// session_rows.sql); both are checked against the reference.

// sessionRowsOf is the query's rebuild of session_rows from the stored rows.
func sessionRowsOf(o *streamOut) []process.SessionRow {
	list := func(table string, f func(any) (string, string)) map[string][]string {
		m := map[string]map[string]bool{}
		for _, r := range o.rows[table] {
			s, v := f(r)
			if m[s] == nil {
				m[s] = map[string]bool{}
			}
			m[s][v] = true
		}
		out := map[string][]string{}
		for s, set := range m {
			for v := range set {
				out[s] = append(out[s], v)
			}
			sort.Strings(out[s])
		}
		return out
	}
	prompts := list(TableSessionPromptIDs, func(r any) (string, string) { x := r.(SessionPromptID); return x.SessionID, x.PromptID })
	devices := list(TableSessionDevices, func(r any) (string, string) { x := r.(SessionDevice); return x.SessionID, x.DeviceID })
	accounts := list(TableSessionCallAccounts, func(r any) (string, string) {
		x := r.(SessionCallAccount)
		return x.SessionID, x.AccountID
	})
	missed := map[string][]int64{}
	for _, r := range o.rows[TableSessionMissed] {
		x := r.(SessionMissed)
		missed[x.SessionID] = append(missed[x.SessionID], x.TSNS)
	}
	var edges []SessionEdge
	for _, r := range o.rows[TableSessionEdges] {
		edges = append(edges, r.(SessionEdge))
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		return a.ChildFirstNS < b.ChildFirstNS || a.ChildFirstNS == b.ChildFirstNS && a.ChildSessionID < b.ChildSessionID
	})
	kids, kinds := map[string][]string{}, map[string][]string{}
	for _, e := range edges {
		kids[e.ParentSessionID] = append(kids[e.ParentSessionID], e.ChildSessionID)
		kinds[e.ParentSessionID] = append(kinds[e.ParentSessionID], e.Kind)
	}

	var out []process.SessionRow
	for _, r := range o.rows[TableSessionRows] {
		k := r.(SessionRowK)
		x := k.row()
		x.PromptIDs, x.Devices, x.CallAccounts = prompts[k.SessionID], devices[k.SessionID], accounts[k.SessionID]
		if k.Events != nil {
			x.CovWindowFrom, x.CovWindowTo, x.CovWindowCount = process.CoverageWindows(missed[k.SessionID])
		}
		x.ChildrenJSON = process.SessionChildrenJSON(kids[k.SessionID], kinds[k.SessionID])
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })

	// Codex titles: a title thread's title call names the same account's Codex
	// thread (without a title of its own) that started last before it, within
	// five minutes, each in turn; alone, a thread's earliest such title.
	var titles []SessionTitleCall
	for _, r := range o.rows[TableSessionTitleCalls] {
		titles = append(titles, r.(SessionTitleCall))
	}
	sort.Slice(titles, func(i, j int) bool {
		a, b := titles[i], titles[j]
		return a.TSNS < b.TSNS || a.TSNS == b.TSNS && a.CallID < b.CallID
	})
	var codex []*process.SessionRow
	for i := range out {
		if r := &out[i]; r.Product == process.ProductCodex && r.Title == nil && r.ModelCalls > 0 {
			codex = append(codex, r)
		}
	}
	has := func(r *process.SessionRow, a string) bool {
		for _, x := range r.CallAccounts {
			if x == a {
				return true
			}
		}
		return false
	}
	for _, t := range titles {
		if t.Title == "" {
			continue
		}
		var pick *process.SessionRow
		for _, r := range codex {
			if has(r, t.AccountID) && 0 <= t.TSNS-r.StartNS && t.TSNS-r.StartNS <= process.TitleWithin {
				if r.TitleAlone == nil {
					r.TitleAlone = ptrTo(t.Title)
				}
				if r.Title == nil && (pick == nil || r.StartNS > pick.StartNS) {
					pick = r
				}
			}
		}
		if pick != nil {
			pick.Title = ptrTo(t.Title)
		}
	}
	for i := range out {
		r := &out[i]
		r.LabelText, r.LabelSource = process.SessionLabel(r.SessionID, r.Title)
		r.LabelTextAlone, r.LabelSourceAlone = process.SessionLabel(r.SessionID, r.TitleAlone)
		r.Title, r.TitleAlone = nil, nil
	}
	return out
}

func ptrTo[T any](v T) *T { return &v }

// refSessionRows is the reference's, with the fields the stored shape doesn't
// carry (Title, TitleAlone: labels carry them) cleared and empty lists as nil.
func refSessionRows(d *process.Derived) []process.SessionRow {
	out := append([]process.SessionRow(nil), d.SessionRows...)
	for i := range out {
		out[i].Title, out[i].TitleAlone = nil, nil
		normalizeSessionRow(&out[i])
	}
	return out
}

func normalizeSessionRow(r *process.SessionRow) {
	for _, p := range []*[]string{&r.PromptIDs, &r.Devices, &r.CallAccounts} {
		if len(*p) == 0 {
			*p = nil
		}
	}
	for _, p := range []*[]int64{&r.CovWindowFrom, &r.CovWindowTo, &r.CovWindowCount} {
		if len(*p) == 0 {
			*p = nil
		}
	}
}

func checkSessionRows(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	want := refSessionRows(reference(rs))
	got := sessionRowsOf(o)
	for i := range got {
		normalizeSessionRow(&got[i])
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: session rows differ: %s", label, sessionRowDiff(got, want))
	}
}

func sessionRowDiff(got, want []process.SessionRow) string {
	if len(got) != len(want) {
		var g, w []string
		for _, r := range got {
			g = append(g, r.SessionID)
		}
		for _, r := range want {
			w = append(w, r.SessionID)
		}
		return fmt.Sprintf("%d rows %v, want %d %v", len(got), g, len(want), w)
	}
	for i := range got {
		gv, wv := reflect.ValueOf(got[i]), reflect.ValueOf(want[i])
		for f := 0; f < gv.NumField(); f++ {
			if !reflect.DeepEqual(gv.Field(f).Interface(), wv.Field(f).Interface()) {
				return fmt.Sprintf("session %s field %s: got %s want %s", got[i].SessionID, gv.Type().Field(f).Name,
					sprint(deptr(gv.Field(f).Interface())), sprint(deptr(wv.Field(f).Interface())))
			}
		}
	}
	return "equal?"
}

func deptr(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "<nil>"
		}
		return rv.Elem().Interface()
	}
	return v
}

// Builders.

// richCall is a Claude Code model call with the fields the session row reads.
func richCall(id, s string, t int64, set func(*canon.Request)) *canon.Request {
	c := ccCall(id, s, t)
	c.PersonID, c.DeviceID, c.IP, c.Client, c.ClientVersion = "a1", "d1", "10.0.0.1", "claude-cli", "1.0.0"
	c.InputTokens, c.OutputTokens, c.CacheReadTokens, c.CacheWriteTokens = 10, 5, 2, 1
	c.UserAgent = "claude-cli/2.0.1 (external, cli)"
	if set != nil {
		set(c)
	}
	return c
}

// titleCall is a Codex title call in thread th, by account a, answering text.
func titleCall(id, th, a, text string, t int64) *canon.Request {
	c := codexCall(id, th, "", t)
	c.HiddenKind, c.PersonID, c.DetailJSON = "title", a, fmt.Sprintf(`{"text":%q}`, text)
	return c
}

func codexThreadCall(id, th, a string, t int64, ext, provider string) *canon.Request {
	c := codexCall(id, th, "turn-"+id, t)
	c.PersonID, c.ExtJSON, c.ProviderJSON = a, ext, provider
	return c
}

const (
	wsExt      = `{"workspaces":{"/home/u/repo":{"latest_git_commit_hash":"abcdef1234"}},"sandbox_mode":"workspace-write"}`
	limitsProv = `{"x-codex-primary-used-percent":"12.5","x-codex-primary-window-minutes":"300","x-codex-plan-type":"plus"}`
	rlProv     = `{"anthropic-ratelimit-unified-status":"allowed","anthropic-ratelimit-unified-7d-utilization":"0.25"}`
)

var sessionCases = []atomicCase{
	{"a visible call opens the session's row", nil,
		func() records.Record { return callEnd(richCall("c1", "s1", 10, nil)) },
		[]string{CaseSesCallVisible, CaseSesOrigin}},
	{"a call in flight counts, and its ending replaces it",
		func() []records.Record { return []records.Record{callStart(richCall("c1", "s1", 10, nil))} },
		func() records.Record { return callEnd(richCall("c1", "s1", 10, nil)) },
		[]string{CaseSesCallVisible, CaseSesOrigin}},
	{"a call in flight alone", nil,
		func() records.Record { return callStart(ccCall("c1", "s1", 10)) },
		[]string{CaseSesCallVisible}},
	{"an ending that's hidden takes the call back",
		func() []records.Record { return []records.Record{callStart(ccCall("c1", "s1", 10))} },
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 10))) },
		[]string{CaseSesCallUnvisible, CaseSesCallHidden, CaseSesOrigin}},
	{"a hidden call is counted apart", nil,
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 10))) },
		[]string{CaseSesCallHidden, CaseSesOrigin}},
	{"a call that isn't an agent's", nil,
		func() records.Record {
			c := ccCall("c1", "s1", 10)
			c.Path = "/v1/models"
			return callEnd(c)
		},
		[]string{CaseSesCallOther}},
	{"a later call's client, version, effort, CLI version and rate limit win",
		func() []records.Record { return []records.Record{callEnd(richCall("c1", "s1", 10, nil))} },
		func() records.Record {
			return callEnd(richCall("c2", "s1", 20, func(c *canon.Request) {
				c.Client, c.ClientVersion, c.Effort, c.UserAgent, c.ProviderJSON = "vscode", "1.1.0", "high", "claude-cli/2.1.0", rlProv
				c.IP, c.DeviceID, c.PersonID, c.ToolUsesJSON, c.OutputTokens = "10.0.0.2", "d2", "a2", `[{"name":"Bash","count":3}]`, 0
				c.PromptID, c.RequestID = "p1", "r1"
			}))
		},
		[]string{CaseSesCallVisible, CaseSesOrigin}},
	{"a Codex thread's workspace, sandbox and limits", nil,
		func() records.Record { return callEnd(codexThreadCall("x1", "t1", "a1", 10, wsExt, limitsProv)) },
		[]string{CaseSesCallVisible, CaseSesCallCodex, CaseSesOrigin}},
	{"a Codex subagent thread's edge to its parent", nil,
		func() records.Record {
			return callEnd(codexThreadCall("x1", "t2", "a1", 10, `{"parent_thread_id":"t1","subagent_kind":"thread_spawn","agent_name":"/root/w"}`, ""))
		},
		[]string{CaseSesCallVisible, CaseSesCallCodex, CaseSesOrigin, CaseSesEdge}},
	{"a fork's origin", nil,
		func() records.Record {
			return callEnd(codexThreadCall("x1", "t2", "a1", 10, `{"forked_from_thread_id":"t1","forked_from_ordinal_exclusive":3,"history_initialization":"warm_fork"}`, ""))
		},
		[]string{CaseSesCallVisible, CaseSesCallCodex, CaseSesOrigin}},
	{"a thread's own title call", nil,
		func() records.Record { return callEnd(titleCall("x1", "t1", "a1", ` "Fix the build" `, 10)) },
		[]string{CaseSesCallHidden, CaseSesCallTitle, CaseSesTitleThread, CaseSesCallCodex, CaseSesOrigin}},
	{"a title thread names an account's Codex thread",
		func() []records.Record {
			return []records.Record{callEnd(codexThreadCall("x1", "t1", "a1", 10, "", ""))}
		},
		func() records.Record { return callEnd(titleCall("y1", "tt", "a1", "Named", 60_000)) },
		[]string{CaseSesCallHidden, CaseSesCallTitle, CaseSesTitleThread, CaseSesCallCodex, CaseSesOrigin}},
	{"a title thread that gains a visible call stops naming others",
		func() []records.Record {
			return []records.Record{callEnd(codexThreadCall("x1", "t1", "a1", 10, "", "")), callEnd(titleCall("y1", "tt", "a1", "Named", 60_000))}
		},
		func() records.Record { return callEnd(codexThreadCall("x2", "tt", "a1", 70_000, "", "")) },
		[]string{CaseSesCallVisible, CaseSesCallCodex, CaseSesOrigin, CaseSesTitleDrop}},
	{"a Claude Code event opens the session's row", nil,
		func() records.Record {
			return acctEvent("s1", "a1", "d1", "tengu_api_query", 10, `{"permissionMode":"bypassPermissions"}`, `{"platform":"darwin","terminal":"iterm","shell":"zsh"}`)
		},
		[]string{CaseSesEvent, CaseSesEventQuery}},
	{"an input prompt", nil,
		func() records.Record { return inputPrompt("s1", "p1", "3", 10) },
		[]string{CaseSesEvent, CaseSesEventPrompt}},
	{"an input prompt with no id",
		nil, func() records.Record { return ccEvent("s1", "tengu_input_prompt", 10, `{"is_negative":"true"}`) },
		[]string{CaseSesEvent, CaseSesEventPrompt, CaseSesEventFriction}},
	{"a cancel and a destructive command", nil,
		func() records.Record {
			return ccEvent("s1", "tengu_bash_tool_command_executed", 10, `{"destructive_category":"git","sandbox_enabled":"true"}`)
		},
		[]string{CaseSesEvent, CaseSesEventFriction}},
	{"a report of a call not captured", nil,
		func() records.Record {
			return ccEvent("s1", "tengu_api_success", 10, `{"requestId":"r9","gzipSkipReason":"custom_ca"}`)
		},
		[]string{CaseSesEvent, CaseSesEventReport, CaseSesReportMissed}},
	{"a report of a captured call",
		func() []records.Record {
			return []records.Record{callEnd(withReq(richCall("c1", "s1", 10, nil), "r1"))}
		},
		func() records.Record { return ccEvent("s1", "tengu_api_success", 20, `{"requestId":"r1"}`) },
		[]string{CaseSesEvent, CaseSesEventReport}},
	{"a call captured after its report",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_success", 20, `{"requestId":"r1"}`)}
		},
		func() records.Record { return callEnd(withReq(richCall("c1", "s1", 10, nil), "r1")) },
		[]string{CaseSesCallVisible, CaseSesReportMatched, CaseSesOrigin}},
	{"a Codex turn event", nil,
		func() records.Record {
			r := ccEvent("t1", "codex_turn_event", 10, `{"approval_policy":"never","sandbox_policy":"read-only"}`)
			r.Event.Source = canon.ProductCodex
			return r
		},
		[]string{CaseSesEventTurn}},
}

var sessionCaseSet = map[string]bool{}

func init() {
	for _, c := range sessionCaseList {
		sessionCaseSet[c] = true
	}
}

func TestSessionRowsAtomic(t *testing.T) {
	for _, c := range sessionCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkSessionRows(t, c.name, append(before, r), o)
			want := append([]string(nil), c.cases...)
			sort.Strings(want)
			got := dedupe(only(d.Cases, sessionCaseSet))
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cases %v, want %v", got, want)
			}
			if d2 := o.apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

func TestSessionRowsShortSequencesEveryOrder(t *testing.T) {
	sets := map[string]func() []records.Record{
		"calls, a report and a prompt": func() []records.Record {
			return []records.Record{callStart(withReq(richCall("c1", "s1", 10, nil), "r1")), callEnd(withReq(richCall("c1", "s1", 10, nil), "r1")),
				ccEvent("s1", "tengu_api_success", 30, `{"requestId":"r1"}`), inputPrompt("s1", "p1", "2", 5),
				callEnd(hidden(ccCall("c2", "s1", 40)))}
		},
		"a title thread and two Codex threads": func() []records.Record {
			return []records.Record{callEnd(codexThreadCall("x1", "t1", "a1", 10, "", "")), callEnd(codexThreadCall("x2", "t2", "a1", 20_000, "", "")),
				callEnd(titleCall("y1", "tt", "a1", "First", 60_000)), callEnd(titleCall("y2", "tu", "a1", "Second", 70_000))}
		},
		"a parent, its subagent and a fork": func() []records.Record {
			return []records.Record{callEnd(codexThreadCall("x1", "t1", "a1", 10, "", "")),
				callEnd(codexThreadCall("x2", "t2", "a1", 20, `{"parent_thread_id":"t1","subagent_kind":"review"}`, "")),
				callEnd(codexThreadCall("x3", "t3", "a1", 30, `{"parent_thread_id":"t1"}`, "")),
				callEnd(codexThreadCall("x4", "t4", "a1", 15, `{"forked_from_thread_id":"t1"}`, ""))}
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
			checkSessionRows(t, fmt.Sprintf("%s %v", name, p), rs, stream(ordered))
		})
	}
}

// generateSessionRows is Claude Code sessions and Codex threads with every input
// a session row reads: calls in flight and ended, hidden and title calls, the
// fields picked newest or earliest, reports that match a call or don't, input
// prompts with and without ids, friction, queries, env, Codex workspaces,
// limits, turns, lineage, and title threads.
func generateSessionRows(rng *rand.Rand, n int) []records.Record {
	var out []records.Record
	pick := func(xs ...string) string { return xs[rng.Intn(len(xs))] }
	for i := 0; i < n; i++ {
		t := int64(1000+rng.Intn(600_000)) + int64(rng.Intn(3))*20*60*1000 // some 20 minutes apart: coverage windows split
		s := fmt.Sprintf("gs-%d", rng.Intn(5))
		th := fmt.Sprintf("gt-%d", rng.Intn(4))
		a := pick("a1", "a2", "")
		switch rng.Intn(12) {
		case 0, 1, 2:
			c := richCall(fmt.Sprintf("gc-%d", i), s, t, func(c *canon.Request) {
				c.PersonID, c.DeviceID, c.IP = a, pick("d1", "d2", ""), pick("10.0.0.1", "10.0.0.2", "")
				c.Client, c.ClientVersion, c.Effort = pick("claude-cli", "vscode", ""), pick("1.0.0", "1.2.0", ""), pick("", "low", "high")
				c.UserAgent, c.ProviderJSON = pick("claude-cli/2.0.1", "claude-cli/2.0.9", "curl/8"), pick("", rlProv)
				c.OutputTokens, c.ToolUsesJSON = int64(rng.Intn(3)), pick("", `[{"name":"Read"}]`, `[{"name":"Bash","count":2}]`)
				c.PromptID, c.RequestID, c.ClientRequestID = pick("p1", "p2", ""), pick("r1", "r2", "r3", ""), pick("cr1", "")
				c.ResponseStartNS = c.StartNS + int64(rng.Intn(3))*ms
				if rng.Intn(6) == 0 {
					c.HiddenKind = "prewarm"
				}
			})
			if rng.Intn(3) > 0 {
				out = append(out, callStart(c))
			}
			if rng.Intn(5) > 0 {
				out = append(out, callEnd(c))
			}
		case 3:
			out = append(out, acctEvent(s, a, pick("d1", "d3", ""), pick("tengu_api_query", "tengu_cancel", "tengu_input_prompt"), t,
				pick(`{}`, `{"permissionMode":"default"}`, `{"permissionMode":"bypassPermissions"}`, `{"is_negative":"true"}`),
				pick("", `{"platform":"darwin"}`, `{"platform":"linux","shell":"zsh"}`, `{"terminal":"t"}`)))
		case 4:
			out = append(out, ccEvent(s, "tengu_api_success", t, fmt.Sprintf(`{"requestId":%q,"clientRequestId":%q,"gzipSkipReason":%q}`,
				pick("r1", "r2", "r3", "r9"), pick("cr1", "cr9", ""), pick("", "custom_ca"))))
		case 5:
			out = append(out, inputPrompt(s, pick("p1", "p2", "p3"), pick("1", "2", "4"), t))
		case 6:
			out = append(out, ccEvent(s, "tengu_bash_tool_command_executed", t,
				fmt.Sprintf(`{"destructive_category":%q,"sandbox_enabled":%q}`, pick("none", "git", ""), pick("true", "false"))))
		case 7, 8:
			c := codexThreadCall(fmt.Sprintf("gx-%d", i), th, pick("a1", "a2"), t,
				pick("", wsExt, `{"parent_thread_id":"gt-0","subagent_kind":"review"}`, `{"forked_from_thread_id":"gt-1","history_initialization":"cold_fork"}`, `{"history_initialization":"cold_resume"}`),
				pick("", limitsProv, `{"x-codex-plan-type":"pro"}`))
			if rng.Intn(5) == 0 {
				c.HiddenKind = pick("prewarm", "title")
				c.DetailJSON = pick("", `{"text":"Own title"}`)
			}
			out = append(out, callEnd(c))
		case 9:
			out = append(out, callEnd(titleCall(fmt.Sprintf("gy-%d", i), fmt.Sprintf("gtt-%d", rng.Intn(6)), pick("a1", "a2"), pick("A title", "Another", ""), t)))
		case 10:
			r := ccEvent(th, "codex_turn_event", t, pick(`{"approval_policy":"never","sandbox_policy":"read-only"}`, `{"sandbox_policy":"danger"}`))
			r.Event.Source = canon.ProductCodex
			out = append(out, r)
		default:
			out = append(out, ccEvent(s, "tengu_api_query", t, `{}`))
		}
	}
	return out
}

func TestSessionRowsGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateSessionRows(rng, 250)
		in := shuffled(rng, rs, 25)
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
		checkSessionRows(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

func TestSessionRowsFlatCost(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100,000-record session")
	}
	mk := func(i int, at int64) records.Record {
		switch i % 4 {
		case 0:
			return callEnd(withReq(richCall(fmt.Sprintf("c%d", i), "big", at, func(c *canon.Request) {
				c.PromptID, c.DeviceID = fmt.Sprintf("p%d", i%50), fmt.Sprintf("d%d", i%3)
			}), fmt.Sprintf("r%d", i)))
		case 1:
			return ccEvent("big", "tengu_api_success", at, fmt.Sprintf(`{"requestId":"r%d"}`, i-1))
		case 2:
			return inputPrompt("big", fmt.Sprintf("p%d", i%50), "2", at)
		}
		return callStart(richCall(fmt.Sprintf("cs%d", i), "big", at, nil))
	}
	measure := func(n int) (float64, time.Duration) {
		s := New("s:big")
		for i := 0; i < n; i++ {
			s.Apply(mk(i, int64(i)*10))
		}
		pre := make([]records.Record, 3000)
		for k := range pre {
			pre[k] = mk(n+k, int64(n)*10+int64(k))
		}
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
	a100k, t100k := measure(100_000)
	t.Logf("per record: n=10 %.1f allocs %v; n=1,000 %.1f allocs %v; n=100,000 %.1f allocs %v", a10, t10, a1k, t1k, a100k, t100k)
	if a100k > a10*1.5+4 {
		t.Errorf("allocations grow with the session: %.1f at 10, %.1f at 100,000", a10, a100k)
	}
	if t100k > 8*t10+20*time.Microsecond {
		t.Errorf("time grows with the session: %v at 10, %v at 100,000", t10, t100k)
	}
}
