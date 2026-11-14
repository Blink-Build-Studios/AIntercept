package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The render's external inputs (docs/PIPELINE.md v7 §3.7), refreshed by each
// run before it renders and kept in the key's state (state.SetExternal), so a
// cold load renders what was committed:
//   - each ended call's wire, read once from its detail object
//     (calls/<id>.ndjson.gz). An object not there yet for a call that ended
//     under Config.DetailWait ago (wall clock) is tried again: the run keeps its flag,
//     with render. After that the call is rendered from its request row
//     (drawer.Missing), as today's drawer does for a missing object;
//   - what the other keys hold of the key's analytics batches (their
//     batch_parts, summed), read in one statement.

// DefaultDetailWait is the rollup service's Config.DetailWait: ingest writes a
// call's detail object when it ends, before the key file holding its ending.
const DefaultDetailWait = 2 * time.Minute

// detailGets is how many detail objects one run reads at once.
const detailGets = 16

// refreshWires reads the detail object of every ended call with no wire yet
// (the state's running set, §2 v8: never a walk over its calls), and drops
// the wire of a call the run's changes took away (an expired row its ending
// replaced). It reports whether one it waits for wasn't there.
func (w *Worker) refreshWires(ctx context.Context, st *state.KeyState, changes []state.Change) (bool, error) {
	for _, c := range changes {
		if c.Table == state.TableFactRequests && c.Row == nil {
			if _, ok := st.External(render.WireID(c.ID)); ok {
				st.SetExternal(render.WireID(c.ID), nil)
			}
		}
	}
	ended := st.Unwired()
	todo := make([]string, 0, len(ended))
	for id := range ended {
		todo = append(todo, id)
	}
	sort.Strings(todo)
	type got struct {
		id   string
		end  int64
		body []byte
		err  error
	}
	out := make([]got, len(todo))
	sem := make(chan struct{}, detailGets)
	var wg sync.WaitGroup
	for i, id := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			b, err := w.cfg.Store.Get(ctx, records.DetailKey(w.cfg.Prefix, id))
			out[i] = got{id, ended[id], b, err}
		}(i, id)
	}
	wg.Wait()
	now := w.now().UnixNano()
	waiting := false
	for _, g := range out {
		switch {
		case errors.Is(g.err, process.ErrNotFound):
			if w.cfg.DetailWait > 0 && now-g.end < w.cfg.DetailWait.Nanoseconds() {
				waiting = true // not there yet: tried again on the next run
				continue
			}
			st.SetExternal(render.WireID(g.id), drawer.Missing{})
		case g.err != nil:
			return false, fmt.Errorf("worker: detail of %s: %w", g.id, g.err)
		default:
			if wire := drawer.ParseDetail(g.id, g.body); wire != nil {
				st.SetExternal(render.WireID(g.id), wire)
			} else {
				st.SetExternal(render.WireID(g.id), drawer.Missing{})
			}
		}
	}
	return waiting, nil
}

// otherParts is the statement reading what other keys hold of a key's batches.
const otherParts = `SELECT batch_id, n, min_name, first_ns, first_session FROM batch_parts
	WHERE batch_id = ANY($1) AND key <> $2`

// partsToRead is the batches whose other keys' parts a run reads again (§2,
// v8): the ones its changes name (a batch it now holds, or no longer does),
// the ones its dirty inputs name (another key changed its part), and, with no
// warm state (every row rendered), every one it holds. Never a walk over its
// rows otherwise.
func partsToRead(st *state.KeyState, changes []state.Change, inputs []DirtyInput, all bool) []string {
	set := map[string]bool{}
	for _, c := range changes {
		for _, b := range state.BatchOfChange(c) {
			set[b] = true
		}
	}
	for _, in := range inputs {
		if in.Kind == inputParts {
			set[in.ID] = true
		}
	}
	if all {
		for _, b := range st.HeldBatches() {
			set[b] = true
		}
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// inputParts is a dirty input's kind for a batch's parts.
const inputParts = "parts"

// refreshParts reads the other keys' parts of the given batches the key
// holds, in one statement on a run connection; a batch it no longer holds
// loses its parts.
func (w *Worker) refreshParts(ctx context.Context, key string, st *state.KeyState, batches []string) error {
	var held []string
	for _, b := range batches {
		if st.HoldsBatch(b) {
			held = append(held, b)
		} else {
			st.SetExternal(render.PartsID(b), nil)
		}
	}
	if len(held) == 0 {
		return nil
	}
	rows, err := w.pool.Query(ctx, otherParts, held, key)
	if err != nil {
		return err
	}
	type part struct {
		batch   string
		n       *int64
		name    *string
		ns      *int64
		session *string
	}
	parts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (part, error) {
		var p part
		return p, r.Scan(&p.batch, &p.n, &p.name, &p.ns, &p.session)
	})
	if err != nil {
		return err
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].batch < parts[j].batch })
	sums := map[string]state.OtherParts{}
	for _, p := range parts {
		s := sums[p.batch]
		var n int64
		if p.n != nil {
			n = *p.n
		}
		s.Add(n, p.name, p.ns, p.session)
		sums[p.batch] = s
	}
	for _, b := range held {
		if s, ok := sums[b]; ok {
			st.SetExternal(render.PartsID(b), s)
		} else {
			st.SetExternal(render.PartsID(b), nil)
		}
	}
	return nil
}

// callBatches is the batches whose call the run's changes inserted or
// changed (its analytics request_txs row): the holder's side of the
// cross-key rule (§2).
func callBatches(changes []state.Change) []string {
	var out []string
	for _, c := range changes {
		if c.Table == state.TableRequestTxs {
			out = append(out, state.BatchOfChange(c)...)
		}
	}
	return out
}

// queueCrossKeys adds to a commit what a change of the key's batch parts owes
// the other keys holding those batches (§3.7, §2 v8: a part, or the batch's
// call, by its analytics request_txs row, as state.HoldsBatch counts them;
// not its fact row, which a key whose call is a copy has none of), and the
// holder's side:
// a commit that inserts or changes a batch call's row re-checks the batch's
// parts (§2, the cross-key rule). First each batch's advisory lock, in order:
// two commits of one batch serialize there, so the second sees the first's
// part or call. Then, in one statement, a render flag on every other key
// holding a part of a changed batch (or its call), with the batch as its
// dirty input: what its run reads again. And in one more, the same on this
// key for the batches whose other keys' parts it now sees aren't the ones its
// render read (seen).
func queueCrossKeys(b *pgx.Batch, key string, changed, calls []string, seen func(batch string) (state.OtherParts, bool)) {
	all := map[string]bool{}
	for _, c := range append(append([]string(nil), changed...), calls...) {
		all[c] = true
	}
	if len(all) == 0 {
		return
	}
	locked := make([]string, 0, len(all))
	for c := range all {
		locked = append(locked, c)
	}
	sort.Strings(locked)
	for _, c := range locked {
		b.Queue("SELECT pg_advisory_xact_lock(hashtextextended('batch:' || $1, 0))", c)
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		b.Queue(`WITH others AS (
		  SELECT key, batch_id AS id FROM batch_parts WHERE batch_id = ANY($1) AND key <> $2
		  UNION SELECT key, call_id FROM request_txs WHERE call_id = ANY($1) AND kind = 'analytics' AND key <> $2),
		flags AS (INSERT INTO dirty_keys (key, seq, since, render) SELECT DISTINCT key, 1, now(), true FROM others
		  ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1, render = true)
		INSERT INTO dirty_inputs (key, kind, id, seq) SELECT key, '`+inputParts+`', id, 1 FROM others
		ON CONFLICT (key, kind, id) DO UPDATE SET seq = dirty_inputs.seq + 1`, changed, key)
	}
	changed = locked
	var ids, names, sessions []*string
	var ns, firsts []*int64
	for _, c := range changed {
		p, ok := seen(c)
		if !ok {
			continue
		}
		id, n := c, p.N
		ids, ns = append(ids, &id), append(ns, &n)
		var name, session *string
		var first *int64
		if p.HasName {
			name = &p.Name
		}
		if p.HasSession {
			session, first = &p.FirstSession, &p.FirstNS
		}
		names, firsts, sessions = append(names, name), append(firsts, first), append(sessions, session)
	}
	b.Queue(`WITH cur AS (
		  SELECT batch_id, sum(n)::bigint AS n, min(nullif(min_name, '')) AS name,
		         (array_agg(first_ns ORDER BY first_ns, first_session) FILTER (WHERE first_session IS NOT NULL))[1] AS ns,
		         (array_agg(first_session ORDER BY first_ns, first_session) FILTER (WHERE first_session IS NOT NULL))[1] AS session
		  FROM batch_parts WHERE batch_id = ANY($1) AND key <> $2 GROUP BY batch_id),
		seen AS (SELECT * FROM unnest($3::text[], $4::bigint[], $5::text[], $6::bigint[], $7::text[]) AS s(batch_id, n, name, ns, session)),
		diff AS (SELECT batch_id FROM cur FULL JOIN seen USING (batch_id)
		  WHERE (cur.n, cur.name, cur.ns, cur.session) IS DISTINCT FROM (seen.n, seen.name, seen.ns, seen.session)),
		flag AS (INSERT INTO dirty_keys (key, seq, since, render) SELECT $2, 1, now(), true WHERE EXISTS (SELECT 1 FROM diff)
		  ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1, render = true)
		INSERT INTO dirty_inputs (key, kind, id, seq) SELECT $2, '`+inputParts+`', batch_id, 1 FROM diff
		ON CONFLICT (key, kind, id) DO UPDATE SET seq = dirty_inputs.seq + 1`, changed, key, ids, ns, names, firsts, sessions)
}

// queueConsumeInputs deletes the dirty inputs a run read, each at the seq it
// read: one written again since (another commit's) stays, for the next run.
func queueConsumeInputs(b *pgx.Batch, key string, inputs []DirtyInput) {
	if len(inputs) == 0 {
		return
	}
	kinds, ids, seqs := make([]string, len(inputs)), make([]string, len(inputs)), make([]int64, len(inputs))
	for i, in := range inputs {
		kinds[i], ids[i], seqs[i] = in.Kind, in.ID, in.Seq
	}
	b.Queue(`DELETE FROM dirty_inputs WHERE key = $1 AND (kind, id, seq) IN
		(SELECT * FROM unnest($2::text[], $3::text[], $4::bigint[]))`, key, kinds, ids, seqs)
}

// consume consumes a run's flag (at the seq it read) and the dirty inputs it
// read, when the run commits nothing.
func (w *Worker) consume(ctx context.Context, key string, seq int64, inputs []DirtyInput) error {
	var b pgx.Batch
	if seq > 0 {
		b.Queue("DELETE FROM dirty_keys WHERE key = $1 AND seq = $2", key, seq)
	}
	queueConsumeInputs(&b, key, inputs)
	if b.Len() == 0 {
		return nil
	}
	return w.pool.SendBatch(ctx, &b).Close()
}
