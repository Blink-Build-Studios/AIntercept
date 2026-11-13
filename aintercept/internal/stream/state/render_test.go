package state

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The session render (render.go, docs/PIPELINE.md build step 3) in the owner's
// layers, called directly: every session's tables, rendered from its own key's
// rows, equal the reference's (process.DeriveRows) for that session. Left out,
// as the render leaves them out: children_json, a title thread's label for
// another session, and an analytics batch's call in flight (its start names no
// session, so it sits in its credential's c: key until its ending).

// rendered is every session key's tables.
func rendered(o *streamOut) map[string]*SessionTables {
	out := map[string]*SessionTables{}
	for k, s := range o.states {
		if sid := sessionOf(k); sid != "" {
			out[sid] = RenderSession(sid, s.Rows())
		}
	}
	return out
}

// refSession is the reference's tables for session sid, as the render holds them.
func refSession(ref *process.Derived, o *streamOut, sid string) *SessionTables {
	st := &SessionTables{}
	own, titled := map[string]process.SessionRow{}, map[string]process.SessionRow{}
	for _, r := range SessionRowsOf(o.rows) {
		own[r.SessionID] = r
	}
	for _, r := range TitledSessionRows(o.rows) {
		titled[r.SessionID] = r
	}
	for _, r := range refSessionRows(ref) {
		if r.SessionID == sid {
			r.ChildrenJSON = nil
			if a, b := own[sid], titled[sid]; a.LabelText != b.LabelText || a.LabelTextAlone != b.LabelTextAlone {
				r.LabelText, r.LabelSource, r.LabelTextAlone, r.LabelSourceAlone = a.LabelText, a.LabelSource, a.LabelTextAlone, a.LabelSourceAlone
			}
			st.SessionRows = append(st.SessionRows, r)
		}
	}
	st.PresenceSegments = filter(ref.Presence, func(r process.PresenceSegment) bool { return r.SessionID == sid })
	st.SessionTools = filter(ref.SessionTools, func(r process.SessionTool) bool { return r.SessionID == sid })
	st.EventRows = filter(ref.EventRows, func(r process.EventRow) bool { return r.SessionID == sid })
	st.PromptRows, st.PromptSteps, st.SessionMarks = refPrompts(ref)
	st.PromptRows = filter(st.PromptRows, func(r process.PromptRow) bool { return r.SessionID == sid })
	st.PromptSteps = filter(st.PromptSteps, func(r process.PromptStep) bool { return r.SessionID == sid })
	st.SessionMarks = filter(st.SessionMarks, func(r process.SessionMark) bool { return r.SessionID == sid })
	reqs := filter(ref.SessionReqs, func(r process.SessionRequestRow) bool {
		x := r.Row
		inFlight := x.Status == nil && !x.StatusErr && x.DurationMS == nil
		return r.Scope == sid && x.SessionID != nil && *x.SessionID == sid && !(x.Kind == "analytics" && inFlight)
	})
	for i := range reqs { // numbered over what's kept
		reqs[i].Seq = int64(i)
	}
	st.SessionRequestRows = reqs
	st.CallToolUses = filter(ref.CallTools, func(r process.CallToolUse) bool { return r.SessionID == sid })
	sortCallTools(st.CallToolUses)
	return st
}

// normalized is the tables with today's tie-free orders imposed where the
// reference leaves ties (tools), and empty lists as nil.
func (t *SessionTables) normalized() *SessionTables {
	c := *t
	c.SessionTools = append([]process.SessionTool(nil), t.SessionTools...)
	sortTools(c.SessionTools)
	c.SessionRows = append([]process.SessionRow(nil), t.SessionRows...)
	for i := range c.SessionRows {
		normalizeSessionRow(&c.SessionRows[i])
	}
	return &c
}

var tableOrder = []string{"session_rows", "presence_segments", "session_tools", "event_rows", "session_marks",
	"prompt_rows", "prompt_steps", "session_request_rows", "call_tool_uses"}

// diff names the first table and row that differ (the same content: a nil list
// as an empty one, as Parquet stores both).
func (t *SessionTables) diff(want *SessionTables) string {
	g, w := t.normalized().Tables(), want.normalized().Tables()
	for _, name := range tableOrder {
		if sameContent(g[name], w[name]) {
			continue
		}
		a, b := reflect.ValueOf(g[name]), reflect.ValueOf(w[name])
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s: %d rows, want %d", name, a.Len(), b.Len())
		}
		for r := 0; r < a.Len(); r++ {
			if !sameContent(a.Index(r).Interface(), b.Index(r).Interface()) {
				return fmt.Sprintf("%s row %d %s", name, r, diffRow(a.Index(r), b.Index(r)))
			}
		}
	}
	return ""
}

func diffRow(a, b reflect.Value) string {
	var out strings.Builder
	out.WriteString("row:")
	for f := 0; f < a.NumField(); f++ {
		if sameContent(a.Field(f).Interface(), b.Field(f).Interface()) {
			continue
		}
		if a.Field(f).Kind() == reflect.Struct {
			fmt.Fprintf(&out, " %s.%s", a.Type().Field(f).Name, diffRow(a.Field(f), b.Field(f)))
			continue
		}
		fmt.Fprintf(&out, " %s got %s want %s", a.Type().Field(f).Name, sprint(deep(a.Field(f))), sprint(deep(b.Field(f))))
	}
	return out.String()
}

// sameContent is whether two values hold the same content: every field, a nil
// pointer apart from a pointer to a zero value, a nil list equal to an empty one.
func sameContent(a, b any) bool { return sameValue(reflect.ValueOf(a), reflect.ValueOf(b)) }

func sameValue(a, b reflect.Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return sameValue(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !sameValue(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !sameValue(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Float32, reflect.Float64:
		return math.Float64bits(a.Float()) == math.Float64bits(b.Float())
	case reflect.Map:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
	return a.Equal(b)
}

// checkRender requires every session's rendered tables to equal the
// reference's over the same records.
func checkRender(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	checkRendered(t, label, rs, o, rendered(o))
}

// checkRendered requires got (each session's tables) to equal the reference's
// over the same records; o is the stream that applied them.
func checkRendered(t *testing.T, label string, rs []records.Record, o *streamOut, got map[string]*SessionTables) {
	t.Helper()
	ref := reference(rs)
	sids := map[string]bool{}
	for s := range got {
		sids[s] = true
	}
	for _, r := range ref.SessionRows {
		sids[r.SessionID] = true
	}
	for _, r := range ref.EventRows {
		sids[r.SessionID] = true
	}
	for _, r := range ref.PromptRows {
		sids[r.SessionID] = true
	}
	ids := make([]string, 0, len(sids))
	for s := range sids {
		ids = append(ids, s)
	}
	sort.Strings(ids)
	for _, sid := range ids {
		g := got[sid]
		if g == nil {
			g = &SessionTables{}
		}
		if d := g.diff(refSession(ref, o, sid)); d != "" {
			t.Fatalf("%s: session %s: %s", label, sid, d)
		}
	}
}

// sameRender is whether two streams render every session alike.
func sameRender(a, b map[string]*SessionTables) bool {
	if len(a) != len(b) {
		return false
	}
	for sid, x := range a {
		y, ok := b[sid]
		if !ok || x.diff(y) != "" {
			return false
		}
	}
	return true
}

// --- the layers ---------------------------------------------------------------

// Atomic: one record per case (every state.Cases branch); each session's
// rendered tables equal the reference's.
func TestRenderSessionAtomic(t *testing.T) {
	var all []atomicCase
	for _, cs := range [][]atomicCase{presenceCases, eventCases, dimCases, promptCases, requestCases, sessionCases, callToolCases, factCases} {
		all = append(all, cs...)
	}
	for _, c := range all {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			o.apply(r)
			checkRender(t, c.name, append(before, r), o)
		})
	}
}

// at is t ms past a fixed hour (2026-11-04T08:00Z).
func at(t int64) int64 { return 1_793_779_200_000 + t }

// renderShortSets are the short sequences the render is run over in every order.
var renderShortSets = map[string]func() []records.Record{
	// The event linking a call (its tengu_api_success) an hour before the call lands.
	"a call landing an hour after the event linking it": func() []records.Record {
		c := withIDs(step("c1", "s1", "p1", at(70*60_000)), "rid1", "crid1")
		return []records.Record{typedPrompt("s1", "p1", "1", at(5*60_000)), success("s1", at(50*60_000), "rid1"),
			callStart(c), callEnd(c), ccEvent("s1", "tengu_api_query", at(55*60_000), `{}`)}
	},
	// The prompt's input event, in the next hour, after its calls.
	"a prompt's input event after its calls": func() []records.Record {
		a := withIDs(step("c1", "s1", "p1", at(50*60_000)), "r1", "cr1")
		b := withIDs(step("c2", "s1", "p1", at(58*60_000)), "r2", "cr2")
		return []records.Record{callEnd(a), callStart(b), callEnd(b), typedPrompt("s1", "p1", "1", at(65*60_000)),
			pev("s1", "p1", "tengu_turn_end", at(66*60_000), `{}`)}
	},
	// A plan-mode segment from one hour into the next: its to_ns is the next segment's start.
	"a plan-mode segment spanning hours": func() []records.Record {
		return []records.Record{ccEvent("s1", "tengu_api_query", at(10*60_000), `{"permissionMode":"default"}`),
			ccEvent("s1", "tengu_api_query", at(50*60_000), `{"permissionMode":"plan"}`),
			ccEvent("s1", "tengu_api_query", at(75*60_000), `{"permissionMode":"default"}`),
			callEnd(withIP(step("c1", "s1", "", at(52*60_000)), "10.0.0.1")),
			callEnd(withIP(step("c2", "s1", "", at(130*60_000)), "10.0.0.2"))}
	},
}

// Short sequences: every order of up to 5 records; the render after each
// prefix equals one pass over the same records, and the whole set the
// reference's.
func TestRenderSessionShortSequencesEveryOrder(t *testing.T) {
	heavy(t)
	sets := renderShortSets
	for name, mk := range sets {
		batchN = map[string]int64{}
		rs := mk()
		checkRender(t, name, rs, stream(rs))
		onePass := map[string]map[string]*SessionTables{} // the prefix's records (by index) → one pass's render
		permute(len(rs), func(p []int) {
			o := stream(nil)
			in := make([]bool, len(rs))
			for k, j := range p {
				o.apply(rs[j])
				in[j] = true
				var set []records.Record
				id := ""
				for x := range rs {
					if in[x] {
						set = append(set, rs[x])
						id += fmt.Sprint(x, ",")
					}
				}
				want, ok := onePass[id]
				if !ok {
					want = rendered(stream(set))
					onePass[id] = want
				}
				if !sameRender(rendered(o), want) {
					t.Fatalf("%s %v after %d: the render differs from one pass over the same records", name, p, k+1)
				}
			}
		})
	}
}

// generateHours is n sessions' prompts, calls, reports, permission modes, blur
// and tool events, spread over hours and days, and a Codex thread each.
func generateHours(rng *rand.Rand, n, per int) []records.Record {
	var out []records.Record
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("hs-%d", i)
		t := at(int64(i) * 7 * hourMS)
		pids := []string{"p0", "p1", "p2", "p3"}
		for j := 0; j < per; j++ {
			t += int64(rng.Intn(25 * 60_000))
			pid := pids[rng.Intn(len(pids))]
			rid := fmt.Sprintf("r%d", rng.Intn(per/4+1))
			switch rng.Intn(12) {
			case 0:
				out = append(out, typedPrompt(s, pid, fmt.Sprint(rng.Intn(5)), t))
			case 1:
				out = append(out, pev(s, pid, "tengu_turn_end", t, `{}`))
			case 2:
				out = append(out, success(s, t, rid))
			case 3:
				out = append(out, ccEvent(s, "tengu_api_query", t, fmt.Sprintf(`{"permissionMode":%q}`, []string{"plan", "default"}[rng.Intn(2)])))
			case 4:
				out = append(out, blur(s, t, int64(rng.Intn(600_000))))
			case 5:
				name := []string{"tengu_tool_search_mode_decision", "tengu_skills_sync_success"}[rng.Intn(2)]
				out = append(out, ccEvent(s, name, t, fmt.Sprintf(`{"mcpToolCount":%d,"total":%d}`, rng.Intn(9), rng.Intn(9))))
			case 6:
				out = append(out, pev(s, pid, "tengu_tool_use_success", t, fmt.Sprintf(`{"requestId":%q,"toolName":"Bash"}`, rid)))
			case 7:
				out = append(out, pev(s, pid, "tengu_cancel", t, fmt.Sprintf(`{"cc_prompt_id":%q}`, pid)))
			default:
				c := withIDs(step(fmt.Sprintf("c-%d-%d", i, j), s, pid, t), rid, "c"+rid)
				c.IP = []string{"10.0.0.1", "10.0.0.2"}[rng.Intn(2)]
				if rng.Intn(4) == 0 {
					c.ToolUsesJSON = `[{"name":"Read"}]`
				}
				if rng.Intn(3) > 0 {
					out = append(out, callStart(c))
				}
				if rng.Intn(5) > 0 {
					out = append(out, callEnd(c))
				}
			}
		}
		th := fmt.Sprintf("hthread-%d", i)
		tc := at(int64(i) * 5 * hourMS)
		for j := 0; j < per/8; j++ {
			tc += int64(rng.Intn(40 * 60_000))
			// Turn ids are the thread's own (UUIDs in real traffic): today's Codex step numbers
			// count a credential's calls of the same turn id across threads.
			out = append(out, callEnd(codexCall(fmt.Sprintf("hx-%d-%d", i, j), th, fmt.Sprintf("%s-turn%d", th, rng.Intn(4)), tc)))
		}
	}
	return out
}

// Generated sequences: shuffled, duplicated, late across hours and days,
// reloaded from a checkpoint halfway; the render equals the reference's and
// one pass's.
func TestRenderSessionGeneratedSequences(t *testing.T) {
	heavy(t)
	for seed := int64(1); seed <= 6/seedFrac+1; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateHours(rng, 3, 120)
		in := shuffled(rng, rs, 30)
		o := stream(in[:len(in)/2])
		for k, s := range o.states { // a checkpoint halfway: the rest applies to decoded states
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
		checkRender(t, fmt.Sprintf("seed %d", seed), rs, o)
		if !sameRender(rendered(o), rendered(stream(rs))) {
			t.Fatalf("seed %d: the render differs from one pass", seed)
		}
	}
}
