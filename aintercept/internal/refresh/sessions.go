package refresh

// The session rollups (docs/PIPELINE.md §5.2): a session's row and its
// Prompts page list, recomputed together from its hours, its keys, its
// prompts and a few index lookups on its facts. They mean what today's
// session row (process/sessionrows.go sessionRow) and prompt rows
// (process/promptrows.go rows) mean. Values a page computes from the stored
// ones aren't stored: the agent (from product), bypass % (queries, bypassed),
// durations (from their times), live, the label (§6.1) and the children
// (session_edges, §5.4).

// newestCall is the session's call c (cond) first by order (a SQL ORDER BY
// over c): an index scan of the session's calls by time from either end. A
// value no call has scans them all; the account's (person_id) is skipped when
// no account appears on the session (session_accounts).
func newestCall(cond, order, cols string) string {
	return `(SELECT ` + cols + ` FROM calls c WHERE c.session_id = $1 AND ` + cond + ` ORDER BY ` + order + ` LIMIT 1)`
}

// ctx is call c's context: input + cache read + cache write, NULL when all are
// 0 (promptrows.go rowCall.context).
const callCtx = `nullif(coalesce(c.input_tokens, 0) + coalesce(c.cache_read_tokens, 0) + coalesce(c.cache_write_tokens, 0), 0)`

// pyStrip is a title call's answer as the API keeps it: said.strip().strip('"')
// (sessionrows.go pyStripTitle; Python's whitespace).
func pyStrip(v string) string {
	const ws = `[ \t\n\v\f\r\u001c-\u001f\u0085   -     　]`
	return `btrim(regexp_replace(` + v + `, '^` + ws + `+|` + ws + `+$', '', 'g'), '"')`
}

// codexCall is a Codex thread's model call, hidden ones too (sessionrows.go
// codexFacts over x.agent).
const codexCall = `(` + modelCall + ` AND c.source = 'codex')`

// sessionCols are sessions' columns.
const sessionCols = `session_id, start_ns, last_ns, product, client, client_version, client_version_ns, hidden_calls,
	own_title, origin_kind, origin_session_id, origin, repo, commit, prompts, captured_from_prompt, model_calls, events,
	frustrated, interrupted, destructive, queries, bypassed, cov_reported, cov_captured, cov_not_captured,
	cov_window_from, cov_window_to, cov_window_count, fields_missing, person_id, device_id, device_platform,
	device_terminal, device_shell, networks, effort, tokens_input, tokens_cache_read, tokens_cache_write, tokens_output,
	outputs_known, tokens_accuracy, tool_calls, has_facts, facts_cwd, facts_approval, facts_sandbox, limits, limits_ns,
	cli_version, cli_version_ns, rl_status, rl_util, rl_overage, rl_ns, sandbox, sandbox_ns, custom_ca, unattributed,
	prompt_event_last_ns`

// newestDesc is today's "newest call" order: latest time, then the latest id.
const newestDesc = at + ` DESC, replace(c.call_id, '#', '~') COLLATE "C" DESC`

// envOf is the newest Claude Code event's env field k (sessionrows.go
// sessEvents.add: env, the greatest value at the same time).
func envOf(k string) string {
	v := jsonText(`v.env -> '` + k + `'`)
	return `(SELECT ` + v + ` FROM events e JOIN event_envs v USING (env_hash)
		WHERE e.session_id = $1 AND e.source = 'claude_code' AND ` + v + ` IS NOT NULL
		ORDER BY e.ts_ns DESC, ` + v + ` COLLATE "C" DESC LIMIT 1)`
}

// sessionSelect is session $1's row, when it has a visible model call or a
// Claude Code event (the API's load_session).
var sessionSelect = `WITH h AS (SELECT coalesce(sum(model_calls), 0) AS model_calls, coalesce(sum(hidden_calls), 0) AS hidden_calls,
		coalesce(sum(events), 0) AS events, coalesce(sum(frustrated), 0) AS frustrated, coalesce(sum(interrupted), 0) AS interrupted,
		coalesce(sum(destructive), 0) AS destructive, coalesce(sum(queries), 0) AS queries, coalesce(sum(bypassed), 0) AS bypassed,
		coalesce(sum(reported), 0) AS reported, coalesce(sum(missed), 0) AS missed,
		sum(input_tokens) AS input_tokens, sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
		sum(output_tokens) AS output_tokens, coalesce(sum(outputs_known), 0) AS outputs_known, sum(tool_calls) AS tool_calls,
		coalesce(sum(custom_ca), 0) AS custom_ca
	FROM session_hours WHERE session_id = $1),
k AS (SELECT nullif(count(DISTINCT key) FILTER (WHERE kind IN ('call_prompt', 'input_prompt')), 0) AS prompts,
		nullif(count(DISTINCT key) FILTER (WHERE kind = 'ip'), 0) AS networks
	FROM session_keys WHERE session_id = $1),
w AS (SELECT array_agg(f ORDER BY f) AS f, array_agg(l ORDER BY f) AS l, array_agg(n ORDER BY f) AS n FROM (
	SELECT min(ts_ns) AS f, max(ts_ns) AS l, count(*) AS n FROM (
		SELECT ts_ns, sum(CASE WHEN prev IS NULL OR ts_ns - prev >= ` + gapSplit + ` THEN 1 ELSE 0 END) OVER (ORDER BY ts_ns, event_key) AS g
		FROM (SELECT ts_ns, event_key, lag(ts_ns) OVER (ORDER BY ts_ns, event_key) AS prev FROM missed_reports WHERE session_id = $1) a) b
	GROUP BY g) x)
SELECT $1, least(ev.first, fc.at), greatest(ev.last, lc.at), fc.product, cl.client, cv.version, cv.ns, h.hidden_calls,
	` + pyStrip(`ttl.text`) + `,
	CASE WHEN o.kind IS NOT NULL OR o.init = 'cold_resume' THEN coalesce(o.kind, 'resume') END,
	CASE WHEN o.kind IS NOT NULL OR o.init = 'cold_resume' THEN o.parent END,
	CASE WHEN o.kind IS NOT NULL OR o.init = 'cold_resume' THEN jsonb_build_object('kind', coalesce(o.kind, 'resume'),
		'sessionId', o.parent, 'turn', nullif(o.ordinal, 0),
		'init', CASE WHEN o.init IN ('warm_fork', 'cold_fork', 'cold_resume') THEN o.init END, 'agentName', o.agent_name) END,
	CASE WHEN coalesce(ws.cwd, '') <> '' THEN regexp_replace(rtrim(ws.cwd, '/'), '^.*/', '') END,
	CASE WHEN coalesce(ws.cwd, '') <> '' AND jsonb_typeof(cx.ws -> ws.cwd -> 'latest_git_commit_hash') = 'string'
		THEN left(cx.ws -> ws.cwd ->> 'latest_git_commit_hash', 7) END,
	k.prompts, ip.captured_from,
	h.model_calls, nullif(h.events, 0), h.frustrated, h.interrupted, h.destructive, h.queries, h.bypassed,
	CASE WHEN h.events > 0 THEN h.reported END,
	CASE WHEN h.events > 0 THEN h.reported - h.missed ELSE h.model_calls END,
	CASE WHEN h.events > 0 THEN h.missed ELSE 0 END,
	CASE WHEN h.events > 0 THEN coalesce(w.f, '{}') ELSE '{}' END,
	CASE WHEN h.events > 0 THEN coalesce(w.l, '{}') ELSE '{}' END,
	CASE WHEN h.events > 0 THEN coalesce(w.n, '{}') ELSE '{}' END,
	h.model_calls > 0 AND NOT EXISTS (SELECT 1 FROM calls c WHERE c.session_id = $1 AND ` + visible + `
		AND (c.account_id IS NOT NULL OR c.device_id IS NOT NULL)),
	coalesce(ea.account_id, ca.account_id), coalesce(ed.device_id, cd.device_id),
	CASE WHEN coalesce(ed.device_id, cd.device_id) IS NOT NULL THEN ` + envOf("platform") + ` END,
	CASE WHEN coalesce(ed.device_id, cd.device_id) IS NOT NULL THEN ` + envOf("terminal") + ` END,
	CASE WHEN coalesce(ed.device_id, cd.device_id) IS NOT NULL THEN ` + envOf("shell") + ` END,
	k.networks, ef.effort,
	h.input_tokens, h.cache_read_tokens, h.cache_write_tokens, h.output_tokens, h.outputs_known,
	CASE WHEN h.model_calls > 0 AND h.outputs_known = h.model_calls THEN 'exact' WHEN h.outputs_known = 0 THEN 'missing' ELSE 'estimated' END,
	h.tool_calls,
	ws.cwd IS NOT NULL OR ` + jsonStrOnly(`cx.turn -> 'approval_policy'`) + ` IS NOT NULL OR fs.v IS NOT NULL,
	ws.cwd, ` + jsonStrOnly(`cx.turn -> 'approval_policy'`) + `, fs.v,
	lim.provider, lim.ns,
	cli.v, cli.ns, rl.status, rl.util, rl.overage, rl.ns, sb.v, sb.ns,
	h.custom_ca > 0,
	h.model_calls > 0 AND NOT acct.any,
	(SELECT max(l.ts_ns) FROM unnest(ARRAY[` + promptEventNames + `]) n(name), LATERAL (SELECT e.ts_ns FROM events e
		WHERE e.session_id = $1 AND e.name = n.name AND e.source = 'claude_code' ORDER BY e.ts_ns DESC LIMIT 1) l)
FROM h, k, w
LEFT JOIN LATERAL (SELECT (SELECT min(ts_ns) FROM events WHERE session_id = $1 AND source = 'claude_code') AS first,
	(SELECT max(ts_ns) FROM events WHERE session_id = $1 AND source = 'claude_code') AS last) ev ON true
LEFT JOIN LATERAL ` + newestCall(visible, callOrder, at+` AS at, `+agent+` AS product`) + ` fc ON true
LEFT JOIN LATERAL ` + newestCall(visible, newestDesc, at+` AS at`) + ` lc ON true
LEFT JOIN LATERAL ` + newestCall(visible+` AND coalesce(c.client_mode, '') <> ''`, newestDesc, `c.client_mode AS client`) + ` cl ON true
LEFT JOIN LATERAL ` + newestCall(visible+` AND coalesce(c.client_version, '') <> ''`, newestDesc, `c.client_version AS version, `+at+` AS ns`) + ` cv ON true
LEFT JOIN LATERAL ` + newestCall(`c.hidden_kind = 'title' AND `+modelCall+` AND jsonb_typeof(c.detail -> 'text') = 'string' AND c.detail ->> 'text' <> ''`,
	newestDesc, `c.detail ->> 'text' AS text`) + ` ttl ON true
LEFT JOIN LATERAL (SELECT CASE WHEN min(` + jsonInt(md("prompt_index")) + `) > 1 THEN min(` + jsonInt(md("prompt_index")) + `) END AS captured_from
	FROM events e WHERE e.session_id = $1 AND e.source = 'claude_code' AND e.name = 'tengu_input_prompt') ip ON true
LEFT JOIN LATERAL (SELECT EXISTS (SELECT 1 FROM session_accounts WHERE session_id = $1) AS any) acct ON true
LEFT JOIN LATERAL (SELECT e.account_id FROM events e WHERE acct.any AND e.session_id = $1 AND e.source = 'claude_code' AND e.account_id IS NOT NULL
	ORDER BY e.ts_ns, e.account_id COLLATE "C" LIMIT 1) ea ON true
LEFT JOIN LATERAL (SELECT c.account_id FROM calls c WHERE acct.any AND c.session_id = $1 AND ` + visible + ` AND c.account_id IS NOT NULL
	ORDER BY ` + at + `, c.account_id COLLATE "C" LIMIT 1) ca ON true
LEFT JOIN LATERAL (SELECT e.device_id FROM events e WHERE e.session_id = $1 AND e.source = 'claude_code' AND e.device_id IS NOT NULL
	AND e.name IN ('tengu_api_success', 'tengu_api_query', 'tengu_input_prompt', 'tengu_cancel',
	               'tengu_bash_tool_command_executed', 'tengu_return_to_session', 'tengu_event_loop_stall')
	ORDER BY e.ts_ns, e.device_id COLLATE "C" LIMIT 1) ed ON true
LEFT JOIN LATERAL ` + newestCall(visible+` AND c.device_id IS NOT NULL`, at+`, c.device_id COLLATE "C"`, `c.device_id`) + ` cd ON true
LEFT JOIN LATERAL ` + newestCall(visible+` AND coalesce(c.effort, '') <> ''`, at+` DESC, replace(c.call_id, '#', '~') COLLATE "C"`, `c.effort`) + ` ef ON true
LEFT JOIN LATERAL ` + newestCall(visible+` AND starts_with(c.user_agent, 'claude-cli/')`,
	at+` DESC, coalesce(substring(c.user_agent from 'claude-cli/([^ ]+)'), '') COLLATE "C" DESC`,
	`coalesce(substring(c.user_agent from 'claude-cli/([^ ]+)'), '') AS v, `+at+` AS ns`) + ` cli ON true
LEFT JOIN LATERAL ` + newestCall(visible+` AND jsonb_typeof(c.provider -> 'anthropic-ratelimit-unified-status') = 'string'`,
	`coalesce(c.response_start_ns, `+at+`) DESC, `+newestDesc,
	`c.provider ->> 'anthropic-ratelimit-unified-status' AS status, `+jsonDouble(`c.provider -> 'anthropic-ratelimit-unified-7d-utilization'`)+` AS util,
	`+jsonStrOnly(`c.provider -> 'anthropic-ratelimit-unified-overage-status'`)+` AS overage, coalesce(c.response_start_ns, `+at+`) AS ns`) + ` rl ON true
LEFT JOIN LATERAL (SELECT ` + jsonText(md("sandbox_enabled")) + ` AS v, e.ts_ns AS ns FROM events e
	WHERE e.session_id = $1 AND e.source = 'claude_code' AND e.name = 'tengu_bash_tool_command_executed' AND ` + jsonText(md("sandbox_enabled")) + ` IS NOT NULL
	ORDER BY e.ts_ns DESC, ` + jsonText(md("sandbox_enabled")) + ` COLLATE "C" DESC LIMIT 1) sb ON true
LEFT JOIN LATERAL (SELECT
	` + newestCall(codexCall+` AND c.ext ? 'workspaces'`, at+` DESC, c.ext::text COLLATE "C" DESC`, `c.ext -> 'workspaces'`) + ` AS ws,
	(SELECT e.metadata FROM events e WHERE e.session_id = $1 AND e.source = 'codex' AND e.name = 'codex_turn_event' AND e.metadata IS NOT NULL
		ORDER BY e.ts_ns DESC, e.metadata::text COLLATE "C" DESC LIMIT 1) AS turn,
	` + newestCall(codexCall+` AND `+jsonText(`c.ext -> 'sandbox_mode'`)+` IS NOT NULL`,
	at+` DESC, `+jsonText(`c.ext -> 'sandbox_mode'`)+` COLLATE "C" DESC`, jsonText(`c.ext -> 'sandbox_mode'`)) + ` AS sandbox
	WHERE fc.product = 'codex') cx ON true
LEFT JOIN LATERAL (SELECT c.provider, ` + at + ` AS ns FROM calls c WHERE fc.product = 'codex' AND c.session_id = $1 AND ` + codexCall + `
	AND c.provider::text LIKE '%x-codex-%' ORDER BY ` + newestDesc + ` LIMIT 1) lim ON true
LEFT JOIN LATERAL (SELECT min(lk.kind) AS kind, min(lk.parent) AS parent, max(lk.ordinal) AS ordinal,
		min(l.init) FILTER (WHERE l.init NOT IN ('', 'new')) AS init,
		min(l.agent_name) FILTER (WHERE l.agent_name NOT IN ('', '/root')) AS agent_name
	FROM calls c, LATERAL (SELECT ` + jsonText(`c.ext -> 'parent_thread_id'`) + ` AS pt, ` + jsonText(`c.ext -> 'subagent_kind'`) + ` AS sk,
		` + jsonText(`c.ext -> 'forked_from_thread_id'`) + ` AS ff, ` + jsonText(`c.ext -> 'history_initialization'`) + ` AS init,
		` + jsonText(`c.ext -> 'agent_name'`) + ` AS agent_name, ` + jsonTrunc(`c.ext -> 'forked_from_ordinal_exclusive'`) + ` AS ord) l,
		LATERAL (SELECT CASE WHEN coalesce(l.pt, '') <> '' THEN CASE WHEN l.sk = 'review' THEN 'review' ELSE 'subagent' END
			WHEN coalesce(l.ff, '') <> '' THEN 'fork' END AS kind,
			CASE WHEN coalesce(l.pt, '') <> '' THEN l.pt WHEN coalesce(l.ff, '') <> '' THEN l.ff END AS parent,
			CASE WHEN coalesce(l.pt, '') = '' AND coalesce(l.ff, '') <> '' THEN l.ord END AS ordinal) lk
	WHERE fc.product = 'codex' AND c.session_id = $1 AND ` + codexCall + ` AND c.ext IS NOT NULL) o ON true
LEFT JOIN LATERAL (SELECT (SELECT key FROM jsonb_object_keys(cx.ws) key LIMIT 1) AS cwd
	WHERE jsonb_typeof(cx.ws) = 'object' AND cx.ws <> '{}') ws ON true
LEFT JOIN LATERAL (SELECT CASE WHEN coalesce(cx.sandbox, '') <> '' THEN cx.sandbox
	ELSE ` + jsonStrOnly(`cx.turn -> 'sandbox_policy'`) + ` END AS v) fs ON true
WHERE h.model_calls > 0 OR h.events > 0`

// gapSplit is claude_code.py GAP_SPLIT_NS: coverage windows split at gaps this
// long (sessionrows.go coverageWindows).
const gapSplit = `900000000000`

// promptEventNames are the events the prompt rows read (promptrows.go
// promptEventNames): the newest's time decides whether a step's events are
// still pending while the session is live.
const promptEventNames = `'tengu_input_prompt', 'tengu_turn_end', 'tengu_cancel', 'tengu_bash_tool_command_executed',
	'tengu_tool_use_success', 'tengu_tool_use_error', 'tengu_tool_use_progress', 'tengu_api_success', 'tengu_paste_text',
	'tengu_paste_image', 'tengu_return_to_session', 'tengu_reactive_compact_attempt', 'tengu_reactive_compact_succeeded',
	'tengu_idle_compact_fired'`

// --- the Prompts page's list ---------------------------------------------------------------------

// itemCols are prompt_items' columns.
const itemCols = `session_id, seq, t_ns, type, prompt_id, prompt_index, open_tail, pasted_bytes, images, hidden,
	between_kind, detail, call_id, tokens_from, tokens_to, attempt_ns`

// kTokens is session.py _k: 1234 -> "1k" (Python's round, half to even).
func kTokens(n string) string {
	return `CASE WHEN ` + n + ` >= 1000 THEN (CASE WHEN ` + n + ` % 1000 > 500 OR ` + n + ` % 1000 = 500 AND (` + n + ` / 1000) % 2 = 1
		THEN ` + n + ` / 1000 + 1 ELSE ` + n + ` / 1000 END)::text || 'k' ELSE ` + n + `::text END`
}

// modelFamily is process/requestrows.go modelFamily: opus, sonnet or haiku.
const modelFamily = `CASE WHEN coalesce(c.model, '') = '' THEN '—' ELSE coalesce(substring(c.model from '^claude-(opus|sonnet|haiku)'), c.model) END`

// itemsSelect is session $1's Prompts page list (promptrows.go rows), in
// today's order (by time, then type; prompts by start and id, the between rows
// in their events' or calls' order):
//   - its prompts, with what the session's other prompts decide: the Codex
//     number (its place), the open tail (the last, with no turn end or turn
//     report status), the pastes and images since the typed prompt before it,
//     and the hidden calls it's given (a prewarm the next prompt to start, others
//     the last started before them: _hidden_by_prompt);
//   - each compaction (session.py compactions: its attempt, its call, the
//     context before and after it from the main calls);
//   - each recap shown on return;
//   - each visible call with no prompt that isn't a compaction's.
var itemsSelect = `WITH p AS (SELECT p.*, row_number() OVER (ORDER BY p.t_ns, p.prompt_id COLLATE "C") AS n, count(*) OVER () AS total
	FROM prompts p WHERE p.session_id = $1),
hc AS (SELECT c.hidden_kind AS kind, ` + at + ` AS t FROM calls c WHERE c.session_id = $1 AND c.hidden_kind IS NOT NULL AND ` + modelCall + `),
hp AS (SELECT CASE WHEN hc.kind = 'prewarm' THEN coalesce(a.pid, b.pid) ELSE coalesce(b.pid, a.pid) END AS pid, hc.kind, count(*) AS n
	FROM hc LEFT JOIN LATERAL (SELECT p.prompt_id AS pid FROM p WHERE p.t_ns >= hc.t ORDER BY p.n LIMIT 1) a ON true
		LEFT JOIN LATERAL (SELECT p.prompt_id AS pid FROM p WHERE p.t_ns < hc.t ORDER BY p.n DESC LIMIT 1) b ON true
	GROUP BY 1, 2),
comp AS (SELECT e.ts_ns AS t, e.batch_call_id AS b, e.batch_cc_n AS bn, lag(e.ts_ns) OVER (ORDER BY ` + evOrder("e") + `) AS prev
	FROM events e WHERE e.session_id = $1 AND e.source = 'claude_code' AND e.name = 'tengu_reactive_compact_succeeded'),
comp2 AS (SELECT comp.*, att.a AS attempt, coalesce(att.a, comp.t) AS start FROM comp
	LEFT JOIN LATERAL (SELECT max(a.ts_ns) AS a FROM events a WHERE a.session_id = $1 AND a.source = 'claude_code'
		AND a.name = 'tengu_reactive_compact_attempt' AND a.ts_ns > coalesce(comp.prev, -1) AND a.ts_ns <= comp.t) att ON true),
comp3 AS (SELECT comp2.*, rq.call_id, fr.ctx AS tokens_from, tt.ctx AS tokens_to,
		EXISTS (SELECT 1 FROM events f WHERE f.session_id = $1 AND f.source = 'claude_code' AND f.name = 'tengu_idle_compact_fired'
			AND f.ts_ns >= comp2.start - 10000000000 AND f.ts_ns <= comp2.start) AS idle
	FROM comp2
	LEFT JOIN LATERAL ` + newestCall(visible+` AND c.request_class = 'compaction' AND `+at+` >= comp2.start AND `+at+` <= comp2.t`, callOrder, `c.call_id`) + ` rq(call_id) ON true
	LEFT JOIN LATERAL ` + newestCall(visible+` AND c.request_class = 'main' AND `+callCtx+` IS NOT NULL AND `+at+` < comp2.start`, newestDesc, callCtx) + ` fr(ctx) ON true
	LEFT JOIN LATERAL ` + newestCall(visible+` AND c.request_class = 'main' AND `+callCtx+` IS NOT NULL AND `+at+` > comp2.t`, callOrder, callCtx) + ` tt(ctx) ON true),
items AS (
	SELECT p.t_ns, 'prompt' AS type, 0 AS grp, p.prompt_id AS ord, 0::bigint AS ord_n, p.prompt_id,
		CASE WHEN p.has_input THEN p.prompt_index WHEN p.product = 'codex' THEN p.n END AS prompt_index,
		p.n = p.total AND p.turn_end_ns IS NULL AND coalesce(p.turn_report ->> 'status', '') = '' AS open_tail,
		pa.pasted, pa.images, coalesce(hj.hidden, '{}') AS hidden,
		NULL AS between_kind, NULL AS detail, NULL AS call_id, NULL::bigint AS tokens_from, NULL::bigint AS tokens_to, NULL::bigint AS attempt_ns
	FROM p
	LEFT JOIN LATERAL (SELECT sum(` + jsonTrunc(md("pastedTextBytes")) + `) FILTER (WHERE e.name = 'tengu_paste_text') AS pasted,
			count(*) FILTER (WHERE e.name = 'tengu_paste_image') AS images
		FROM events e WHERE e.session_id = $1 AND e.source = 'claude_code' AND e.name IN ('tengu_paste_text', 'tengu_paste_image')
		  AND e.ts_ns > coalesce((SELECT max(q.t_ns) FROM p q WHERE q.has_input AND q.t_ns < p.t_ns), -1) AND e.ts_ns <= p.t_ns) pa ON p.has_input
	LEFT JOIN (SELECT pid, jsonb_object_agg(kind, n) AS hidden FROM hp WHERE pid IS NOT NULL GROUP BY pid) hj ON hj.pid = p.prompt_id
	UNION ALL
	SELECT t, 'between', 1, b, bn, NULL, NULL, false, NULL, NULL, NULL,
		CASE WHEN idle THEN 'idle-compaction' ELSE 'compaction' END,
		CASE WHEN idle THEN 'idle compaction' ELSE 'compaction' END
			|| CASE WHEN tokens_from <> 0 AND tokens_to <> 0 THEN ' · context ' || ` + kTokens("tokens_from") + ` || ' → ' || ` + kTokens("tokens_to") + ` || ' tokens' ELSE '' END,
		call_id, tokens_from, tokens_to, attempt
	FROM comp3
	UNION ALL
	SELECT e.ts_ns, 'between', 2, e.batch_call_id, e.batch_cc_n, NULL, NULL, false, NULL, NULL, NULL, 'recap', 'recap shown on return', NULL, NULL, NULL, NULL
	FROM events e WHERE e.session_id = $1 AND e.source = 'claude_code' AND e.name = 'tengu_return_to_session' AND ` + pyTrue(md("hadRecap")) + `
	UNION ALL
	SELECT ` + at + `, 'between', 3, replace(c.call_id, '#', '~'), 0, NULL, NULL, false, NULL, NULL, NULL, 'call',
		coalesce(nullif(c.request_class, ''), '—') || ' · ' || ` + modelFamily + ` || ' · ' || coalesce(nullif(c.stop_reason, ''), '—'),
		c.call_id, NULL, NULL, NULL
	FROM calls c WHERE c.session_id = $1 AND c.prompt_id IS NULL AND ` + visible + `
	  AND c.call_id NOT IN (SELECT call_id FROM comp3 WHERE call_id IS NOT NULL))
SELECT $1, row_number() OVER (ORDER BY t_ns, type COLLATE "C", grp, ord COLLATE "C", ord_n), t_ns, type, prompt_id, prompt_index,
	open_tail, pasted, images, hidden, between_kind, detail, call_id, tokens_from, tokens_to, attempt_ns
FROM items`

// sessionRefresh recomputes session $1's row and its Prompts page list.
// Both are replaced: a session's row leaves when its calls and events have
// (retention), and its list renumbers as prompts arrive.
var sessionRefresh = []string{
	`DELETE FROM sessions WHERE session_id = $1`,
	`INSERT INTO sessions (` + sessionCols + `) ` + sessionSelect,
	`DELETE FROM prompt_items WHERE session_id = $1`,
	`INSERT INTO prompt_items (` + itemCols + `) ` + itemsSelect,
}

// sessionDays is each (account, UTC day) the session's accounts and hours give:
// the account_day rows a session refresh marks (§5.3), so an account seen on
// the session late still gets the session's earlier days.
const sessionDays = `SELECT DISTINCT a.account_id || '/' || (h.hour_ns - h.hour_ns % 86400000000000)
	FROM session_accounts a JOIN session_hours h USING (session_id) WHERE a.session_id = $1`
