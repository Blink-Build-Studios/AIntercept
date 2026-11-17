// Package refresh is v9's rollup refresher (docs/PIPELINE.md §5.1): it claims
// dirty rows, recomputes each from the facts in its own transaction (one
// INSERT … SELECT per rollup, the next level marked dirty in the same
// transaction) and releases it by seq. Nothing is held in memory: Postgres
// holds the facts, the rollups and the dirty list, and every refresh
// recomputes from the source, so it's right whatever order the facts came in.
//
// It also runs the housekeeping (§5.5): the open-call sweep and retention.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Coalesce is how long a step, prompt, session or account-day row waits after it's
// marked before it's claimed, so a busy one is recomputed at most this often
// however many flushes touch it (§5.1). A var so tests can drain at once.
var Coalesce = 5 * time.Second

// Stale is how long a claim lasts: a row claimed longer ago (its worker died)
// is claimed again.
const Stale = time.Minute

// claimBatch is how many rows one claim takes.
const claimBatch = 32

// Kinds are the dirty kinds this refresher recomputes (§5.3).
var Kinds = []string{"minute", "hour", "cred_minute", "cred_hour", "step", "prompt", "session", "account_day"}

// coalesced are the kinds a claim waits Coalesce for.
var coalesced = []string{"step", "prompt", "session", "account_day"}

// Claim is a claimed dirty row: what it is, and its seq when claimed.
type Claim struct {
	Kind, Key string
	Seq       int64
}

// ClaimRows claims up to n dirty rows, oldest first, in a short transaction of
// its own: rows unclaimed (or claimed longer than Stale ago) and, for the
// coalesced kinds, marked at least Coalesce ago, skipping rows another worker
// is claiming. No lock is held once it returns.
func ClaimRows(ctx context.Context, db *pgxpool.Pool, n int) ([]Claim, error) {
	rows, err := db.Query(ctx, `UPDATE dirty d SET claimed_at = now() FROM (
		SELECT kind, key FROM dirty
		WHERE kind = ANY($1) AND (claimed_at IS NULL OR claimed_at < now() - $2::interval)
		  AND (kind <> ALL($3) OR since <= now() - $4::interval)
		ORDER BY since LIMIT $5 FOR UPDATE SKIP LOCKED) c
		WHERE d.kind = c.kind AND d.key = c.key
		RETURNING d.kind, d.key, d.seq`, Kinds, Stale, coalesced, Coalesce, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Claim, error) {
		var c Claim
		return c, r.Scan(&c.Kind, &c.Key, &c.Seq)
	})
}

// Release ends a claim after its refresh committed: the row goes if it wasn't
// marked again since the claim (its seq is the same); otherwise it's left
// dirty, unclaimed, marked now (so a coalesced row waits again), and is
// refreshed again. It reports whether the row went.
func Release(ctx context.Context, db *pgxpool.Pool, c Claim) (bool, error) {
	var gone bool
	err := db.QueryRow(ctx, `WITH del AS (DELETE FROM dirty WHERE kind = $1 AND key = $2 AND seq = $3 RETURNING 1),
		upd AS (UPDATE dirty SET claimed_at = NULL, since = now()
		        WHERE kind = $1 AND key = $2 AND NOT EXISTS (SELECT 1 FROM del) RETURNING 1)
		SELECT EXISTS (SELECT 1 FROM del)`, c.Kind, c.Key, c.Seq).Scan(&gone)
	return gone, err
}

// Refresh recomputes c's rollup rows from the facts and marks the next level
// dirty, in one transaction.
func Refresh(ctx context.Context, db *pgxpool.Pool, c Claim) error {
	switch c.Kind {
	case "step":
		return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return noJIT(ctx, tx, func() error { return refreshStep(ctx, tx, c.Key) }) })
	case "prompt":
		return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return noJIT(ctx, tx, func() error { return refreshPrompt(ctx, tx, c.Key) }) })
	}
	id, at, args := c.Key, int64(0), []any(nil)
	switch c.Kind {
	case "session": // keyed by an id alone
		args = []any{id}
	default:
		var err error
		if id, at, err = splitKey(c.Key); err != nil {
			return fmt.Errorf("refresh %s %q: %w", c.Kind, c.Key, err)
		}
	}
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL jit = off`); err != nil {
			return err
		}
		var stmts []string
		var next []mark
		width := minuteNS
		switch c.Kind {
		case "minute":
			at -= at % minuteNS
			stmts = append([]string{minuteRow}, minuteKeys...)
			next = []mark{{"hour", key(id, at-at%hourNS)}}
		case "hour":
			at -= at % hourNS
			width = hourNS
			stmts = append([]string{hourRow}, hourKeys...)
			next = []mark{{"session", id}}
		case "cred_minute":
			at -= at % minuteNS
			stmts = credMinute
			next = []mark{{"cred_hour", key(id, at-at%hourNS)}}
		case "cred_hour":
			at -= at % hourNS
			width = hourNS
			stmts = credHour
		case "session":
			stmts = sessionRefresh
		case "account_day":
			at -= at % dayNS
			width = dayNS
			stmts = dayRefresh
		default:
			return fmt.Errorf("refresh: no rollup for dirty kind %q", c.Kind)
		}
		if args == nil {
			args = []any{id, at, at + width}
		}
		for _, s := range stmts {
			if _, err := tx.Exec(ctx, s, args[:params(s)]...); err != nil {
				return fmt.Errorf("refresh %s %q: %w", c.Kind, c.Key, err)
			}
		}
		if c.Kind == "session" {
			// §5.3: a session marks each of its accounts' days, every day it
			// has, so an account seen on it late still gets its earlier days.
			rows, err := tx.Query(ctx, "SELECT 'account_day', k FROM ("+sessionDays+") d(k)", id)
			if err != nil {
				return err
			}
			ms, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (mark, error) {
				var m mark
				return m, r.Scan(&m.kind, &m.key)
			})
			if err != nil {
				return err
			}
			next = append(next, ms...)
		}
		return markDirty(ctx, tx, next)
	})
}

// noJIT runs f with JIT off in tx. The planner prices a result lookup (GIN's
// default selectivity) and a session's lookups high enough to compile them, and
// compiling costs seconds where running takes milliseconds (a step: 3.3 s of
// JIT, 0.8 ms of work).
func noJIT(ctx context.Context, tx pgx.Tx, f func() error) error {
	if _, err := tx.Exec(ctx, `SET LOCAL jit = off`); err != nil {
		return err
	}
	return f()
}

// stepAt is where a step is, or its call would be: its prompt, session and time.
type stepAt struct {
	prompt, session string
	at              int64
}

// refreshStep recomputes step id (a call id) from its call alone, and its
// prompts' rows (the one it was in, the one it's in now) from their keys
// (§5.1). It marks its sessions and its minutes (their work keys read it).
func refreshStep(ctx context.Context, tx pgx.Tx, id string) error {
	where := func(sql string) (*stepAt, error) {
		var s stepAt
		err := tx.QueryRow(ctx, sql, id).Scan(&s.prompt, &s.session, &s.at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return &s, err
	}
	was, err := where(`SELECT prompt_id, session_id, t_ns FROM steps WHERE call_id = $1`)
	if err != nil {
		return fmt.Errorf("refresh step %q: %w", id, err)
	}
	now, err := where(`SELECT c.prompt_id, c.session_id, ` + at + ` FROM calls c
		WHERE c.call_id = $1 AND c.prompt_id IS NOT NULL AND ` + visible)
	if err != nil {
		return fmt.Errorf("refresh step %q: %w", id, err)
	}
	if was == nil && now == nil {
		return nil
	}
	var prompts []string
	var next []mark
	for _, s := range []*stepAt{was, now} {
		if s != nil {
			prompts = append(prompts, s.prompt)
			next = append(next, mark{"session", s.session}, mark{"minute", key(s.session, s.at-s.at%minuteNS)})
		}
	}
	prompts = promptList(prompts...)
	if _, err := tx.Exec(ctx, lockPrompts, prompts); err != nil {
		return fmt.Errorf("refresh step %q: %w", id, err)
	}
	for _, s := range stepRefresh {
		if _, err := tx.Exec(ctx, s, id); err != nil {
			return fmt.Errorf("refresh step %q: %w", id, err)
		}
	}
	for _, s := range promptRows {
		if _, err := tx.Exec(ctx, s, prompts); err != nil {
			return fmt.Errorf("refresh step %q: %w", id, err)
		}
	}
	return markDirty(ctx, tx, next)
}

// refreshPrompt recomputes prompt id from all its calls (promptFull): what
// retention marks, for a prompt that lost calls. It marks its sessions, those
// it was in and is in now, and their minutes are retention's.
func refreshPrompt(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, lockPrompts, []string{id}); err != nil {
		return fmt.Errorf("refresh prompt %q: %w", id, err)
	}
	sessions := func() ([]mark, error) {
		rows, err := tx.Query(ctx, "SELECT DISTINCT 'session', session_id FROM steps WHERE prompt_id = $1", id)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (mark, error) {
			var m mark
			return m, r.Scan(&m.kind, &m.key)
		})
	}
	next, err := sessions()
	if err != nil {
		return fmt.Errorf("refresh prompt %q: %w", id, err)
	}
	for _, s := range promptFull {
		if _, err := tx.Exec(ctx, s, id); err != nil {
			return fmt.Errorf("refresh prompt %q: %w", id, err)
		}
	}
	for _, s := range promptRows {
		if _, err := tx.Exec(ctx, s, []string{id}); err != nil {
			return fmt.Errorf("refresh prompt %q: %w", id, err)
		}
	}
	now, err := sessions()
	if err != nil {
		return fmt.Errorf("refresh prompt %q: %w", id, err)
	}
	return markDirty(ctx, tx, append(next, now...))
}

// dayNS is a UTC day.
const dayNS = 24 * hourNS

// params is how many parameters statement s takes ($1…$n).
func params(s string) int {
	n := 0
	for i := 1; i <= 3; i++ {
		if strings.Contains(s, "$"+strconv.Itoa(i)) {
			n = i
		}
	}
	return n
}

// Drain claims, refreshes and releases until nothing is left to claim. It
// returns how many rows it refreshed. A row whose refresh fails stays claimed,
// so it's tried again once its claim is stale, and doesn't hold up the rest.
func Drain(ctx context.Context, db *pgxpool.Pool) (int, error) {
	n := 0
	for ctx.Err() == nil {
		claims, err := ClaimRows(ctx, db, claimBatch)
		if err != nil {
			return n, err
		}
		if len(claims) == 0 {
			return n, nil
		}
		for _, c := range claims {
			if err := Refresh(ctx, db, c); err != nil {
				if ctx.Err() != nil {
					return n, ctx.Err()
				}
				slog.Warn("ALARM refresh failed; retried once its claim is stale", "kind", c.Kind, "key", c.Key, "err", err)
				continue
			}
			if _, err := Release(ctx, db, c); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, ctx.Err()
}

// mark is a dirty row to mark.
type mark struct{ kind, key string }

// markDirty marks rows dirty (a new seq for one already there), sorted so
// concurrent markers take their locks in one order, as the flush does.
func markDirty(ctx context.Context, tx pgx.Tx, ms []mark) error {
	if len(ms) == 0 {
		return nil
	}
	slices.SortFunc(ms, func(a, b mark) int {
		if c := strings.Compare(a.kind, b.kind); c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	})
	ms = slices.Compact(ms)
	kinds, keys := make([]string, len(ms)), make([]string, len(ms))
	for i, m := range ms {
		kinds[i], keys[i] = m.kind, m.key
	}
	_, err := tx.Exec(ctx, `INSERT INTO dirty (kind, key) SELECT * FROM unnest($1::text[], $2::text[])
		ON CONFLICT (kind, key) DO UPDATE SET seq = excluded.seq`, kinds, keys)
	return err
}

func key(id string, ns int64) string { return id + "/" + strconv.FormatInt(ns, 10) }

// splitKey is a dirty key's id and time: "<id>/<ns>", the id possibly "" (a
// credential row's calls without one). A key without a time has ns 0.
func splitKey(k string) (string, int64, error) {
	i := strings.LastIndexByte(k, '/')
	if i < 0 {
		return k, 0, nil
	}
	ns, err := strconv.ParseInt(k[i+1:], 10, 64)
	if err != nil {
		return "", 0, errors.New("the key's time isn't a number")
	}
	return k[:i], ns, nil
}
