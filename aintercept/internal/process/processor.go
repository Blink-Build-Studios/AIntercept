// Package process is stage 2 of the data pipeline (docs/ui/BUILD_PLAN.md §7.5):
// `aintercept process` turns the observer's raw partitions into shaped Parquet
// tables the query API reads directly.
//
// How it finds every file exactly once:
//   - It polls. Each poll lists the current and previous hours' partitions
//     (cheap however much history there is); a full listing runs at startup
//     and every FullEvery, for anything later than that.
//   - A ledger, not a cursor: the files to process are those listed but not
//     in the ledger, in any order. Observer pods upload independently and name
//     a file by when its batch started, so files don't arrive in name order.
//   - A run writes its fact parts and the new snapshots, then commits by
//     writing the manifest. Readers use only what the manifest names. A run
//     that dies before committing leaves files nothing names; they're removed
//     at the next start, and its raw files are processed again.
//   - The state (snapshots, pending transactions, ledger) lives in the store,
//     so a restarted processor resumes from the last commit.
//
// What it relies on: all rows of a transaction come from the one observer pod
// that handled its stream, and each pod uploads its batches in order, so a
// transaction's rows are seen in the order they happened. Files from different
// pods may arrive in any order. Within that, a call's ending row arriving
// before its headers is still handled (end markers, kept for 6 hours of data
// time, or 7 days for a call written without its ending because it waited past
// those 6 hours); a disconnect arriving before an earlier complete of the same
// call is not (it can only happen when a pod's earlier upload failed and was
// retried).
package process

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// rawKey matches the observer's partition layout under the raw prefix.
var rawKey = regexp.MustCompile(`(^|/)dt=\d{4}-\d{2}-\d{2}/hour=\d{2}/instance=[^/]+/[^/]+\.ndjson\.gz$`)

// Fact tables gain a part per run; snapshot tables are replaced each run.
var factTables = []string{"requests", "events", "ledger"}
var snapshotTables = []string{"sessions", "prompts", "person_devices", "people", "pending", "pending_bodies", "request_rows", "person_last", "prompt_rows", "prompt_steps", "session_rows", "session_marks", "presence_segments", "event_rows", "session_tools", "session_request_rows", "person_map", "person_sessions", "account_facts", "account_devices", "call_tool_uses"}

// Config wires a processor to its stores.
type Config struct {
	Raw       Store
	RawPrefix string // e.g. "aintercept" (raw keys are <prefix>/dt=…)
	Out       Store
	OutPrefix string // e.g. "aintercept/shaped"
	// BatchFiles bounds the raw files one run processes (default 500).
	BatchFiles int
	// CompactAt merges a fact table's parts once it has this many (default 64).
	CompactAt int
	// Grace is how long a replaced file stays readable before it's deleted
	// (readers may still be reading it). Default 5 minutes.
	Grace time.Duration
	// Now is the time the listing windows are chosen by (the newest raw files
	// are named for it). Wall is the clock readers are on: commits and grace
	// periods. Both default to time.Now; a replay runs Now on data time.
	Now  func() time.Time
	Wall func() time.Time
	// SweepAll lists and loads sealed days too: a one-off sweep for a raw file
	// that landed more than days late (PROCESS_SWEEP_ALL).
	SweepAll bool
}

// Manifest is the commit point: the files that make up each table now.
type Manifest struct {
	Version   int                 `json:"version"`
	Run       int64               `json:"run"`
	Committed time.Time           `json:"committed"`
	Facts     map[string][]string `json:"facts"`     // table -> part keys
	Snapshots map[string]string   `json:"snapshots"` // table -> key
	NewestNS  int64               `json:"newest_ns"` // newest raw row time processed
	// Garbage is replaced files and when they may be deleted.
	Garbage []Garbage `json:"garbage,omitempty"`
	// Unread is raw files that couldn't be read at this commit (retried when they change).
	Unread []string `json:"unread,omitempty"`
	// Parts describes each fact part (data-time range; the day of a day file).
	Parts map[string]PartInfo `json:"parts,omitempty"`
	// SealedThrough is the newest sealed day (YYYY-MM-DD): days up to it are
	// complete, and the full listing and the ledger load skip them.
	SealedThrough string `json:"sealed_through,omitempty"`
	// ContentRetained: some processed row carried a body's content (the
	// observer ran with full-body capture into these partitions). Sticky.
	ContentRetained bool `json:"content_retained,omitempty"`
}

type Garbage struct {
	Key   string    `json:"key"`
	After time.Time `json:"after"`
}

// Stats is what /stats reports.
type Stats struct {
	Run            int64     `json:"run"`
	LastRun        time.Time `json:"last_run"`
	FilesProcessed int64     `json:"files_processed"`
	FilesPending   int       `json:"files_pending"`
	FilesUnread    []string  `json:"files_unread,omitempty"`
	RowsRead       int64     `json:"rows_read"`
	BadLines       int64     `json:"bad_lines"`
	Requests       int64     `json:"requests"`
	Events         int64     `json:"events"`
	Sessions       int       `json:"sessions"`
	LagSeconds     float64   `json:"lag_seconds"` // now minus the newest raw row processed
	Errors         int64     `json:"errors"`
	LastError      string    `json:"last_error,omitempty"`
}

// Processor runs the polls. Not safe for two processors on the same output:
// deploy one replica.
type Processor struct {
	cfg  Config
	man  Manifest
	st   *state
	seen map[string]int64 // raw key -> size, from the ledger
	// cursor is the last processed key in each pod's folder of an hour: a pod
	// uploads in name order, so a poll asks only for what comes after it.
	cursor map[string]string
	// unread are raw files that couldn't be read, by size: retried when they change.
	unread map[string]int64
	// dirty: a run failed after folding rows into st, so st no longer matches the
	// last commit; the next poll reloads it before going on.
	dirty bool
	// rowParts is what each committed fact part gives the request rows
	// (requestrows.go). Reset at Open: a failed run's keys are reused by its retry.
	rowParts *rowParts

	mu    sync.Mutex
	stats Stats
}

func New(cfg Config) *Processor {
	if cfg.BatchFiles == 0 {
		cfg.BatchFiles = 500
	}
	if cfg.CompactAt == 0 {
		cfg.CompactAt = 64
	}
	if cfg.Grace == 0 {
		cfg.Grace = 5 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Wall == nil {
		cfg.Wall = time.Now
	}
	cfg.RawPrefix = strings.Trim(cfg.RawPrefix, "/")
	cfg.OutPrefix = strings.Trim(cfg.OutPrefix, "/")
	return &Processor{cfg: cfg, st: newState(), seen: map[string]int64{}, cursor: map[string]string{}, unread: map[string]int64{},
		rowParts: newRowParts(cfg.Out)}
}

func (p *Processor) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.FilesUnread = append([]string(nil), s.FilesUnread...)
	return s
}

func (p *Processor) outKey(parts ...string) string {
	return path.Join(append([]string{p.cfg.OutPrefix}, parts...)...)
}

// Open loads the last commit and removes what an uncommitted run left behind.
func (p *Processor) Open(ctx context.Context) error {
	p.st, p.seen, p.cursor, p.unread = newState(), map[string]int64{}, map[string]string{}, map[string]int64{}
	p.rowParts = newRowParts(p.cfg.Out)
	b, err := p.cfg.Out.Get(ctx, p.outKey("manifest.json"))
	switch {
	case errors.Is(err, ErrNotFound):
		p.man = Manifest{Version: 1, Facts: map[string][]string{}, Snapshots: map[string]string{}, Parts: map[string]PartInfo{}}
	case err != nil:
		return fmt.Errorf("read manifest: %w", err)
	default:
		if err := json.Unmarshal(b, &p.man); err != nil {
			return fmt.Errorf("parse manifest: %w", err)
		}
	}
	if p.man.Parts == nil {
		p.man.Parts = map[string]PartInfo{}
	}
	// Unreadable files stay unread across a restart (so sealThrough keeps their
	// days open); size -1 retries each once, at the next listing.
	for _, k := range p.man.Unread {
		p.unread[k] = -1
	}
	for _, k := range p.man.Facts["ledger"] {
		if d := p.man.Parts[k].Day; d != "" && d <= p.man.SealedThrough && !p.cfg.SweepAll {
			continue // a sealed day: its files are never listed again
		}
		rows, err := readParquet[Ledger](ctx, p.cfg.Out, k)
		if err != nil {
			return fmt.Errorf("read ledger %s: %w", k, err)
		}
		for _, r := range rows {
			p.saw(r.RawKey, r.Size)
		}
	}
	var sessions []Session
	var prompts []Prompt
	var devices []PersonDevice
	var pending []Pending
	if err := loadSnapshot(ctx, p, "sessions", &sessions); err != nil {
		return err
	}
	if err := loadSnapshot(ctx, p, "prompts", &prompts); err != nil {
		return err
	}
	if err := loadSnapshot(ctx, p, "person_devices", &devices); err != nil {
		return err
	}
	if err := loadSnapshot(ctx, p, "pending", &pending); err != nil {
		return err
	}
	p.st.load(sessions, prompts, devices, pending, p.man.NewestNS)
	var bodies []PendingBodies
	if err := loadSnapshot(ctx, p, "pending_bodies", &bodies); err != nil {
		return err
	}
	p.st.loadBodies(bodies)
	var accounts []Account
	if err := loadSnapshot(ctx, p, "accounts", &accounts); err != nil {
		return err
	}
	p.st.loadAccounts(accounts)
	// End markers aren't committed (there's one per transaction of the last
	// hours); they're rebuilt from the requests that ended in that window.
	for _, k := range p.man.Facts["requests"] {
		if info, ok := p.man.Parts[k]; ok && p.man.NewestNS-info.MaxNS > int64(pendingTTL) {
			continue // older than any marker's window
		}
		rows, err := readParquet[markerRow](ctx, p.cfg.Out, k)
		if err != nil {
			return fmt.Errorf("read %s: %w", k, err)
		}
		for _, r := range rows {
			if r.WSTurn == 0 {
				p.st.markDone(r.CorrelationID, max(r.EndNS, r.StartNS))
			}
		}
	}
	p.st.dropOldMarkers()
	p.stats.Run, p.stats.Sessions = p.man.Run, len(sessions)
	return p.removeOrphans(ctx)
}

func loadSnapshot[T any](ctx context.Context, p *Processor, table string, into *[]T) error {
	k := p.man.Snapshots[table]
	if k == "" {
		return nil
	}
	rows, err := readParquet[T](ctx, p.cfg.Out, k)
	if err != nil {
		return fmt.Errorf("read %s: %w", k, err)
	}
	*into = rows
	return nil
}

// removeOrphans deletes output files no commit names (a run that died before
// committing) and garbage whose grace period is over.
func (p *Processor) removeOrphans(ctx context.Context) error {
	named := map[string]bool{p.outKey("manifest.json"): true}
	for _, ks := range p.man.Facts {
		for _, k := range ks {
			named[k] = true
		}
	}
	for _, k := range p.man.Snapshots {
		named[k] = true
	}
	for _, g := range p.man.Garbage {
		named[g.Key] = true
	}
	pre := ""
	if p.cfg.OutPrefix != "" {
		pre = p.cfg.OutPrefix + "/"
	}
	objs, err := p.cfg.Out.List(ctx, pre)
	if err != nil {
		return fmt.Errorf("list output: %w", err)
	}
	for _, o := range objs {
		if !named[o.Key] {
			if err := p.cfg.Out.Delete(ctx, o.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// Poll runs one pass: lists raw files (recent hours, or everything when full),
// and processes the new ones in runs of BatchFiles. Returns the files processed.
func (p *Processor) Poll(ctx context.Context, full bool) (int, error) {
	if p.dirty {
		// The last run folded rows into the state and then failed to commit:
		// reapplying them on top would lose their requests and count them twice.
		if err := p.Open(ctx); err != nil {
			return 0, p.fail(fmt.Errorf("reload after a failed run: %w", err))
		}
		p.dirty = false
		full = true // the cursors were reset
	}
	todo, err := p.newFiles(ctx, full)
	if err != nil {
		return 0, p.fail(err)
	}
	p.mu.Lock()
	p.stats.FilesPending = len(todo)
	p.mu.Unlock()
	done := 0
	for len(todo) > 0 {
		n := min(len(todo), p.cfg.BatchFiles)
		if err := p.run(ctx, todo[:n]); err != nil {
			p.dirty = true
			return done, p.fail(err)
		}
		done += n
		todo = todo[n:]
		p.mu.Lock()
		p.stats.FilesPending = len(todo)
		p.mu.Unlock()
	}
	if done == 0 { // nothing new: still collect garbage that's due
		if err := p.collectGarbage(ctx); err != nil {
			return 0, p.fail(err)
		}
	}
	return done, nil
}

func (p *Processor) fail(err error) error {
	p.mu.Lock()
	p.stats.Errors++
	p.stats.LastError = err.Error()
	p.mu.Unlock()
	return err
}

// saw records a raw file as processed and moves its folder's cursor.
func (p *Processor) saw(key string, size int64) {
	p.seen[key] = size
	dir := path.Dir(key) + "/"
	if key > p.cursor[dir] {
		p.cursor[dir] = key
	}
}

// newFiles lists the raw files not yet processed. A full listing walks
// everything; a poll asks each pod folder of the current and previous hours
// only for the files after its cursor, so its cost is the new files. The
// ledger decides either way: a file is new when it isn't in it.
func (p *Processor) newFiles(ctx context.Context, full bool) ([]Object, error) {
	base := ""
	if p.cfg.RawPrefix != "" {
		base = p.cfg.RawPrefix + "/"
	}
	var listed []Object
	if full {
		days := []string{base + "dt="}
		if !p.cfg.SweepAll && p.man.SealedThrough != "" { // only the days not sealed
			dirs, err := p.cfg.Raw.ListDirs(ctx, base)
			if err != nil {
				return nil, fmt.Errorf("list %s: %w", base, err)
			}
			days = days[:0]
			for _, d := range dirs {
				day := strings.TrimSuffix(strings.TrimPrefix(d, base+"dt="), "/")
				if strings.HasPrefix(d, base+"dt=") && day > p.man.SealedThrough {
					days = append(days, d)
				}
			}
		}
		for _, d := range days {
			objs, err := p.cfg.Raw.List(ctx, d)
			if err != nil {
				return nil, fmt.Errorf("list %s: %w", d, err)
			}
			listed = append(listed, objs...)
		}
	} else {
		now := p.cfg.Now().UTC()
		for _, t := range []time.Time{now.Add(-time.Hour), now} {
			hour := base + t.Format("dt=2006-01-02/hour=15/")
			dirs, err := p.cfg.Raw.ListDirs(ctx, hour)
			if err != nil {
				return nil, fmt.Errorf("list %s: %w", hour, err)
			}
			for _, dir := range dirs {
				objs, err := p.cfg.Raw.ListAfter(ctx, dir, p.cursor[dir])
				if err != nil {
					return nil, fmt.Errorf("list %s: %w", dir, err)
				}
				listed = append(listed, objs...)
			}
		}
	}
	var out []Object
	for _, o := range listed {
		rel := strings.TrimPrefix(o.Key, base)
		if !rawKey.MatchString(rel) || strings.HasPrefix(rel, "capture/") {
			continue
		}
		if _, ok := p.seen[o.Key]; ok || p.sealed(o.Key) {
			continue
		}
		if o.Size == 0 {
			continue // an upload still being written; listed again once it has bytes
		}
		if size, bad := p.unread[o.Key]; bad && size == o.Size {
			continue // unreadable last time and unchanged since
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return rawOrder(out[i].Key) < rawOrder(out[j].Key) })
	return out, nil
}

// rawOrder sorts raw files by when their batch started (the file name), across
// pods, so a run mostly sees a transaction's rows in the order they happened.
func rawOrder(key string) string {
	return path.Base(key) + "|" + key
}

// run processes one batch of raw files and commits it.
func (p *Processor) run(ctx context.Context, files []Object) error {
	runID := p.man.Run + 1
	var out batch
	var ledger []Ledger
	var rows, bad int64
	content := false
	for _, f := range files {
		b, err := p.cfg.Raw.Get(ctx, f.Key)
		var lines [][]byte
		if err == nil {
			lines, err = gunzipLines(b)
		}
		if err != nil {
			// A file that isn't readable yet (truncated, not gzip) is retried
			// once it changes; it isn't recorded as processed.
			slog.Warn("process: raw file unreadable; retrying when it changes", "key", f.Key, "err", err)
			p.unread[f.Key] = f.Size
			continue
		}
		delete(p.unread, f.Key)
		var n, nbad int64
		for _, line := range lines {
			var e events.Event
			if json.Unmarshal(line, &e) != nil || e.Phase == "" || e.CorrelationID == "" {
				nbad++
				continue
			}
			p.st.apply(&e, line, f.Key, &out)
			if e.BodyB64 != "" {
				content = true
			}
			n++
		}
		rows += n
		bad += nbad
		ledger = append(ledger, Ledger{RawKey: f.Key, Size: f.Size, Rows: n, Bad: nbad, Run: runID})
	}
	p.st.expire(&out)

	next := p.man.clone()
	next.Run, next.Committed, next.NewestNS = runID, p.cfg.Wall().UTC(), p.st.newestNS
	next.ContentRetained = next.ContentRetained || content
	next.Unread = nil
	for k := range p.unread {
		next.Unread = append(next.Unread, k)
	}
	sort.Strings(next.Unread)
	tag := fmt.Sprintf("run-%012d.parquet", runID)
	if err := addPart(ctx, p, &next, "requests", tag, out.requests); err != nil {
		return err
	}
	if err := addPart(ctx, p, &next, "events", tag, out.events); err != nil {
		return err
	}
	// Written: let them go before the request rows read everything back.
	nRequests, nEvents := int64(len(out.requests)), int64(len(out.events))
	out = batch{}
	if err := addPart(ctx, p, &next, "ledger", tag, ledger); err != nil {
		return err
	}
	sessions, prompts, devices, people, pending := p.st.snapshot()
	if err := setSnapshot(ctx, p, &next, "sessions", tag, sessions); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "prompts", tag, prompts); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "person_devices", tag, devices); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "people", tag, people); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "pending", tag, pending); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "pending_bodies", tag, p.st.bodyRows()); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "accounts", tag, p.st.accountRows()); err != nil {
		return err
	}
	for _, t := range factTables {
		if len(next.runParts(t)) >= p.cfg.CompactAt {
			if err := p.compact(ctx, &next, t, runID); err != nil {
				return err
			}
		}
	}
	// Derived from all the committed data, so after the parts and compaction.
	derived, err := p.rowParts.derive(ctx, &next, pending, p.st.accountRows(), people, sessions)
	if err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "request_rows", tag, derived.RequestRows); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "person_last", tag, derived.PersonLast); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "prompt_rows", tag, derived.PromptRows); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "prompt_steps", tag, derived.PromptSteps); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "session_rows", tag, derived.SessionRows); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "session_marks", tag, derived.SessionMarks); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "presence_segments", tag, derived.Presence); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "event_rows", tag, derived.EventRows); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "session_tools", tag, derived.SessionTools); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "session_request_rows", tag, derived.SessionReqs); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "person_map", tag, derived.PersonMap); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "person_sessions", tag, derived.PersonSess); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "account_facts", tag, derived.AcctFacts); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "account_devices", tag, derived.AcctDevices); err != nil {
		return err
	}
	if err := setSnapshot(ctx, p, &next, "call_tool_uses", tag, derived.CallTools); err != nil {
		return err
	}
	next.SealedThrough = p.sealThrough(&next)
	if err := p.commit(ctx, next); err != nil {
		return err
	}
	for _, l := range ledger {
		p.saw(l.RawKey, l.Size)
	}
	if err := p.collectGarbage(ctx); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats.Run, p.stats.LastRun = runID, next.Committed
	p.stats.FilesProcessed += int64(len(ledger))
	p.stats.RowsRead += rows
	p.stats.BadLines += bad
	p.stats.Requests += nRequests
	p.stats.Events += nEvents
	p.stats.Sessions = len(sessions)
	p.stats.FilesUnread = p.stats.FilesUnread[:0]
	for k := range p.unread {
		p.stats.FilesUnread = append(p.stats.FilesUnread, k)
	}
	sort.Strings(p.stats.FilesUnread)
	if p.st.newestNS > 0 {
		p.stats.LagSeconds = p.cfg.Now().Sub(time.Unix(0, p.st.newestNS)).Seconds()
	}
	return nil
}

func (m Manifest) clone() Manifest {
	n := m
	n.Facts = map[string][]string{}
	for k, v := range m.Facts {
		n.Facts[k] = append([]string(nil), v...)
	}
	n.Snapshots = map[string]string{}
	for k, v := range m.Snapshots {
		n.Snapshots[k] = v
	}
	n.Garbage = append([]Garbage(nil), m.Garbage...)
	n.Parts = map[string]PartInfo{}
	for k, v := range m.Parts {
		n.Parts[k] = v
	}
	return n
}

func (p *Processor) commit(ctx context.Context, next Manifest) error {
	b, err := json.MarshalIndent(next, "", " ")
	if err != nil {
		return err
	}
	if err := p.cfg.Out.Put(ctx, p.outKey("manifest.json"), b); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	p.man = next
	return nil
}

func (p *Processor) collectGarbage(ctx context.Context) error {
	now := p.cfg.Wall()
	var keep []Garbage
	var due []string
	for _, g := range p.man.Garbage {
		if now.After(g.After) {
			due = append(due, g.Key)
		} else {
			keep = append(keep, g)
		}
	}
	if len(due) == 0 {
		return nil
	}
	next := p.man.clone()
	next.Garbage = keep
	// Commit first: a crash after it leaves files nothing names, which Open removes.
	if err := p.commit(ctx, next); err != nil {
		return err
	}
	for _, k := range due {
		if err := p.cfg.Out.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

func addPart[T fact](ctx context.Context, p *Processor, m *Manifest, table, tag string, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	k := p.outKey(table, tag)
	if err := writeParquet(ctx, p.cfg.Out, k, rows); err != nil {
		return fmt.Errorf("write %s: %w", k, err)
	}
	m.Facts[table] = append(m.Facts[table], k)
	m.Parts[k] = rangeOf(rows)
	return nil
}

func setSnapshot[T any](ctx context.Context, p *Processor, m *Manifest, table, tag string, rows []T) error {
	k := p.outKey(table, tag)
	if err := writeParquet(ctx, p.cfg.Out, k, rows); err != nil {
		return fmt.Errorf("write %s: %w", k, err)
	}
	if old := m.Snapshots[table]; old != "" {
		m.Garbage = append(m.Garbage, Garbage{Key: old, After: p.cfg.Wall().Add(p.cfg.Grace)})
	}
	m.Snapshots[table] = k
	return nil
}

// compact merges a fact table's run parts into its day files.
func (p *Processor) compact(ctx context.Context, m *Manifest, table string, runID int64) error {
	switch table {
	case "requests":
		return compactByDay[Request](ctx, p, m, table, runID)
	case "events":
		return compactByDay[Event](ctx, p, m, table, runID)
	case "ledger":
		return compactByDay[Ledger](ctx, p, m, table, runID)
	}
	return nil
}

// --- file formats --------------------------------------------------------------------

func gunzipLines(b []byte) ([][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	for _, l := range bytes.Split(raw, []byte{'\n'}) {
		if l = bytes.TrimSpace(l); len(l) > 0 {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func writeParquet[T any](ctx context.Context, s Store, key string, rows []T) error {
	return s.Put(ctx, key, encodeParquet(rows))
}

// encodeParquet writes rows through the schema's Deconstruct. (parquet-go
// v0.32.0's typed batch Write stores a zero value as the zero, not null, in an
// optional column once that column has held a non-zero value;
// TestOptionalZeroIsNull guards this.)
func encodeParquet[T any](rows []T) []byte {
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[T](&buf, parquet.Compression(&parquet.Zstd))
	schema := w.Schema()
	batch := make([]parquet.Row, 0, 1024)
	for i := range rows {
		batch = append(batch, schema.Deconstruct(nil, &rows[i]))
		if len(batch) == cap(batch) || i == len(rows)-1 {
			if _, err := w.WriteRows(batch); err != nil {
				panic(err) // writing to memory; only a schema bug can fail
			}
			batch = batch[:0]
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func readParquet[T any](ctx context.Context, s Store, key string) ([]T, error) {
	b, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return parquet.Read[T](bytes.NewReader(b), int64(len(b)))
}

// eachParquet calls fn for each row of a part, decoding a batch at a time, so a
// caller that keeps few of them holds only those.
func eachParquet[T any](ctx context.Context, s Store, key string, fn func(T)) error {
	b, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	f, err := parquet.OpenFile(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return err
	}
	r := parquet.NewGenericReader[T](f)
	defer r.Close()
	for {
		batch := make([]T, 1024)
		n, err := r.Read(batch)
		for _, row := range batch[:n] {
			fn(row)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
