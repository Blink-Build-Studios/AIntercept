package refresh

import (
	"context"
	"fmt"
	"strings"

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
		case c == "missed":
			src = "ms."
		case strings.Contains(eventAggs, " AS "+c+","), strings.HasSuffix(eventAggs, " AS "+c):
			src = "ev."
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
	ev AS (SELECT e.session_id AS sid, ` + b("e.ts_ns") + ` AS t, ` + eventAggs + `
		FROM events e WHERE e.session_id IS NOT NULL AND e.source = 'claude_code' GROUP BY 1, 2),
	ms AS (SELECT r.session_id AS sid, ` + b("r.ts_ns") + ` AS t, count(*) AS missed
		FROM events r WHERE r.session_id IS NOT NULL AND ` + missedWhere + ` GROUP BY 1, 2),
	rq AS (SELECT sid, t, jsonb_object_agg(kind, n) FILTER (WHERE NOT hidden) AS requests,
			jsonb_object_agg(kind, n) FILTER (WHERE hidden) AS requests_hidden
		FROM (SELECT c.scope_session_id AS sid, ` + b(at) + ` AS t, c.kind || '/' || c.key_hash AS kind, c.hidden_kind IS NOT NULL AS hidden, count(*) AS n
		      FROM calls c WHERE c.scope_session_id IS NOT NULL AND ` + requestRows + ` GROUP BY 1, 2, 3, 4) x
		GROUP BY 1, 2),
	k AS (SELECT sid, t FROM cc UNION SELECT sid, t FROM ag UNION SELECT sid, t FROM ev UNION SELECT sid, t FROM ms
		UNION SELECT sid, t FROM rq)
	SELECT k.sid, k.t, ` + strings.Join(cols, ", ") + `,
		coalesce(rq.requests, '{}'), coalesce(rq.requests_hidden, '{}')
	FROM k LEFT JOIN cc USING (sid, t) LEFT JOIN ag USING (sid, t) LEFT JOIN ev USING (sid, t)
		LEFT JOIN ms USING (sid, t) LEFT JOIN rq USING (sid, t)`
}

// sessionNorm is a session_minutes or session_hours row as compared: sums of
// doubles rounded, since an hour adds its minutes' in another order.
func sessionNorm(t string) string {
	cols := []string{"session_id", t}
	for _, c := range sumColumns {
		if c == "hook_ms_sum" || c == "cost_usd" {
			c = "round(" + c + "::numeric, 6)"
		}
		cols = append(cols, c)
	}
	return strings.Join(append(cols, "requests", "requests_hidden"), ", ")
}

const nonZero = `(model_calls + hidden_calls + events + missed > 0 OR requests <> '{}' OR requests_hidden <> '{}')`

// checks are each rollup table with its rows as compared and from scratch.
var checks = []struct{ table, rows, scratch string }{
	{"session_minutes", "SELECT " + sessionNorm("minute_ns") + " FROM session_minutes WHERE " + nonZero,
		"SELECT " + sessionNorm("t") + " FROM (" + sessionScratch(minuteNS) + ") s(" + scratchCols("t") + ")"},
	{"session_hours", "SELECT " + sessionNorm("hour_ns") + " FROM session_hours WHERE " + nonZero,
		"SELECT " + sessionNorm("t") + " FROM (" + sessionScratch(hourNS) + ") s(" + scratchCols("t") + ")"},
	{"session_keys", "SELECT session_id, hour_ns, kind, key, n FROM session_keys",
		"SELECT sid, " + bucket("at", hourNS) + ", kind, key, count(*) FROM (" +
			keyItems("e.session_id IS NOT NULL", "c.session_id IS NOT NULL") + ") k GROUP BY 1, 2, 3, 4"},
	{"missed_reports", "SELECT event_key, session_id, minute_ns, ts_ns, request_id, client_request_id FROM missed_reports",
		"SELECT r.event_key, r.session_id, " + bucket("r.ts_ns", minuteNS) + ", r.ts_ns, r.request_id, r.client_request_id " +
			"FROM events r WHERE r.session_id IS NOT NULL AND " + missedWhere},
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

// Check compares every rollup table with its from-scratch rows.
func Check(ctx context.Context, q pg.Querier) ([]Result, error) {
	var out []Result
	for _, c := range checks {
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
	return out, nil
}
