package refresh

import (
	"fmt"
	"strings"
)

// The session rollups (docs/PIPELINE.md §5.2): a session's row and its
// Prompts page list, recomputed together from its hours, its keys, its
// prompts and a few index lookups on its facts. They mean what today's
// session row (process/sessionrows.go sessionRow) and prompt rows
// (process/promptrows.go rows) mean. Values a page computes from the stored
// ones aren't stored: the agent (from product),
// durations (from their times), live, the label (§6.1) and the children
// (session_edges, §5.4).

// newestCall is the session's call c (cond) first by order (a SQL ORDER BY
// over c): an index scan of the session's calls by time from either end. A
// value no call has scans them all; the account's (person_id) is skipped when
// no account appears on the session (session_accounts).
func newestCall(cond, order, cols string) string {
	return `(SELECT ` + cols + ` FROM calls c WHERE c.session_id = $1 AND ` + cond + ` ORDER BY ` + order + ` LIMIT 1)`
}

// hasKeys are the values a session row looks up on its newest (or first) call
// that has one, each with what makes a call have it. A minute's calls that
// have one give it a key (kind "has", keyItems), so the session's lookup
// reads only the calls of the newest (first) hour its keys show it in, and
// none when no call has it: never the session's other calls.
var hasKeys = []struct{ key, cond string }{
	{"client", visible + ` AND coalesce(c.client_mode, '') <> ''`},
	{"version", visible + ` AND coalesce(c.client_version, '') <> ''`},
	{"title", `c.hidden_kind = 'title' AND ` + modelCall + ` AND jsonb_typeof(c.detail -> 'text') = 'string' AND c.detail ->> 'text' <> ''`},
	{"account", visible + ` AND c.account_id IS NOT NULL`},
	{"device", visible + ` AND c.device_id IS NOT NULL`},
	{"effort", visible + ` AND coalesce(c.effort, '') <> ''`},
	{"cli", visible + ` AND starts_with(c.user_agent, 'claude-cli/')`},
	{"ratelimit", visible + ` AND jsonb_typeof(c.provider -> 'anthropic-ratelimit-unified-status') = 'string'`},
	{"workspaces", codexCall + ` AND c.ext ? 'workspaces'`},
	{"sandbox", codexCall + ` AND ` + jsonText(`c.ext -> 'sandbox_mode'`) + ` IS NOT NULL`},
	{"limits", codexCall + ` AND c.provider::text LIKE '%x-codex-%'`},
}

func hasCond(key string) string {
	for _, h := range hasKeys {
		if h.key == key {
			return h.cond
		}
	}
	panic("refresh: no has key " + key)
}

// hasItems is the has keys of the model calls c `where` keeps (keyItems' columns).
func hasItems(where string) string {
	vals := make([]string, len(hasKeys))
	for i, h := range hasKeys {
		vals[i] = `(CASE WHEN ` + h.cond + ` THEN '` + h.key + `' END)`
	}
	return `SELECT c.session_id, ` + at + `, 'has', x.v, 1 FROM calls c, LATERAL (VALUES ` + strings.Join(vals, ", ") + `) AS x(v)
	WHERE ` + where + ` AND ` + modelCall + ` AND x.v IS NOT NULL`
}

// inHour is the session's call c that has key (hasKeys) first by order (a SQL
// ORDER BY over c), among the calls of the newest hour (first: the earliest)
// its keys show one in; slack widens that hour backwards (an order by a time
// other than the call's).
func inHour(key string, first bool, slack int64, order, cols string) string {
	agg := "max"
	if first {
		agg = "min"
	}
	return `(SELECT ` + cols + ` FROM calls c, (SELECT ` + agg + `(hour_ns) AS h FROM session_keys
		WHERE session_id = $1 AND kind = 'has' AND key = '` + key + `') k
	WHERE c.session_id = $1 AND ` + at + ` >= k.h - ` + fmt.Sprint(slack) + ` AND ` + at + ` < k.h + ` + fmt.Sprint(hourNS) + `
	  AND ` + hasCond(key) + ` ORDER BY ` + order + ` LIMIT 1)`
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
	own_title, origin_kind, origin_session_id, origin, repo, commit, prompts, model_calls, fields_missing, person_id, device_id,
	networks, effort, tokens_input, tokens_cache_read, tokens_cache_write, tokens_output,
	outputs_known, tokens_accuracy, tool_calls, has_facts, facts_cwd, facts_approval, facts_sandbox, limits, limits_ns,
	cli_version, cli_version_ns, rl_status, rl_util, rl_overage, rl_ns, unattributed,
	turns, turns_interrupted, active_ns, work, failed_calls, recorded, tools`

// newestDesc is today's "newest call" order: latest time, then the latest id.
const newestDesc = at + ` DESC, replace(c.call_id, '#', '~') COLLATE "C" DESC`

// sessionSelect is session $1's row, when it has a visible model call (the
// API's load_session). A turn is interrupted when one of its steps stopped on
// an interrupt or the next turn's request says it was (prompts.follows_interrupt).
var sessionSelect = `WITH h AS (SELECT coalesce(sum(model_calls), 0) AS model_calls, coalesce(sum(hidden_calls), 0) AS hidden_calls,
		sum(input_tokens) AS input_tokens, sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
		sum(output_tokens) AS output_tokens, coalesce(sum(outputs_known), 0) AS outputs_known, sum(tool_calls) AS tool_calls
	FROM session_hours WHERE session_id = $1),
k AS (SELECT nullif(count(DISTINCT key) FILTER (WHERE kind = 'call_prompt'), 0) AS prompts,
		nullif(count(DISTINCT key) FILTER (WHERE kind = 'ip'), 0) AS networks,
		coalesce(sum(n) FILTER (WHERE kind = 'failed'), 0) AS failed, coalesce(bool_or(kind = 'recorded'), false) AS recorded
	FROM session_keys WHERE session_id = $1)
SELECT $1, fc.at, lc.at, fc.product, cl.client, cv.version, cv.ns, h.hidden_calls,
	` + pyStrip(`ttl.text`) + `,
	CASE WHEN o.kind IS NOT NULL OR o.init = 'cold_resume' THEN coalesce(o.kind, 'resume') END,
	CASE WHEN o.kind IS NOT NULL OR o.init = 'cold_resume' THEN o.parent END,
	CASE WHEN o.kind IS NOT NULL OR o.init = 'cold_resume' THEN jsonb_build_object('kind', coalesce(o.kind, 'resume'),
		'sessionId', o.parent, 'turn', nullif(o.ordinal, 0),
		'init', CASE WHEN o.init IN ('warm_fork', 'cold_fork', 'cold_resume') THEN o.init END, 'agentName', o.agent_name) END,
	CASE WHEN coalesce(ws.cwd, '') <> '' THEN regexp_replace(rtrim(ws.cwd, '/'), '^.*/', '') END,
	CASE WHEN coalesce(ws.cwd, '') <> '' AND jsonb_typeof(cx.ws -> ws.cwd -> 'latest_git_commit_hash') = 'string'
		THEN left(cx.ws -> ws.cwd ->> 'latest_git_commit_hash', 7) END,
	k.prompts, h.model_calls,
	h.model_calls > 0 AND NOT EXISTS (SELECT 1 FROM session_keys WHERE session_id = $1 AND kind = 'has' AND key IN ('account', 'device')),
	ca.account_id, cd.device_id,
	k.networks, ef.effort,
	h.input_tokens, h.cache_read_tokens, h.cache_write_tokens, h.output_tokens, h.outputs_known,
	CASE WHEN h.model_calls > 0 AND h.outputs_known = h.model_calls THEN 'exact' WHEN h.outputs_known = 0 THEN 'missing' ELSE 'estimated' END,
	h.tool_calls,
	ws.cwd IS NOT NULL OR cx.sandbox IS NOT NULL, ws.cwd, NULL, cx.sandbox,
	lim.provider, lim.ns,
	cli.v, cli.ns, rl.status, rl.util, rl.overage, rl.ns,
	h.model_calls > 0 AND NOT acct.any,
	tn.turns, tn.interrupted, tn.active,
	` + workObj(`(SELECT kind, key, n FROM session_keys WHERE session_id = $1 AND starts_with(kind, 'w:'))`) + `,
	k.failed, k.recorded,
	(SELECT coalesce(jsonb_object_agg(name, n), '{}') FROM (SELECT CASE WHEN kind = 'call_mcp'
			THEN 'mcp__' || (key::jsonb ->> 0) || '__' || (key::jsonb ->> 1) ELSE key END AS name, sum(n)::bigint AS n
		FROM session_keys WHERE session_id = $1 AND kind IN ('call_builtin', 'call_mcp') GROUP BY 1) x)
FROM h, k
LEFT JOIN LATERAL (SELECT count(*) AS turns, count(*) FILTER (WHERE interrupted OR next_follows) AS interrupted,
	coalesce(sum(greatest(ended_ns - t_ns, 0)), 0) AS active
	FROM (SELECT interrupted, ended_ns, t_ns,
		coalesce(lead(follows_interrupt) OVER (ORDER BY t_ns, prompt_id COLLATE "C"), false) AS next_follows
		FROM prompts WHERE session_id = $1) p) tn ON true
LEFT JOIN LATERAL ` + newestCall(visible, callOrder, at+` AS at, `+agent+` AS product`) + ` fc ON true
LEFT JOIN LATERAL ` + newestCall(visible, newestDesc, at+` AS at`) + ` lc ON true
LEFT JOIN LATERAL ` + inHour("client", false, 0, newestDesc, `c.client_mode AS client`) + ` cl ON true
LEFT JOIN LATERAL ` + inHour("version", false, 0, newestDesc, `c.client_version AS version, `+at+` AS ns`) + ` cv ON true
LEFT JOIN LATERAL ` + inHour("title", false, 0, newestDesc, `c.detail ->> 'text' AS text`) + ` ttl ON true
LEFT JOIN LATERAL (SELECT EXISTS (SELECT 1 FROM session_accounts WHERE session_id = $1) AS any) acct ON true
LEFT JOIN LATERAL (SELECT ca.account_id FROM ` + inHour("account", true, 0, at+`, c.account_id COLLATE "C"`, `c.account_id`) + ` ca(account_id)
	WHERE acct.any) ca ON true
LEFT JOIN LATERAL ` + inHour("device", true, 0, at+`, c.device_id COLLATE "C"`, `c.device_id`) + ` cd ON true
LEFT JOIN LATERAL ` + inHour("effort", false, 0, at+` DESC, replace(c.call_id, '#', '~') COLLATE "C"`, `c.effort`) + ` ef ON true
LEFT JOIN LATERAL ` + inHour("cli", false, 0,
	at+` DESC, coalesce(substring(c.user_agent from 'claude-cli/([^ ]+)'), '') COLLATE "C" DESC`,
	`coalesce(substring(c.user_agent from 'claude-cli/([^ ]+)'), '') AS v, `+at+` AS ns`) + ` cli ON true
LEFT JOIN LATERAL ` + inHour("ratelimit", false, hourNS,
	`coalesce(c.response_start_ns, `+at+`) DESC, `+newestDesc,
	`c.provider ->> 'anthropic-ratelimit-unified-status' AS status, `+jsonDouble(`c.provider -> 'anthropic-ratelimit-unified-7d-utilization'`)+` AS util,
	`+jsonStrOnly(`c.provider -> 'anthropic-ratelimit-unified-overage-status'`)+` AS overage, coalesce(c.response_start_ns, `+at+`) AS ns`) + ` rl ON true
LEFT JOIN LATERAL (SELECT
	` + inHour("workspaces", false, 0, at+` DESC, c.ext::text COLLATE "C" DESC`, `c.ext -> 'workspaces'`) + ` AS ws,
	nullif(` + inHour("sandbox", false, 0,
	at+` DESC, `+jsonText(`c.ext -> 'sandbox_mode'`)+` COLLATE "C" DESC`, jsonText(`c.ext -> 'sandbox_mode'`)) + `, '') AS sandbox
	WHERE fc.product = 'codex') cx ON true
LEFT JOIN LATERAL (SELECT l.* FROM ` + inHour("limits", false, 0, newestDesc, `c.provider, `+at+` AS ns`) + ` l(provider, ns)
	WHERE fc.product = 'codex') lim ON true
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
WHERE h.model_calls > 0`

// --- the Prompts page's list ---------------------------------------------------------------------

// itemCols are prompt_items' columns.
const itemCols = `session_id, seq, t_ns, type, prompt_id, prompt_index, open_tail, hidden, between_kind, detail, call_id`

// modelFamily is process/requestrows.go modelFamily: opus, sonnet or haiku.
const modelFamily = `CASE WHEN coalesce(c.model, '') = '' THEN '—' ELSE coalesce(substring(c.model from '^claude-(opus|sonnet|haiku)'), c.model) END`

// itemsSelect is session $1's Prompts page list, by time (then type; prompts
// by start and id, the calls by id):
//   - its prompts, with their places (the number), the open tail (the last,
//     while a step of it is open), and the hidden calls it's given (a prewarm
//     the next prompt to start, others the last started before them:
//     _hidden_by_prompt);
//   - each visible call with no prompt.
var itemsSelect = `WITH p AS (SELECT p.*, row_number() OVER (ORDER BY p.t_ns, p.prompt_id COLLATE "C") AS n, count(*) OVER () AS total
	FROM prompts p WHERE p.session_id = $1),
hc AS (SELECT c.hidden_kind AS kind, ` + at + ` AS t FROM calls c WHERE c.session_id = $1 AND c.hidden_kind IS NOT NULL AND ` + modelCall + `),
hp AS (SELECT CASE WHEN hc.kind = 'prewarm' THEN coalesce(a.pid, b.pid) ELSE coalesce(b.pid, a.pid) END AS pid, hc.kind, count(*) AS n
	FROM hc LEFT JOIN LATERAL (SELECT p.prompt_id AS pid FROM p WHERE p.t_ns >= hc.t ORDER BY p.n LIMIT 1) a ON true
		LEFT JOIN LATERAL (SELECT p.prompt_id AS pid FROM p WHERE p.t_ns < hc.t ORDER BY p.n DESC LIMIT 1) b ON true
	GROUP BY 1, 2),
items AS (
	SELECT p.t_ns, 'prompt' AS type, 0 AS grp, p.prompt_id AS ord, p.prompt_id, p.n AS prompt_index,
		p.n = p.total AND p.unfinished_max_ns IS NOT NULL AS open_tail, coalesce(hj.hidden, '{}') AS hidden,
		NULL AS between_kind, NULL AS detail, NULL AS call_id
	FROM p
	LEFT JOIN (SELECT pid, jsonb_object_agg(kind, n) AS hidden FROM hp WHERE pid IS NOT NULL GROUP BY pid) hj ON hj.pid = p.prompt_id
	UNION ALL
	SELECT ` + at + `, 'between', 3, replace(c.call_id, '#', '~'), NULL, NULL, false, NULL, 'call',
		coalesce(nullif(c.request_class, ''), '—') || ' · ' || ` + modelFamily + ` || ' · ' || coalesce(nullif(c.stop_reason, ''), '—'),
		c.call_id
	FROM calls c WHERE c.session_id = $1 AND c.prompt_id IS NULL AND ` + visible + `)
SELECT $1, row_number() OVER (ORDER BY t_ns, type COLLATE "C", grp, ord COLLATE "C"), t_ns, type, prompt_id, prompt_index,
	open_tail, hidden, between_kind, detail, call_id
FROM items`

// sessionRefresh recomputes session $1's row and its Prompts page list.
// Both are replaced: a session's row leaves when its calls have (retention),
// and its list renumbers as prompts arrive.
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
