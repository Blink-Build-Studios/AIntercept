package refresh

import (
	"fmt"
	"strings"
)

// The SQL each rollup row is recomputed with (docs/PIPELINE.md §5.2), over one
// dirty key's facts. Every value means what today's code means by it; each
// predicate and value below names the code it ports.

const (
	minuteNS = int64(60e9)
	hourNS   = 60 * minuteNS
)

// unnamed is whether call c names no session today though it has one here:
// a Codex call that never really ended. Today's row of a call still waiting
// is read from its headers alone, and Codex names its thread only in what
// ingest adds at the end; one the sweep closed is the same row.
const unnamed = `(c.source = 'codex' AND (c.end_ns IS NULL OR c.swept))`

// modelCall is the API's agent_calls view over calls c: a coding agent's
// model call with a session (process/requestrows.go agentCall,
// canon.IsModelCall). A call with no product to Anthropic's API is Claude
// Code's, as callFromRequest has it.
const modelCall = `(NOT c.synthetic AND c.session_id IS NOT NULL AND NOT ` + unnamed + ` AND (
	(coalesce(c.source, 'claude_code') = 'claude_code' AND c.host = 'api.anthropic.com'
	 AND starts_with(c.path, '/v1/messages') AND NOT starts_with(c.path, '/v1/messages/count_tokens'))
	OR (c.source = 'codex' AND c.host = 'chatgpt.com' AND c.path = '/backend-api/codex/responses'
	 AND (c.ws_turn IS NOT NULL OR c.method = 'POST'))))`

// visible is the API's cc_calls: modelCall with no hidden kind (ccCall).
const visible = `(c.hidden_kind IS NULL AND ` + modelCall + `)`

// agent is a call's coding agent (its product, Claude Code's for a call to
// Anthropic's API with none; render/rules.go agentCall).
const agent = `(CASE WHEN c.source IS NULL AND c.host = 'api.anthropic.com' THEN 'claude_code' ELSE c.source END)`

// agentCall is the overview's agent_calls table (render/rules.go agentCall): every call
// of Claude Code or Codex that isn't a probe.
const agentCall = `(NOT c.synthetic AND ` + agent + ` IN ('claude_code', 'codex'))`

// transaction is one the request log lists (process/requestrows.go
// transaction): a coding agent's, not a probe, and a WebSocket's turns rather
// than its connection.
const transaction = `(` + agentCall + ` AND NOT (c.ws_turn IS NULL AND c.method = 'GET' AND c.path = '/backend-api/codex/responses'))`

// at is call c's time: its start, else its end (callFromRequest's ts).
const at = `coalesce(c.start_ns, c.end_ns)`

// in is whether call c's time (its start, else its end: callFromRequest's ts)
// is in [$2, $3), found through an index on (<col>, start_ns).
const in = `(c.start_ns >= $2 AND c.start_ns < $3 OR c.start_ns IS NULL AND c.end_ns >= $2 AND c.end_ns < $3)`

// jsonText is json_extract_string (process/requestrows.go jsonString): a
// string's value, another scalar's text, NULL for null or absent.
func jsonText(v string) string { return "(" + v + " #>> '{}')" }

// numeric is the regular expression a string must match for TRY_CAST to read it as a number.
const numeric = `'^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$'`

// jsonDouble is TRY_CAST(json_extract_string(v) AS DOUBLE) (process/requestrows.go jsonDouble).
func jsonDouble(v string) string {
	return fmt.Sprintf(`(CASE jsonb_typeof(%[1]s) WHEN 'number' THEN (%[1]s #>> '{}')::float8
		WHEN 'string' THEN CASE WHEN btrim(%[1]s #>> '{}') ~ %[2]s THEN btrim(%[1]s #>> '{}')::float8 END END)`, v, numeric)
}

// jsonInt is TRY_CAST(json_extract_string(v) AS BIGINT), a decimal rounded half
// away from zero (process/requestrows.go jsonInt).
func jsonInt(v string) string {
	return fmt.Sprintf(`(CASE jsonb_typeof(%[1]s) WHEN 'number' THEN round((%[1]s #>> '{}')::numeric)::bigint
		WHEN 'string' THEN CASE WHEN btrim(%[1]s #>> '{}') ~ %[2]s THEN round(btrim(%[1]s #>> '{}')::numeric)::bigint END END)`, v, numeric)
}

// pyTruthy is Python's bool(v) for a JSON value (process/promptrows.go pyTruthy).
func pyTruthy(v string) string {
	return fmt.Sprintf(`coalesce(CASE jsonb_typeof(%[1]s) WHEN 'string' THEN %[1]s #>> '{}' <> '' WHEN 'boolean' THEN %[1]s = 'true'
		WHEN 'number' THEN (%[1]s #>> '{}')::numeric <> 0 WHEN 'array' THEN jsonb_array_length(%[1]s) > 0
		WHEN 'object' THEN %[1]s <> '{}' ELSE false END, false)`, v)
}

// pyStr is session.py _str (process/promptrows.go pyStr): NULL for null, an
// object or a list; else its text.
func pyStr(v string) string {
	return fmt.Sprintf(`(CASE WHEN jsonb_typeof(%[1]s) IN ('string', 'number', 'boolean') THEN %[1]s #>> '{}' END)`, v)
}

// toolCount is a call's tool calls: rows.tool_calls' counts summed, int(count
// or 1) per tool (process/sessionrows.go toolCount).
const toolCount = `(SELECT coalesce(sum(CASE jsonb_typeof(u -> 'count')
		WHEN 'number' THEN CASE WHEN (u ->> 'count')::numeric = 0 THEN 1 ELSE trunc((u ->> 'count')::numeric)::bigint END
		WHEN 'string' THEN CASE WHEN btrim(u ->> 'count') ~ '^[+-]?[0-9]+$' THEN btrim(u ->> 'count')::bigint ELSE 1 END
		ELSE 1 END), 0)
	FROM jsonb_array_elements(CASE jsonb_typeof(c.tool_uses) WHEN 'array' THEN c.tool_uses ELSE '[]' END) u
	WHERE jsonb_typeof(u) = 'object')`

// report is whether event r is a Claude Code report of a model call
// (tengu_api_success: process/requestrows.go eventAgg.add, d.reports).
const report = `(r.source = 'claude_code' AND r.name = 'tengu_api_success')`

// md is event e's metadata field k.
func md(k string) string { return "(e.metadata -> '" + k + "')" }

// missedWhere is whether report r is missed: no visible model call of its
// session has its request id, nor its client request id
// (process/requestrows.go missedRows; the timeline's not-captured marks,
// sessionmarks.go).
const missedWhere = report + `
	AND NOT EXISTS (SELECT 1 FROM calls c WHERE r.request_id IS NOT NULL AND c.request_id = r.request_id
	                AND c.session_id = r.session_id AND ` + visible + `)
	AND NOT EXISTS (SELECT 1 FROM calls c WHERE r.client_request_id IS NOT NULL AND c.client_request_id = r.client_request_id
	                AND c.session_id = r.session_id AND ` + visible + `)`

// minuteMissed recomputes missed_reports for one (session $1, minute [$2,
// $3)): the minute's missed reports. A report that's captured now leaves, so
// the minute's rows are replaced.
var minuteMissed = []string{
	`DELETE FROM missed_reports WHERE session_id = $1 AND minute_ns = $2`,
	`INSERT INTO missed_reports (event_key, session_id, minute_ns, ts_ns, request_id, client_request_id)
	SELECT r.event_key, r.session_id, $2, r.ts_ns, r.request_id, r.client_request_id
	FROM events r WHERE r.session_id = $1 AND r.ts_ns >= $2 AND r.ts_ns < $3 AND ` + missedWhere,
}

// The values of a session's minute (and hour), as aggregates over its calls c
// and events e. Each is one GROUP BY's select list, used by the refresh over
// one key's rows and by the from-scratch check over all of them.
//   - callAggs, over its visible calls: the timeline's model-call lane and the
//     person summary's sums are over session_marks' 'call' marks, one per
//     visible call (sessionmarks.go): its class, tokens (nzi: a zero is NULL,
//     as the facts store it), context (input + cache read + cache write, NULL
//     when all are 0: promptrows.go context) and whether a report of the
//     session names it (Unreported). outputs_known and tool_calls are the
//     session row's (sessionrows.go sessionRow).
//   - hiddenAggs, over its agent calls with a hidden kind: the session row's
//     hidden_calls.
//   - eventAggs, over its Claude Code events (eventrows.go event_rows): events,
//     those not internal (session.py is_internal, stored at insert), friction
//     (sessionrows.go sessEvents.add: negative prompts, cancels, destructive
//     commands), queries and bypassed (the same), reports, and what the person
//     summary sums (pages.py _EVENT_TOTALS over personmap.go eventSums), and
//     the reports Claude Code sent past a custom CA (personmap.go eventSums
//     gzipCA: the session row's custom_ca).
const callAggs = `count(*) AS model_calls,
	count(*) FILTER (WHERE c.request_class = 'main') AS main_calls,
	count(*) FILTER (WHERE c.request_class = 'subagent') AS subagent_calls,
	count(*) FILTER (WHERE
		NOT EXISTS (SELECT 1 FROM events r WHERE c.request_id IS NOT NULL AND r.request_id = c.request_id
		            AND r.session_id = c.session_id AND ` + report + `)
		AND NOT EXISTS (SELECT 1 FROM events r WHERE c.client_request_id IS NOT NULL AND r.client_request_id = c.client_request_id
		                AND r.session_id = c.session_id AND ` + report + `)) AS unreported,
	sum(c.input_tokens) AS input_tokens, sum(c.cache_read_tokens) AS cache_read_tokens,
	sum(c.cache_write_tokens) AS cache_write_tokens, sum(c.output_tokens) AS output_tokens,
	count(c.output_tokens) AS outputs_known,
	sum(nullif(coalesce(c.input_tokens, 0) + coalesce(c.cache_read_tokens, 0) + coalesce(c.cache_write_tokens, 0), 0)) AS context_sum,
	sum(` + toolCount + `) FILTER (WHERE c.tool_uses IS NOT NULL) AS tool_calls`

const hiddenAggs = `count(*) AS hidden_calls`

var eventAggs = `count(*) AS events,
	count(*) FILTER (WHERE NOT e.internal) AS events_shown,
	count(*) FILTER (WHERE e.name = 'tengu_input_prompt' AND ` + jsonText(md("is_negative")) + ` = 'true') AS frustrated,
	count(*) FILTER (WHERE e.name = 'tengu_cancel') AS interrupted,
	count(*) FILTER (WHERE e.name = 'tengu_bash_tool_command_executed'
		AND coalesce(` + jsonText(md("destructive_category")) + `, '') NOT IN ('', 'none')) AS destructive,
	count(*) FILTER (WHERE e.name = 'tengu_bash_tool_command_executed'
		AND ` + jsonText(md("destructive_target_scope")) + ` = 'outside_cwd') AS outside_cwd,
	count(*) FILTER (WHERE e.name = 'tengu_api_query' AND coalesce(` + jsonText(md("permissionMode")) + `, '') <> '') AS queries,
	count(*) FILTER (WHERE e.name = 'tengu_api_query' AND ` + jsonText(md("permissionMode")) + ` = 'bypassPermissions') AS bypassed,
	count(*) FILTER (WHERE e.name = 'tengu_api_success') AS reported,
	sum(` + jsonDouble(md("preToolHookDurationMs")) + `) FILTER (WHERE e.name = 'tengu_tool_use_success') AS hook_ms_sum,
	count(` + jsonDouble(md("preToolHookDurationMs")) + `) FILTER (WHERE e.name = 'tengu_tool_use_success') AS hook_ms_n,
	sum(` + jsonInt(md("cache_creation_5m_input_tokens")) + `) FILTER (WHERE e.name = 'tengu_api_success') AS cache_5m,
	sum(` + jsonInt(md("cache_creation_1h_input_tokens")) + `) FILTER (WHERE e.name = 'tengu_api_success') AS cache_1h,
	sum(` + jsonDouble(md("costUSD")) + `) FILTER (WHERE e.name = 'tengu_api_success') AS cost_usd,
	count(*) FILTER (WHERE e.name = 'tengu_api_success' AND ` + jsonText(md("gzipSkipReason")) + ` = 'custom_ca') AS custom_ca`

// requestRows is the Requests tab's rows (stream_sql.py tx_rows): a
// transaction with a credential, listed under its scope session (its own, else
// its batch's). requests counts them by kind and credential ("<kind>/<key
// hash>"), requests_hidden the hidden ones. The tab lists only the
// credentials of the session's visible calls and of the analytics posts that
// carried its events (process/requestrows.go: callHashes, corrHashes), a set
// that grows as calls arrive, so a read keeps the credentials in it
// (session_credentials, and the calls of its session_keys batches) rather than
// a new credential re-marking the session's earlier minutes.
const requestRows = `c.key_hash IS NOT NULL AND NOT ` + unnamed + ` AND ` + transaction

// minuteRow recomputes session_minutes for one (session $1, minute [$2, $3)),
// after its missed_reports: the aggregates above, the minute's missed reports
// and its Requests tab rows by kind.
var minuteRow = `INSERT INTO session_minutes AS m SELECT $1, $2,
	cc.model_calls, cc.main_calls, cc.subagent_calls, ag.hidden_calls, cc.unreported,
	cc.input_tokens, cc.cache_read_tokens, cc.cache_write_tokens, cc.output_tokens, cc.outputs_known,
	cc.context_sum, cc.tool_calls,
	ev.events, ev.events_shown, ev.frustrated, ev.interrupted, ev.destructive, ev.outside_cwd,
	ev.queries, ev.bypassed, ev.reported, (SELECT count(*) FROM missed_reports WHERE session_id = $1 AND minute_ns = $2),
	ev.hook_ms_sum, ev.hook_ms_n, ev.cache_5m, ev.cache_1h, ev.cost_usd, ev.custom_ca,
	coalesce((SELECT jsonb_object_agg(k, n) FROM (SELECT c.kind || '/' || c.key_hash AS k, count(*) AS n FROM calls c
		WHERE c.scope_session_id = $1 AND ` + in + ` AND c.hidden_kind IS NULL AND ` + requestRows + `
		GROUP BY 1) x), '{}'),
	coalesce((SELECT jsonb_object_agg(k, n) FROM (SELECT c.kind || '/' || c.key_hash AS k, count(*) AS n FROM calls c
		WHERE c.scope_session_id = $1 AND ` + in + ` AND c.hidden_kind IS NOT NULL AND ` + requestRows + `
		GROUP BY 1) x), '{}')
FROM (SELECT ` + callAggs + ` FROM calls c WHERE c.session_id = $1 AND ` + in + ` AND ` + visible + `) cc,
	(SELECT ` + hiddenAggs + ` FROM calls c WHERE c.session_id = $1 AND ` + in + ` AND c.hidden_kind IS NOT NULL AND ` + modelCall + `) ag,
	(SELECT ` + eventAggs + ` FROM events e WHERE e.session_id = $1 AND e.ts_ns >= $2 AND e.ts_ns < $3 AND e.source = 'claude_code') ev
ON CONFLICT (session_id, minute_ns) DO UPDATE SET ` + setAll(sumColumns) + `, requests = excluded.requests, requests_hidden = excluded.requests_hidden`

// sumColumns are session_minutes' and session_hours' values that an hour sums
// from its minutes (the requests maps are summed per kind).
var sumColumns = []string{
	"model_calls", "main_calls", "subagent_calls", "hidden_calls", "unreported",
	"input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "outputs_known", "context_sum", "tool_calls",
	"events", "events_shown", "frustrated", "interrupted", "destructive", "outside_cwd", "queries", "bypassed",
	"reported", "missed", "hook_ms_sum", "hook_ms_n", "cache_5m", "cache_1h", "cost_usd", "custom_ca",
}

func setAll(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c + " = excluded." + c
	}
	return strings.Join(out, ", ")
}

// sumMap is a jsonb {kind: n} map summed per kind over session_minutes m in
// the hour [$2, $3) of session $1 ('{}' for none).
func sumMap(col string) string {
	return `coalesce((SELECT jsonb_object_agg(k, n) FROM (SELECT j.k, sum(j.v::bigint) AS n
		FROM session_minutes m, jsonb_each_text(m.` + col + `) AS j(k, v)
		WHERE m.session_id = $1 AND m.minute_ns >= $2 AND m.minute_ns < $3 GROUP BY j.k) x), '{}')`
}

// hourRow recomputes session_hours for one (session $1, hour [$2, $3)): the
// sums of its 60 minute rows. The hour's row is kept with zeros when its
// minutes have gone, like a minute's.
var hourRow = func() string {
	sums := make([]string, len(sumColumns))
	for i, c := range sumColumns {
		sums[i] = "sum(" + c + ")"
		if !nullable[c] {
			sums[i] = "coalesce(sum(" + c + "), 0)"
		}
	}
	return `INSERT INTO session_hours SELECT $1, $2, ` + strings.Join(sums, ", ") + `, ` + sumMap("requests") + `, ` + sumMap("requests_hidden") + `
	FROM session_minutes WHERE session_id = $1 AND minute_ns >= $2 AND minute_ns < $3
	ON CONFLICT (session_id, hour_ns) DO UPDATE SET ` + setAll(sumColumns) + `, requests = excluded.requests, requests_hidden = excluded.requests_hidden`
}()

// nullable are the sums that are NULL when nothing had a value.
var nullable = map[string]bool{
	"input_tokens": true, "cache_read_tokens": true, "cache_write_tokens": true, "output_tokens": true,
	"context_sum": true, "tool_calls": true, "hook_ms_sum": true, "cache_5m": true, "cache_1h": true, "cost_usd": true,
}

// session_minute_keys' rows of one (session $1, minute [$2, $3)) are each kind
// of thing the session's pages count distinct, with its occurrences.
//   - name, tool, mcp_tool, word, destructive, git, pr: the person summary's
//     counted dimensions (pages.py _EVENT_DIMENSIONS over personmap.go
//     eventSums); hook (hookName) and head (head_sha), its distinct counts; batch
//     (an event's analytics post), its batch count.
//   - event_builtin and event_mcp ([server, tool]): the tools a session's
//     tengu_tool_use_success events name (eventrows.go, session_tools); call_builtin
//     and call_mcp, those its ended calls' tool_use blocks name (session_tools,
//     call_tool_uses).
//   - ip: its visible calls' networks; call_prompt: their prompts (the person
//     summary's prompt count); input_prompt: its typed prompts' ids, or
//     input:<time> for one with none (sessionrows.go prompts).
//
// keyItems is one row per occurrence: (session, its time, kind, key), over the
// events and calls the filters keep.
func keyItems(events, calls string) string {
	return `SELECT e.session_id AS sid, e.ts_ns AS at, x.k AS kind, x.v AS key FROM events e
	LEFT JOIN LATERAL (SELECT ` + pyTruthy(md("toolName")) + ` AS used,
		` + jsonText(md("toolName")) + ` AS tool, coalesce(` + md("isMcp") + ` #>> '{}' = 'true', false) AS mcp,
		coalesce(nullif(` + pyStr(md("mcpServerName")) + `, ''),
			CASE WHEN starts_with(` + jsonText(md("toolName")) + `, 'mcp__') AND cardinality(string_to_array(` + jsonText(md("toolName")) + `, '__')) >= 3
			     THEN split_part(` + jsonText(md("toolName")) + `, '__', 2) END, '') AS server
		WHERE e.name = 'tengu_tool_use_success') t ON true
	CROSS JOIN LATERAL (VALUES
		('name', e.name),
		('head', e.head_sha),
		('batch', e.batch_call_id),
		(CASE WHEN t.mcp THEN 'mcp_tool' ELSE 'tool' END, t.tool),
		('word', CASE WHEN e.name = 'tengu_bash_tool_command_executed' THEN ` + jsonText(md("bash_argv0")) + ` END),
		('destructive', CASE WHEN e.name = 'tengu_bash_tool_command_executed' AND ` + jsonText(md("destructive_category")) + ` <> 'none'
		 THEN ` + jsonText(md("destructive_category")) + ` END),
		('git', CASE WHEN e.name = 'tengu_git_operation' THEN ` + jsonText(md("operation")) + ` END),
		('pr', CASE WHEN e.name = 'tengu_session_linked_to_pr' THEN ` + jsonText(md("prNumber")) + ` END),
		('hook', CASE WHEN e.name = 'tengu_run_hook' THEN ` + jsonText(md("hookName")) + ` END),
		('input_prompt', CASE WHEN e.name = 'tengu_input_prompt' THEN coalesce(e.prompt_id, 'input:' || e.ts_ns) END),
		(CASE WHEN t.server <> '' THEN 'event_mcp' ELSE 'event_builtin' END,
		 CASE WHEN t.used AND t.server <> '' THEN jsonb_build_array(t.server, t.tool)::text
		      WHEN t.used AND NOT t.mcp THEN t.tool END)
	) AS x(k, v)
	WHERE ` + events + ` AND e.source = 'claude_code' AND x.v IS NOT NULL
	UNION ALL
	SELECT c.session_id, ` + at + `, x.k, x.v FROM calls c, LATERAL (VALUES ('ip', c.ip), ('call_prompt', c.prompt_id)) AS x(k, v)
	WHERE ` + calls + ` AND ` + visible + ` AND x.v IS NOT NULL
	UNION ALL
	SELECT c.session_id, ` + at + `, CASE WHEN u ->> 'kind' = 'mcp' AND u ->> 'server' <> '' AND u ->> 'tool' <> '' THEN 'call_mcp' ELSE 'call_builtin' END,
	       CASE WHEN u ->> 'kind' = 'mcp' AND u ->> 'server' <> '' AND u ->> 'tool' <> ''
	            THEN jsonb_build_array(u ->> 'server', u ->> 'tool')::text ELSE u ->> 'tool' END
	FROM calls c, jsonb_array_elements(CASE jsonb_typeof(c.tool_uses) WHEN 'array' THEN c.tool_uses ELSE '[]' END) u
	WHERE ` + calls + ` AND c.end_ns IS NOT NULL AND jsonb_typeof(u) = 'object' AND u ->> 'tool' <> ''`
}

// minuteKeys recomputes session_minute_keys for one (session $1, minute
// [$2, $3)) from the minute's events and calls: a thing the minute no longer
// holds leaves, so the minute's rows are replaced.
var minuteKeys = []string{
	`DELETE FROM session_minute_keys WHERE session_id = $1 AND minute_ns = $2`,
	`INSERT INTO session_minute_keys (session_id, minute_ns, kind, key, n) SELECT $1, $2, kind, key, count(*) FROM (` +
		keyItems("e.session_id = $1 AND e.ts_ns >= $2 AND e.ts_ns < $3", "c.session_id = $1 AND "+in) + `) k GROUP BY kind, key`,
}

// hourKeys recomputes session_keys for one (session $1, hour [$2, $3)): the
// sums of its 60 minutes' key rows, replaced as the minutes' are.
var hourKeys = []string{
	`DELETE FROM session_keys WHERE session_id = $1 AND hour_ns = $2`,
	`INSERT INTO session_keys (session_id, hour_ns, kind, key, n) SELECT $1, $2, kind, key, sum(n) FROM session_minute_keys
	WHERE session_id = $1 AND minute_ns >= $2 AND minute_ns < $3 GROUP BY kind, key`,
}

// credMinute recomputes cred_minutes for one (credential $1, minute [$2, $3)):
// its agent calls (render/rules.go agentCall: the overview's request count) by
// kind, hidden and agent, and how many are the request log's transactions.
// A group can empty (a call's end can hide it), so the minute's rows are replaced.
var credMinute = []string{
	`DELETE FROM cred_minutes WHERE key_hash = $1 AND minute_ns = $2`,
	`INSERT INTO cred_minutes (key_hash, minute_ns, kind, hidden, agent, n, n_tx)
	SELECT $1, $2, c.kind, c.hidden_kind IS NOT NULL, ` + agent + `, count(*), count(*) FILTER (WHERE ` + transaction + `)
	FROM calls c
	WHERE (c.key_hash = $1 OR $1 = '' AND c.key_hash IS NULL) AND ` + in + ` AND ` + agentCall + `
	GROUP BY 3, 4, 5`,
}

// credHour recomputes cred_hours for one (credential $1, hour [$2, $3)): the
// sums of its minutes.
var credHour = []string{
	`DELETE FROM cred_hours WHERE key_hash = $1 AND hour_ns = $2`,
	`INSERT INTO cred_hours (key_hash, hour_ns, kind, hidden, agent, n, n_tx)
	SELECT $1, $2, kind, hidden, agent, sum(n), sum(n_tx) FROM cred_minutes
	WHERE key_hash = $1 AND minute_ns >= $2 AND minute_ns < $3 GROUP BY kind, hidden, agent`,
}
