package stream

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// A tiered table base: run files fold into level-0 segments, and Fanout
// segments of one level fold into one segment of the next. Each row is
// rewritten once per level it climbs, O(log N) times over its life, never
// the whole base per fold.

type segment struct {
	level int
	seg   int64
	parts []FileRef
}

// segmentsOf is a table's base as the commit lists it, grouped into segments
// (consecutive parts sharing Seg), in the commit's order.
func segmentsOf(c *Commit, table string) []segment {
	var out []segment
	for _, p := range c.Base[table] {
		if n := len(out); n > 0 && out[n-1].seg == p.Seg {
			out[n-1].parts = append(out[n-1].parts, p)
			continue
		}
		out = append(out, segment{level: p.Level, seg: p.Seg, parts: []FileRef{p}})
	}
	return out
}

// checkShape: segments oldest first (level descending, then creation order),
// fewer than fanout per level, each segment's parts in key order and never
// overlapping.
func checkShape(t *testing.T, c *Commit, table string, fanout int) []segment {
	t.Helper()
	segs := segmentsOf(c, table)
	perLevel := map[int]int{}
	for i, s := range segs {
		perLevel[s.level]++
		if i > 0 {
			p := segs[i-1]
			if p.level < s.level || (p.level == s.level && p.seg >= s.seg) {
				t.Fatalf("%s: segments out of order (oldest first): %+v then %+v", table, p, s)
			}
		}
		for j, part := range s.parts {
			if part.MinKey > part.MaxKey || (j > 0 && s.parts[j-1].MaxKey >= part.MinKey) {
				t.Fatalf("%s: segment %d parts overlap or are out of key order: %+v", table, s.seg, s.parts)
			}
		}
	}
	for l, n := range perLevel {
		if n >= fanout {
			t.Fatalf("%s: level %d holds %d segments, fanout %d: it should have folded", table, l, n, fanout)
		}
	}
	return segs
}

func latest(t *testing.T, h *harness, ctx context.Context) *Commit {
	t.Helper()
	c, err := LoadLatest(ctx, h.s, h.l, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func partKeys(segs []segment) map[string]bool {
	out := map[string]bool{}
	for _, s := range segs {
		for _, p := range s.parts {
			out[p.Key] = true
		}
	}
	return out
}

// Atomic: runs fold into a new level-0 segment; an older segment is left as it
// is (not rewritten) until its level fills.
func TestTieredRunsFoldIntoLevelZero(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns, w.Fanout, w.CompactEvery = 3, 4, time.Hour
	cycle := func() {
		h.upload(0, h.gen.batch(10))
		h.clock.Advance(5 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		cycle()
	}
	first := checkShape(t, latest(t, h, ctx), countTable, 4)
	if len(first) != 1 || first[0].level != 0 {
		t.Fatalf("after 3 runs: want one level-0 segment, got %+v", first)
	}
	for i := 0; i < 3; i++ {
		cycle()
	}
	c := latest(t, h, ctx)
	segs := checkShape(t, c, countTable, 4)
	if len(segs) != 2 || segs[0].level != 0 || segs[1].level != 0 {
		t.Fatalf("after 6 runs: want two level-0 segments, got %+v", segs)
	}
	now := partKeys(segs)
	for k := range partKeys(first) {
		if !now[k] {
			t.Fatalf("the first segment was rewritten (%s gone) by a fold that only needed the new runs", k)
		}
	}
	if len(c.Runs[countTable]) >= 3 {
		t.Fatalf("runs not folded: %d", len(c.Runs[countTable]))
	}
	h.checkAll(ctx)
}

// Atomic, per level transition: fanout segments of a level fold into one of
// the next, which goes after that level's older segments and before every
// newer one.
func TestTieredLevelsCascade(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns, w.Fanout, w.CompactEvery = 2, 2, time.Hour
	maxLevel := 0
	for i := 0; i < 32; i++ {
		h.upload(0, h.gen.batch(8))
		h.clock.Advance(5 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		for _, s := range checkShape(t, latest(t, h, ctx), countTable, 2) {
			if s.level > maxLevel {
				maxLevel = s.level
			}
		}
		h.checkAll(ctx)
	}
	if maxLevel < 3 {
		t.Fatalf("32 runs at 2 runs per fold and fanout 2 should reach level 3, got %d", maxLevel)
	}
}

// Atomic: a tombstone survives a fold while older segments hold rows it must
// remove, and is dropped only in the bottom (oldest) segment.
func TestTieredTombstonesKeptUntilBottom(t *testing.T) {
	h, ctx := setup(t, 1)
	h.m = toggleModel{}
	w := h.worker("a", 1)
	w.CompactRuns, w.Fanout, w.CompactEvery = 2, 2, time.Hour
	keptAbove := false
	for i := 0; i < 24; i++ {
		h.upload(0, h.gen.batch(9))
		h.clock.Advance(5 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		h.checkAll(ctx)
		segs := checkShape(t, latest(t, h, ctx), "toggled", 2)
		for i, s := range segs {
			for _, p := range s.parts {
				b, err := h.s.Get(ctx, p.Key)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := toggleTable.Decode(b)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					if !r.Tomb {
						continue
					}
					if i == 0 {
						t.Fatalf("the bottom segment (%d) holds a tombstone for %s: nothing older is left for it to remove", s.seg, r.Key)
					}
					keptAbove = true
				}
			}
		}
	}
	if !keptAbove {
		t.Fatal("no tombstone was ever kept in a segment above the bottom: the test didn't exercise the case")
	}
}

// Atomic: a fenced owner's fold neither commits nor overwrites the new
// owner's files.
func TestTieredFencedOwnerFold(t *testing.T) {
	h, ctx := setup(t, 1)
	a := h.worker("a", 1)
	a.CompactRuns, a.Fanout, a.CompactEvery = 2, 2, time.Hour
	h.upload(0, h.gen.batch(10))
	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	b := h.worker("b", 2)
	b.CompactRuns, b.Fanout, b.CompactEvery = 2, 2, time.Hour
	for i := 0; i < 4; i++ { // b folds, more than once
		h.upload(0, h.gen.batch(10))
		h.clock.Advance(5 * time.Second)
		if _, err := b.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	before := partKeys(segmentsOf(latest(t, h, ctx), countTable))
	saved := map[string][]byte{}
	for k := range before {
		v, err := h.s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		saved[k] = v
	}
	h.upload(0, h.gen.batch(10)) // a, unaware, reaches its own fold
	if _, err := a.Cycle(ctx); !errors.Is(err, ErrFenced) {
		t.Fatalf("a stale owner's cycle: want ErrFenced, got %v", err)
	}
	for k, v := range saved {
		got, err := h.s.Get(ctx, k)
		if err != nil || string(got) != string(v) {
			t.Fatalf("the new owner's %s was overwritten or lost by the fenced fold", k)
		}
	}
	if _, err := b.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.checkAll(ctx)
}

// Sequences: folds (by run count, by time, and a full Compact) interleaved
// with cycles in every order: the newest-wins view always equals one pass.
func TestTieredFoldsInEveryOrder(t *testing.T) {
	g := newGen(7, 6)
	batches := [][]records.Record{g.batch(12), g.batch(12), g.batch(12), g.batch(12)}
	ops := []string{"u0", "u1", "u2", "full", "time"}
	permute(len(ops), func(order []int) {
		h, ctx := setup(t, 1)
		w := h.worker("a", 1)
		w.CompactRuns, w.Fanout, w.CompactEvery = 2, 2, time.Minute
		for _, i := range order {
			switch ops[i] {
			case "full":
				if err := w.Compact(ctx); err != nil {
					t.Fatal(err)
				}
			case "time":
				h.clock.Advance(2 * time.Minute)
				h.upload(0, batches[3])
				if _, err := w.Cycle(ctx); err != nil {
					t.Fatal(err)
				}
			default:
				h.upload(0, batches[int(ops[i][1]-'0')])
				h.clock.Advance(5 * time.Second)
				if _, err := w.Cycle(ctx); err != nil {
					t.Fatal(err)
				}
			}
			checkShape(t, latest(t, h, ctx), countTable, 2)
			h.checkAll(ctx)
		}
	})
}

// rowModel writes one row per record (the record's id), so a bucket's table
// grows with history: the case compaction's cost must stay O(log N) per row in.
type rowModel struct{ CountModel }

type rowVal struct {
	TS int64 `parquet:"ts"`
}

const rowsTable = "t_rows"

var _ = RegisterTable[rowVal](rowsTable)

func (rowModel) Tables() []string { return []string{rowsTable} }

func (rowModel) Apply(st any, r *records.Record) Delta {
	s := st.(*countState)
	s.N++
	return Delta{rowsTable: {{Key: s.Key, ID: r.RecordID, Value: rowVal{TS: RecordTS(r)}}}}
}

// tieredSizes are the history sizes the cost test runs (rows in); -tags full
// adds 100,000 and 1,000,000.
var tieredSizes = []int{10, 1000}

// Cost: rows rewritten by compaction per row in, and the segments a key
// lookup opens, grow O(log N), not O(N).
func TestTieredRewritesPerRowGrowLogarithmically(t *testing.T) {
	const fanout, runs = 4, 4
	for _, n := range tieredSizes {
		h, ctx := setup(t, 1)
		h.m = rowModel{}
		h.gen = newGen(11, 50)
		w := h.worker("a", 1)
		w.CompactRuns, w.Fanout, w.CompactEvery, w.PartRows = runs, fanout, time.Hour, 4096
		batch := n / 2000
		if batch < 10 {
			batch = 10
		}
		in := 0
		for in < n {
			k := batch
			if n-in < k {
				k = n - in
			}
			h.upload(0, h.gen.batch(k))
			in += k
			h.clock.Advance(5 * time.Second)
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
		}
		segs := checkShape(t, latest(t, h, ctx), rowsTable, fanout)
		maxLevel := 0
		for _, s := range segs {
			if s.level > maxLevel {
				maxLevel = s.level
			}
		}
		perFold := float64(batch * runs)
		bound := 2 + math.Log(math.Max(1, float64(n)/perFold))/math.Log(fanout)
		rewrites := float64(w.BaseRowsWritten) / float64(n)
		t.Logf("n=%d: %.2f rewrites per row (bound %.2f), %d levels, %d segments", n, rewrites, bound, maxLevel+1, len(segs))
		if rewrites > bound {
			t.Fatalf("n=%d: %.2f rewrites per row, want at most %.2f (O(log N))", n, rewrites, bound)
		}
		if float64(maxLevel) > bound {
			t.Fatalf("n=%d: %d levels, want at most %.2f", n, maxLevel+1, bound)
		}
		if len(segs) > fanout*(maxLevel+1) {
			t.Fatalf("n=%d: a key lookup opens up to %d segments, want at most fanout×levels = %d", n, len(segs), fanout*(maxLevel+1))
		}
		if n > 10 && w.BaseRowsWritten == 0 {
			t.Fatalf("n=%d: never compacted", n)
		}
		h.checkAll(ctx)
	}
}

// permute calls fn with every order of 0..n-1.
func permute(n int, fn func([]int)) {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			fn(p)
			return
		}
		for i := k; i < n; i++ {
			p[k], p[i] = p[i], p[k]
			rec(k + 1)
			p[k], p[i] = p[i], p[k]
		}
	}
	rec(0)
}
