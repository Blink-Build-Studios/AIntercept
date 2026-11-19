package refresh

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HousekeepingLock is the housekeeping's advisory lock (§5.5): "aintercept"
// in ASCII, then a number (the listing's is …0020).
const HousekeepingLock int64 = 0x61696e7465720021

// How long a call may wait for its end, in data time, before the sweep closes
// it: the old engine's (expire.go ExpireAfter, ExpireAfterWS, in git history).
const (
	SweepAfter   = 6 * time.Hour
	SweepAfterWS = 7 * 24 * time.Hour
)

// batch is the most rows one sweep or retention statement touches.
const batch = 5000

// sweep closes up to batch open calls whose wait has passed in data time (the
// newest record any pod sent, ingest_pods), as the old engine did:
// end_ns is the start plus the wait, swept is set (a real end still replaces
// it: flush), and the rollup rows the call counts in are marked dirty as a
// flush of it would mark them.
var sweepSQL = fmt.Sprintf(`WITH clock AS (SELECT max(last_ns) AS ns FROM ingest_pods),
due AS (
	SELECT c.call_id FROM calls c, clock
	WHERE c.end_ns IS NULL AND (c.transport IS DISTINCT FROM 'websocket' AND c.start_ns < clock.ns - $1
	                            OR c.transport = 'websocket' AND c.start_ns < clock.ns - $2)
	ORDER BY c.start_ns LIMIT $3 FOR UPDATE SKIP LOCKED),
swept AS (
	UPDATE calls c SET swept = true,
		end_ns = c.start_ns + CASE WHEN c.transport = 'websocket' THEN $2 ELSE $1 END
	FROM due WHERE c.call_id = due.call_id AND c.end_ns IS NULL
	RETURNING c.*),
marks AS (
	SELECT 'minute' AS kind, c.session_id || '/' || (c.start_ns - c.start_ns %% %[1]d) AS key FROM swept c
	WHERE c.session_id IS NOT NULL
	UNION SELECT 'minute', c.scope_session_id || '/' || (c.start_ns - c.start_ns %% %[1]d) FROM swept c
	WHERE c.scope_session_id IS NOT NULL
	UNION SELECT 'cred_minute', coalesce(c.key_hash, '') || '/' || (c.start_ns - c.start_ns %% %[1]d) FROM swept c
	WHERE %[2]s
	UNION SELECT 'step', c.call_id FROM swept c WHERE c.prompt_id IS NOT NULL),
marked AS (
	INSERT INTO dirty (kind, key) SELECT kind, key FROM marks ORDER BY kind, key
	ON CONFLICT (kind, key) DO UPDATE SET seq = excluded.seq RETURNING 1)
SELECT (SELECT count(*) FROM swept), (SELECT count(*) FROM marked)`, minuteNS, agentCall)

// Sweep closes every open call whose wait has passed in data time, a batch
// at a time. It returns how many it closed.
func Sweep(ctx context.Context, db *pgxpool.Pool) (int64, error) {
	var total int64
	for {
		var n, marked int64
		if err := db.QueryRow(ctx, sweepSQL, int64(SweepAfter), int64(SweepAfterWS), batch).Scan(&n, &marked); err != nil {
			return total, err
		}
		total += n
		if n < batch {
			return total, nil
		}
	}
}

// retained are the tables retention trims, each with what makes a row old ($1:
// the cutoff, ns): the facts by their time and the
// rollups by theirs. The cutoff is a whole hour, so a rollup row goes exactly
// when all its facts have. A credential goes when it was last verified before
// the cutoff; the other identity rows (accounts, sessions' accounts and
// credentials, devices, parents) are a handful per person and stay.
var retained = []struct{ table, old string }{
	{"calls", "start_ns < $1"},
	{"calls", "start_ns IS NULL AND end_ns < $1"},
	{"session_minutes", "minute_ns < $1"},
	{"session_hours", "hour_ns < $1"},
	{"session_minute_keys", "minute_ns < $1"},
	{"session_keys", "hour_ns < $1"},
	{"cred_minutes", "minute_ns < $1"},
	{"cred_hours", "hour_ns < $1"},
	{"credentials", "verified_ns < $1"},
	{"ingest_files", "loaded_at < to_timestamp($1::bigint / 1e9)"},
}

// Retain deletes the facts older than days before now (from the start of
// that hour), and their rollup rows, a batch per statement. Raw files expire
// by the bucket's lifecycle rule on raw/ (§5.5). It returns the rows deleted.
func Retain(ctx context.Context, db *pgxpool.Pool, days int, now time.Time) (int64, error) {
	cut := now.Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Hour)
	var total int64
	for _, r := range retained {
		for {
			tag, err := db.Exec(ctx, fmt.Sprintf(`DELETE FROM %[1]s WHERE ctid = ANY(ARRAY(
				SELECT ctid FROM %[1]s WHERE %[2]s LIMIT %[3]d))`, r.table, r.old, batch), cut.UnixNano())
			if err != nil {
				return total, fmt.Errorf("retention: %s: %w", r.table, err)
			}
			total += tag.RowsAffected()
			if tag.RowsAffected() < batch {
				break
			}
		}
	}
	// The prompt, session and day rollups that held facts before the cut are
	// recomputed from what's left (a row with nothing left goes), as any
	// refresh is: marked dirty, not deleted here.
	if _, err := db.Exec(ctx, retainMarks, cut.UnixNano()); err != nil {
		return total, fmt.Errorf("retention: marks: %w", err)
	}
	return total, nil
}

// retainMarks marks the prompts, sessions and account days that began before
// the cut ($1).
const retainMarks = `INSERT INTO dirty (kind, key)
	SELECT kind, key FROM (SELECT 'prompt' AS kind, prompt_id AS key FROM prompts WHERE t_ns < $1
		UNION SELECT 'session', session_id FROM sessions WHERE start_ns < $1
		UNION SELECT 'account_day', account_id || '/' || day_ns FROM account_days WHERE day_ns < $1) m
	ORDER BY kind, key
	ON CONFLICT (kind, key) DO UPDATE SET seq = excluded.seq`

// Housekeeper runs the housekeeping on whichever worker holds
// HousekeepingLock: the sweep every SweepEvery and retention every
// RetainEvery (with RetentionDays 0, never).
type Housekeeper struct {
	SweepEvery, RetainEvery time.Duration
	RetentionDays           int

	swept, retained time.Time
}

// Run does what's due, if this worker gets the lock.
func (h *Housekeeper) Run(ctx context.Context, db *pgxpool.Pool) error {
	now := time.Now()
	sweep := now.Sub(h.swept) >= h.SweepEvery
	retain := h.RetentionDays > 0 && now.Sub(h.retained) >= h.RetainEvery
	if !sweep && !retain {
		return nil
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", HousekeepingLock).Scan(&ok); err != nil || !ok {
		return err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", HousekeepingLock); err != nil {
			conn.Conn().Close(context.Background()) // a closed session frees its locks
		}
	}()
	if sweep {
		n, err := Sweep(ctx, db)
		if err != nil {
			return fmt.Errorf("sweep: %w", err)
		}
		h.swept = now
		if n > 0 {
			slog.Info("worker: swept open calls", "n", n)
		}
	}
	if retain {
		n, err := Retain(ctx, db, h.RetentionDays, now)
		if err != nil {
			return err
		}
		h.retained = now
		if n > 0 {
			slog.Info("worker: retention", "deleted", n)
		}
	}
	return nil
}

// Lag is the age of the oldest unloaded file or dirty row of Kinds, whichever
// is older (0: none): what KEDA scales the workers on (§5.5).
func Lag(ctx context.Context, db *pgxpool.Pool) (time.Duration, error) {
	var secs float64
	err := db.QueryRow(ctx, `SELECT greatest(0,
		(SELECT extract(epoch FROM now() - min(listed_at)) FROM ingest_files WHERE loaded_at IS NULL),
		(SELECT extract(epoch FROM now() - min(since)) FROM dirty WHERE kind = ANY($1)))::float8`, Kinds).Scan(&secs)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)).Round(time.Microsecond), nil
}
