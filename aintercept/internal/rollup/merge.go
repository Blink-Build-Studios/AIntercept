package rollup

// The merge (docs/PIPELINE.md §3): every 30 seconds one replica, holding
// lock:merge, folds the keys committed since its last run into the org-level
// partitions the API reads for person and org pages.
//
// Layout (under <prefix>/v1/merge/):
//
//	commit/<n 12 digits>.json                     create-only; the current partitions
//	days/<YYYY-MM-DD>/<n 12 digits>-<nonce>/<table>.parquet
//	index/<n 12 digits>-<nonce>/<table>.parquet
//
// Every row of a key lands in one partition: an account key's (a:) in the
// index, which stays small, and every other key's in the day of its last
// activity (a c: or ip: key's own day). A run rewrites only the partitions its
// keys touch: their days now, the days they were in before (a resumed session
// moves to today), and the index, which also holds merge_keys, each key's day.
// Untouched partitions are carried over by reference. Files use the stream
// table codec, so the API reads them like a key's own output.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// TableMergeKeys is the index's table of every merged key's partition (Day,
// empty for the index itself) and the key commit it came from.
const TableMergeKeys = "merge_keys"

// MergeKey is a merge_keys row; the row's key is the routing key.
type MergeKey struct {
	Day string `parquet:"day"`
	N   int64  `parquet:"n"`
}

func init() { stream.RegisterTable[MergeKey](TableMergeKeys) }

// MergeCommit is merge/commit/<n>.json. Paths are relative to <prefix>/v1/merge/.
type MergeCommit struct {
	N         int64          `json:"n"`
	Days      map[string]Out `json:"days"`  // day (YYYY-MM-DD) -> its current output
	Index     Out            `json:"index"` // the account keys' rows, and merge_keys
	NewestNS  int64          `json:"newest_ns"`
	UpdatedNS int64          `json:"updated_ns"`
	// ContentRetained: some call's bodies were read (full interception).
	ContentRetained bool `json:"content_retained"`
}

// MergeDir is <prefix>/v1/merge/.
func MergeDir(prefix string) string { return join(prefix, "v1/merge") + "/" }

// MergeCommitKey is merge commit n's object key.
func MergeCommitKey(prefix string, n int64) string {
	return fmt.Sprintf("%scommit/%012d.json", MergeDir(prefix), n)
}

// MergeQueue is the merge's view of Redis: the keys committed since the last
// run (SPOP committed), putting them back when a run fails, and whether Redis
// still holds the merge's marker (a wipe loses `committed` too).
type MergeQueue interface {
	Pop(ctx context.Context, n int) ([]string, error)
	Requeue(ctx context.Context, keys []string) error
	Alive(ctx context.Context) (bool, error)
	MarkAlive(ctx context.Context) error
}

// ErrMergeFenced is a run that lost its commit to another merge.
var ErrMergeFenced = errors.New("rollup: merge commit taken by another run")

// Merger is the merge loop's one run.
type Merger struct {
	Store  stream.Store
	Prefix string
	Queue  MergeQueue
	Clock  stream.Clock
	// Batch caps the keys one run takes (default 5,000); the rest wait for the next.
	Batch int
	// Recent is how far back a Redis wipe is recovered from the recent index (15 min).
	Recent time.Duration
}

// MergeResult is what a run did.
type MergeResult struct {
	N         int64    // the commit written (0: nothing to do)
	Keys      int      // keys merged
	Rewritten []string // partitions rewritten ("index" or a day)
	Recovered bool     // keys came from the recent index (Redis lost them)
}

// RunOnce merges the keys committed since the last run. A run that loses its
// commit, or fails, puts its keys back.
func (m *Merger) RunOnce(ctx context.Context) (res MergeResult, err error) {
	clock := m.Clock
	if clock == nil {
		clock = stream.RealClock{}
	}
	keys, recovered, err := m.take(ctx, clock.Now())
	if err != nil || len(keys) == 0 {
		return res, err
	}
	res.Keys, res.Recovered = len(keys), recovered
	defer func() {
		if err != nil {
			if qerr := m.Queue.Requeue(context.WithoutCancel(ctx), keys); qerr != nil {
				err = errors.Join(err, qerr)
			}
		}
	}()
	prev, err := ReadNewestMerge(ctx, m.Store, m.Prefix)
	if err != nil {
		return res, err
	}
	next := MergeCommit{N: 1, Days: map[string]Out{}}
	if prev != nil {
		next.N, next.NewestNS, next.ContentRetained = prev.N+1, prev.NewestNS, prev.ContentRetained
		for d, o := range prev.Days {
			next.Days[d] = o
		}
		next.Index = prev.Index
	}

	// Where each key's rows were, and are now.
	oldDay := map[string]string{}
	if prev != nil {
		rows, err := m.readOut(ctx, prev.Index, TableMergeKeys)
		if err != nil {
			return res, err
		}
		for _, r := range rows {
			oldDay[r.Key] = r.Value.(MergeKey).Day
		}
	}
	changed := map[string]bool{}
	newRows := map[string]map[string][]stream.Row{} // partition -> table -> rows
	var mk []stream.Row
	for _, key := range keys {
		c, err := ReadNewest(ctx, m.Store, m.Prefix, key)
		if err != nil {
			return res, err
		}
		if c == nil {
			continue // not committed yet (a stale entry): nothing to merge
		}
		tables, err := ReadKeyTables(ctx, m.Store, m.Prefix, c)
		if err != nil {
			return res, err
		}
		changed[key] = true
		day := DayOf(key, tables, c.UpdatedNS)
		part := partName(day)
		if newRows[part] == nil {
			newRows[part] = map[string][]stream.Row{}
		}
		for t, rows := range tables {
			newRows[part][t] = append(newRows[part][t], rows...)
		}
		mk = append(mk, stream.Row{Key: key, Value: MergeKey{Day: day, N: c.N}})
		if n := rawNewest(tables); n > next.NewestNS {
			next.NewestNS = n
		}
		if c.NewestRowNS > next.NewestNS {
			next.NewestNS = c.NewestRowNS
		}
		next.ContentRetained = next.ContentRetained || retainsContent(tables)
	}
	if len(changed) == 0 {
		return res, nil
	}
	touched := map[string]bool{"index": true} // merge_keys changes every run
	for key := range changed {
		if d, ok := oldDay[key]; ok {
			touched[partName(d)] = true
		}
	}
	for part := range newRows {
		touched[part] = true
	}
	parts := make([]string, 0, len(touched))
	for p := range touched {
		parts = append(parts, p)
	}
	sort.Strings(parts)
	nonce := newNonce()
	for _, part := range parts {
		var prevOut Out
		if part == "index" {
			prevOut = next.Index
		} else {
			prevOut = next.Days[strings.TrimPrefix(part, "day=")]
		}
		add := newRows[part]
		if part == "index" {
			if add == nil {
				add = map[string][]stream.Row{}
			}
			add[TableMergeKeys] = mk
		}
		dir := fmt.Sprintf("%s/%012d-%s/", partDir(part), next.N, nonce)
		out, err := m.rewrite(ctx, prevOut, dir, changed, add)
		if err != nil {
			return res, err
		}
		if part == "index" {
			next.Index = out
		} else if len(out.Tables) == 0 {
			delete(next.Days, strings.TrimPrefix(part, "day="))
		} else {
			next.Days[strings.TrimPrefix(part, "day=")] = out
		}
		res.Rewritten = append(res.Rewritten, strings.TrimPrefix(part, "day="))
	}
	next.UpdatedNS = clock.Now().UnixNano()
	body, err := json.Marshal(next)
	if err != nil {
		return res, err
	}
	ok, err := m.Store.PutIfAbsent(ctx, MergeCommitKey(m.Prefix, next.N), body)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrMergeFenced
	}
	res.N = next.N
	return res, nil
}

// take is this run's keys: what Redis holds, or after a wipe (the marker is
// gone), every key the recent index names for the last Recent.
func (m *Merger) take(ctx context.Context, now time.Time) ([]string, bool, error) {
	alive, err := m.Queue.Alive(ctx)
	if err != nil {
		return nil, false, err
	}
	batch := m.Batch
	if batch <= 0 {
		batch = 5000
	}
	keys, err := m.Queue.Pop(ctx, batch)
	if err != nil || alive {
		return keys, false, err
	}
	// Redis lost what the merge had (or this is its first run): every key
	// committed within the recent window, and whatever was queued since.
	recent, err := RecentKeys(ctx, m.Store, m.Prefix, now, m.recent())
	if err == nil {
		err = m.Queue.MarkAlive(ctx)
	}
	if err != nil {
		if qerr := m.Queue.Requeue(context.WithoutCancel(ctx), keys); qerr != nil {
			err = errors.Join(err, qerr)
		}
		return nil, false, err
	}
	seen := map[string]bool{}
	for _, k := range recent {
		seen[k] = true
	}
	for _, k := range keys {
		if !seen[k] {
			recent = append(recent, k)
		}
	}
	return recent, true, nil
}

func (m *Merger) recent() time.Duration {
	if m.Recent > 0 {
		return m.Recent
	}
	return 15 * time.Minute
}

// rewrite is a partition's new output: its previous rows without the changed
// keys' rows, plus theirs now.
func (m *Merger) rewrite(ctx context.Context, prev Out, dir string, changed map[string]bool, add map[string][]stream.Row) (Out, error) {
	tables := map[string]bool{}
	for _, t := range prev.Tables {
		tables[t] = true
	}
	for t := range add {
		tables[t] = true
	}
	names := make([]string, 0, len(tables))
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	out := Out{Dir: dir}
	for _, t := range names {
		var keep []stream.Row
		if contains(prev.Tables, t) {
			rows, err := m.readOut(ctx, prev, t)
			if err != nil {
				return Out{}, err
			}
			for _, r := range rows {
				if !changed[r.Key] {
					keep = append(keep, r)
				}
			}
		}
		rows := stream.MergeRows(keep, add[t])
		if len(rows) == 0 {
			continue
		}
		tab, err := stream.TableFor(t)
		if err != nil {
			return Out{}, err
		}
		b, err := tab.Encode(rows)
		if err != nil {
			return Out{}, err
		}
		if err := m.Store.Put(ctx, MergeDir(m.Prefix)+TableFile(dir, t), b); err != nil {
			return Out{}, err
		}
		out.Tables = append(out.Tables, t)
	}
	return out, nil
}

func (m *Merger) readOut(ctx context.Context, o Out, table string) ([]stream.Row, error) {
	if o.Dir == "" || !contains(o.Tables, table) {
		return nil, nil
	}
	return readTableFile(ctx, m.Store, MergeDir(m.Prefix)+TableFile(o.Dir, table), table)
}

// ReadNewestMerge is the newest merge commit (nil before the first).
func ReadNewestMerge(ctx context.Context, s stream.Store, prefix string) (*MergeCommit, error) {
	objs, err := s.List(ctx, MergeDir(prefix)+"commit/")
	if err != nil {
		return nil, err
	}
	var best string
	var bestN int64 = -1
	for _, o := range objs {
		if n, ok := CommitN(o.Key); ok && n > bestN {
			best, bestN = o.Key, n
		}
	}
	if best == "" {
		return nil, nil
	}
	b, err := s.Get(ctx, best)
	if err != nil {
		return nil, err
	}
	var c MergeCommit
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("rollup: merge commit %s: %w", best, err)
	}
	return &c, nil
}

// ReadKeyTables is a key's current rows by table: its output directories
// folded oldest first, the newest row per (key, id) winning, tombstones gone.
func ReadKeyTables(ctx context.Context, s stream.Store, prefix string, c *Commit) (map[string][]stream.Row, error) {
	sets := map[string][][]stream.Row{}
	for _, o := range c.Out {
		for _, t := range o.Tables {
			rows, err := readTableFile(ctx, s, KeyDir(prefix, c.Key)+TableFile(o.Dir, t), t)
			if err != nil {
				return nil, err
			}
			sets[t] = append(sets[t], rows)
		}
	}
	out := map[string][]stream.Row{}
	for t, ss := range sets {
		if rows := stream.MergeRows(ss...); len(rows) > 0 {
			out[t] = rows
		}
	}
	return out, nil
}

func readTableFile(ctx context.Context, s stream.Store, key, table string) ([]stream.Row, error) {
	tab, err := stream.TableFor(table)
	if err != nil {
		return nil, err
	}
	b, err := s.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("rollup: %s: %w", key, err)
	}
	return tab.Decode(b)
}

// RecentKeys is every key the recent index names in the minutes since now - d.
func RecentKeys(ctx context.Context, s stream.Store, prefix string, now time.Time, d time.Duration) ([]string, error) {
	seen := map[string]bool{}
	for t := now.Add(-d).Truncate(time.Minute); !t.After(now); t = t.Add(time.Minute) {
		objs, err := s.List(ctx, RecentPrefix(prefix, t))
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			b, err := s.Get(ctx, o.Key)
			if err != nil {
				return nil, err
			}
			var keys []string
			if err := json.Unmarshal(b, &keys); err != nil {
				return nil, fmt.Errorf("rollup: recent %s: %w", o.Key, err)
			}
			for _, k := range keys {
				seen[k] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// DayOf is the day (YYYY-MM-DD, UTC) a key's rows are merged under: an account
// key's are the index's (""); a c:<credential>:<day> or ip:<ip>:<hour> key's,
// the day it names; any other key's, the day of its last activity (its rows'
// last_ns, else ts_ns), else of its commit.
func DayOf(key string, tables map[string][]stream.Row, updatedNS int64) string {
	switch {
	case strings.HasPrefix(key, "a:"):
		return ""
	case strings.HasPrefix(key, "c:"), strings.HasPrefix(key, "ip:"):
		if d := keyDay(key); d != "" {
			return d
		}
	}
	ns := newestOf(tables)
	if ns == 0 {
		ns = updatedNS
	}
	return time.Unix(0, ns).UTC().Format("2006-01-02")
}

// keyDay is the YYYY-MM-DD a c: or ip: key ends with (ip:'s is a day and hour).
func keyDay(key string) string {
	i := strings.LastIndexByte(key, ':')
	if i < 0 {
		return ""
	}
	s := key[i+1:]
	for _, layout := range []string{"2006-01-02", "20060102", "2006-01-02T15", "20060102T15"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// newestOf is the newest last_ns, else ts_ns, among a key's rows.
func newestOf(tables map[string][]stream.Row) int64 {
	var best int64
	for name, rows := range tables {
		tab, err := stream.TableFor(name)
		if err != nil {
			continue
		}
		cols := tab.Columns()
		has := func(c string) bool { return contains(cols, c) }
		for _, col := range []string{"last_ns", "ts_ns"} {
			if !has(col) {
				continue
			}
			for _, r := range rows {
				if v, ok := tab.ValueMap(r.Value)[col].(int64); ok && v > best {
					best = v
				}
			}
			break
		}
	}
	return best
}

// rawTimes are the fact tables' raw row times (the proxy's clock): a call's
// start and end, a call in flight's first row, an event's row. The newest is
// what today's processor reports as its newest row.
var rawTimes = []struct{ table, col string }{
	{state.TableFactRequests, "start_ns"}, {state.TableFactRequests, "end_ns"},
	{state.TableFactPending, "seen_ns"}, {state.TableFactEvents, "row_ns"},
}

// rawNewest is the newest raw row time among a key's fact rows.
func rawNewest(tables map[string][]stream.Row) int64 {
	var best int64
	for _, rt := range rawTimes {
		rows := tables[rt.table]
		if len(rows) == 0 {
			continue
		}
		tab, err := stream.TableFor(rt.table)
		if err != nil {
			continue
		}
		for _, r := range rows {
			if v, ok := tab.ValueMap(r.Value)[rt.col].(int64); ok && v > best {
				best = v
			}
		}
	}
	return best
}

// retainsContent is whether a key's calls had their bodies read (a detail).
func retainsContent(tables map[string][]stream.Row) bool {
	for _, r := range tables[state.TableFactRequests] {
		if c, ok := r.Value.(canon.Request); ok && c.DetailJSON != "" {
			return true
		}
	}
	return false
}

func partName(day string) string {
	if day == "" {
		return "index"
	}
	return "day=" + day
}

func partDir(part string) string {
	if part == "index" {
		return "index"
	}
	return "days/" + strings.TrimPrefix(part, "day=")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
