package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The prompt list (prompt rows and steps) and the session marks: every case
// atomically, every order of short sequences, long generated sequences through a
// checkpoint, and flat cost. Each is checked against the reference
// (process.DeriveRows) through the query's view (prompts_view_test.go).

// pev is a Claude Code event of session s with prompt id pid (the event's
// prompt_id column) at t ms.
func pev(s, pid, name string, t int64, md string) records.Record {
	return ccEventWith(s, name, t, md, func(e *canon.Event) { e.PromptID = pid })
}

// step is a visible model call of prompt pid: class, request ids and tokens.
func step(id, s, pid string, t int64) *canon.Request {
	c := ccCall(id, s, t)
	c.PromptID, c.RequestClass, c.Model, c.StopReason = pid, "main", "claude-opus-4-1", "end_turn"
	c.InputTokens, c.OutputTokens, c.CacheReadTokens = 100, 20, 1000
	return c
}

func withIDs(c *canon.Request, rid, crid string) *canon.Request {
	c.RequestID, c.ClientRequestID = rid, crid
	return c
}

func withClass(c *canon.Request, class string) *canon.Request { c.RequestClass = class; return c }
func withIP(c *canon.Request, ip string) *canon.Request       { c.IP = ip; return c }
func asTitle(c *canon.Request) *canon.Request                 { c.HiddenKind = "title"; return c }

func success(s string, t int64, rid string) records.Record {
	return ccEvent(s, "tengu_api_success", t, fmt.Sprintf(`{"requestId":%q,"ttftMs":12.5,"costUSD":0.01,"cache_creation_5m_input_tokens":3}`, rid))
}

func codexTurnEvent(thread, turn string, t int64, md string) records.Record {
	recSeq++
	key := fmt.Sprintf("cx-%06d", recSeq)
	b := "cbatch-" + thread
	batchN[b]++
	e := &canon.Event{EventKey: key, CorrelationID: b, RowNS: t * ms, NS: t * ms, Seq: batchN[b], Source: canon.ProductCodex,
		Name: "codex_turn_event", SessionID: thread, PromptID: turn, MetadataJSON: md}
	return rec(records.Record{Kind: records.KindClientEvent, EventKey: key, BatchID: b, BatchN: batchN[b], Event: e})
}

func prompted(pid string) func() []records.Record {
	return func() []records.Record { return []records.Record{inputPrompt("s1", pid, "1", 1000)} }
}

var promptCases = []atomicCase{
	{"an input prompt makes its prompt", nil,
		func() records.Record { return inputPrompt("s1", "p1", "3", 1000) },
		[]string{CasePromptInput, CaseMarkPromptIndex}},
	{"a later input prompt takes over the marks' number", prompted("p1"),
		func() records.Record { return inputPrompt("s1", "p1", "4", 2000) },
		[]string{CasePromptInput, CaseMarkPromptIndex}},
	{"an earlier input prompt becomes the prompt's input", prompted("p1"),
		func() records.Record { return inputPrompt("s1", "p1", "2", 500) },
		[]string{CasePromptInput}},
	{"a negative input is frustrated", nil,
		func() records.Record {
			return ccEvent("s1", "tengu_input_prompt", 1000, `{"cc_prompt_id":"p1","prompt_index":1,"is_negative":true}`)
		},
		[]string{CasePromptInput, CaseMarkFriction}},
	{"an input with no prompt id is a prompt of its own", nil,
		func() records.Record { return ccEvent("s1", "tengu_input_prompt", 1000, `{"prompt_length":12}`) },
		[]string{CasePromptInputUnnamed}},
	{"a turn end gives the prompt its duration", prompted("p1"),
		func() records.Record { return pev("s1", "p1", "tengu_turn_end", 9000, `{}`) },
		[]string{CasePromptTurnEnd}},
	{"a cancel interrupts the prompt", prompted("p1"),
		func() records.Record { return pev("s1", "p1", "tengu_cancel", 3000, `{"cc_prompt_id":"p1"}`) },
		[]string{CasePromptCancel, CaseMarkFriction}},
	{"a destructive command", prompted("p1"),
		func() records.Record {
			return pev("s1", "p1", "tengu_bash_tool_command_executed", 3000,
				`{"cc_prompt_id":"p1","destructive_category":"git_reset","destructive_target_scope":"repo","tool_use_id":"tu1","bash_argv0":"git"}`)
		},
		[]string{CasePromptDestructive, CaseStepBash, CaseMarkFriction}},
	{"a finished tool counts in its prompt", prompted("p1"),
		func() records.Record {
			return pev("s1", "p1", "tengu_tool_use_success", 3000, `{"requestId":"r1","toolName":"Read","toolUseID":"tu1"}`)
		},
		[]string{CasePromptTool, CaseStepTool}},
	{"a paste before a typed prompt", prompted("p1"),
		func() records.Record { return ccEvent("s1", "tengu_paste_text", 900, `{"pastedTextBytes":40}`) },
		[]string{CasePromptPaste}},
	{"an image paste after the prompt goes to none yet", prompted("p1"),
		func() records.Record { return ccEvent("s1", "tengu_paste_image", 1500, `{}`) },
		[]string{CasePromptPaste}},
	{"a typed prompt between a paste and its prompt takes the paste",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_paste_text", 1500, `{"pastedTextBytes":40}`), inputPrompt("s1", "p2", "2", 2000)}
		},
		func() records.Record { return inputPrompt("s1", "p1", "1", 1800) },
		[]string{CasePromptInput}},
	{"a prewarm goes to the next prompt", prompted("p1"),
		func() records.Record { return callEnd(hidden(step("h1", "s1", "", 500))) },
		[]string{CasePromptHidden}},
	{"a title goes to the prompt before it", prompted("p1"),
		func() records.Record { return callEnd(asTitle(step("h1", "s1", "", 1500))) },
		[]string{CasePromptHidden}},
	{"a new prompt takes a hidden call from its neighbour",
		func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callEnd(hidden(step("h1", "s1", "", 3000))), inputPrompt("s1", "p3", "3", 5000)}
		},
		func() records.Record { return inputPrompt("s1", "p2", "2", 4000) },
		[]string{CasePromptInput, CasePromptHidden}},
	{"a Codex turn's report", func() []records.Record {
		return []records.Record{callEnd(codexCall("x1", "th1", "turn1", 1000))}
	},
		func() records.Record {
			return codexTurnEvent("th1", "turn1", 4000, `{"status":"completed","duration_ms":3000,"shell_command_count":2}`)
		},
		[]string{CasePromptCodexReport}},
	{"a step", prompted("p1"),
		func() records.Record { return callEnd(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1")) },
		[]string{CaseStepCall, CaseMarkCall}},
	{"a step in flight", prompted("p1"),
		func() records.Record { return callStart(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1")) },
		[]string{CaseStepPending, CaseMarkCall}},
	{"its ending replaces a step in flight",
		func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callStart(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1"))}
		},
		func() records.Record { return callEnd(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1")) },
		[]string{CaseStepCall, CaseMarkCall}},
	{"its ending hides a step in flight",
		func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callStart(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1"))}
		},
		func() records.Record { return callEnd(hidden(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1"))) },
		[]string{CaseStepGone, CasePromptHidden}},
	{"a report reaches its step and call mark",
		func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callEnd(withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1"))}
		},
		func() records.Record { return success("s1", 2600, "r1") },
		[]string{CaseStepSuccess, CaseMarkReported, CaseMarkUnreported}},
	{"a report by client request id", func() []records.Record {
		return []records.Record{callEnd(withIDs(step("c1", "s1", "p1", 2000), "", "cr1"))}
	},
		func() records.Record {
			return ccEvent("s1", "tengu_api_success", 2600, `{"clientRequestId":"cr1","ttftMs":3}`)
		},
		[]string{CaseStepSuccess, CaseMarkReported, CaseMarkUnreported}},
	{"a report with no step is missed", nil,
		func() records.Record { return success("s1", 2600, "r9") },
		[]string{CaseMarkReported}},
	{"a step takes back a report's missed mark",
		func() []records.Record { return []records.Record{success("s1", 2600, "r1")} },
		func() records.Record { return callEnd(withIDs(step("c1", "s1", "p1", 2000), "r1", "")) },
		[]string{CaseStepCall, CaseMarkCall, CaseMarkMissed}},
	{"a progress tick is a running tool", prompted("p1"),
		func() records.Record {
			return pev("s1", "p1", "tengu_tool_use_progress", 2500, `{"requestId":"r1","toolName":"Bash","toolUseID":"tu1"}`)
		},
		[]string{CaseStepTick}},
	{"a Bash event joins its step's tool",
		func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callEnd(withIDs(step("c1", "s1", "p1", 2000), "r1", "")),
				pev("s1", "p1", "tengu_tool_use_success", 2500, `{"requestId":"r1","toolName":"Bash","toolUseID":"tu1"}`)}
		},
		func() records.Record {
			return pev("s1", "p1", "tengu_bash_tool_command_executed", 2400, `{"tool_use_id":"tu1","bash_argv0":"ls","exit_code":0}`)
		},
		[]string{CaseStepBash}},
	{"recorded exit codes of another step's tool call",
		func() []records.Record {
			a := step("c1", "s1", "p1", 2000)
			a.DetailJSON = `{"calls":[{"id":"t1","commands":["ls"]}]}`
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callEnd(a)}
		},
		func() records.Record {
			b := step("c2", "s1", "p1", 1500)
			b.DetailJSON = `{"prompt":"hello","results":{"t1":{"exit":[2]}}}`
			return callEnd(b)
		},
		[]string{CaseStepCall, CaseStepResults, CaseMarkCall}},
	{"a call with no prompt is a between row", nil,
		func() records.Record { return callEnd(withIDs(step("c1", "s1", "", 2000), "r1", "")) },
		[]string{CaseBetweenCall, CaseMarkCall}},
	{"a recap shown on return", nil,
		func() records.Record { return ccEvent("s1", "tengu_return_to_session", 3000, `{"hadRecap":true}`) },
		[]string{CaseBetweenRecap}},
	{"a compaction", func() []records.Record {
		return []records.Record{callEnd(withClass(step("m1", "s1", "p1", 1000), "main")), callEnd(withClass(step("m2", "s1", "p1", 9000), "main"))}
	},
		func() records.Record { return ccEvent("s1", "tengu_reactive_compact_succeeded", 5000, `{}`) },
		[]string{CaseCompaction}},
	{"its attempt starts it",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_reactive_compact_succeeded", 5000, `{}`)}
		},
		func() records.Record { return ccEvent("s1", "tengu_reactive_compact_attempt", 4000, `{}`) },
		[]string{CaseCompactionAttempt}},
	{"an idle firing makes it idle",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_reactive_compact_attempt", 4000, `{}`), ccEvent("s1", "tengu_reactive_compact_succeeded", 5000, `{}`)}
		},
		func() records.Record { return ccEvent("s1", "tengu_idle_compact_fired", 3500, `{}`) },
		[]string{CaseCompactionFired}},
	{"a main call's context before it",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_reactive_compact_succeeded", 5000, `{}`)}
		},
		func() records.Record { return callEnd(withClass(step("m1", "s1", "p1", 1000), "main")) },
		[]string{CaseStepCall, CaseCompactionContext, CaseMarkCall}},
	{"its compaction call is attached, not a between row",
		func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_reactive_compact_attempt", 4000, `{}`), ccEvent("s1", "tengu_reactive_compact_succeeded", 5000, `{}`)}
		},
		func() records.Record { return callEnd(withClass(step("k1", "s1", "", 4500), "compaction")) },
		[]string{CaseCompactionCall, CaseMarkCall}},
	{"a permission mode", nil,
		func() records.Record { return ccEvent("s1", "tengu_api_query", 1000, `{"permissionMode":"plan"}`) },
		[]string{CaseMarkPermission}},
	{"a call's client IP starts a network segment", nil,
		func() records.Record { return callEnd(withIP(step("c1", "s1", "p1", 1000), "10.0.0.1")) },
		[]string{CaseStepCall, CaseMarkCall}},
}

// promptCaseSet is the cases the prompt tests name.
var promptCaseSet = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range PromptCases {
		m[c] = true
	}
	return m
}()

func TestPromptsAtomic(t *testing.T) {
	for _, c := range promptCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkPrompts(t, c.name, append(before, r), o)
			took := map[string]bool{}
			for _, x := range d.Cases {
				took[x] = true
			}
			for _, x := range c.cases {
				if !took[x] {
					t.Fatalf("took %v, want %v among them", only(d.Cases, promptCaseSet), c.cases)
				}
			}
			if d2 := o.apply(r); !reflect.DeepEqual(d2.Cases, []string{CaseDuplicate}) || len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

// Short sequences: every order of a few records ends in the reference's rows.
func TestPromptsShortSequencesEveryOrder(t *testing.T) {
	sets := map[string]func() []records.Record{
		"prompt, its step in flight and ended, its report": func() []records.Record {
			c := withIDs(step("c1", "s1", "p1", 2000), "r1", "cr1")
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), callStart(c), callEnd(c), success("s1", 2600, "r1"),
				pev("s1", "p1", "tengu_turn_end", 3000, `{}`)}
		},
		"two prompts, a paste and a hidden call": func() []records.Record {
			return []records.Record{inputPrompt("s1", "p1", "1", 1000), inputPrompt("s1", "p2", "2", 4000),
				ccEvent("s1", "tengu_paste_text", 3000, `{"pastedTextBytes":7}`), callEnd(hidden(step("h1", "s1", "", 2000))),
				callEnd(step("c1", "s1", "p2", 4500))}
		},
		"a compaction around main calls": func() []records.Record {
			return []records.Record{callEnd(withClass(step("m1", "s1", "p1", 1000), "main")), ccEvent("s1", "tengu_reactive_compact_attempt", 4000, `{}`),
				ccEvent("s1", "tengu_reactive_compact_succeeded", 5000, `{}`), callEnd(withClass(step("k1", "s1", "", 4500), "compaction")),
				callEnd(withClass(step("m2", "s1", "p1", 9000), "main"))}
		},
		"tools, a tick and Bash": func() []records.Record {
			return []records.Record{callEnd(withIDs(step("c1", "s1", "p1", 2000), "r1", "")),
				pev("s1", "p1", "tengu_tool_use_progress", 2100, `{"requestId":"r1","toolName":"Bash","toolUseID":"tu1"}`),
				pev("s1", "p1", "tengu_tool_use_success", 2500, `{"requestId":"r1","toolName":"Bash","toolUseID":"tu1"}`),
				pev("s1", "p1", "tengu_bash_tool_command_executed", 2400, `{"tool_use_id":"tu1","bash_argv0":"rm","destructive_category":"rm_rf"}`)}
		},
		"permission modes and IPs": func() []records.Record {
			return []records.Record{ccEvent("s1", "tengu_api_query", 1000, `{"permissionMode":"plan"}`), ccEvent("s1", "tengu_api_query", 2000, `{"permissionMode":"default"}`),
				callEnd(withIP(step("c1", "s1", "p1", 1500), "10.0.0.1")), callEnd(withIP(step("c2", "s1", "p1", 2500), "::1")),
				ccEvent("s1", "tengu_api_query", 3000, `{"permissionMode":"plan"}`)}
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
			checkPrompts(t, fmt.Sprintf("%s %v", name, p), rs, stream(ordered))
		})
	}
}

// generatePrompts is n sessions of about per records each: prompts typed and
// not, steps in flight and ended (some hidden later), reports by either id,
// tools, ticks, Bash, pastes, hidden calls, compactions, recaps, permission
// modes, IPs, recorded bodies, and a Codex thread with turn reports. Times are
// distinct (ties among hidden calls and Codex reports are the reference's
// arbitrary order).
func generatePrompts(rng *rand.Rand, n, per int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("ps-%d", i)
		t := int64(1_000_000 + i*10_000_000)
		pids := []string{"p0", "p1", "p2", "p3", "p4", "p5"}
		for j := 0; j < per; j++ {
			t += 1 + int64(rng.Intn(3000))
			pid := pids[rng.Intn(len(pids))]
			rid := fmt.Sprintf("r%d", rng.Intn(per/5+1))
			switch rng.Intn(20) {
			case 0, 1:
				out = append(out, inputPrompt(s, pid, fmt.Sprint(rng.Intn(9)), t))
			case 2:
				out = append(out, ccEvent(s, "tengu_input_prompt", t, `{"prompt_length":5,"is_negative":true}`))
			case 3:
				out = append(out, pev(s, pid, "tengu_turn_end", t, `{}`))
			case 4:
				out = append(out, pev(s, pid, "tengu_cancel", t, fmt.Sprintf(`{"cc_prompt_id":%q}`, pid)))
			case 5:
				out = append(out, pev(s, pid, "tengu_bash_tool_command_executed", t,
					fmt.Sprintf(`{"cc_prompt_id":%q,"tool_use_id":"tu%d","bash_argv0":"rm","exit_code":%d,"destructive_category":%q}`,
						pid, rng.Intn(20), rng.Intn(3), []string{"none", "rm_rf", ""}[rng.Intn(3)])))
			case 6:
				name := []string{"tengu_tool_use_success", "tengu_tool_use_error", "tengu_tool_use_progress"}[rng.Intn(3)]
				out = append(out, pev(s, pid, name, t, fmt.Sprintf(`{"requestId":%q,"toolName":%q,"toolUseID":"tu%d"}`,
					rid, []string{"Read", "Bash", "mcp__gh__pr"}[rng.Intn(3)], rng.Intn(20))))
			case 7:
				if rng.Intn(2) == 0 {
					out = append(out, success(s, t, rid))
				} else {
					out = append(out, ccEvent(s, "tengu_api_success", t, fmt.Sprintf(`{"clientRequestId":"c%s"}`, rid)))
				}
			case 8:
				if rng.Intn(2) == 0 {
					out = append(out, ccEvent(s, "tengu_paste_text", t, fmt.Sprintf(`{"pastedTextBytes":%d}`, rng.Intn(100))))
				} else {
					out = append(out, ccEvent(s, "tengu_paste_image", t, `{}`))
				}
			case 9:
				c := step(fmt.Sprintf("h-%d-%d", i, j), s, "", t)
				if rng.Intn(2) == 0 {
					hidden(c)
				} else {
					asTitle(c)
				}
				out = append(out, callEnd(c))
			case 10:
				out = append(out, ccEvent(s, []string{"tengu_reactive_compact_attempt", "tengu_reactive_compact_succeeded", "tengu_idle_compact_fired"}[rng.Intn(3)], t, `{}`))
			case 11:
				out = append(out, ccEvent(s, "tengu_return_to_session", t, fmt.Sprintf(`{"hadRecap":%v}`, rng.Intn(2) == 0)))
			case 12:
				out = append(out, ccEvent(s, "tengu_api_query", t, fmt.Sprintf(`{"permissionMode":%q}`, []string{"plan", "default", ""}[rng.Intn(3)])))
			default:
				p := pid
				if rng.Intn(4) == 0 {
					p = ""
				}
				c := withIDs(step(fmt.Sprintf("c-%d-%d", i, j), s, p, t), rid, "c"+rid)
				c.RequestClass = []string{"main", "main", "subagent", "compaction", "auxiliary", ""}[rng.Intn(6)]
				c.IP = []string{"10.0.0.1", "10.0.0.2", "::1", ""}[rng.Intn(4)]
				c.OutputTokens = int64(rng.Intn(3) * 10)
				if rng.Intn(6) == 0 {
					c.DetailJSON = fmt.Sprintf(`{"prompt":"hi %d","text":"ok","calls":[{"id":"t%d","commands":["ls"]}],"results":{"t%d":{"exit":[%d]}}}`,
						j, rng.Intn(5), rng.Intn(5), rng.Intn(3))
				}
				if rng.Intn(5) == 0 {
					c.Status = 500
				}
				if rng.Intn(3) > 0 {
					out = append(out, callStart(c))
				}
				if rng.Intn(5) > 0 {
					end := *c
					if rng.Intn(8) == 0 {
						hidden(&end)
					}
					out = append(out, callEnd(&end))
				}
			}
		}
		th := fmt.Sprintf("thread-%d", i)
		tc := int64(1_000_000 + i*10_000_000)
		for j := 0; j < per/10; j++ {
			tc += 1 + int64(rng.Intn(5000))
			turn := fmt.Sprintf("turn%d", rng.Intn(4))
			if rng.Intn(3) == 0 {
				out = append(out, codexTurnEvent(th, turn, tc, fmt.Sprintf(`{"status":%q,"duration_ms":%d}`, []string{"completed", ""}[rng.Intn(2)], rng.Intn(9000))))
			} else {
				out = append(out, callEnd(codexCall(fmt.Sprintf("x-%d-%d", i, j), th, turn, tc)))
			}
		}
	}
	return out
}

func TestPromptsGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generatePrompts(rng, 3, 200)
		in := shuffled(rng, rs, 40)
		o := stream(in[:len(in)/2])
		for k, s := range o.states { // a checkpoint halfway
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
		checkPrompts(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

// Flat cost: a record applied to a session holding 10, 1,000 or 100,000 records
// costs the same.
func TestPromptsFlatCost(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100,000-record session")
	}
	measure := func(n int) (float64, time.Duration) {
		s := New("s:big")
		for i := 0; i < n; i++ {
			pid := fmt.Sprintf("p%d", i/8)
			at := int64(i) * 1000
			switch i % 8 {
			case 0:
				s.Apply(inputPrompt("big", pid, "1", at))
			case 1, 2:
				s.Apply(callEnd(withIDs(step(fmt.Sprintf("c%d", i), "big", pid, at), fmt.Sprintf("r%d", i), "")))
			case 3:
				s.Apply(success("big", at, fmt.Sprintf("r%d", i-2)))
			case 4:
				s.Apply(pev("big", pid, "tengu_tool_use_success", at, fmt.Sprintf(`{"requestId":"r%d","toolName":"Read"}`, i-3)))
			case 5:
				s.Apply(callEnd(hidden(step(fmt.Sprintf("h%d", i), "big", "", at))))
			case 6:
				s.Apply(ccEvent("big", []string{"tengu_reactive_compact_succeeded", "tengu_api_query"}[i%2], at, `{"permissionMode":"plan"}`))
			default:
				s.Apply(pev("big", pid, "tengu_turn_end", at, `{}`))
			}
		}
		pre := make([]records.Record, 3000)
		for k := range pre {
			at := int64(n)*1000 + int64(k)
			pid := fmt.Sprintf("q%d", k/6)
			switch k % 6 {
			case 0:
				pre[k] = inputPrompt("big", pid, "1", at)
			case 1:
				pre[k] = callEnd(withIDs(step(fmt.Sprintf("cx%d", k), "big", pid, at), fmt.Sprintf("rx%d", k), ""))
			case 2:
				pre[k] = success("big", at, fmt.Sprintf("rx%d", k-1))
			case 3:
				pre[k] = pev("big", pid, "tengu_tool_use_success", at, fmt.Sprintf(`{"requestId":"rx%d","toolName":"Read"}`, k-2))
			case 4:
				pre[k] = callEnd(hidden(step(fmt.Sprintf("hx%d", k), "big", "", at)))
			default:
				pre[k] = pev("big", pid, "tengu_turn_end", at, `{}`)
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
	a100k, t100k := measure(100_000)
	t.Logf("per record: n=10 %.1f allocs %v; n=1,000 %.1f allocs %v; n=100,000 %.1f allocs %v", a10, t10, a1k, t1k, a100k, t100k)
	if a100k > a10*1.5+4 {
		t.Errorf("allocations grow with the session: %.1f at 10, %.1f at 100,000", a10, a100k)
	}
	if t100k > 8*t10+20*time.Microsecond {
		t.Errorf("time grows with the session: %v at 10, %v at 100,000", t10, t100k)
	}
}
