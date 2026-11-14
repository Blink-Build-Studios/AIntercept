package state_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// promptStep is a step whose request headers name its prompt, class and
// client request id, and whose response headers its request id, as Claude
// Code's do: the drawer's step N of M, prev/next, retries and facts read them.
func promptStep(id, s, pid string, t int64) *canon.Request {
	c := state.WithIDs(state.Step(id, s, pid, t), "rid-"+id, "crid-"+id)
	c.ClientJSON = fmt.Sprintf(`{"x-claude-code-prompt-id":%q,"x-claude-code-request-class":"main","x-client-request-id":%q,"x-claude-code-session-id":%q}`,
		pid, "crid-"+id, s)
	c.ProviderJSON = fmt.Sprintf(`{"request-id":"rid-%s"}`, id)
	c.MessageID, c.PreviousMessageID = "msg-"+id, ""
	return c
}

// stepSuccess is a step's tengu_api_success, its prompt and request id as columns.
func stepSuccess(pid, id string, t int64) records.Record {
	r := state.Success("s1", t, "rid-"+id)
	r.Event.PromptID, r.Event.RequestID = pid, "rid-"+id
	return r
}

func promptSession(prompts, steps int) []records.Record {
	var rs []records.Record
	for p := 0; p < prompts; p++ {
		pid := fmt.Sprintf("p%d", p)
		base := int64(p) * 600_000
		rs = append(rs, state.TypedPrompt("s1", pid, fmt.Sprint(p+1), state.At(base)))
		for i := 0; i < steps; i++ {
			id := fmt.Sprintf("c%d-%d", p, i)
			at := state.At(base + int64(i+1)*10_000)
			rs = append(rs, state.CallEnd(promptStep(id, "s1", pid, at)), stepSuccess(pid, id, at+600),
				state.Pev("s1", pid, "tengu_api_query", at-100, `{}`))
		}
	}
	return rs
}

// A run derives only the drawer rows its records can have changed (§3.7):
// what a row reads of other steps and events, by role. A new step of the last
// prompt derives itself and its lane's previous step (its next changed); the
// prompt's step count is stored once, not in the rows. Its success derives
// its own row; an event no row reads derives none; a late query of the first
// prompt derives the rows whose query window holds it. Every run is also held
// to a whole render (VerifyIncremental).
func TestRenderedDrawerDerivesOnlyWhatChanged(t *testing.T) {
	state.ResetBatches()
	const prompts, steps = 12, 4
	x := newRunner()
	for _, r := range promptSession(prompts, steps) {
		x.apply(t, r)
	}
	last := fmt.Sprintf("p%d", prompts-1)
	at := state.At(int64(prompts-1)*600_000 + int64(steps+1)*10_000)
	for _, c := range []struct {
		what string
		r    records.Record
		want int
	}{
		{"a new step of the last prompt", state.CallEnd(promptStep("c-new", "s1", last, at)), 2},
		{"its success", stepSuccess(last, "c-new", at+700), 1},
		{"an event no row looks up", state.CCEvent("s1", "tengu_turn_end", at+5_000, `{}`), 0},
		{"a late query of the first prompt", state.Pev("s1", "p0", "tengu_api_query", state.At(10_050), `{}`), steps},
	} {
		x.derived = 0
		x.apply(t, c.r)
		if x.derived != c.want {
			t.Errorf("%s derived %d drawer rows, want %d", c.what, x.derived, c.want)
		}
	}
}

// drawerSession is a session as the drawer reads it, generated: prompts whose
// steps name their prompt, class and client request id in their headers, a
// message-id chain (sometimes broken), failures and their retries, successes,
// tool uses and their bash runs (with the sandbox setting), stalls, typed
// prompts, hidden calls, and successes of calls the proxy never saw (reported
// rows).
func drawerSession(rng *rand.Rand, sid string, n int) []records.Record {
	var rs []records.Record
	t := state.At(0)
	pids := []string{"p0", "p1", "p2", "p3", "p4"}
	prevMsg := ""
	for j := 0; j < n; j++ {
		t += int64(rng.Intn(40_000))
		pid := pids[rng.Intn(len(pids))]
		id := fmt.Sprintf("%s-c%d", sid, j)
		ev := func(name, md string, set func(e *canon.Event)) {
			r := state.Pev(sid, pid, name, t+int64(rng.Intn(3_000)), md)
			if set != nil {
				set(r.Event)
			}
			rs = append(rs, r)
		}
		switch rng.Intn(14) {
		case 0:
			rs = append(rs, state.TypedPrompt(sid, pid, fmt.Sprint(rng.Intn(6)), t))
		case 1:
			ev("tengu_api_retry", fmt.Sprintf(`{"attempt":%d,"delayMs":%d}`, rng.Intn(3)+1, rng.Intn(5000)), nil)
		case 2:
			ev("tengu_bash_tool_command_executed", fmt.Sprintf(`{"tool_use_id":"tu-%d","exit_code":%d,"sandbox_enabled":%q}`,
				rng.Intn(n/3+1), rng.Intn(2), []string{"true", "false", ""}[rng.Intn(3)]), nil)
		case 3:
			rid := fmt.Sprintf("rid-%s-c%d", sid, rng.Intn(j+1))
			ev("tengu_tool_use_success", fmt.Sprintf(`{"requestId":%q,"toolName":"Bash","toolUseID":"tu-%d"}`, rid, rng.Intn(n/3+1)),
				func(e *canon.Event) { e.ToolUseID = fmt.Sprintf("tu-%d", rng.Intn(n/3+1)) })
		case 4:
			rid := fmt.Sprintf("rid-%s-c%d", sid, rng.Intn(j+1))
			ev("tengu_streaming_stall_summary", fmt.Sprintf(`{"request_id":%q,"stall_count":%d}`, rid, rng.Intn(4)), nil)
		case 5: // a success whose call the proxy never saw
			ev("tengu_api_success", fmt.Sprintf(`{"requestId":"lost-%d","inputTokens":5,"outputTokens":7,"ttftMs":3.5}`, j),
				func(e *canon.Event) { e.RequestID = fmt.Sprintf("lost-%d", j) })
		default:
			c := promptStep(id, sid, pid, t)
			if rng.Intn(4) == 0 {
				c.RequestClass = "haiku"
				c.ClientJSON = strings.Replace(c.ClientJSON, `"main"`, `"haiku"`, 1)
			}
			if rng.Intn(5) > 0 {
				c.PreviousMessageID = prevMsg
			}
			prevMsg = c.MessageID
			switch rng.Intn(8) {
			case 0:
				c.Status, c.Error = 529, "overloaded"
			case 1:
				c.HiddenKind = "title"
			}
			rs = append(rs, state.CallEnd(c))
			if rng.Intn(3) > 0 {
				ev("tengu_api_success", fmt.Sprintf(`{"requestId":"rid-%s","costUSD":0.01,"attempt":%d}`, id, rng.Intn(3)),
					func(e *canon.Event) { e.RequestID = "rid-" + id })
			}
			if rng.Intn(2) == 0 {
				ev("tengu_api_query", `{}`, nil)
			}
		}
	}
	return rs
}

// Generated drawer sessions, shuffled and duplicated, one record per run: after
// every run the incremental render is the whole render's (VerifyIncremental),
// and at the end the rows are one pass's.
func TestRenderedDrawerIncrementalGenerated(t *testing.T) {
	state.Heavy(t)
	for seed := int64(1); seed <= 8/state.SeedFrac+1; seed++ {
		rng := rand.New(rand.NewSource(seed))
		state.ResetBatches()
		rs := append(drawerSession(rng, "ds1", 150), drawerSession(rng, "ds2", 60)...)
		x := newRunner()
		for i, r := range state.Shuffled(rng, rs, 20) {
			x.apply(t, r)
			if i == len(rs)/2 {
				coldLoad(t, x)
			}
		}
		if d := x.fake.Diff(onePass(t, rs)); d != "" {
			t.Fatalf("seed %d: the fake isn't one pass's render: %s", seed, d)
		}
		if x.derived == 0 {
			t.Fatalf("seed %d: no drawer row derived", seed)
		}
	}
}

// Each kind of lookup a row makes, changed alone (no other lookup of the row
// sees the change): the run must derive the row again, or the incremental
// render isn't the whole render's (VerifyIncremental panics).
func TestRenderedDrawerEachLookupAlone(t *testing.T) {
	noPrompt := func(id string, at int64) *canon.Request {
		c := promptStep(id, "s1", "", at)
		c.ClientJSON = fmt.Sprintf(`{"x-claude-code-request-class":"main","x-client-request-id":"crid-%s","x-claude-code-session-id":"s1"}`, id)
		return c
	}
	ev := func(name string, at int64, md string, set func(e *canon.Event)) records.Record {
		r := state.CCEvent("s1", name, at, md)
		set(r.Event)
		return r
	}
	failed := func(c *canon.Request) *canon.Request { c.Status, c.Error = 529, "overloaded"; return c }
	for name, c := range map[string]struct {
		before  func() []records.Record
		trigger func() records.Record
	}{
		// R's previous message is D's, and D lands after R, later in time (its prompt another).
		"the chain's previous step": {
			func() []records.Record {
				r := promptStep("r", "s1", "pB", state.At(1000))
				r.PreviousMessageID = "msg-d"
				return []records.Record{state.CallEnd(r)}
			},
			func() records.Record { return state.CallEnd(promptStep("d", "s1", "pA", state.At(5000))) },
		},
		// A typed prompt whose prompt id is only in its metadata: R's prompt number.
		"the prompt index": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(1000)))}
			},
			func() records.Record {
				return ev("tengu_input_prompt", state.At(500), `{"cc_prompt_id":"pA","prompt_index":3}`, func(*canon.Event) {})
			},
		},
		// A stall summary naming R's request id only in its metadata.
		"a stall": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(1000)))}
			},
			func() records.Record {
				return ev("tengu_streaming_stall_summary", state.At(1200), `{"request_id":"rid-r","stall_count":2}`, func(*canon.Event) {})
			},
		},
		// R failed with no prompt; a retry found by its request id; the next call with no prompt lands.
		"the next step with no prompt": {
			func() []records.Record {
				return []records.Record{state.CallEnd(failed(noPrompt("r", state.At(1000)))),
					ev("tengu_api_retry", state.At(1600), `{"attempt":1}`, func(e *canon.Event) { e.RequestID = "rid-r" })}
			},
			func() records.Record { return state.CallEnd(noPrompt("n", state.At(4000))) },
		},
		// A success that only its metadata ties to R (its prompt's).
		"a success by request id": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(1000)))}
			},
			func() records.Record {
				return state.Pev("s1", "pA", "tengu_api_success", state.At(1700), `{"requestId":"rid-r","costUSD":0.5}`)
			},
		},
		// A query of R's prompt in its window.
		"a query": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(1000)))}
			},
			func() records.Record {
				return state.Pev("s1", "pA", "tengu_api_query", state.At(1500), `{"permissionMode":"plan"}`)
			},
		},
		// A retry of R's prompt after it failed.
		"a retry": {
			func() []records.Record {
				return []records.Record{state.CallEnd(failed(promptStep("r", "s1", "pA", state.At(1000))))}
			},
			func() records.Record {
				return state.Pev("s1", "pA", "tengu_api_retry", state.At(2500), `{"attempt":1,"delayMs":800}`)
			},
		},
		// The bash run of R's tool use, after R's sandbox window.
		"a bash run": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(1000))),
					ev("tengu_tool_use_success", state.At(1100), `{"requestId":"rid-r","toolName":"Bash","toolUseID":"tu1"}`,
						func(e *canon.Event) { e.RequestID = "rid-r" })}
			},
			func() records.Record {
				return state.Pev("s1", "pA", "tengu_bash_tool_command_executed", state.At(9000), `{"tool_use_id":"tu1","exit_code":2}`)
			},
		},
		// The next step of R's prompt (its lane's next).
		"the lane's next step": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(1000)))}
			},
			func() records.Record { return state.CallEnd(promptStep("n", "s1", "pA", state.At(3000))) },
		},
		// A step of R's prompt landing before it (R's step number).
		"a step before it": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(3000))),
					state.CallEnd(promptStep("n", "s1", "pA", state.At(5000)))}
			},
			func() records.Record { return state.CallEnd(promptStep("e", "s1", "pA", state.At(1000))) },
		},
		// R's tool result is delivered in the step after it; a step of another prompt lands before R.
		"the step order": {
			func() []records.Record {
				return []records.Record{state.CallEnd(promptStep("r", "s1", "pA", state.At(2000))),
					state.CallEnd(promptStep("n", "s1", "pA", state.At(3000))),
					ev("tengu_tool_use_success", state.At(2100), `{"requestId":"rid-r","toolName":"Bash","toolUseID":"tu1"}`,
						func(e *canon.Event) { e.RequestID = "rid-r" })}
			},
			func() records.Record { return state.CallEnd(promptStep("early", "s1", "pZ", state.At(1000))) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			state.ResetBatches()
			x := newRunner()
			for _, r := range c.before() {
				x.apply(t, r)
			}
			x.derived = 0
			x.apply(t, c.trigger())
			if x.derived == 0 {
				t.Fatal("nothing derived")
			}
		})
	}
}

// A run's drawer cost doesn't grow with the session (§3.7): the scope is kept
// and updated, the rows a change reaches are derived, and the delta is
// written. A new step and its success in a session of 20 prompts and in one
// of 400 take about the same (timed with the check off; under -race it's
// skipped, as the slow tests are).
func TestRenderedDrawerRunCostIsFlat(t *testing.T) {
	state.Heavy(t)
	drawer.VerifyIncremental = false
	defer func() { drawer.VerifyIncremental = true }()
	runCost := func(prompts int) time.Duration {
		state.ResetBatches()
		o := state.NewStream(nil)
		var base *render.Baseline
		run := func(r records.Record) *render.Rows {
			changed := drawer.Changed{}
			for _, c := range o.Apply(r).Changes {
				if changed[c.Table] == nil {
					changed[c.Table] = map[string]bool{}
				}
				changed[c.Table][c.ID] = true
			}
			rows, err := render.RenderNext("s:s1", o.States()["s:s1"].Rows(), nil, day, base, changed, nil)
			if err != nil {
				t.Fatal(err)
			}
			base = rows.Baseline()
			return rows
		}
		for _, r := range promptSession(prompts, 5) {
			run(r)
		}
		var best time.Duration
		for i := 0; i < 5; i++ {
			at := state.At(int64(prompts)*600_000 + int64(i+1)*10_000)
			id := fmt.Sprintf("c-new%d", i)
			d := run(state.CallEnd(promptStep(id, "s1", "p-last", at))).Took.Drawer
			d += run(stepSuccess("p-last", id, at+700)).Took.Drawer
			if i == 0 || d < best {
				best = d
			}
		}
		return best
	}
	small, large := runCost(20), runCost(400)
	t.Logf("a step and its success: %v with 20 prompts, %v with 400", small, large)
	if large > 3*small+2*time.Millisecond {
		t.Errorf("the drawer's run cost grows with the session: %v with 20 prompts, %v with 400", small, large)
	}
}
