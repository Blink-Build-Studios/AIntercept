package stream

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Defaults; a Bucket's zero fields take them.
const (
	DefaultGrace        = 5 * time.Minute  // record files are listed from the watermark minus this
	DefaultCacheBytes   = 256 << 20        // key state kept in memory per bucket
	DefaultCompactRuns  = 64               // run files per table before compacting
	DefaultCompactEvery = 10 * time.Minute // compact at least this often when there are runs
	DefaultPartRows     = 4096             // rows per base part
	DefaultFanout       = 8                // segments per level before they fold into the next
	GCGrace             = 10 * time.Minute // a dropped file outlives the commit that dropped it by this
	gcEvery             = time.Minute
	minuteLayout        = "2006-01-02T15:04"
)

// Bucket is one owned bucket's worker: each Cycle reads the record files new
// since the watermark, applies them key by key, and commits. It's single-
// threaded; a replica runs one per owned bucket.
type Bucket struct {
	S       Store
	L       Layout
	M       Model
	B, Bits int
	Epoch   int64
	Replica string
	Clock   Clock

	Grace        time.Duration
	CacheBytes   int
	CompactRuns  int
	CompactEvery time.Duration
	PartRows     int
	// Fanout is how many segments of one level fold into one of the next
	// (tiered table bases; 0: DefaultFanout).
	Fanout int
	// BaseRowsWritten counts the table rows compaction has written, for cost
	// tests: each row should be rewritten O(log N) times over its life.
	BaseRowsWritten int64
	// MaxFiles caps the record files one cycle reads (0: no cap), oldest first.
	MaxFiles int

	commit  *Commit
	loaded  bool
	cache   *stateCache
	loads   int
	lastGC  time.Time
	pending []Garbage // collected but not yet dropped from a commit
}

// CycleStats is what one cycle did.
type CycleStats struct {
	Files, Records, Keys int
	Committed            bool
}

func (w *Bucket) defaults() {
	if w.Grace == 0 {
		w.Grace = DefaultGrace
	}
	if w.CacheBytes == 0 {
		w.CacheBytes = DefaultCacheBytes
	}
	if w.CompactRuns == 0 {
		w.CompactRuns = DefaultCompactRuns
	}
	if w.CompactEvery == 0 {
		w.CompactEvery = DefaultCompactEvery
	}
	if w.PartRows == 0 {
		w.PartRows = DefaultPartRows
	}
	if w.Clock == nil {
		w.Clock = RealClock{}
	}
	if w.cache == nil {
		w.cache = newStateCache(w.CacheBytes, w.M)
	}
}

// Loads is how many key states were read back from state files.
func (w *Bucket) Loads() int { return w.loads }

// ResetCache drops every cached key state (a cold replica).
func (w *Bucket) ResetCache() { w.cache = nil }

// Reset forgets everything in memory; the next cycle starts from the log.
func (w *Bucket) Reset() { w.commit, w.loaded, w.cache = nil, false, nil }

// Cycle processes what's new. ErrFenced: another owner committed first.
func (w *Bucket) Cycle(ctx context.Context) (CycleStats, error) {
	w.defaults()
	var st CycleStats
	if !w.loaded {
		c, err := LoadLatest(ctx, w.S, w.L, w.B)
		if err != nil {
			return st, err
		}
		w.commit, w.loaded = c, true
	}
	prev := w.commit
	if prev == nil {
		prev = &Commit{Bucket: w.B, Base: map[string][]FileRef{}, Runs: map[string][]FileRef{}}
	}
	now := w.Clock.Now()
	files, err := w.newFiles(ctx, prev)
	if err != nil {
		return st, err
	}
	if w.MaxFiles > 0 && len(files) > w.MaxFiles {
		files = files[:w.MaxFiles]
	}
	if len(files) == 0 {
		return st, w.maybeGC(ctx, now)
	}

	// Read and group by key.
	byKey := map[string][]*records.Record{}
	for _, f := range files {
		raw, err := w.S.Get(ctx, f)
		if err != nil {
			return st, fmt.Errorf("read %s: %w", f, err)
		}
		rs, err := records.Decode(raw)
		if err != nil {
			return st, fmt.Errorf("decode %s: %w", f, err)
		}
		st.Records += len(rs)
		for i := range rs {
			byKey[rs[i].RoutingKey] = append(byKey[rs[i].RoutingKey], &rs[i])
		}
	}
	st.Files, st.Keys = len(files), len(byKey)

	// Apply, key by key, in (ts, record id) order. Apply mutates cached state,
	// so any failure from here on drops everything in memory: the next cycle
	// starts again from the log and re-reads these files.
	applied := false
	defer func() {
		if applied && !st.Committed {
			w.Reset()
		}
	}()
	applied = true
	deltas := map[string][]Row{}
	states := map[string]any{}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	stateFiles := map[string][]Row{}
	for _, k := range keys {
		s, err := w.state(ctx, prev, k, stateFiles)
		if err != nil {
			return st, err
		}
		rs := byKey[k]
		sortRecords(rs)
		for _, r := range rs {
			for t, rows := range w.M.Apply(s, r) {
				deltas[t] = append(deltas[t], rows...)
			}
		}
		states[k] = s
	}

	// Write runs and state, then commit.
	c := prev.next(w.Epoch, w.Replica, now)
	for _, t := range sortedTables(deltas) {
		tb, err := TableFor(t)
		if err != nil {
			return st, err
		}
		rows := collapse([][]Row{deltas[t]}, true)
		ref, err := w.writeRows(ctx, tb, w.L.RunKey(t, w.B, w.Epoch, c.Seq), rows)
		if err != nil {
			return st, err
		}
		c.Runs[t] = append(c.Runs[t], ref)
	}
	srows := make([]Row, 0, len(states))
	for _, k := range keys {
		b, err := w.M.Encode(states[k])
		if err != nil {
			return st, err
		}
		srows = append(srows, Row{Key: k, ID: k, Value: b})
	}
	ref, err := w.writeRows(ctx, stateCodec{}, w.L.StateKey(w.B, w.Epoch, c.Seq), srows)
	if err != nil {
		return st, err
	}
	ref.Group = c.Seq << groupBits
	c.State = append(c.State, ref)
	w.advance(c, files)
	if err := w.maybeCompact(ctx, c, now); err != nil {
		return st, err
	}
	c.Garbage = pruneGarbage(c.Garbage, w.pending)
	ok, err := TryCommit(ctx, w.S, w.L, c)
	if err != nil {
		return st, err
	}
	if !ok {
		return st, ErrFenced
	}
	w.commit = c
	w.pending = nil
	for k, s := range states {
		w.cache.put(k, s)
	}
	st.Committed = true
	return st, w.maybeGC(ctx, now)
}

// newFiles lists the record files after the watermark (minus the grace window)
// that no commit has read, oldest minute first.
func (w *Bucket) newFiles(ctx context.Context, c *Commit) ([]string, error) {
	return listNew(ctx, w.S, w.L, w.B, c, w.Grace)
}

func listNew(ctx context.Context, s Store, l Layout, b int, c *Commit, grace time.Duration) ([]string, error) {
	files, err := listNewInfo(ctx, s, l, b, c, grace)
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Key
	}
	return out, err
}

func listNewInfo(ctx context.Context, s Store, l Layout, b int, c *Commit, grace time.Duration) ([]Info, error) {
	prefix := records.ObjectKeyPrefix(l.Prefix, b)
	after := ""
	if c != nil && c.Watermark != "" {
		wm, err := time.Parse(minuteLayout, c.Watermark)
		if err != nil {
			return nil, err
		}
		after = prefix + "minute=" + wm.Add(-grace).Format(minuteLayout) // keeps that minute
	}
	dirs, err := s.ListDirsAfter(ctx, prefix, after)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	if c != nil {
		for _, p := range c.Processed {
			seen[p] = true
		}
	}
	var out []Info
	for _, d := range dirs {
		fs, err := s.ListInfo(ctx, d)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			if !seen[f.Key] && strings.HasSuffix(f.Key, ".parquet") {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

func minuteOf(key string) string {
	i := strings.Index(key, "minute=")
	if i < 0 {
		return ""
	}
	rest := key[i+len("minute="):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// advance moves the watermark to the newest minute read and keeps the names
// of files read in the grace window behind it.
func (w *Bucket) advance(c *Commit, files []string) {
	for _, f := range files {
		if m := minuteOf(f); m > c.Watermark {
			c.Watermark = m
		}
	}
	wm, _ := time.Parse(minuteLayout, c.Watermark)
	floor := wm.Add(-w.Grace).Format(minuteLayout)
	var keep []string
	for _, p := range append(c.Processed, files...) {
		if minuteOf(p) >= floor {
			keep = append(keep, p)
		}
	}
	c.Processed = keep
}

// codec is a file format: a table's typed columns, or state's opaque blobs.
type codec interface {
	Encode([]Row) ([]byte, error)
	Decode([]byte) ([]Row, error)
}

// stateCodec stores each key's encoded state (a Row whose Value is []byte).
type stateCodec struct{}

func (stateCodec) Encode(rows []Row) ([]byte, error) {
	out := make([]StateRow, len(rows))
	for i, r := range rows {
		out[i] = StateRow{Key: r.Key, Data: r.Value.([]byte)}
	}
	return EncodeState(out)
}

func (stateCodec) Decode(b []byte) ([]Row, error) {
	rs, err := DecodeState(b)
	out := make([]Row, len(rs))
	for i, r := range rs {
		out[i] = Row{Key: r.Key, ID: r.Key, Value: r.Data}
	}
	return out, err
}

func (w *Bucket) writeRows(ctx context.Context, cd codec, key string, rows []Row) (FileRef, error) {
	b, err := cd.Encode(rows)
	if err != nil {
		return FileRef{}, err
	}
	if err := w.S.Put(ctx, key, b); err != nil {
		return FileRef{}, err
	}
	ref := FileRef{Key: key, Rows: len(rows), Bytes: int64(len(b))}
	if len(rows) > 0 {
		ref.MinKey, ref.MaxKey = rows[0].Key, rows[len(rows)-1].Key
	}
	return ref, nil
}

// state is key's state: cached, else loaded from every state file that holds a
// checkpoint of it (the base, then each cycle's run since), else new.
func (w *Bucket) state(ctx context.Context, c *Commit, key string, read map[string][]Row) (any, error) {
	if s, ok := w.cache.get(key); ok {
		return s, nil
	}
	files := make([]FileRef, 0, len(c.StateBase)+len(c.State))
	files = append(append(files, c.StateBase...), c.State...)
	var chunks [][]byte
	for _, f := range files {
		if !f.has(key) {
			continue
		}
		rows, ok := read[f.Key]
		if !ok {
			raw, err := w.S.Get(ctx, f.Key)
			if err != nil {
				return nil, fmt.Errorf("state %s: %w", f.Key, err)
			}
			if rows, err = (stateCodec{}).Decode(raw); err != nil {
				return nil, err
			}
			read[f.Key] = rows
		}
		i := sort.Search(len(rows), func(i int) bool { return rows[i].Key >= key })
		if i < len(rows) && rows[i].Key == key {
			chunks = append(chunks, rows[i].Value.([]byte))
		}
	}
	if len(chunks) == 0 {
		return w.M.New(key), nil
	}
	w.loads++
	return w.M.Decode(key, chunks)
}

func sortedTables(d map[string][]Row) []string {
	out := make([]string, 0, len(d))
	for t := range d {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// maybeCompact folds a table's runs into a new base generation once there are
// CompactRuns of them (or CompactEvery has passed), and the state runs into a
// state base the same way. Replaced files become garbage.
func (w *Bucket) maybeCompact(ctx context.Context, c *Commit, now time.Time) error {
	return w.compact(ctx, c, now, false)
}

// Compact folds everything into a new base generation now, as its own commit.
func (w *Bucket) Compact(ctx context.Context) error {
	w.defaults()
	if !w.loaded {
		c, err := LoadLatest(ctx, w.S, w.L, w.B)
		if err != nil {
			return err
		}
		w.commit, w.loaded = c, true
	}
	if w.commit == nil {
		return nil
	}
	now := w.Clock.Now()
	c := w.commit.next(w.Epoch, w.Replica, now)
	if err := w.compact(ctx, c, now, true); err != nil {
		w.Reset()
		return err
	}
	ok, err := TryCommit(ctx, w.S, w.L, c)
	if err != nil || !ok {
		w.Reset()
		if err == nil {
			err = ErrFenced
		}
		return err
	}
	w.commit = c
	return nil
}

func (w *Bucket) compact(ctx context.Context, c *Commit, now time.Time, force bool) error {
	due := force || (now.Sub(c.LastCompact) >= w.CompactEvery && len(c.State) > 1)
	for _, runs := range c.Runs {
		if len(runs) >= w.CompactRuns {
			due = true
		}
	}
	if len(c.State) >= w.CompactRuns {
		due = true
	}
	if !due {
		return nil
	}
	gen := c.Gen + 1
	after := now.Add(GCGrace)
	for _, t := range sortedTables(refMap(c)) {
		tb, err := TableFor(t)
		if err != nil {
			return err
		}
		parts, err := w.merge(ctx, tb, append(append([]FileRef(nil), c.Base[t]...), c.Runs[t]...),
			func(p int) string { return w.L.BaseKey(t, w.B, gen, w.Epoch, c.Seq, p) })
		if err != nil {
			return err
		}
		for _, f := range append(c.Base[t], c.Runs[t]...) {
			c.Garbage = append(c.Garbage, Garbage{Key: f.Key, After: after})
		}
		c.Base[t], c.Runs[t] = parts, nil
	}
	if err := w.foldState(ctx, c, gen, after); err != nil {
		return err
	}
	c.Gen, c.LastCompact = gen, now
	return nil
}

func refMap(c *Commit) map[string][]Row {
	m := map[string][]Row{}
	for t := range c.Base {
		m[t] = nil
	}
	for t := range c.Runs {
		m[t] = nil
	}
	return m
}

// merge reads files oldest first, folds them (newest wins, tombstones drop)
// and writes the result in key-ranged parts of at most PartRows rows. A key's
// rows never straddle two parts.
func (w *Bucket) merge(ctx context.Context, cd codec, files []FileRef, name func(int) string) ([]FileRef, error) {
	sets := make([][]Row, 0, len(files))
	for _, f := range files {
		raw, err := w.S.Get(ctx, f.Key)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", f.Key, err)
		}
		rows, err := cd.Decode(raw)
		if err != nil {
			return nil, err
		}
		sets = append(sets, rows)
	}
	rows := MergeRows(sets...)
	var out []FileRef
	for start, part := 0, 0; start < len(rows); part++ {
		end := start + w.PartRows
		if end > len(rows) {
			end = len(rows)
		}
		for end < len(rows) && rows[end].Key == rows[end-1].Key {
			end++
		}
		ref, err := w.writeRows(ctx, cd, name(part), rows[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
		start = end
	}
	return out, nil
}

// State is folded in tiers, so a key's checkpoints never pile up and none is
// rewritten often: the state files are groups (a cycle's run, or a fold of
// several), oldest first; when the newest tierFanout groups are of one size
// class (bytes, in powers of tierFanout), or a group is smaller than the one
// after it, they're folded into one. A key's state is then spread over
// O(log N) groups (a cold load reads one part of each), and each checkpoint is
// rewritten O(log N) times over its life.
const (
	tierFanout = 4
	tierUnit   = 4 << 10 // bytes: class 0 is anything smaller
	groupBits  = 20      // a group id is the commit's seq, then which fold of it
)

type stateGroup struct {
	id    int64
	parts []FileRef
	bytes int64
}

func tierClass(b int64) int {
	c := 0
	for b >= tierUnit {
		b /= tierFanout
		c++
	}
	return c
}

// stateGroups is the state files as groups, oldest first.
func stateGroups(files []FileRef) []stateGroup {
	var out []stateGroup
	for _, f := range files {
		b := f.Bytes
		if b == 0 {
			b = int64(f.Rows) * 1024 // written before sizes were recorded
		}
		if n := len(out); n > 0 && out[n-1].id == f.Group {
			out[n-1].parts = append(out[n-1].parts, f)
			out[n-1].bytes += b
			continue
		}
		out = append(out, stateGroup{id: f.Group, parts: []FileRef{f}, bytes: b})
	}
	return out
}

// foldState folds the state groups in tiers (base groups, then this
// compaction's cycle runs) and leaves the result as the commit's state base.
func (w *Bucket) foldState(ctx context.Context, c *Commit, gen int64, after time.Time) error {
	var stack []stateGroup
	fold, part := int64(0), 0
	for _, g := range stateGroups(append(append([]FileRef(nil), c.StateBase...), c.State...)) {
		stack = append(stack, g)
		for {
			n, k := len(stack), 0
			switch {
			case n >= tierFanout && sameClass(stack[n-tierFanout:]):
				k = tierFanout
			case n >= 2 && tierClass(stack[n-2].bytes) < tierClass(stack[n-1].bytes):
				k = 2
			}
			if k == 0 {
				break
			}
			fold++
			merged, err := w.mergeGroups(ctx, stack[n-k:], (c.Seq<<groupBits)|fold,
				func() string { p := part; part++; return w.L.StateBaseKey(w.B, gen, w.Epoch, c.Seq, p) })
			if err != nil {
				return err
			}
			for _, old := range stack[n-k:] {
				for _, f := range old.parts {
					c.Garbage = append(c.Garbage, Garbage{Key: f.Key, After: after})
				}
			}
			stack = append(stack[:n-k], merged)
		}
	}
	c.StateBase, c.State = nil, nil
	for _, g := range stack {
		c.StateBase = append(c.StateBase, g.parts...)
	}
	return nil
}

func sameClass(gs []stateGroup) bool {
	for _, g := range gs[1:] {
		if tierClass(g.bytes) != tierClass(gs[0].bytes) {
			return false
		}
	}
	return true
}

// mergeGroups folds groups (oldest first) into one: per key, the model's merge
// of its checkpoints in order, written in key-ranged parts. It reads one part
// of each group at a time, so its memory is a few parts, not the groups.
func (w *Bucket) mergeGroups(ctx context.Context, gs []stateGroup, id int64, name func() string) (stateGroup, error) {
	its := make([]*partIter, len(gs))
	for i, g := range gs {
		its[i] = &partIter{w: w, ctx: ctx, refs: g.parts}
	}
	out := stateGroup{id: id}
	var buf []Row
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		ref, err := w.writeRows(ctx, stateCodec{}, name(), buf)
		if err != nil {
			return err
		}
		ref.Group = id
		out.parts = append(out.parts, ref)
		out.bytes += ref.Bytes
		buf = nil
		return nil
	}
	for {
		key, found := "", false
		for _, it := range its {
			r, err := it.peek()
			if err != nil {
				return out, err
			}
			if r != nil && (!found || r.Key < key) {
				key, found = r.Key, true
			}
		}
		if !found {
			break
		}
		var chunks [][]byte
		for _, it := range its {
			if r, _ := it.peek(); r != nil && r.Key == key {
				chunks = append(chunks, r.Value.([]byte))
				it.i++
			}
		}
		b := chunks[0]
		if len(chunks) > 1 {
			var err error
			if b, err = w.M.Merge(key, chunks); err != nil {
				return out, err
			}
		}
		buf = append(buf, Row{Key: key, ID: key, Value: b})
		if len(buf) >= w.PartRows {
			if err := flush(); err != nil {
				return out, err
			}
		}
	}
	return out, flush()
}

// partIter reads a group's rows in key order, one part at a time.
type partIter struct {
	w    *Bucket
	ctx  context.Context
	refs []FileRef
	rows []Row
	i    int
}

func (it *partIter) peek() (*Row, error) {
	for it.i >= len(it.rows) {
		if len(it.refs) == 0 {
			return nil, nil
		}
		raw, err := it.w.S.Get(it.ctx, it.refs[0].Key)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", it.refs[0].Key, err)
		}
		if it.rows, err = (stateCodec{}).Decode(raw); err != nil {
			return nil, err
		}
		it.refs, it.i = it.refs[1:], 0
	}
	return &it.rows[it.i], nil
}

func pruneGarbage(gs, deleted []Garbage) []Garbage {
	gone := map[string]bool{}
	for _, g := range deleted {
		gone[g.Key] = true
	}
	var out []Garbage
	for _, g := range gs {
		if !gone[g.Key] {
			out = append(out, g)
		}
	}
	return out
}

func (w *Bucket) maybeGC(ctx context.Context, now time.Time) error {
	if now.Sub(w.lastGC) < gcEvery {
		return nil
	}
	return w.GC(ctx)
}

// GC deletes, for this bucket: garbage whose grace has passed; table and
// state files no commit names, older than the grace period (a fenced owner's
// leftovers); and commits older than the grace period other than the newest.
func (w *Bucket) GC(ctx context.Context) error {
	w.defaults()
	now := w.Clock.Now()
	w.lastGC = now
	// Always against the newest commit, never a possibly stale one in memory:
	// a file only that newer commit names must not look orphaned.
	var known int64
	if w.commit != nil {
		known = w.commit.Seq
	}
	c, err := LoadLatestAfter(ctx, w.S, w.L, w.B, known)
	if err != nil || c == nil {
		return err
	}
	for _, g := range c.Garbage {
		if now.After(g.After) {
			if err := w.S.Delete(ctx, g.Key); err != nil {
				return err
			}
			w.pending = append(w.pending, g)
		}
	}
	named := map[string]bool{}
	for _, k := range c.Files() {
		named[k] = true
	}
	var prefixes []string
	for _, t := range w.M.Tables() {
		prefixes = append(prefixes, w.L.tablePrefix(t, w.B))
	}
	prefixes = append(prefixes, w.L.statePrefix(w.B))
	for _, p := range prefixes {
		objs, err := w.S.ListInfo(ctx, p)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if !named[o.Key] && now.Sub(o.Modified) > GCGrace {
				if err := w.S.Delete(ctx, o.Key); err != nil {
					return err
				}
			}
		}
	}
	commits, err := w.S.ListInfo(ctx, w.L.CommitPrefix(w.B))
	if err != nil {
		return err
	}
	for i, o := range commits {
		if i < len(commits)-1 && now.Sub(o.Modified) > GCGrace {
			if err := w.S.Delete(ctx, o.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadTable is one table of one bucket as of its newest commit: the base parts
// and run files folded, newest wins.
func ReadTable(ctx context.Context, s Store, l Layout, b int, table string) ([]Row, error) {
	c, err := LoadLatest(ctx, s, l, b)
	if err != nil || c == nil {
		return nil, err
	}
	tb, err := TableFor(table)
	if err != nil {
		return nil, err
	}
	return readRefs(ctx, s, tb, append(append([]FileRef(nil), c.Base[table]...), c.Runs[table]...))
}

func readRefs(ctx context.Context, s Store, tb *Table, refs []FileRef) ([]Row, error) {
	sets := make([][]Row, 0, len(refs))
	for _, f := range refs {
		raw, err := s.Get(ctx, f.Key)
		if errors.Is(err, process.ErrNotFound) {
			return nil, fmt.Errorf("%s: named by the commit but missing", f.Key)
		}
		if err != nil {
			return nil, err
		}
		rows, err := tb.Decode(raw)
		if err != nil {
			return nil, err
		}
		sets = append(sets, rows)
	}
	return MergeRows(sets...), nil
}

// ReadAll is every table across every bucket.
func ReadAll(ctx context.Context, s Store, l Layout, m Model, buckets int) (map[string][]Row, error) {
	out := map[string][]Row{}
	for b := 0; b < buckets; b++ {
		for _, t := range m.Tables() {
			rows, err := ReadTable(ctx, s, l, b, t)
			if err != nil {
				return nil, err
			}
			out[t] = append(out[t], rows...)
		}
	}
	return out, nil
}

// stateCache is key state kept in memory, least recently used out first,
// capped by the model's size estimate.
type stateCache struct {
	m     Model
	cap   int
	bytes int
	ll    *list.List
	idx   map[string]*list.Element
}

type cacheEntry struct {
	key  string
	st   any
	size int
}

func newStateCache(capBytes int, m Model) *stateCache {
	return &stateCache{m: m, cap: capBytes, ll: list.New(), idx: map[string]*list.Element{}}
}

func (c *stateCache) get(key string) (any, bool) {
	if e, ok := c.idx[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*cacheEntry).st, true
	}
	return nil, false
}

func (c *stateCache) put(key string, st any) {
	size := c.m.Size(st)
	if e, ok := c.idx[key]; ok {
		ce := e.Value.(*cacheEntry)
		c.bytes += size - ce.size
		ce.st, ce.size = st, size
		c.ll.MoveToFront(e)
	} else {
		c.idx[key] = c.ll.PushFront(&cacheEntry{key: key, st: st, size: size})
		c.bytes += size
	}
	for c.bytes > c.cap && c.ll.Len() > 0 {
		e := c.ll.Back()
		ce := e.Value.(*cacheEntry)
		c.ll.Remove(e)
		delete(c.idx, ce.key)
		c.bytes -= ce.size
	}
}
