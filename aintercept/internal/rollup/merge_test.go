package rollup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// mtRow is the merge tests' table: enough to place a key by its last activity.
type mtRow struct {
	LastNS int64  `parquet:"last_ns"`
	V      string `parquet:"v"`
}

const mtTable = "mt_rows"

func init() { stream.RegisterTable[mtRow](mtTable) }

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func day(d int) int64 { return t0.Add(time.Duration(d) * 24 * time.Hour).UnixNano() }

// memQueue is Redis's `committed` set and the merge's marker.
type memQueue struct {
	set   map[string]bool
	alive bool
}

func newQueue(keys ...string) *memQueue {
	q := &memQueue{set: map[string]bool{}, alive: true}
	for _, k := range keys {
		q.set[k] = true
	}
	return q
}

func (q *memQueue) Pop(_ context.Context, n int) ([]string, error) {
	var out []string
	for k := range q.set {
		if len(out) == n {
			break
		}
		out = append(out, k)
		delete(q.set, k)
	}
	sort.Strings(out)
	return out, nil
}

func (q *memQueue) Requeue(_ context.Context, keys []string) error {
	for _, k := range keys {
		q.set[k] = true
	}
	return nil
}
func (q *memQueue) Alive(context.Context) (bool, error) { return q.alive, nil }
func (q *memQueue) MarkAlive(context.Context) error     { q.alive = true; return nil }

// commitKey writes a key's commit n whose single new output holds rows (an id
// with an empty value is a tombstone), on top of commit n-1's outputs.
func commitKey(t *testing.T, s stream.Store, key string, n int64, rows map[string]mtRow) {
	t.Helper()
	ctx := context.Background()
	var prev []Out
	if c, err := ReadNewest(ctx, s, "", key); err != nil {
		t.Fatal(err)
	} else if c != nil {
		prev = c.Out
	}
	var rs []stream.Row
	for id, v := range rows {
		if v == (mtRow{}) {
			rs = append(rs, stream.Row{Key: key, ID: id, Tomb: true})
		} else {
			rs = append(rs, stream.Row{Key: key, ID: id, Value: v})
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	tab, _ := stream.TableFor(mtTable)
	b, err := tab.Encode(rs)
	if err != nil {
		t.Fatal(err)
	}
	dir := OutDir(n, "t")
	if err := s.Put(ctx, KeyDir("", key)+TableFile(dir, mtTable), b); err != nil {
		t.Fatal(err)
	}
	c := Commit{N: n, Key: key, Out: append(prev, Out{Dir: dir, Tables: []string{mtTable}}), UpdatedNS: day(0)}
	body, _ := json.Marshal(c)
	if ok, err := s.PutIfAbsent(ctx, CommitKey("", key, n), body); err != nil || !ok {
		t.Fatalf("commit %s %d: %v %v", key, n, ok, err)
	}
}

// merged is every merged row by partition ("index" or a day), as key/id -> value.
func merged(t *testing.T, s stream.Store) map[string]map[string]mtRow {
	t.Helper()
	ctx := context.Background()
	c, err := ReadNewestMerge(ctx, s, "")
	if err != nil || c == nil {
		t.Fatalf("merge commit: %v %v", c, err)
	}
	out := map[string]map[string]mtRow{}
	read := func(part string, o Out) {
		if !contains(o.Tables, mtTable) {
			return
		}
		rows, err := readTableFile(ctx, s, MergeDir("")+TableFile(o.Dir, mtTable), mtTable)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]mtRow{}
		for _, r := range rows {
			m[r.Key+"/"+r.ID] = r.Value.(mtRow)
		}
		out[part] = m
	}
	read("index", c.Index)
	for d, o := range c.Days {
		read(d, o)
	}
	return out
}

func run(t *testing.T, s stream.Store, q MergeQueue) MergeResult {
	t.Helper()
	res, err := (&Merger{Store: s, Queue: q, Clock: stream.NewFakeClock(t0.Add(72 * time.Hour))}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMergePlacesEachKeyInOnePartition(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	commitKey(t, s, "s:a", 1, map[string]mtRow{"1": {LastNS: day(0), V: "a1"}})
	commitKey(t, s, "s:b", 1, map[string]mtRow{"1": {LastNS: day(1), V: "b1"}})
	commitKey(t, s, "a:acct", 1, map[string]mtRow{"1": {LastNS: day(1), V: "x"}})
	commitKey(t, s, "c:h:2026-11-12", 1, map[string]mtRow{"1": {LastNS: day(0), V: "c"}})
	res := run(t, s, newQueue("s:a", "s:b", "a:acct", "c:h:2026-11-12"))
	if res.N != 1 || res.Keys != 4 {
		t.Fatalf("result %+v", res)
	}
	want := map[string]map[string]mtRow{
		"index":      {"a:acct/1": {LastNS: day(1), V: "x"}},
		"2026-11-10": {"s:a/1": {LastNS: day(0), V: "a1"}},
		"2026-11-11": {"s:b/1": {LastNS: day(1), V: "b1"}},
		"2026-11-12": {"c:h:2026-11-12/1": {LastNS: day(0), V: "c"}},
	}
	if got := merged(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
}

func TestMergeRewritesOnlyTouchedPartitions(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	commitKey(t, s, "s:a", 1, map[string]mtRow{"1": {LastNS: day(0), V: "a1"}})
	commitKey(t, s, "s:b", 1, map[string]mtRow{"1": {LastNS: day(1), V: "b1"}})
	run(t, s, newQueue("s:a", "s:b"))
	before, _ := ReadNewestMerge(context.Background(), s, "")
	commitKey(t, s, "s:a", 2, map[string]mtRow{"2": {LastNS: day(0) + 1, V: "a2"}})
	res := run(t, s, newQueue("s:a"))
	sort.Strings(res.Rewritten)
	if !reflect.DeepEqual(res.Rewritten, []string{"2026-11-10", "index"}) {
		t.Fatalf("rewrote %v", res.Rewritten)
	}
	after, _ := ReadNewestMerge(context.Background(), s, "")
	if after.Days["2026-11-11"].Dir != before.Days["2026-11-11"].Dir {
		t.Fatal("an untouched day was rewritten")
	}
	if got := merged(t, s)["2026-11-10"]; len(got) != 2 {
		t.Fatalf("day 10: %v", got)
	}
}

func TestMergeMovesAResumedSessionToItsNewDay(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	commitKey(t, s, "s:a", 1, map[string]mtRow{"1": {LastNS: day(0), V: "a1"}})
	commitKey(t, s, "s:b", 1, map[string]mtRow{"1": {LastNS: day(0), V: "b1"}})
	run(t, s, newQueue("s:a", "s:b"))
	commitKey(t, s, "s:a", 2, map[string]mtRow{"2": {LastNS: day(2), V: "a2"}})
	run(t, s, newQueue("s:a"))
	got := merged(t, s)
	if _, ok := got["2026-11-10"]["s:a/1"]; ok {
		t.Fatal("the resumed session's rows stayed in its old day")
	}
	if len(got["2026-11-10"]) != 1 || len(got["2026-11-12"]) != 2 {
		t.Fatalf("days %v", got)
	}
}

func TestMergeDropsTombstonedRows(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	commitKey(t, s, "s:a", 1, map[string]mtRow{"1": {LastNS: day(0), V: "a1"}, "2": {LastNS: day(0), V: "a2"}})
	commitKey(t, s, "s:a", 2, map[string]mtRow{"2": {}})
	run(t, s, newQueue("s:a"))
	if got := merged(t, s)["2026-11-10"]; len(got) != 1 || got["s:a/1"].V != "a1" {
		t.Fatalf("merged %v", got)
	}
}

func TestMergeFencedRunRequeuesItsKeys(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	commitKey(t, s, "s:a", 1, map[string]mtRow{"1": {LastNS: day(0), V: "a1"}})
	if ok, _ := s.PutIfAbsent(context.Background(), MergeCommitKey("", 1), []byte(`{"n":1,"days":{}}`)); !ok {
		t.Fatal("setup")
	}
	// A run that read no commit yet races the one above: it loses n=1.
	q := newQueue("s:a")
	m := &Merger{Store: fencedStore{s}, Queue: q, Clock: stream.NewFakeClock(t0)}
	_, err := m.RunOnce(context.Background())
	if !errors.Is(err, ErrMergeFenced) {
		t.Fatalf("err %v", err)
	}
	if !q.set["s:a"] {
		t.Fatal("the fenced run lost its key")
	}
}

// fencedStore hides merge commits from listing, as a run that listed before
// another's commit landed sees them.
type fencedStore struct{ stream.Store }

func (f fencedStore) List(ctx context.Context, prefix string) ([]process.Object, error) {
	if prefix == MergeDir("")+"commit/" {
		return nil, nil
	}
	return f.Store.List(ctx, prefix)
}

func TestMergeRecoversKeysAfterARedisWipe(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	now := t0.Add(72 * time.Hour)
	commitKey(t, s, "s:a", 1, map[string]mtRow{"1": {LastNS: day(0), V: "a1"}})
	commitKey(t, s, "s:old", 1, map[string]mtRow{"1": {LastNS: day(0), V: "o"}})
	body, _ := json.Marshal([]string{"s:a"})
	_ = s.Put(context.Background(), RecentName("", now.Add(-5*time.Minute), "pod-1", 1), body)
	old, _ := json.Marshal([]string{"s:old"})
	_ = s.Put(context.Background(), RecentName("", now.Add(-time.Hour), "pod-1", 2), old)
	q := &memQueue{set: map[string]bool{}, alive: false} // wiped: no marker, no keys
	res, err := (&Merger{Store: s, Queue: q, Clock: stream.NewFakeClock(now)}).RunOnce(context.Background())
	if err != nil || !res.Recovered || res.Keys != 1 || !q.alive {
		t.Fatalf("res %+v err %v alive %v", res, err, q.alive)
	}
}

// Every order of three keys' updates, merged one key per run or all at once,
// ends with the same partitions: each key's newest rows, in its newest day.
func TestMergeOrderFree(t *testing.T) {
	type up struct {
		key  string
		rows map[string]mtRow
	}
	ups := []up{
		{"s:a", map[string]mtRow{"1": {LastNS: day(0), V: "a1"}}},
		{"s:a", map[string]mtRow{"2": {LastNS: day(1), V: "a2"}}},
		{"s:b", map[string]mtRow{"1": {LastNS: day(0), V: "b1"}}},
		{"a:x", map[string]mtRow{"1": {LastNS: day(0), V: "x"}}},
	}
	var want map[string]map[string]mtRow
	for _, perm := range permutations(len(ups)) {
		for _, together := range []bool{false, true} {
			s := stream.NewMemStore(stream.RealClock{})
			n := map[string]int64{}
			q := newQueue()
			for _, i := range perm {
				u := ups[i]
				if u.key == "s:a" && n["s:a"] == 0 && i == 1 { // a's second update can't land before its first
					continue
				}
				n[u.key]++
				commitKey(t, s, u.key, n[u.key], u.rows)
				q.set[u.key] = true
				if !together {
					run(t, s, q)
				}
			}
			if n["s:a"] < 2 { // apply the skipped second update
				n["s:a"]++
				commitKey(t, s, "s:a", n["s:a"], ups[1].rows)
				q.set["s:a"] = true
			}
			run(t, s, q)
			got := merged(t, s)
			if want == nil {
				want = got
			} else if !reflect.DeepEqual(got, want) {
				t.Fatalf("order %v together=%v: %v, want %v", perm, together, got, want)
			}
		}
	}
	if len(want["2026-11-11"]) != 2 || len(want["2026-11-10"]) != 1 || len(want["index"]) != 1 {
		t.Fatalf("partitions %v", want)
	}
}

// Generated: random keys and updates, merges at random points with lost
// commits; the final partitions hold exactly each key's newest rows.
func TestMergeGenerated(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := stream.NewMemStore(stream.RealClock{})
		q := newQueue()
		n := map[string]int64{}
		final := map[string]map[string]mtRow{}
		for step := 0; step < 60; step++ {
			key := fmt.Sprintf("s:%d", rng.Intn(6))
			if rng.Intn(5) == 0 {
				key = fmt.Sprintf("a:%d", rng.Intn(2))
			}
			id := fmt.Sprint(rng.Intn(4))
			v := mtRow{LastNS: day(rng.Intn(4)), V: fmt.Sprint(step)}
			if final[key] == nil {
				final[key] = map[string]mtRow{}
			}
			if _, ok := final[key][id]; ok && rng.Intn(4) == 0 {
				v = mtRow{}
				delete(final[key], id)
			} else {
				final[key][id] = v
			}
			n[key]++
			commitKey(t, s, key, n[key], map[string]mtRow{id: v})
			q.set[key] = true
			if rng.Intn(3) == 0 {
				run(t, s, q)
			}
		}
		run(t, s, q)
		got := merged(t, s)
		seen := map[string]mtRow{}
		for _, rows := range got {
			for k, v := range rows {
				if _, dup := seen[k]; dup {
					t.Fatalf("seed %d: %s in two partitions", seed, k)
				}
				seen[k] = v
			}
		}
		want := map[string]mtRow{}
		for key, rows := range final {
			for id, v := range rows {
				want[key+"/"+id] = v
			}
		}
		if !reflect.DeepEqual(seen, want) {
			t.Fatalf("seed %d: merged %v, want %v", seed, seen, want)
		}
	}
}

func permutations(n int) [][]int {
	if n == 1 {
		return [][]int{{0}}
	}
	var out [][]int
	for _, p := range permutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}
