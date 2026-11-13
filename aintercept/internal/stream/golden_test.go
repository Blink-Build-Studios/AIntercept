package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/stream-golden")

// goldenDir is the contract fixture the API's reader is tested against (at the
// repo root, so both sides find it): every object a small run writes, and the
// tables a reader must see. Any change to the format shows up as a diff here.
const goldenDir = "../../../testdata/stream-golden"

// goldenModel writes the stand-in table and a table with tombstones.
type goldenModel struct{ CountModel }

func (goldenModel) Tables() []string { return []string{countTable, "toggled"} }

func (m goldenModel) Apply(st any, r *records.Record) Delta {
	d := m.CountModel.Apply(st, r)
	s := st.(*countState)
	if s.N%2 == 1 {
		d["toggled"] = []Row{{Key: s.Key, ID: s.Key, Value: toggleRow{N: s.N}}}
	} else {
		d["toggled"] = []Row{{Key: s.Key, ID: s.Key, Tomb: true}}
	}
	return d
}

// goldenLayout runs two buckets through several commits, a compaction and
// tombstones, deterministically, and returns every object written.
func goldenLayout(t *testing.T) (map[string][]byte, map[string][]Row) {
	t.Helper()
	n := 0
	saved := nonce
	nonce = func() string { n++; return fmt.Sprintf("nonce-%04d", n) }
	defer func() { nonce = saved }()
	clock := NewFakeClock(time.Date(2027, 1, 5, 10, 0, 0, 0, time.UTC))
	s := NewMemStore(clock)
	h := &harness{t: t, s: s, clock: clock, l: Layout{Prefix: "golden/"}, bits: 1, gen: newGen(42, 4), m: goldenModel{}}
	ctx := context.Background()
	ws := []*Bucket{}
	for b := 0; b < 2; b++ {
		w := h.worker("replica-a", 3)
		w.B, w.CompactRuns, w.Fanout, w.PartRows = b, 2, 2, 4
		ws = append(ws, w)
	}
	for i := 0; i < 7; i++ { // 7 commits per bucket: runs fold every 2, levels every 2 segments, so a level-1 and a level-0 segment
		h.upload(i%2, h.gen.batch(6))
		clock.Advance(20 * time.Second)
		for _, w := range ws {
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The golden must show what a reader has to handle: segments at more than
	// one level whose key ranges overlap (newest wins across them).
	tiered := false
	for b := 0; b < 2; b++ {
		c, err := LoadLatest(ctx, s, h.l, b)
		if err != nil {
			t.Fatal(err)
		}
		for table := range c.Base {
			segs := segmentsOf(c, table)
			for i := 1; i < len(segs); i++ {
				p, q := segs[i-1], segs[i]
				if p.level > q.level && p.parts[0].MinKey <= q.parts[len(q.parts)-1].MaxKey &&
					q.parts[0].MinKey <= p.parts[len(p.parts)-1].MaxKey {
					tiered = true
				}
			}
		}
	}
	if !tiered {
		t.Fatal("the golden has no overlapping segments at different levels: change its parameters")
	}
	tables, err := ReadAll(ctx, s, h.l, h.m, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := Reference(h.m, h.uploaded)
	if !tablesEqual(tables, want) {
		t.Fatalf("golden run differs from one pass: %s", diffTables(tables, want))
	}
	objs := map[string][]byte{}
	all, _ := s.List(ctx, "")
	for _, o := range all {
		b, _ := s.Get(ctx, o.Key)
		objs[o.Key] = b
	}
	return objs, tables
}

// expectedJSON is the tables a reader must produce: for each table, the
// newest-wins rows (no tombstones), sorted by key then id, each value as
// {column: value}.
func expectedJSON(t *testing.T, tables map[string][]Row) []byte {
	type row struct {
		Key   string `json:"key"`
		ID    string `json:"id"`
		Value any    `json:"value"`
	}
	out := map[string][]row{}
	for name, rows := range nonEmpty(tables) {
		tb, err := TableFor(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			out[name] = append(out[name], row{r.Key, r.ID, tb.ValueMap(r.Value)})
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestStreamGolden(t *testing.T) {
	objs, tables := goldenLayout(t)
	objs["expected.json"] = expectedJSON(t, tables)
	if *updateGolden {
		for _, old := range []string{"golden", "expected.json"} { // README.md stays
			if err := os.RemoveAll(filepath.Join(goldenDir, old)); err != nil {
				t.Fatal(err)
			}
		}
		for k, b := range objs {
			p := filepath.Join(goldenDir, filepath.FromSlash(k))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	have := map[string][]byte{}
	err := filepath.WalkDir(goldenDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(goldenDir, p)
		if rel == "README.md" {
			return nil
		}
		b, err := os.ReadFile(p)
		have[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatalf("%v (generate it: go test ./internal/stream -run TestStreamGolden -update)", err)
	}
	var diffs []string
	for k, b := range objs {
		if hb, ok := have[k]; !ok {
			diffs = append(diffs, "missing "+k)
		} else if !bytes.Equal(hb, b) {
			diffs = append(diffs, "differs "+k)
		}
	}
	for k := range have {
		if _, ok := objs[k]; !ok {
			diffs = append(diffs, "extra "+k)
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("the stream format changed (update the golden with -update, and the API's reader with it):\n%s",
			strings.Join(diffs, "\n"))
	}
}
