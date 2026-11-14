package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// Retention (docs/PIPELINE.md §3.6) is a singleton, every hour. It forgets
// the keys whose day (keys.day) is older than the window, each in one
// transaction, guarded on the n it read:
//  1. the key row is locked (lock_timeout 1 s: a key with a commit in flight
//     is skipped until the next pass), and kept if it moved since the pass
//     read it (new input) or its day is no longer old;
//  2. its rows go, by the key or the session each table's primary key starts
//     with, at most retainBatch rows a statement;
//  3. its key row goes; COMMIT.
//
// Then only what the deleted row named: its state chunks, its garbage and its
// in/ files up to its last applied. A file that came in after stays, and the
// key starts over from it. Account keys have no day and are never forgotten.
//
// The same pass trims the recent index a day behind the tail's cursor, and,
// with RawDays, applies the local raw cap.

// Retention is the retention singleton's settings.
type Retention struct {
	Days    int  // keys whose day is older are forgotten (0: never)
	RawDays int  // the local raw cap: raw/, applied in/ and (Calls) calls/ past it (0: off)
	Calls   bool // the raw cap also deletes calls/ objects (a local directory store)
}

// RetentionResult is what one pass did.
type RetentionResult struct {
	Forgotten int // keys forgotten
	Skipped   int // keys left for the next pass (locked, or moved since the pass read them)
	Objects   int // objects deleted
}

// retainBatch is the most rows one DELETE removes.
var retainBatch = 5000

// RetentionSingleton runs Retain every hour on whichever worker holds RetentionLock.
func RetentionSingleton(w *Worker, r Retention) Singleton {
	return Singleton{Name: "retention", Lock: RetentionLock, Every: time.Hour, Run: func(ctx context.Context, now time.Time) error {
		res, err := w.Retain(ctx, r, now)
		if res.Forgotten+res.Skipped+res.Objects > 0 {
			slog.Info("worker: retention", "forgotten", res.Forgotten, "skipped", res.Skipped, "objects", res.Objects)
		}
		return err
	}}
}

// retentionFromEnv reads ROLLUP_RETENTION_DAYS (default 15) and
// ROLLUP_RETENTION_RAW_DAYS (default 0); a value that isn't a count takes the
// default.
func retentionFromEnv(getenv func(string) string) Retention {
	count := func(k string, d int) int {
		if n, err := strconv.Atoi(getenv(k)); err == nil && n >= 0 {
			return n
		}
		return d
	}
	return Retention{Days: count("ROLLUP_RETENTION_DAYS", 15), RawDays: count("ROLLUP_RETENTION_RAW_DAYS", 0)}
}

// Retain runs one pass at now.
func (w *Worker) Retain(ctx context.Context, r Retention, now time.Time) (RetentionResult, error) {
	var res RetentionResult
	var errs []error
	if r.Days > 0 {
		cutoff := now.UTC().Add(-time.Duration(r.Days) * 24 * time.Hour).Format(time.DateOnly)
		rows, err := w.pool.Query(ctx, "SELECT key, n FROM keys WHERE day <> '' AND day < $1 ORDER BY day, key", cutoff)
		if err != nil {
			return res, err
		}
		type kn struct {
			key string
			n   int64
		}
		old, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (kn, error) {
			var x kn
			return x, r.Scan(&x.key, &x.n)
		})
		if err != nil {
			return res, err
		}
		for _, k := range old {
			row, ok, err := w.forget(ctx, k.key, k.n, cutoff)
			if err != nil {
				return res, err
			}
			if !ok {
				res.Skipped++
				continue
			}
			res.Forgotten++
			res.Objects += w.deleteNamed(ctx, row)
		}
	}
	n, err := w.trimRecent(ctx)
	res.Objects += n
	errs = append(errs, err)
	if r.RawDays > 0 {
		n, err := w.rawCap(ctx, r, now.Add(-time.Duration(r.RawDays)*24*time.Hour))
		res.Objects += n
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}

// forget deletes key's rows and its key row in one transaction, if the row is
// still at n and its day still before cutoff. ok false: kept (moved, or
// locked past lock_timeout). It returns the row it deleted.
func (w *Worker) forget(ctx context.Context, key string, n int64, cutoff string) (row KeyRow, ok bool, err error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return row, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after COMMIT
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '1s'"); err != nil {
		return row, false, err
	}
	var garbage []byte
	var levels []int32
	row.Key = key
	err = tx.QueryRow(ctx, `SELECT `+keyColumns+` FROM keys k WHERE key = $1 AND n = $2 AND day <> '' AND day < $3 FOR UPDATE`,
		key, n, cutoff).Scan(&row.N, &row.State, &levels, &row.LastApplied, &row.Grace, &garbage, &row.RenderVersion,
		&row.UpdatedNS, &row.NewestRowNS, &row.ExpiresNS, &row.Day, &row.FirstRequestNS, &row.EarliestNS, &row.ContentRetained)
	if errors.Is(err, pgx.ErrNoRows) || lockTimeout(err) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	if err := jsonGarbage(garbage, &row); err != nil {
		return row, false, err
	}
	sid, session := strings.CutPrefix(key, "s:")
	for _, t := range append(storedTables(), render.Tables...) {
		owner, arg := "key", key
		if t.Kind != pg.Stored {
			if owner = render.OwnerColumn(t.Name); owner != "key" {
				if !session {
					continue // a key that isn't a session's owns no session rows
				}
				arg = sid
			}
		}
		stmt := fmt.Sprintf("DELETE FROM %[1]s WHERE ctid = ANY(ARRAY(SELECT ctid FROM %[1]s WHERE %[2]s = $1 LIMIT %[3]d))",
			t.Name, owner, retainBatch)
		for {
			tag, err := tx.Exec(ctx, stmt, arg)
			if lockTimeout(err) {
				return row, false, nil
			}
			if err != nil {
				return row, false, fmt.Errorf("retention: %s %s: %w", key, t.Name, err)
			}
			if tag.RowsAffected() < int64(retainBatch) {
				break
			}
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM keys WHERE key = $1", key); err != nil {
		return row, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		if lockTimeout(err) {
			return row, false, nil
		}
		return row, false, err
	}
	return row, true, nil
}

// deleteNamed deletes what a forgotten key row named: its state chunks, its
// garbage, and its in/ files up to its last applied. A failure leaves an
// orphan, never a key missing its state: the row is already gone.
func (w *Worker) deleteNamed(ctx context.Context, row KeyRow) int {
	dir := rollup.KeyDir(w.cfg.Prefix, row.Key)
	n := 0
	del := func(k string) {
		if err := w.cfg.Store.Delete(ctx, k); err == nil {
			n++
		}
	}
	for _, c := range row.State {
		del(dir + c)
	}
	for _, g := range row.Garbage {
		del(dir + g.Path)
	}
	if row.LastApplied != "" {
		in := rollup.InPrefix(w.cfg.Prefix, row.Key)
		objs, err := w.cfg.Store.ListAfter(ctx, in, "")
		if err != nil {
			slog.Warn("worker: retention: list in/", "key", row.Key, "err", err)
		}
		for _, o := range objs {
			if strings.TrimPrefix(o.Key, in) <= row.LastApplied {
				del(o.Key)
			}
		}
	}
	return n
}

// trimRecent deletes the recent index's objects a day behind the tail's cursor.
func (w *Worker) trimRecent(ctx context.Context) (int, error) {
	var last string
	if err := w.pool.QueryRow(ctx, "SELECT coalesce(last_name, '') FROM tail").Scan(&last); err != nil {
		return 0, err
	}
	at, err := keyflag.NameTime(last)
	if last == "" || err != nil {
		return 0, nil // never read yet
	}
	before := at.Add(-24 * time.Hour)
	dir := keyflag.Prefix(w.cfg.Prefix)
	objs, err := w.cfg.Store.ListAfter(ctx, dir, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range objs {
		t, err := keyflag.NameTime(strings.TrimPrefix(o.Key, dir))
		if err != nil || !t.Before(before) {
			continue
		}
		if err := w.cfg.Store.Delete(ctx, o.Key); err == nil {
			n++
		}
	}
	return n, nil
}

// rawCap applies the local raw cap at before: raw day folders (and full
// interception's capture/ ones) of days before its day, in/ files older than
// it that their key already applied (before last_applied − grace), and with
// r.Calls, calls/ objects written before it.
func (w *Worker) rawCap(ctx context.Context, r Retention, before time.Time) (int, error) {
	n := 0
	del := func(k string) {
		if err := w.cfg.Store.Delete(ctx, k); err == nil {
			n++
		}
	}
	root := ""
	if p := strings.Trim(w.cfg.Prefix, "/"); p != "" {
		root = p + "/"
	}
	cutDay := "dt=" + before.UTC().Format(time.DateOnly)
	for _, base := range []string{root, root + "capture/"} {
		dirs, err := w.cfg.Store.ListDirs(ctx, base)
		if err != nil {
			return n, err
		}
		for _, d := range dirs {
			name := strings.TrimSuffix(strings.TrimPrefix(d, base), "/")
			if !strings.HasPrefix(name, "dt=") || name >= cutDay {
				continue
			}
			objs, err := w.cfg.Store.List(ctx, d)
			if err != nil {
				return n, err
			}
			for _, o := range objs {
				del(o.Key)
			}
		}
	}

	rows, err := w.pool.Query(ctx, "SELECT key, last_applied FROM keys WHERE coalesce(last_applied, '') <> ''")
	if err != nil {
		return n, err
	}
	type applied struct{ key, last string }
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (applied, error) {
		var a applied
		return a, r.Scan(&a.key, &a.last)
	})
	if err != nil {
		return n, err
	}
	for _, k := range keys {
		lastAt, err := rollup.InTime(k.last)
		if err != nil {
			continue
		}
		bound := lastAt.Add(-w.cfg.Grace)
		if before.Before(bound) {
			bound = before
		}
		in := rollup.InPrefix(w.cfg.Prefix, k.key)
		objs, err := w.cfg.Store.ListAfter(ctx, in, "")
		if err != nil {
			return n, err
		}
		for _, o := range objs {
			if t, err := rollup.InTime(strings.TrimPrefix(o.Key, in)); err == nil && t.Before(bound) {
				del(o.Key)
			}
		}
	}

	if r.Calls {
		objs, err := w.cfg.Store.ListInfo(ctx, root+"calls/")
		if err != nil {
			return n, err
		}
		for _, o := range objs {
			if o.Modified.Before(before) {
				del(o.Key)
			}
		}
	}
	return n, nil
}

// lockTimeout is a statement that gave up waiting for a lock (lock_timeout).
func lockTimeout(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "55P03"
}
