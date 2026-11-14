package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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

// refreshWires reads the detail object of every ended call that has no wire
// yet. It reports whether one it waits for wasn't there.
func (w *Worker) refreshWires(ctx context.Context, st *state.KeyState) (bool, error) {
	ended := drawer.EndedCalls(st.Rows())
	var todo []string
	for id := range ended {
		if _, ok := st.External(render.WireID(id)); !ok {
			todo = append(todo, id)
		}
	}
	// A call no longer ended here (an expired row its ending replaced) drops its wire.
	for id := range st.Externals() {
		if c, ok := strings.CutPrefix(id, "wire:"); ok {
			if _, still := ended[c]; !still {
				st.SetExternal(id, nil)
			}
		}
	}
	sort.Strings(todo)
	type got struct {
		id   string
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
			out[i] = got{id, b, err}
		}(i, id)
	}
	wg.Wait()
	now := w.now().UnixNano()
	waiting := false
	for _, g := range out {
		switch {
		case errors.Is(g.err, process.ErrNotFound):
			if w.cfg.DetailWait > 0 && now-ended[g.id] < w.cfg.DetailWait.Nanoseconds() {
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

// refreshParts reads the other keys' parts of every batch the key holds, on a
// run connection.
func (w *Worker) refreshParts(ctx context.Context, key string, st *state.KeyState) error {
	batches := drawer.Batches(st.Rows())
	sums := map[string]state.OtherParts{}
	if len(batches) > 0 {
		rows, err := w.pool.Query(ctx, otherParts, batches, key)
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
		for _, p := range parts {
			s := sums[p.batch]
			var n int64
			if p.n != nil {
				n = *p.n
			}
			s.Add(n, p.name, p.ns, p.session)
			sums[p.batch] = s
		}
	}
	held := map[string]bool{}
	for _, b := range batches {
		held[b] = true
		if s, ok := sums[b]; ok {
			st.SetExternal(render.PartsID(b), s)
		} else {
			st.SetExternal(render.PartsID(b), nil)
		}
	}
	for id := range st.Externals() {
		if b, ok := strings.CutPrefix(id, "parts:"); ok && !held[b] {
			st.SetExternal(id, nil)
		}
	}
	return nil
}

// queueCrossKeys adds to a commit what a change of the key's batch parts owes
// the other keys holding those batches (§3.7). First each changed batch's
// advisory lock, in order: two commits changing one batch serialize there, so
// the second sees the first's part. Then a render flag on every other key
// holding a part of a changed batch (or its call), and on this key if the
// other keys' parts it now sees aren't the ones its render read (seen).
func queueCrossKeys(b *pgx.Batch, key string, changed []string, seen func(batch string) (state.OtherParts, bool)) {
	if len(changed) == 0 {
		return
	}
	sort.Strings(changed)
	for _, c := range changed {
		b.Queue("SELECT pg_advisory_xact_lock(hashtextextended('batch:' || $1, 0))", c)
	}
	b.Queue(`WITH others AS (
		  SELECT key FROM batch_parts WHERE batch_id = ANY($1) AND key <> $2
		  UNION SELECT key FROM requests WHERE call_id = ANY($1) AND key <> $2)
		INSERT INTO dirty_keys (key, seq, since, render) SELECT key, 1, now(), true FROM others
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1, render = true`, changed, key)
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
		seen AS (SELECT * FROM unnest($3::text[], $4::bigint[], $5::text[], $6::bigint[], $7::text[]) AS s(batch_id, n, name, ns, session))
		INSERT INTO dirty_keys (key, seq, since, render)
		SELECT $2, 1, now(), true WHERE EXISTS (
		  SELECT 1 FROM cur FULL JOIN seen USING (batch_id)
		  WHERE (cur.n, cur.name, cur.ns, cur.session) IS DISTINCT FROM (seen.n, seen.name, seen.ns, seen.session))
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1, render = true`, changed, key, ids, ns, names, firsts, sessions)
}
