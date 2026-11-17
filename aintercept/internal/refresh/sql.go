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

// The values of a session's minute (and hour), as aggregates over its calls
// c. Each is one GROUP BY's select list, used by the refresh over one key's
// rows and by the from-scratch check over all of them.
//   - callAggs, over its visible calls: the timeline's model-call lane and the
//     person summary's sums are over session_marks' 'call' marks, one per
//     visible call (sessionmarks.go): its class, tokens (nzi: a zero is NULL,
//     as the facts store it) and context (input + cache read + cache write,
//     NULL when all are 0: promptrows.go context). outputs_known and
//     tool_calls are the session row's (sessionrows.go sessionRow).
//   - hiddenAggs, over its agent calls with a hidden kind: the session row's
//     hidden_calls.
const callAggs = `count(*) AS model_calls,
	count(*) FILTER (WHERE c.request_class = 'main') AS main_calls,
	count(*) FILTER (WHERE c.request_class = 'subagent') AS subagent_calls,
	sum(c.input_tokens) AS input_tokens, sum(c.cache_read_tokens) AS cache_read_tokens,
	sum(c.cache_write_tokens) AS cache_write_tokens, sum(c.output_tokens) AS output_tokens,
	count(c.output_tokens) AS outputs_known,
	sum(nullif(coalesce(c.input_tokens, 0) + coalesce(c.cache_read_tokens, 0) + coalesce(c.cache_write_tokens, 0), 0)) AS context_sum,
	sum(` + toolCount + `) FILTER (WHERE c.tool_uses IS NOT NULL) AS tool_calls`

const hiddenAggs = `count(*) AS hidden_calls`

// requestRows is the Requests tab's rows (stream_sql.py tx_rows): a
// transaction with a credential, listed under its scope session (its own). requests counts them by kind and credential ("<kind>/<key
// hash>"), requests_hidden the hidden ones. The tab lists only the
// credentials of the session's visible calls and of the analytics posts that
// carried its events (process/requestrows.go: callHashes, corrHashes), a set
// that grows as calls arrive, so a read keeps the credentials in it
// (session_credentials, and the calls of its session_keys batches) rather than
// a new credential re-marking the session's earlier minutes.
const requestRows = `c.key_hash IS NOT NULL AND NOT ` + unnamed + ` AND ` + transaction

// minuteRow recomputes session_minutes for one (session $1, minute [$2, $3)):
// the aggregates above and its Requests tab rows by kind.
var minuteRow = `INSERT INTO session_minutes AS m SELECT $1, $2,
	cc.model_calls, cc.main_calls, cc.subagent_calls, ag.hidden_calls,
	cc.input_tokens, cc.cache_read_tokens, cc.cache_write_tokens, cc.output_tokens, cc.outputs_known,
	cc.context_sum, cc.tool_calls,
	coalesce((SELECT jsonb_object_agg(k, n) FROM (SELECT c.kind || '/' || c.key_hash AS k, count(*) AS n FROM calls c
		WHERE c.scope_session_id = $1 AND ` + in + ` AND c.hidden_kind IS NULL AND ` + requestRows + `
		GROUP BY 1) x), '{}'),
	coalesce((SELECT jsonb_object_agg(k, n) FROM (SELECT c.kind || '/' || c.key_hash AS k, count(*) AS n FROM calls c
		WHERE c.scope_session_id = $1 AND ` + in + ` AND c.hidden_kind IS NOT NULL AND ` + requestRows + `
		GROUP BY 1) x), '{}')
FROM (SELECT ` + callAggs + ` FROM calls c WHERE c.session_id = $1 AND ` + in + ` AND ` + visible + `) cc,
	(SELECT ` + hiddenAggs + ` FROM calls c WHERE c.session_id = $1 AND ` + in + ` AND c.hidden_kind IS NOT NULL AND ` + modelCall + `) ag
ON CONFLICT (session_id, minute_ns) DO UPDATE SET ` + setAll(sumColumns) + `, requests = excluded.requests, requests_hidden = excluded.requests_hidden`

// sumColumns are session_minutes' and session_hours' values that an hour sums
// from its minutes (the requests maps are summed per kind).
var sumColumns = []string{
	"model_calls", "main_calls", "subagent_calls", "hidden_calls",
	"input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "outputs_known", "context_sum", "tool_calls",
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
	"context_sum": true, "tool_calls": true,
}

// session_minute_keys' rows of one (session $1, minute [$2, $3)) are each kind
// of thing the session's pages count distinct, with its occurrences.
//   - call_builtin and call_mcp ([server, tool]): the tools its ended calls'
//     tool_use blocks name (session_tools, call_tool_uses).
//   - ip: its visible calls' networks; call_prompt: their prompts (the person
//     summary's prompt count).
//   - has: which newest values its model calls have (sessions.go hasKeys).
//   - offer, offer_main (key: the tool list's hash, tool_lists): the tool lists
//     its visible calls carried, and its main thread's (handoff §11S: offered vs
//     used, and how often the main thread's list changed).
//   - failed: its visible calls that failed (an error status, or none once
//     ended); compaction: its compaction calls; subagent (key: the agent id):
//     its subagents' calls; recorded: its calls that kept their content.
//   - w:…: its steps' work (prompts.go stepItems, their step_keys), the Work
//     lane's and the session's.
//
// keyItems is one row per occurrence: (session, its time, kind, key, n: 1, or
// a work piece's count), over the calls and steps the filters keep.
func keyItems(calls, steps string) string {
	return `SELECT c.session_id AS sid, ` + at + ` AS at, x.k AS kind, x.v AS key, 1::bigint AS n
	FROM calls c, LATERAL (VALUES ('ip', c.ip), ('call_prompt', c.prompt_id)) AS x(k, v)
	WHERE ` + calls + ` AND ` + visible + ` AND x.v IS NOT NULL
	UNION ALL
	SELECT c.session_id, ` + at + `, CASE WHEN u ->> 'kind' = 'mcp' AND u ->> 'server' <> '' AND u ->> 'tool' <> '' THEN 'call_mcp' ELSE 'call_builtin' END,
	       CASE WHEN u ->> 'kind' = 'mcp' AND u ->> 'server' <> '' AND u ->> 'tool' <> ''
	            THEN jsonb_build_array(u ->> 'server', u ->> 'tool')::text ELSE u ->> 'tool' END, 1
	FROM calls c, jsonb_array_elements(CASE jsonb_typeof(c.tool_uses) WHEN 'array' THEN c.tool_uses ELSE '[]' END) u
	WHERE ` + calls + ` AND c.end_ns IS NOT NULL AND jsonb_typeof(u) = 'object' AND u ->> 'tool' <> ''
	UNION ALL
	SELECT c.session_id, ` + at + `, x.k, x.v, 1 FROM calls c, LATERAL (VALUES
		('offer', c.tools_list),
		(CASE WHEN coalesce(c.request_class, 'main') = 'main' THEN 'offer_main' END, c.tools_list),
		(CASE WHEN c.end_ns IS NOT NULL AND (coalesce(c.status, 0) = 0 OR c.status >= 400) THEN 'failed' END, ''),
		(CASE WHEN c.request_class = 'compaction' THEN 'compaction' END, ''),
		(CASE WHEN c.request_class = 'subagent' THEN 'subagent' END, nullif(c.agent_id, '')),
		(CASE WHEN c.detail IS NOT NULL THEN 'recorded' END, '')) AS x(k, v)
	WHERE ` + calls + ` AND ` + visible + ` AND x.k IS NOT NULL AND x.v IS NOT NULL
	UNION ALL
	` + hasItems(calls) + `
	UNION ALL
	SELECT s.session_id, s.t_ns, k.kind, k.key, k.n FROM steps s JOIN step_keys k USING (call_id)
	WHERE ` + steps + ` AND starts_with(k.kind, 'w:')`
}

// minuteKeys recomputes session_minute_keys for one (session $1, minute
// [$2, $3)) from the minute's calls and steps: a thing the minute no longer
// holds leaves, so the minute's rows are replaced.
var minuteKeys = []string{
	`DELETE FROM session_minute_keys WHERE session_id = $1 AND minute_ns = $2`,
	`INSERT INTO session_minute_keys (session_id, minute_ns, kind, key, n) SELECT $1, $2, kind, key, sum(n) FROM (` +
		keyItems("c.session_id = $1 AND "+in, "s.session_id = $1 AND s.t_ns >= $2 AND s.t_ns < $3") + `) k GROUP BY kind, key`,
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
