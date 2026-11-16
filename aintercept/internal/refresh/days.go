package refresh

// The directory's per-day rollup (docs/PIPELINE.md §5.2, owner decision 2):
// an account's activity inside one UTC day, per agent. The People page sums a
// window's days, so a person's row counts what happened in the window, not
// the lifetime totals of the sessions active in it (§8 difference 4).
//
// An account's sessions are those it appears on (session_accounts: a visible
// model call or a Claude Code event names it, as process/requestrows.go
// personSessions has them), and a session's agent is its first visible model
// call's (sessionrows.go sessionRow: product, agentOf; Claude Code's without
// one). A session is active in a day when it has a visible model call or a
// Claude Code event in it, and its numbers are its hours' in that day, as
// rollups.py _PEOPLE sums its sessions' rows:
//   - sessions, session_ids, sessions_with_events: the sessions active (their
//     ids, so a window counts each session once however many of its days it
//     was active on; call_session_ids: those with a model call that day, the
//     overview's agent cards' sessions; with_events: those
//     with a Claude Code event in the day; their coverage counts);
//   - prompts: their prompts in the day (call and typed prompts, distinct per
//     session, as session_rows.prompts);
//   - model_calls, outputs_known, tokens: their visible calls' (tokensOut,
//     tokensAccuracy, the activity strip);
//   - queries, bypassed: bypass %;
//   - reported, missed: coverage (the windows are read from missed_reports);
//   - first_ns, last_ns: the day's first and last activity (lastSeenAt, an
//     agent's firstSeenAt);
//   - repos: its sessions' repos (the People page's Working on, handoff §11R).
// Devices are read from account_devices (§6.4), and live, limits and versions
// from the newest sessions rows.

// daySelect is account $1's day [$2, $3) by agent.
var daySelect = `SELECT $1, $2, CASE WHEN s.product = 'codex' THEN 'codex' ELSE 'claude-code' END,
	count(*), array_agg(sa.session_id ORDER BY sa.session_id COLLATE "C"),
	coalesce(array_agg(sa.session_id ORDER BY sa.session_id COLLATE "C") FILTER (WHERE h.model_calls > 0), '{}'),
	count(*) FILTER (WHERE h.events > 0), coalesce(sum(k.prompts), 0), sum(h.model_calls), sum(h.outputs_known),
	sum(h.input_tokens), sum(h.cache_read_tokens), sum(h.cache_write_tokens), sum(h.output_tokens),
	sum(h.queries), sum(h.bypassed), sum(h.reported), sum(h.missed),
	min(least(ev.first, cf.first)), max(greatest(ev.last, cl.last)),
	coalesce(array_agg(DISTINCT s.repo ORDER BY s.repo) FILTER (WHERE s.repo IS NOT NULL), '{}')
FROM session_accounts sa JOIN sessions s USING (session_id)
CROSS JOIN LATERAL (SELECT coalesce(sum(model_calls), 0) AS model_calls, coalesce(sum(events), 0) AS events,
		coalesce(sum(outputs_known), 0) AS outputs_known, sum(input_tokens) AS input_tokens, sum(cache_read_tokens) AS cache_read_tokens,
		sum(cache_write_tokens) AS cache_write_tokens, sum(output_tokens) AS output_tokens, coalesce(sum(queries), 0) AS queries,
		coalesce(sum(bypassed), 0) AS bypassed, coalesce(sum(reported), 0) AS reported, coalesce(sum(missed), 0) AS missed
	FROM session_hours WHERE session_id = sa.session_id AND hour_ns >= $2 AND hour_ns < $3) h
CROSS JOIN LATERAL (SELECT count(DISTINCT key) AS prompts FROM session_keys
	WHERE session_id = sa.session_id AND hour_ns >= $2 AND hour_ns < $3 AND kind IN ('call_prompt', 'input_prompt')) k
CROSS JOIN LATERAL (SELECT min(ts_ns) AS first, max(ts_ns) AS last FROM events
	WHERE session_id = sa.session_id AND source = 'claude_code' AND ts_ns >= $2 AND ts_ns < $3) ev
LEFT JOIN LATERAL (SELECT ` + at + ` AS first FROM calls c WHERE c.session_id = sa.session_id AND ` + at + ` >= $2 AND ` + at + ` < $3 AND ` + visible + `
	ORDER BY ` + callOrder + ` LIMIT 1) cf ON true
LEFT JOIN LATERAL (SELECT ` + at + ` AS last FROM calls c WHERE c.session_id = sa.session_id AND ` + at + ` >= $2 AND ` + at + ` < $3 AND ` + visible + `
	ORDER BY ` + newestDesc + ` LIMIT 1) cl ON true
WHERE sa.account_id = $1 AND (h.model_calls > 0 OR h.events > 0)
GROUP BY 3`

// dayRefresh recomputes account $1's day [$2, $3): replaced, since an agent's
// row leaves when its sessions' activity in the day has.
var dayRefresh = []string{
	`DELETE FROM account_days WHERE account_id = $1 AND day_ns = $2`,
	`INSERT INTO account_days (account_id, day_ns, agent, sessions, session_ids, call_session_ids, sessions_with_events, prompts, model_calls, outputs_known,
		tokens_input, tokens_cache_read, tokens_cache_write, tokens_output, queries, bypassed, reported, missed, first_ns, last_ns,
		repos) ` + daySelect,
}
