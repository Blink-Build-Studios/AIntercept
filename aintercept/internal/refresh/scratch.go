package refresh

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
)

// The from-scratch check (docs/PIPELINE.md §9, step 4): every rollup row
// equals one GROUP BY over all the facts, computed in one pass with the same
// definitions the refresh uses for one key. It catches a missed dirty mark, a
// lost update or an order the refresh got wrong; what each value means is
// held to today's code by the unit tests and the v7 comparison.

// Result is one rollup table's comparison.
type Result struct {
	Table    string
	Rows     int64 // the rollup's rows (with a value: a minute or hour left all zero isn't counted)
	Scratch  int64 // the from-scratch rows
	Missing  int64 // from-scratch rows the rollup lacks or has otherwise
	Extra    int64 // rollup rows from scratch lacks or has otherwise
	Checksum string
}

// Equal is whether the rollup is exactly the from-scratch rows.
func (r Result) Equal() bool { return r.Rows == r.Scratch && r.Missing == 0 && r.Extra == 0 }

func bucket(x string, width int64) string { return fmt.Sprintf("(%[1]s - %[1]s %% %[2]d)", x, width) }

// sessionScratch is session_minutes (width a minute) or session_hours (an
// hour) from scratch: (session_id, time, sumColumns…, requests, requests_hidden).
func sessionScratch(width int64) string {
	cols := make([]string, len(sumColumns))
	for i, c := range sumColumns {
		src := "cc."
		switch {
		case c == "hidden_calls":
			src = "ag."
		}
		cols[i] = src + c
		if !nullable[c] {
			cols[i] = "coalesce(" + src + c + ", 0)"
		}
	}
	b := func(x string) string { return bucket(x, width) }
	return `WITH
	cc AS (SELECT c.session_id AS sid, ` + b(at) + ` AS t, ` + callAggs + `
		FROM calls c WHERE c.session_id IS NOT NULL AND ` + visible + ` GROUP BY 1, 2),
	ag AS (SELECT c.session_id AS sid, ` + b(at) + ` AS t, ` + hiddenAggs + `
		FROM calls c WHERE c.hidden_kind IS NOT NULL AND ` + modelCall + ` GROUP BY 1, 2),
	rq AS (SELECT sid, t, jsonb_object_agg(kind, n) FILTER (WHERE NOT hidden) AS requests,
			jsonb_object_agg(kind, n) FILTER (WHERE hidden) AS requests_hidden
		FROM (SELECT c.scope_session_id AS sid, ` + b(at) + ` AS t, c.kind || '/' || c.key_hash AS kind, c.hidden_kind IS NOT NULL AS hidden, count(*) AS n
		      FROM calls c WHERE c.scope_session_id IS NOT NULL AND ` + requestRows + ` GROUP BY 1, 2, 3, 4) x
		GROUP BY 1, 2),
	k AS (SELECT sid, t FROM cc UNION SELECT sid, t FROM ag UNION SELECT sid, t FROM rq)
	SELECT k.sid, k.t, ` + strings.Join(cols, ", ") + `,
		coalesce(rq.requests, '{}'), coalesce(rq.requests_hidden, '{}')
	FROM k LEFT JOIN cc USING (sid, t) LEFT JOIN ag USING (sid, t) LEFT JOIN rq USING (sid, t)`
}

// sessionNorm is a session_minutes or session_hours row as compared.
func sessionNorm(t string) string {
	cols := append([]string{"session_id", t}, sumColumns...)
	return strings.Join(append(cols, "requests", "requests_hidden"), ", ")
}

const nonZero = `(model_calls + hidden_calls > 0 OR requests <> '{}' OR requests_hidden <> '{}')`

// checks are each rollup table with its rows as compared and from scratch.
var checks = []struct{ table, rows, scratch string }{
	{"session_minutes", "SELECT " + sessionNorm("minute_ns") + " FROM session_minutes WHERE " + nonZero,
		"SELECT " + sessionNorm("t") + " FROM (" + sessionScratch(minuteNS) + ") s(" + scratchCols("t") + ")"},
	{"session_hours", "SELECT " + sessionNorm("hour_ns") + " FROM session_hours WHERE " + nonZero,
		"SELECT " + sessionNorm("t") + " FROM (" + sessionScratch(hourNS) + ") s(" + scratchCols("t") + ")"},
	{"session_minute_keys", "SELECT session_id, minute_ns, kind, key, n FROM session_minute_keys",
		"SELECT sid, " + bucket("at", minuteNS) + ", kind, key, sum(n) FROM (" +
			keyItems("c.session_id IS NOT NULL", "true") + ") k GROUP BY 1, 2, 3, 4"},
	{"session_keys", "SELECT session_id, hour_ns, kind, key, n FROM session_keys",
		"SELECT sid, " + bucket("at", hourNS) + ", kind, key, sum(n) FROM (" +
			keyItems("c.session_id IS NOT NULL", "true") + ") k GROUP BY 1, 2, 3, 4"},
	{"cred_minutes", "SELECT key_hash, minute_ns, kind, hidden, agent, n, n_tx FROM cred_minutes", credScratch(minuteNS)},
	{"cred_hours", "SELECT key_hash, hour_ns, kind, hidden, agent, n, n_tx FROM cred_hours", credScratch(hourNS)},
}

func scratchCols(t string) string {
	return strings.Join(append(append([]string{"session_id", t}, sumColumns...), "requests", "requests_hidden"), ", ")
}

func credScratch(width int64) string {
	return `SELECT coalesce(c.key_hash, ''), ` + bucket(at, width) + `, c.kind, c.hidden_kind IS NOT NULL, ` + agent + `,
		count(*), count(*) FILTER (WHERE ` + transaction + `)
	FROM calls c WHERE ` + agentCall + ` GROUP BY 1, 2, 3, 4, 5`
}

// DB is what Check reads through: a pool.
type DB interface {
	pg.Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Check compares every rollup table with its from-scratch rows.
func Check(ctx context.Context, q DB) ([]Result, error) {
	var out []Result
	for _, c := range append(checks, dayCheck) {
		r := Result{Table: c.table}
		err := q.QueryRow(ctx, `WITH a AS (`+c.rows+`), b AS (`+c.scratch+`)
			SELECT (SELECT count(*) FROM a), (SELECT count(*) FROM b),
				(SELECT count(*) FROM (SELECT * FROM b EXCEPT ALL SELECT * FROM a) x),
				(SELECT count(*) FROM (SELECT * FROM a EXCEPT ALL SELECT * FROM b) y),
				(SELECT coalesce(sum(('x' || substr(md5(a::text), 1, 15))::bit(60)::bigint), 0)::text FROM a)`).
			Scan(&r.Rows, &r.Scratch, &r.Missing, &r.Extra, &r.Checksum)
		if err != nil {
			return nil, fmt.Errorf("check %s: %w", c.table, err)
		}
		out = append(out, r)
	}
	rs, err := checkRecomputed(ctx, q)
	return append(out, rs...), err
}

// recomputed are the prompt, session and day rollups, each with every key
// the facts give it from scratch and how a key's rows are computed, in the
// order they read each other (sessions read prompts, days sessions). A prompt
// is computed from all its calls at once (refreshPrompt), the refresh does it
// a step at a time: the check holds the two equal.
var recomputed = []struct {
	kind, keys string
	tables     []string
	run        func(ctx context.Context, tx pgx.Tx, key string) error
}{
	{"prompt", `SELECT DISTINCT prompt_id FROM calls c WHERE c.prompt_id IS NOT NULL AND ` + visible,
		[]string{"prompts", "steps", "step_keys", "prompt_keys"}, refreshPrompt},
	{"session", `SELECT DISTINCT session_id FROM calls c WHERE ` + visible,
		[]string{"sessions", "prompt_items"}, stmtsOf(sessionRefresh, "session")},
	{"account_day", `SELECT DISTINCT a.account_id || '/' || (` + at + ` - ` + at + ` % ` + fmt.Sprint(dayNS) + `)
		FROM session_accounts a JOIN calls c USING (session_id) WHERE ` + visible,
		[]string{"account_days"}, stmtsOf(dayRefresh, "account_day")},
}

// stmtsOf runs a rollup's statements for a key of kind.
func stmtsOf(stmts []string, kind string) func(ctx context.Context, tx pgx.Tx, key string) error {
	return func(ctx context.Context, tx pgx.Tx, k string) error {
		args, err := keyArgs(kind, k)
		if err != nil {
			return err
		}
		for _, s := range stmts {
			if _, err := tx.Exec(ctx, s, args[:params(s)]...); err != nil {
				return err
			}
		}
		return nil
	}
}

// checkRecomputed compares the prompt, session and day rollups with the same
// rollups computed again, inside a transaction it rolls back: every row
// deleted, then every key's computed from the facts (and the rollups below
// it, themselves checked above) with the refresh's own statements. It
// catches a missed dirty mark, a lost update or an order the refresh got
// wrong; what each value means is held to today's code by the unit tests and
// the v7 comparison.
func checkRecomputed(ctx context.Context, q DB) ([]Result, error) {
	tx, err := q.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))                  //nolint:errcheck // a rollback after a failure has nothing to report
	if _, err := tx.Exec(ctx, `SET LOCAL jit = off`); err != nil { // as Refresh runs them (noJIT)
		return nil, err
	}
	var tables []string
	for _, r := range recomputed {
		tables = append(tables, r.tables...)
	}
	for _, t := range tables {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE was_`+t+` ON COMMIT DROP AS SELECT * FROM `+t); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+t); err != nil {
			return nil, err
		}
	}
	for _, r := range recomputed {
		rows, err := tx.Query(ctx, r.keys)
		if err != nil {
			return nil, fmt.Errorf("check %s keys: %w", r.kind, err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if err := r.run(ctx, tx, k); err != nil {
				return nil, fmt.Errorf("check %s %q: %w", r.kind, k, err)
			}
		}
	}
	var out []Result
	for _, t := range tables {
		r := Result{Table: t}
		err := tx.QueryRow(ctx, `WITH a AS (SELECT * FROM was_`+t+`), b AS (SELECT * FROM `+t+`)
			SELECT (SELECT count(*) FROM a), (SELECT count(*) FROM b),
				(SELECT count(*) FROM (SELECT * FROM b EXCEPT ALL SELECT * FROM a) x),
				(SELECT count(*) FROM (SELECT * FROM a EXCEPT ALL SELECT * FROM b) y),
				(SELECT coalesce(sum(('x' || substr(md5(a::text), 1, 15))::bit(60)::bigint), 0)::text FROM a)`).
			Scan(&r.Rows, &r.Scratch, &r.Missing, &r.Extra, &r.Checksum)
		if err != nil {
			return nil, fmt.Errorf("check %s: %w", t, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// dayCheck is account_days against one SQL over the facts alone (not the
// session rollups it reads): each account's sessions' visible calls per UTC
// day, by the session's agent (owner decision 2: the directory sums activity
// inside the window).
var dayCheck = struct{ table, rows, scratch string }{"account_days (facts)",
	`SELECT account_id, day_ns, agent, sessions, session_ids, call_session_ids, prompts, model_calls, outputs_known, tokens_input,
		tokens_cache_read, tokens_cache_write, tokens_output, first_ns, last_ns FROM account_days`,
	`WITH d AS (SELECT c.session_id AS sid, ` + at + ` - ` + at + ` % ` + fmt.Sprint(dayNS) + ` AS day, ` + at + ` AS t, c.prompt_id AS prompt,
			c.input_tokens, c.cache_read_tokens, c.cache_write_tokens, c.output_tokens
		FROM calls c WHERE ` + visible + `),
	agent AS (SELECT DISTINCT ON (c.session_id) c.session_id AS sid, ` + agent + ` AS product FROM calls c WHERE ` + visible + `
		ORDER BY c.session_id, ` + callOrder + `),
	sd AS (SELECT sid, day, count(*) AS calls, count(DISTINCT prompt) AS prompts, count(output_tokens) AS outputs_known,
			sum(input_tokens) AS input_tokens, sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
			sum(output_tokens) AS output_tokens, min(t) AS first, max(t) AS last
		FROM d GROUP BY 1, 2)
	SELECT a.account_id, sd.day, CASE WHEN agent.product = 'codex' THEN 'codex' ELSE 'claude-code' END,
		count(*), array_agg(a.session_id ORDER BY a.session_id COLLATE "C"),
		array_agg(a.session_id ORDER BY a.session_id COLLATE "C"), sum(sd.prompts), sum(sd.calls), sum(sd.outputs_known),
		sum(sd.input_tokens), sum(sd.cache_read_tokens), sum(sd.cache_write_tokens), sum(sd.output_tokens), min(sd.first), max(sd.last)
	FROM session_accounts a JOIN sd ON sd.sid = a.session_id LEFT JOIN agent ON agent.sid = a.session_id
	GROUP BY 1, 2, 3`,
}

// keyArgs is a refresh's arguments for a key of kind (Refresh's).
func keyArgs(kind, k string) ([]any, error) {
	switch kind {
	case "session":
		return []any{k}, nil
	}
	id, at, err := splitKey(k)
	if err != nil {
		return nil, err
	}
	at -= at % dayNS
	return []any{id, at, at + dayNS}, nil
}
