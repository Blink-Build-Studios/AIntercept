package state_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render/rendertest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The rendered tables (docs/PIPELINE.md v7 build step 3) in the owner's
// layers, on the fake (render/rendertest): one run per record (render the
// record's key, diff against its baseline, write the diff), and after each
// run the fake holds exactly what one pass over the same records renders; each
// diff writes only the rows that changed (the fake refuses any other write);
// the rows read back as the API loads them equal the reference's
// (process.DeriveRows) for every session, and the folds equal theirs computed
// from the records.

const day = "2026-11-04"

// batchRE is the person summary's batch of an event id (people.py: regexp_extract(event_id, '^(.*):[0-9]+$', 1)).
var batchRE = regexp.MustCompile(`^(.*):[0-9]+$`)

func colIndex(t *pg.Table, name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	panic(t.Name + " has no " + name)
}

// runner is the worker's render half over a stream: per key, the warm
// baseline of what its last commit wrote.
type runner struct {
	o       *state.Stream
	fake    *rendertest.Fake
	base    map[string]*render.Baseline
	pgRuns  func(*render.Diff) // also writes each diff to Postgres (nil: the fake only)
	writes  int                // rows upserted or deleted
	derived int                // drawer rows derived (the rest were the memo's)
}

func newRunner() *runner {
	return &runner{o: state.NewStream(nil), fake: rendertest.New(), base: map[string]*render.Baseline{}}
}

func renderKey(t *testing.T, key string, s *state.KeyState) *render.Rows {
	t.Helper()
	r, err := render.Render(key, s.Rows(), day)
	if err != nil {
		t.Fatalf("render %s: %v", key, err)
	}
	return r
}

// run is one run for key: render, diff against the baseline, write strictly.
func (x *runner) run(t *testing.T, key string) {
	t.Helper()
	r := renderKey(t, key, x.o.States()[key])
	d := r.Diff(x.base[key])
	x.write(t, d)
	x.base[key] = r.Baseline()
}

func (x *runner) write(t *testing.T, d *render.Diff) {
	t.Helper()
	if err := x.fake.Apply(d, true); err != nil {
		t.Fatalf("key %s: the diff wrote a row that didn't change: %v", d.Key, err)
	}
	u, del := d.Counts()
	x.writes += u + del
	if x.pgRuns != nil {
		x.pgRuns(d)
	}
}

// plant puts a row an older render wrote in the fake (and Postgres) as is.
func (x *runner) plant(t *testing.T, table string, row []any) {
	t.Helper()
	x.fake.Plant(table, row)
	if x.pgRuns != nil {
		x.pgRuns(rendertest.WithTemplates(&render.Diff{Tables: []render.TableDiff{{Table: pg.TableNamed(table), Upserts: [][]any{row}}}}))
	}
}

// apply is a run for one record, as the worker's: the drawer derives again
// only the rows the record's delta can have changed (drawer.RenderNext), and
// VerifyIncremental (init) holds that to a whole render on every run.
func (x *runner) apply(t *testing.T, r records.Record) {
	t.Helper()
	d := x.o.Apply(r)
	changed := drawer.Changed{}
	for _, c := range d.Changes {
		if changed[c.Table] == nil {
			changed[c.Table] = map[string]bool{}
		}
		changed[c.Table][c.ID] = true
	}
	key := r.RoutingKey
	rows, err := render.RenderNext(key, x.o.States()[key].Rows(), nil, day, x.base[key], changed)
	if err != nil {
		t.Fatalf("render %s: %v", key, err)
	}
	x.derived += rows.DrawerDerived()
	x.write(t, rows.Diff(x.base[key]))
	x.base[key] = rows.Baseline()
}

func init() { drawer.VerifyIncremental = true }

// onePass is the fake holding one pass's render over rs, every key written whole.
func onePass(t *testing.T, rs []records.Record) *rendertest.Fake {
	t.Helper()
	o := state.NewStream(rs)
	var all []*render.Rows
	for k, s := range o.States() {
		all = append(all, renderKey(t, k, s))
	}
	return rendertest.Written(all...)
}

// checkReference requires the fake's rows to be the reference's over rs: each
// session's tables (as the API loads them) and the folds.
func checkReference(t *testing.T, label string, rs []records.Record, o *state.Stream, f *rendertest.Fake) {
	t.Helper()
	got := map[string]*state.SessionTables{}
	for _, sid := range f.Sessions() {
		got[sid] = f.Session(sid)
	}
	state.CheckRendered(t, label, rs, o, got)
	// The extra columns: event_rows.batch is the summary's regexp over event_id,
	// session_rows.key_day the key's day.
	for _, row := range f.Rows("event_rows") {
		tb := pg.TableNamed("event_rows")
		id, batch := row[colIndex(tb, "event_id")].(string), row[colIndex(tb, "batch")]
		want := ""
		if m := batchRE.FindStringSubmatch(id); m != nil {
			want = m[1]
		}
		if batch != want {
			t.Fatalf("%s: event %q's batch %v, want %q", label, id, batch, want)
		}
	}
	for _, row := range f.Rows("session_rows") {
		if d := row[colIndex(pg.TableNamed("session_rows"), "key_day")]; d != day {
			t.Fatalf("%s: key_day %v", label, d)
		}
	}
	want := refFolds(rs, got)
	have := f.Folds()
	for _, name := range []string{"session_call_days", "agent_calls", "agent_call_hours", "batch_parts"} {
		if !reflect.DeepEqual(have[name], want[name]) && len(have[name])+len(want[name]) > 0 {
			t.Fatalf("%s: %s:\n got %q\nwant %q", label, name, have[name], want[name])
		}
	}
}

// refFolds is the folds computed from the records (and, for the call days, the
// sessions' reference marks, which CheckRendered has held equal to got's).
func refFolds(rs []records.Record, sessions map[string]*state.SessionTables) map[string][]string {
	row := func(vs ...any) string {
		parts := make([]string, len(vs))
		for i, v := range vs {
			parts[i] = fmt.Sprint(v)
			if v == nil {
				parts[i] = "NULL"
			}
		}
		return strings.Join(parts, " ")
	}
	out := map[string][]string{}
	// session_call_days: per session and _ACTIVITY's day, its call marks.
	for sid, st := range sessions {
		type agg struct{ n, first, last int64 }
		days := map[int64]*agg{}
		for _, m := range st.SessionMarks {
			if m.Kind != "call" {
				continue
			}
			d := int64(math.Floor(float64(m.TNS) / 1e9 / 86400))
			if a := days[d]; a == nil {
				days[d] = &agg{1, m.TNS, m.TNS}
			} else {
				a.n, a.first, a.last = a.n+1, min(a.first, m.TNS), max(a.last, m.TNS)
			}
		}
		for d, a := range days {
			out["session_call_days"] = append(out["session_call_days"], row(sid, d, a.n, a.first, a.last))
		}
	}
	// agent_calls: as /overview counts requests (the merge's merge_agent_calls).
	seen := map[string]bool{}
	// A key holds an ended call's row when the ending is its own (not a copy
	// for its start's key), and a call in flight when no ending reached it.
	ended := map[[2]string]bool{}
	for _, r := range rs {
		if r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection {
			ended[[2]string{r.RoutingKey, r.CallID}] = true
		}
	}
	type call struct {
		key string
		ts  any
	}
	calls := map[string]call{}
	type part struct {
		n                int64
		name, session    string
		ns               int64
		hasName, hasSess bool
	}
	parts := map[[2]string]*part{}
	for _, r := range rs {
		if seen[r.RecordID] {
			continue
		}
		seen[r.RecordID] = true
		switch r.Kind {
		case records.KindCallEnd, records.KindWSConnection:
			c := r.Call
			if records.RoutingKey(&r) != r.RoutingKey {
				continue // a copy
			}
			product := c.Product
			if product == "" && c.Host == "api.anthropic.com" {
				product = canon.ProductClaudeCode
			}
			if c.Synthetic || product != canon.ProductClaudeCode && product != canon.ProductCodex {
				continue
			}
			var ts any
			if ns := c.StartNS; ns != 0 {
				ts = ns
			} else if c.EndNS != 0 {
				ts = c.EndNS
			}
			calls[c.CallID] = call{r.RoutingKey, ts}
		case records.KindCallStart:
			c := r.Call
			var h struct{ Originator string }
			json.Unmarshal([]byte(c.ClientJSON), &h) //nolint:errcheck // none: no originator
			if ended[[2]string{r.RoutingKey, c.CallID}] || strings.Contains(c.CallID, "#") || c.Synthetic || c.StartNS == 0 ||
				c.Host != "api.anthropic.com" && !strings.HasPrefix(h.Originator, "codex") {
				continue
			}
			calls[c.CallID] = call{r.RoutingKey, c.StartNS}
		case records.KindClientEvent:
			e := r.Event
			if e == nil || !process.IsAnalyticsEvent(*e) {
				continue
			}
			k := [2]string{e.CorrelationID, r.RoutingKey}
			p := parts[k]
			if p == nil {
				p = &part{}
				parts[k] = p
			}
			p.n++
			if e.Name != "" && (!p.hasName || e.Name < p.name) {
				p.name, p.hasName = e.Name, true
			}
			if e.SessionID != "" && (!p.hasSess || e.NS < p.ns || e.NS == p.ns && e.SessionID < p.session) {
				p.ns, p.session, p.hasSess = e.NS, e.SessionID, true
			}
		}
	}
	hours := map[[2]any]int64{}
	for id, c := range calls {
		out["agent_calls"] = append(out["agent_calls"], row(c.key, id, c.ts))
		if ns, ok := c.ts.(int64); ok {
			hours[[2]any{ns / 3_600_000_000_000 * 3_600_000_000_000, c.key}]++
		}
	}
	for h, n := range hours {
		out["agent_call_hours"] = append(out["agent_call_hours"], row(h[0], h[1], n))
	}
	for k, p := range parts {
		var name, ns, sess any
		if p.hasName {
			name = p.name
		}
		if p.hasSess {
			ns, sess = p.ns, p.session
		}
		out["batch_parts"] = append(out["batch_parts"], row(k[0], k[1], p.n, name, ns, sess))
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// --- the layers ---------------------------------------------------------------

// Atomic: one record per case (every state.Cases branch), each run alone.
func TestRenderedAtomic(t *testing.T) {
	for _, c := range state.AtomicCases() {
		t.Run(c.Name, func(t *testing.T) {
			state.ResetBatches()
			var rs []records.Record
			if c.Before != nil {
				rs = c.Before()
			}
			rs = append(rs, c.Rec())
			x := newRunner()
			for _, r := range rs {
				x.apply(t, r)
			}
			if d := x.fake.Diff(onePass(t, rs)); d != "" {
				t.Fatalf("the fake isn't one pass's render: %s", d)
			}
			checkReference(t, c.Name, rs, x.o, x.fake)
		})
	}
}

// foldSets are short sequences for the folds: a batch across two sessions
// and a credential's key, a call in flight, a Codex call, a call an hour later.
var foldSets = map[string]func() []records.Record{
	"agent calls, in flight and ended, across hours": func() []records.Record {
		a := state.WithIDs(state.Step("c1", "s1", "p1", state.At(10*60_000)), "r1", "cr1")
		b := state.Step("c2", "s1", "p1", state.At(70*60_000))
		return []records.Record{state.CallStart(a), state.CallEnd(a), state.CallStart(b),
			state.CallEnd(state.CodexCall("x1", "t1", "turn1", state.At(30*60_000))),
			state.CCEvent("s1", "tengu_api_query", state.At(11*60_000), `{}`)}
	},
	"a batch's events in two sessions": func() []records.Record {
		return []records.Record{state.CCEvent("s1", "tengu_b", state.At(1000), `{}`), state.CCEvent("s2", "tengu_a", state.At(900), `{}`),
			state.CCEvent("s1", "tengu_a", state.At(800), `{}`), state.CCEvent("s2", "", state.At(800), `{}`),
			state.CallEnd(state.Step("c9", "s2", "", state.At(2000)))}
	},
}

// Short sequences: every order of up to 5 records, one run per record; after
// each, the fake is one pass's render of the records so far.
func TestRenderedShortSequencesEveryOrder(t *testing.T) {
	state.Heavy(t)
	sets := map[string]func() []records.Record{}
	for k, v := range state.ShortSets() {
		sets[k] = v
	}
	for k, v := range foldSets {
		sets[k] = v
	}
	for name, mk := range sets {
		state.ResetBatches()
		rs := mk()
		onePasses := map[string]*rendertest.Fake{}
		writes := 0
		state.Permute(len(rs), func(p []int) {
			x := newRunner()
			in := make([]bool, len(rs))
			for k, j := range p {
				x.apply(t, rs[j])
				in[j] = true
				var set []records.Record
				id := ""
				for i := range rs {
					if in[i] {
						set = append(set, rs[i])
						id += fmt.Sprint(i, ",")
					}
				}
				want, ok := onePasses[id]
				if !ok {
					want = onePass(t, set)
					onePasses[id] = want
				}
				if d := x.fake.Diff(want); d != "" {
					t.Fatalf("%s %v after %d: %s", name, p, k+1, d)
				}
			}
			writes += x.writes
			if p[0] == 0 && p[1] == 1 && p[2] == 2 {
				checkReference(t, name, rs, x.o, x.fake)
			}
		})
		if writes == 0 {
			t.Fatalf("%s: nothing written", name)
		}
	}
}

// Generated sequences: shuffled, duplicated, late across hours and days, and
// reloaded cold halfway. A cold load's baseline, the render of the replayed
// state, is the warm one, so with nothing new it writes nothing. A changed
// render_version rewrites a key's rows whole, an older render's included.
func TestRenderedGeneratedSequences(t *testing.T) {
	state.Heavy(t)
	for seed := int64(1); seed <= 6/state.SeedFrac+1; seed++ {
		rng := rand.New(rand.NewSource(seed))
		state.ResetBatches()
		rs := state.GenerateHours(rng, 3, 120)
		in := state.Shuffled(rng, rs, 30)
		x := newRunner()
		for _, r := range in[:len(in)/2] {
			x.apply(t, r)
		}
		coldLoad(t, x)
		rewriteOne(t, x, rng)
		for _, r := range in[len(in)/2:] {
			x.apply(t, r)
		}
		label := fmt.Sprintf("seed %d", seed)
		if d := x.fake.Diff(onePass(t, rs)); d != "" {
			t.Fatalf("%s: the fake isn't one pass's render: %s", label, d)
		}
		checkReference(t, label, rs, x.o, x.fake)
	}
}

// coldLoad reloads every key from its checkpoint: its baseline is the render of
// the replayed state, which is the warm one, and a run with nothing new writes
// nothing.
func coldLoad(t *testing.T, x *runner) {
	t.Helper()
	for k, s := range x.o.States() {
		b, err := state.Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		cold, err := state.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		x.o.States()[k] = cold
		r := renderKey(t, k, cold)
		if !r.Baseline().Equal(x.base[k]) {
			t.Fatalf("key %s: the cold load's baseline isn't what the last commit wrote", k)
		}
		if d := r.Diff(r.Baseline()); !d.Empty() {
			t.Fatalf("key %s: a cold load with nothing new writes %d tables", k, len(d.Tables))
		}
		x.base[k] = r.Baseline()
	}
}

// rewriteOne gives a session key rows an older render wrote (a stale row in
// each of its tables, and one of its key's) and runs it with another
// render_version: it rewrites the key's rows whole.
func rewriteOne(t *testing.T, x *runner, rng *rand.Rand) {
	t.Helper()
	var keys []string
	for k := range x.o.States() {
		if strings.HasPrefix(k, "s:") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	key := keys[rng.Intn(len(keys))]
	sid := strings.TrimPrefix(key, "s:")
	for _, tb := range render.Tables {
		row := make([]any, len(tb.Columns))
		for i, c := range tb.Columns {
			switch {
			case c.Name == render.OwnerColumn(tb.Name) && c.Name == "key":
				row[i] = key
			case c.Name == render.OwnerColumn(tb.Name):
				row[i] = sid
			case c.Type == "text" || c.Type == "text NOT NULL":
				row[i] = "stale"
			case c.Type == "bigint" || c.Type == "bigint NOT NULL":
				row[i] = int64(1) << 40
			case c.Type == "text[]":
				row[i] = []string{"stale"}
			case c.Type == "bigint[]":
				row[i] = []int64{1 << 40}
			}
		}
		x.plant(t, tb.Name, row)
	}
	r := renderKey(t, key, x.o.States()[key])
	x.write(t, r.Rewrite())
	x.base[key] = r.Baseline()
	for _, tb := range render.Tables {
		for _, row := range x.fake.Rows(tb.Name) {
			for _, v := range row {
				if v == "stale" || reflect.DeepEqual(v, []string{"stale"}) {
					t.Fatalf("%s: a stale row survived the rewrite: %v", tb.Name, row)
				}
			}
		}
	}
}

// --- named cases ----------------------------------------------------------------

// runs applies rs one run each and is the runner.
func runs(t *testing.T, rs []records.Record) *runner {
	t.Helper()
	x := newRunner()
	for _, r := range rs {
		x.apply(t, r)
	}
	return x
}

// lastDiff applies r and is its run's diff.
func lastDiff(t *testing.T, x *runner, r records.Record) *render.Diff {
	t.Helper()
	x.o.Apply(r)
	rows := renderKey(t, r.RoutingKey, x.o.States()[r.RoutingKey])
	d := rows.Diff(x.base[r.RoutingKey])
	x.write(t, d)
	x.base[r.RoutingKey] = rows.Baseline()
	return d
}

func tableDiff(d *render.Diff, name string) render.TableDiff {
	for _, td := range d.Tables {
		if td.Table.Name == name {
			return td
		}
	}
	return render.TableDiff{}
}

func colOf(td render.TableDiff, name string) int {
	for i, c := range td.Table.Columns {
		if c.Name == name {
			return i
		}
	}
	panic(name)
}

// A new call renumbers no later mark block: its marks' rows are the call's own
// (and the network lane's last segment), never a report's, friction's or
// permission point's.
func TestRenderedNewCallRenumbersNoLaterBlock(t *testing.T) {
	state.ResetBatches()
	c1 := state.WithIP(state.WithIDs(state.Step("c1", "s1", "p1", state.At(60_000)), "r1", "cr1"), "10.0.0.1")
	x := runs(t, []records.Record{
		state.TypedPrompt("s1", "p1", "1", state.At(1000)), state.CallEnd(c1),
		state.Success("s1", state.At(70_000), "r1"), state.Success("s1", state.At(75_000), "r-missing"),
		state.Pev("s1", "p1", "tengu_cancel", state.At(80_000), `{"cc_prompt_id":"p1"}`),
		state.CCEvent("s1", "tengu_api_query", state.At(90_000), `{"permissionMode":"plan"}`),
	})
	kinds := map[string]bool{}
	for _, row := range x.fake.Rows("session_marks") {
		kinds[row[2].(string)] = true
	}
	for _, k := range []string{"call", "reported", "missed", "friction", "permission", "network"} {
		if !kinds[k] {
			t.Fatalf("the setup has no %s mark: %v", k, kinds)
		}
	}
	c2 := state.WithIP(state.WithIDs(state.Step("c2", "s1", "p1", state.At(100_000)), "r2", "cr2"), "10.0.0.1")
	td := tableDiff(lastDiff(t, x, state.CallEnd(c2)), "session_marks")
	kind, seq := colOf(td, "kind"), colOf(td, "seq")
	var got []string
	for _, row := range td.Upserts {
		got = append(got, fmt.Sprintf("%s@%d", row[kind], row[seq].(int64)>>32))
		if k := row[kind]; k != "call" && k != "network" {
			t.Errorf("a new call rewrote a %s mark: %v", k, row)
		}
	}
	if len(td.Deletes) > 0 || len(got) == 0 {
		t.Fatalf("marks written: %v, deleted %v", got, td.Deletes)
	}
}

// A late event rewrites only the event rows after it.
func TestRenderedLateEventRewritesOnlyTheRowsAfterIt(t *testing.T) {
	state.ResetBatches()
	var rs []records.Record
	for i := 0; i < 20; i++ {
		rs = append(rs, state.CCEvent("s1", "tengu_api_query", state.At(int64(i)*60_000), `{}`))
	}
	late := state.CCEvent("s1", "tengu_late", state.At(15*60_000+1), `{}`)
	x := runs(t, rs)
	td := tableDiff(lastDiff(t, x, late), "event_rows")
	seq := colOf(td, "seq")
	var seqs []int64
	for _, row := range td.Upserts {
		seqs = append(seqs, row[seq].(int64))
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	want := []int64{16, 17, 18, 19, 20} // the late event's place, and the rows after it
	if !reflect.DeepEqual(seqs, want) || len(td.Deletes) > 0 {
		t.Fatalf("event rows written %v (deleted %v), want %v", seqs, td.Deletes, want)
	}
}

// A key's day changes only its session row.
func TestRenderedDayChangesOnlyTheSessionRow(t *testing.T) {
	state.ResetBatches()
	x := runs(t, []records.Record{state.TypedPrompt("s1", "p1", "1", state.At(1000)),
		state.CallEnd(state.Step("c1", "s1", "p1", state.At(2000)))})
	r, err := render.Render("s:s1", x.o.States()["s:s1"].Rows(), "2026-11-05")
	if err != nil {
		t.Fatal(err)
	}
	d := r.Diff(x.base["s:s1"])
	if u, del := d.Counts(); len(d.Tables) != 1 || d.Tables[0].Table.Name != "session_rows" || u != 1 || del != 0 {
		t.Fatalf("a new day wrote %d tables (%d upserts, %d deletes)", len(d.Tables), u, del)
	}
}

// --- on Postgres ------------------------------------------------------------------

// The layers' writes on Postgres: a generated sequence's runs, each diff
// written in its own transaction, and a rewrite; Postgres then holds exactly
// the fake's rows.
func TestRenderedOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pg.Open(ctx, pgtest.Fresh(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pg.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	state.ResetBatches()
	rng := rand.New(rand.NewSource(7))
	rs := state.GenerateHours(rng, 2, 60)
	in := state.Shuffled(rng, rs, 10)
	x := newRunner()
	x.pgRuns = func(d *render.Diff) {
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return d.Write(ctx, tx) }); err != nil {
			t.Fatalf("writing key %s's diff: %v", d.Key, err)
		}
	}
	for _, r := range in[:len(in)/2] {
		x.apply(t, r)
	}
	samePostgres(t, ctx, pool, x.fake)
	rewriteOne(t, x, rng)
	for _, r := range in[len(in)/2:] {
		x.apply(t, r)
	}
	samePostgres(t, ctx, pool, x.fake)
	if d := x.fake.Diff(onePass(t, rs)); d != "" {
		t.Fatal(d)
	}
}

// samePostgres requires Postgres's rendered tables to hold the fake's rows.
func samePostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *rendertest.Fake) {
	t.Helper()
	got := rendertest.New()
	for _, tb := range render.Tables {
		rows, err := pool.Query(ctx, "SELECT * FROM "+tb.Name)
		if err != nil {
			t.Fatal(err)
		}
		vals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([]any, error) { return r.Values() })
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			got.Plant(tb.Name, rendertest.FromPostgres(tb, v))
		}
	}
	if d := got.Diff(f); d != "" {
		t.Fatalf("Postgres isn't the fake: %s", d)
	}
}
