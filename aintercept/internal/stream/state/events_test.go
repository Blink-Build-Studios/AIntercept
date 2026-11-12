package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The event rows and session tools, rebuilt the way the query does: the stored
// rows numbered by (time, batch, number), joined to the request links, prompt
// numbers and Codex turns (the SQL in the API does the same).
func eventRowsOf(o *streamOut) []process.EventRow {
	bySess := map[string][]Event{}
	for _, r := range o.rows[TableEvents] {
		e := r.(Event)
		bySess[e.SessionID] = append(bySess[e.SessionID], e)
	}
	links := map[[2]string]RequestLink{}
	for _, r := range o.rows[TableRequestLinks] {
		l := r.(RequestLink)
		links[[2]string{l.SessionID, l.RequestID}] = l
	}
	index := map[string]map[string]int64{}
	for _, r := range o.rows[TablePromptIndex] {
		p := r.(PromptIndex)
		if index[p.SessionID] == nil {
			index[p.SessionID] = map[string]int64{}
		}
		index[p.SessionID][p.PromptID] = p.Index
	}
	turns := map[string][]CodexTurn{}
	for _, r := range o.rows[TableCodexTurns] {
		c := r.(CodexTurn)
		turns[c.SessionID] = append(turns[c.SessionID], c)
	}
	for s, ts := range turns { // dense_rank over (first call, prompt): the Codex number wins
		sort.Slice(ts, func(i, j int) bool {
			return ts[i].FirstNS < ts[j].FirstNS || ts[i].FirstNS == ts[j].FirstNS && ts[i].PromptID < ts[j].PromptID
		})
		if index[s] == nil {
			index[s] = map[string]int64{}
		}
		for n, c := range ts {
			index[s][c.PromptID] = int64(n + 1)
		}
	}
	var out []process.EventRow
	var sids []string
	for s := range bySess {
		sids = append(sids, s)
	}
	sort.Strings(sids)
	for _, s := range sids {
		evs := bySess[s]
		sort.Slice(evs, func(i, j int) bool {
			a, b := evs[i], evs[j]
			switch {
			case a.TsNS != b.TsNS:
				return a.TsNS < b.TsNS
			case a.BatchID != b.BatchID:
				return a.BatchID < b.BatchID
			case a.EvSeq != b.EvSeq:
				return a.EvSeq < b.EvSeq
			case a.RowNS != b.RowNS:
				return a.RowNS < b.RowNS
			case a.Name != b.Name:
				return a.Name < b.Name
			case a.OrderMD != b.OrderMD:
				return a.OrderMD < b.OrderMD
			case a.N != b.N: // identical events: the rank ingest gave them
				return a.N < b.N
			}
			return a.EventKey < b.EventKey
		})
		for i, e := range evs {
			r := e.EventRow
			r.Seq = int64(i)
			if n, ok := index[s][e.PromptID]; ok && e.PromptID != "" {
				r.PromptIndex = &n
			}
			switch r.LinkKind {
			case "request":
				if l, ok := links[[2]string{s, r.LinkID}]; ok {
					r.LinkID, r.LinkLabel = l.CallID, l.Label
				} else {
					r.LinkID, r.LinkLabel = "reported:"+r.LinkID, "not captured"
				}
			case "prompt":
				if n, ok := index[s][r.LinkID]; ok {
					r.LinkLabel = "#" + strconv.FormatInt(n, 10)
				} else {
					r.LinkLabel = "prompt"
				}
			}
			out = append(out, r)
		}
	}
	return out
}

func sessionToolsOf(o *streamOut) []process.SessionTool {
	var out []process.SessionTool
	for _, r := range o.rows[TableSessionTools] {
		out = append(out, r.(process.SessionTool))
	}
	sortTools(out)
	return out
}

func sortTools(out []process.SessionTool) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
}

func checkEvents(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	ref := reference(rs)
	if got, want := eventRowsOf(o), ref.EventRows; !(len(got) == 0 && len(want) == 0) && !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: event rows differ\n got %s\nwant %s", label, dump(got), dump(want))
	}
	want := append([]process.SessionTool(nil), ref.SessionTools...)
	sortTools(want)
	if got := sessionToolsOf(o); !(len(got) == 0 && len(want) == 0) && !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: session tools differ\n got %+v\nwant %+v", label, got, want)
	}
}

func dump(rows []process.EventRow) string {
	s := ""
	for _, r := range rows {
		pi := "nil"
		if r.PromptIndex != nil {
			pi = strconv.FormatInt(*r.PromptIndex, 10)
		}
		s += fmt.Sprintf("\n  %s #%d %s %s %s|%s|%s pi=%s", r.SessionID, r.Seq, r.EventID, r.Name, r.LinkKind, r.LinkID, r.LinkLabel, pi)
	}
	return s
}

func withReq(c *canon.Request, rid string) *canon.Request { c.RequestID = rid; return c }

func codexCall(id, thread, turn string, t int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: t * ms, EndNS: (t + 300) * ms, Host: "chatgpt.com",
		Method: "POST", Path: "/backend-api/codex/responses", Status: 200, SessionID: thread, PromptID: turn,
		Product: canon.ProductCodex, KeyHash: "k2"}
}

func inputPrompt(s, pid string, n string, t int64) records.Record {
	return ccEvent(s, "tengu_input_prompt", t, fmt.Sprintf(`{"cc_prompt_id":%q,"prompt_index":%s}`, pid, n))
}

var eventCases = []atomicCase{
	{"event linking to the session", nil,
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{"a":1}`) },
		[]string{CaseEventRow + ".session"}},
	{"event linking to a device",
		nil, func() records.Record {
			return ccEventWith("s1", "tengu_event_loop_stall", 1000, `{}`, func(e *canon.Event) { e.DeviceID = "dev1" })
		},
		[]string{CaseEventRow + ".device"}},
	{"event linking to a tool, and the tool it used", nil,
		func() records.Record {
			return ccEvent("s1", "tengu_tool_use_success", 1000, `{"toolUseID":"tu1","toolName":"Bash"}`)
		},
		[]string{CaseEventRow + ".tool", CaseSessionTool}},
	{"an MCP tool, named by its server",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_tool_use_success", 5000, `{"toolName":"mcp__srv__go"}`)}
		},
		func() records.Record {
			return ccEvent("s1", "tengu_tool_use_success", 1000, `{"toolName":"mcp__srv__go"}`) // earlier: the first time moves
		},
		[]string{CaseEventRow + ".session", CaseSessionTool}},
	{"a tool link with no tool name", nil,
		func() records.Record { return ccEvent("s1", "tengu_other", 1000, `{"tool_use_id":"tu9"}`) },
		[]string{CaseEventRow + ".tool"}},
	{"a later use of a tool keeps its first time",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_tool_use_success", 1000, `{"toolName":"Read"}`)}
		},
		func() records.Record { return ccEvent("s1", "tengu_tool_use_success", 5000, `{"toolName":"Read"}`) },
		[]string{CaseEventRow + ".session"}},
	{"event linking to a request no call has", nil,
		func() records.Record { return ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`) },
		[]string{CaseEventRow + ".request"}},
	{"a call arriving for a linked request",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`)}
		},
		func() records.Record { return callEnd(withReq(ccCall("c1", "s1", 900), "r1")) },
		[]string{CaseRequestLink}},
	{"a call in flight links, unlabelled by its model",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`)}
		},
		func() records.Record { return callStart(withReq(ccCall("c1", "s1", 900), "r1")) },
		[]string{CaseRequestLink}},
	{"its ending relabels it",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`), callStart(withReq(ccCall("c1", "s1", 900), "r1"))}
		},
		func() records.Record {
			c := withReq(ccCall("c1", "s1", 900), "r1")
			c.Model, c.StopReason, c.RequestClass = "claude-opus-4-1", "end_turn", "main"
			return callEnd(c)
		},
		[]string{CaseRequestLink}},
	{"the newer of two calls with one request id",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`), callEnd(withReq(ccCall("c1", "s1", 900), "r1"))}
		},
		func() records.Record { return callEnd(withReq(ccCall("c2", "s1", 950), "r1")) },
		[]string{CaseRequestLink}},
	{"a hidden call doesn't link",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`)}
		},
		func() records.Record { return callEnd(hidden(withReq(ccCall("c1", "s1", 900), "r1"))) },
		nil},
	{"a call's tool uses are session tools", nil,
		func() records.Record {
			c := ccCall("c1", "s1", 900)
			c.ToolUsesJSON = `[{"kind":"builtin","tool":"Edit"},{"kind":"mcp","server":"gh","tool":"pr"},{"kind":"mcp","tool":"x"}]`
			return callEnd(c)
		},
		[]string{CaseSessionTool, CaseSessionTool, CaseSessionTool}},
	{"event linking to a prompt with no number", nil,
		func() records.Record {
			return ccEventWith("s1", "tengu_api_query", 1000, `{}`, func(e *canon.Event) { e.PromptID = "p1" })
		},
		[]string{CaseEventRow + ".prompt"}},
	{"an input prompt numbers its prompt",
		func() []records.Record {
			return []records.Record{ccEventWith("s1", "tengu_api_query", 2000, `{}`, func(e *canon.Event) { e.PromptID = "p1" })}
		},
		func() records.Record { return inputPrompt("s1", "p1", "3", 1000) },
		[]string{CaseEventRow + ".session", CasePromptIndex}},
	{"a later input prompt renumbers it",
		func() []records.Record { return []records.Record{inputPrompt("s1", "p1", "3", 1000)} },
		func() records.Record { return inputPrompt("s1", "p1", `"4"`, 2000) },
		[]string{CaseEventRow + ".session", CasePromptIndex}},
	{"an earlier input prompt doesn't",
		func() []records.Record { return []records.Record{inputPrompt("s1", "p1", "3", 2000)} },
		func() records.Record { return inputPrompt("s1", "p1", "4", 1000) },
		[]string{CaseEventRow + ".session"}},
	{"a Codex turn's first call numbers it",
		func() []records.Record { return []records.Record{callEnd(codexCall("x1", "t1", "turn-b", 5000))} },
		func() records.Record { return callEnd(codexCall("x2", "t1", "turn-a", 1000)) },
		[]string{CaseCodexTurn}},
	{"an earlier call of the same turn",
		func() []records.Record { return []records.Record{callEnd(codexCall("x1", "t1", "turn-a", 5000))} },
		func() records.Record { return callEnd(codexCall("x2", "t1", "turn-a", 1000)) },
		[]string{CaseCodexTurn}},
}

var eventCaseSet = map[string]bool{
	CaseEventRow + ".request": true, CaseEventRow + ".prompt": true, CaseEventRow + ".tool": true,
	CaseEventRow + ".device": true, CaseEventRow + ".session": true,
	CasePromptIndex: true, CaseSessionTool: true, CaseRequestLink: true, CaseCodexTurn: true,
}

func TestEventsAtomic(t *testing.T) {
	for _, c := range eventCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{} // a case's events are its batches' whole
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkEvents(t, c.name, append(before, r), o)
			if got := only(d.Cases, eventCaseSet); !reflect.DeepEqual(got, c.cases) {
				t.Fatalf("cases %v, want %v", got, c.cases)
			}
			if d2 := o.apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

func TestEventsShortSequencesEveryOrder(t *testing.T) {
	sets := map[string]func() []records.Record{
		"request link, start, end, a second call": func() []records.Record {
			c := withReq(ccCall("c1", "s1", 900), "r1")
			return []records.Record{ccEvent("s1", "tengu_api_success", 1000, `{"requestId":"r1"}`), callStart(c), callEnd(c),
				callEnd(withReq(ccCall("c2", "s1", 800), "r1"))}
		},
		"prompt numbers and links": func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "2", 1000), inputPrompt("s1", "p1", "5", 3000),
				ccEventWith("s1", "tengu_api_query", 2000, `{}`, func(e *canon.Event) { e.PromptID = "p1" }),
				ccEvent("s1", "tengu_tool_use_success", 2500, `{"toolName":"Read"}`), ccEvent("s1", "tengu_tool_use_success", 500, `{"toolName":"Read"}`)}
		},
		"codex turns": func() []records.Record {
			return []records.Record{callEnd(codexCall("x1", "t1", "a", 5000)), callEnd(codexCall("x2", "t1", "b", 3000)),
				callEnd(codexCall("x3", "t1", "a", 1000))}
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
			checkEvents(t, fmt.Sprintf("%s %v", name, p), rs, stream(ordered))
		})
	}
}

// generateEvents is n sessions of events of every link kind, prompts, tools and
// calls with request ids and tool uses, and Codex threads.
func generateEvents(rng *rand.Rand, n, per int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("es-%d", i)
		t := int64(1_000_000 + rng.Intn(100_000))
		for j := 0; j < per; j++ {
			t += int64(rng.Intn(3000))
			rid := fmt.Sprintf("r%d", rng.Intn(per/4+1))
			pid := fmt.Sprintf("p%d", rng.Intn(5))
			tool := []string{"Read", "Edit", "Bash", "mcp__gh__pr"}[rng.Intn(4)]
			switch rng.Intn(9) {
			case 0:
				out = append(out, ccEvent(s, "tengu_api_success", t, fmt.Sprintf(`{"requestId":%q}`, rid)))
			case 1:
				out = append(out, ccEvent(s, "tengu_tool_use_success", t, fmt.Sprintf(`{"toolUseID":"tu%d","toolName":%q}`, j, tool)))
			case 2:
				out = append(out, inputPrompt(s, pid, strconv.Itoa(rng.Intn(9)), t))
			case 3:
				out = append(out, ccEventWith(s, "tengu_api_query", t, `{}`, func(e *canon.Event) { e.PromptID = pid }))
			case 4:
				out = append(out, ccEventWith(s, "tengu_event_loop_stall", t, `{}`, func(e *canon.Event) { e.DeviceID = "d" }))
			case 5:
				out = append(out, ccEvent(s, "tengu_other", t, `{"x":"y"}`))
			default:
				c := withReq(ccCall(fmt.Sprintf("c-%d-%d", i, j), s, t), rid)
				c.Model, c.StopReason, c.RequestClass = []string{"", "claude-opus-4-1"}[rng.Intn(2)], "end_turn", "main"
				c.ToolUsesJSON = fmt.Sprintf(`[{"kind":"builtin","tool":%q}]`, tool)
				if rng.Intn(5) == 0 {
					c.HiddenKind = "title"
				}
				if rng.Intn(3) > 0 {
					out = append(out, callStart(c))
				}
				if rng.Intn(5) > 0 {
					out = append(out, callEnd(c))
				}
			}
		}
		th := fmt.Sprintf("thread-%d", i)
		for j := 0; j < per/10; j++ {
			out = append(out, callEnd(codexCall(fmt.Sprintf("x-%d-%d", i, j), th, fmt.Sprintf("turn%d", rng.Intn(6)), int64(1_000_000+rng.Intn(500_000)))))
		}
	}
	return out
}

func TestEventsGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 20/seedFrac; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := append(generateEvents(rng, 3, 200), generateSessions(rng, 2, 100)...)
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
		checkEvents(t, fmt.Sprintf("seed %d", seed), rs, o)
		checkPresence(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

func TestEventsFlatCost(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100,000-record session")
	}
	measure := func(n int) (float64, time.Duration) {
		s := New("s:big")
		for i := 0; i < n; i++ {
			switch i % 4 {
			case 0:
				s.Apply(ccEvent("big", "tengu_api_success", int64(i)*1000, fmt.Sprintf(`{"requestId":"r%d"}`, i)))
			case 1:
				s.Apply(callEnd(withReq(ccCall(fmt.Sprintf("c%d", i), "big", int64(i)*1000), fmt.Sprintf("r%d", i-1))))
			case 2:
				s.Apply(ccEvent("big", "tengu_tool_use_success", int64(i)*1000, fmt.Sprintf(`{"toolName":"T%d"}`, i%50)))
			default:
				s.Apply(inputPrompt("big", fmt.Sprintf("p%d", i), "1", int64(i)*1000))
			}
		}
		pre := make([]records.Record, 3000)
		for k := range pre {
			at := int64(n)*1000 + int64(k)
			switch k % 3 {
			case 0:
				pre[k] = ccEvent("big", "tengu_api_success", at, `{"requestId":"rx"}`)
			case 1:
				pre[k] = callEnd(withReq(ccCall(fmt.Sprintf("cx%d", k), "big", at), "rx"))
			default:
				pre[k] = ccEvent("big", "tengu_tool_use_success", at, `{"toolName":"T1"}`)
			}
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
	a100k, t100k := measure(flatBig)
	t.Logf("per record: n=10 %.1f allocs %v; n=1,000 %.1f allocs %v; n=100,000 %.1f allocs %v", a10, t10, a1k, t1k, a100k, t100k)
	if a100k > a10*1.5+4 {
		t.Errorf("allocations grow with the session: %.1f at 10, %.1f at 100,000", a10, a100k)
	}
	if t100k > 8*t10+20*time.Microsecond {
		t.Errorf("time grows with the session: %v at 10, %v at 100,000", t10, t100k)
	}
}
