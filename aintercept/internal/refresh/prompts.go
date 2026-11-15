package refresh

import (
	"fmt"
	"regexp"
	"strings"
)

// The prompt and step rollups (docs/PIPELINE.md §5.2): one prompt's rows,
// recomputed from its visible model calls (by prompt_id) and its events (by
// prompt_id, and its calls' request ids). They mean what today's prompt rows
// mean (process/promptrows.go rows and step, session.py items and _step),
// except what depends on the session's other prompts, which prompt_items
// holds (sessions.go). A refresh's key is the prompt id, or input:<batch>:<n>
// for a tengu_input_prompt with none (promptrows.go: "input:" + its event id).

// as is a predicate over calls c written for another alias.
func as(alias, pred string) string {
	return regexp.MustCompile(`\bc\.`).ReplaceAllString(pred, alias+".")
}

// realEnd is a call's end as today's row has it: none for one still open or
// one the sweep closed (stream/state/expire.go todaysRow: the start alone).
const realEnd = `(CASE WHEN c.swept THEN NULL ELSE c.end_ns END)`

// callOrder is today's order of a session's calls: time, then id with '#'
// written '~' (process/requestrows.go callFromRequest: the row's request id).
const callOrder = at + `, replace(c.call_id, '#', '~') COLLATE "C"`

// evOrder is today's order of a session's events: time, batch, place in the
// batch (promptrows.go promptRows: ts, corr, n; batch_cc_n is n).
func evOrder(e string) string {
	return e + `.ts_ns, ` + e + `.batch_call_id COLLATE "C", ` + e + `.batch_cc_n`
}

// jsonTrunc is session.py _int (promptrows.go pyIntOf): int(float(v)), toward
// zero, for a number or a numeric string; NULL otherwise (or out of range).
func jsonTrunc(v string) string {
	return fmt.Sprintf(`(CASE jsonb_typeof(%[1]s)
		WHEN 'number' THEN CASE WHEN abs((%[1]s #>> '{}')::numeric) < 9e18 THEN trunc((%[1]s #>> '{}')::numeric)::bigint END
		WHEN 'string' THEN CASE WHEN btrim(%[1]s #>> '{}') ~ %[2]s AND abs(btrim(%[1]s #>> '{}')::numeric) < 9e18
		                        THEN trunc(btrim(%[1]s #>> '{}')::numeric)::bigint END END)`, v, numeric)
}

// jsonNum is agents._num (promptrows.go pyNum): a JSON number only.
func jsonNum(v string) string {
	return fmt.Sprintf(`(CASE WHEN jsonb_typeof(%[1]s) = 'number' THEN (%[1]s #>> '{}')::float8 END)`, v)
}

// jsonStrOnly is a JSON string's value (promptrows.go jsonStr); NULL otherwise.
func jsonStrOnly(v string) string {
	return fmt.Sprintf(`(CASE WHEN jsonb_typeof(%[1]s) = 'string' THEN %[1]s #>> '{}' END)`, v)
}

// pyTrue is session.py _true (promptrows.go pyTrue): true or "true".
func pyTrue(v string) string { return `coalesce(` + v + ` IN ('true'::jsonb, '"true"'::jsonb), false)` }

// emd is event x's metadata field k.
func emd(x, k string) string { return "(" + x + ".metadata -> '" + k + "')" }

// --- a step --------------------------------------------------------------------------------------

// fields is jsonb_to_record over a metadata object, each field as jsonb: one
// read of the (often compressed) object for all the fields a row needs.
func fields(md, alias string, ks ...string) string {
	cols := make([]string, len(ks))
	for i, k := range ks {
		cols[i] = `"` + k + `" jsonb`
	}
	return `jsonb_to_record(CASE jsonb_typeof(` + md + `) WHEN 'object' THEN ` + md + ` ELSE '{}' END) AS ` + alias + `(` + strings.Join(cols, ", ") + `)`
}

// f is field k of a fields record.
func f(alias, k string) string { return alias + `."` + k + `"` }

// tf is a tool event's metadata field k, as a column of the tool rows.
func tf(k string) string { return f("t", k) }

// toolObj is the API's ToolCallData of tool event t, its metadata tm
// (promptrows.go joins.tool), with its Bash event b (the session's last
// tengu_bash_tool_command_executed with its tool use id).
var toolObj = `jsonb_build_object(
	'toolUseId', ` + pyStr(tf("toolUseID")) + `,
	'name', coalesce(nullif(` + pyStr(tf("toolName")) + `, ''), '—'),
	'mcpServer', CASE WHEN coalesce(` + pyStr(tf("mcpServerName")) + `, '') <> '' THEN ` + pyStr(tf("mcpServerName")) + `
		WHEN starts_with(` + pyStr(tf("toolName")) + `, 'mcp__') AND cardinality(string_to_array(` + pyStr(tf("toolName")) + `, '__')) >= 3
		THEN split_part(` + pyStr(tf("toolName")) + `, '__', 2) END,
	'target', NULL,
	'status', CASE t.name WHEN 'tengu_tool_use_success' THEN 'ok' WHEN 'tengu_tool_use_progress' THEN 'running' ELSE 'error' END,
	'durationMs', ` + jsonDouble(tf("durationMs")) + `,
	'exitCode', b.exit_code, 'command', b.argv0,
	'resultBytes', ` + jsonTrunc(tf("toolResultSizeBytes")) + `,
	'destructive', CASE WHEN b.category IS NOT NULL THEN jsonb_build_object('category', b.category, 'scope', b.scope) END)`

// toolFields is a tool event t's metadata fields the tools read.
var toolFields = fields("t.metadata", "tm", "toolUseID", "toolName", "mcpServerName", "durationMs", "toolResultSizeBytes")

// bashOf is tool event t's Bash event (promptrows.go joins: bash, by
// str(tool_use_id) when truthy; the last in the session's order).
var bashOf = `LEFT JOIN LATERAL (SELECT ` + pyStr(f("xm", "bash_argv0")) + ` AS argv0, ` + jsonTrunc(f("xm", "exit_code")) + ` AS exit_code,
		CASE WHEN ` + pyStr(f("xm", "destructive_category")) + ` NOT IN ('none', '') THEN ` + pyStr(f("xm", "destructive_category")) + ` END AS category,
		` + pyStr(f("xm", "destructive_target_scope")) + ` AS scope
	FROM (SELECT x.metadata FROM events x
		WHERE coalesce(` + pyStr(tf("toolUseID")) + `, '') <> '' AND x.tool_use_id = ` + pyStr(tf("toolUseID")) + `
		  AND x.session_id = t.session_id AND x.source = 'claude_code' AND x.name = 'tengu_bash_tool_command_executed'
		  AND ` + pyTruthy(emd("x", "tool_use_id")) + ` AND ` + pyStr(emd("x", "tool_use_id")) + ` = ` + pyStr(tf("toolUseID")) + `
		ORDER BY ` + strings.ReplaceAll(evOrder("x"), ",", " DESC,") + ` DESC LIMIT 1) x,
		` + fields("x.metadata", "xm", "bash_argv0", "exit_code", "destructive_category", "destructive_target_scope") + `) b ON true`

// toolKey is a tool event's identity among a step's tools (promptrows.go
// toolsOf: _str(toolUseID) or _str(toolName)).
var toolKey = `CASE WHEN coalesce(` + pyStr(f("tm", "toolUseID")) + `, '') <> '' THEN 's' || ` + pyStr(f("tm", "toolUseID")) + `
	WHEN ` + pyStr(f("tm", "toolName")) + ` IS NOT NULL THEN 's' || ` + pyStr(f("tm", "toolName")) + ` ELSE 'n' END`

// stepTools is call c's tools (promptrows.go toolsOf): its request id's
// finished tool events in the session's order, and those plus the tools its
// progress ticks show still running (the first tick of each tool not
// finished). A tick that isn't one of those is dropped before its tool and
// Bash event are read.
var stepTools = `LEFT JOIN LATERAL (SELECT coalesce(jsonb_agg(obj ORDER BY ts_ns, b, n) FILTER (WHERE NOT tick), '[]') AS tools,
		coalesce(jsonb_agg(obj ORDER BY tick, ts_ns, b, n), '[]') AS live
	FROM (SELECT t.*, ` + toolObj + ` AS obj FROM (
		SELECT e.* FROM (SELECT e.*, row_number() OVER (PARTITION BY k, tick ORDER BY ts_ns, b, n) = 1 AS first,
				bool_or(NOT tick) OVER (PARTITION BY k) AS done
			FROM (SELECT t.name = 'tengu_tool_use_progress' AS tick, t.name, t.session_id, ` + toolKey + ` AS k, tm.*,
					t.ts_ns, t.batch_call_id COLLATE "C" AS b, t.batch_cc_n AS n
				FROM events t CROSS JOIN LATERAL ` + toolFields + `
				WHERE c.request_id IS NOT NULL AND t.request_id = c.request_id AND t.session_id = c.session_id AND t.source = 'claude_code'
				  AND t.name IN ('tengu_tool_use_success', 'tengu_tool_use_error', 'tengu_tool_use_progress')) e) e
		WHERE NOT tick OR first AND NOT done) t ` + bashOf + `) x) tl ON true`

// stepReport is call c's tengu_api_success (promptrows.go joins.success): the
// session's first naming its request id (as its request or client request
// id), else its client request id. Found by the request id indexes: the
// session is compared with IS NOT DISTINCT FROM so it isn't an index condition
// (a (session, name) scan per step costs more than the few ids' rows).
var stepReport = `LEFT JOIN LATERAL (SELECT true AS found, ` + jsonTrunc(f("rm", "cache_creation_5m_input_tokens")) + ` AS cache5m,
		` + jsonTrunc(f("rm", "cache_creation_1h_input_tokens")) + ` AS cache1h,
		` + jsonDouble(f("rm", "ttftMs")) + ` AS ttft, ` + jsonDouble(f("rm", "costUSD")) + ` AS cost
	FROM (SELECT r.metadata FROM events r
		WHERE r.session_id IS NOT DISTINCT FROM c.session_id AND r.source = 'claude_code' AND r.name = 'tengu_api_success'
		  AND (r.request_id IN (c.request_id, c.client_request_id) OR r.client_request_id IN (c.request_id, c.client_request_id))
		ORDER BY (c.request_id IN (r.request_id, r.client_request_id)) IS TRUE DESC, ` + evOrder("r") + ` LIMIT 1) r,
		` + fields("r.metadata", "rm", "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens", "ttftMs", "costUSD") + `) rep ON true`

// detailCalls is call c's recorded tool calls (promptrows.go readDetail: calls).
const detailCalls = `(CASE jsonb_typeof(c.detail -> 'calls') WHEN 'array' THEN c.detail -> 'calls' ELSE '[]' END)`

// results is the exit codes of the prompt's recorded tool calls, by tool
// call id: for each, the session's first visible call, in order, that carries
// its result back with codes or as an error (promptrows.go joins.results, the
// session's steps). That call can be a later prompt's (the prompt after an
// interrupt), so it's found through the index on detail's results, one lookup
// per tool call (OFFSET 0 keeps the planner from mixing in the session's
// index), and its flush marks this prompt (flush: ResultIDs).
var results = `res AS MATERIALIZED (SELECT ids.session_id, ids.k, r.codes
	FROM (SELECT DISTINCT c.session_id, x ->> 'id' AS k FROM pc c, jsonb_array_elements(` + detailCalls + `) x
		WHERE x ->> 'id' IS NOT NULL) ids
	CROSS JOIN LATERAL (SELECT CASE WHEN jsonb_typeof(v -> 'exit') = 'array' AND jsonb_array_length(v -> 'exit') > 0
			THEN ARRAY(SELECT jsonb_array_elements_text(v -> 'exit')::bigint) ELSE ARRAY[1::bigint] END AS codes
		FROM (SELECT c.detail -> 'results' -> ids.k AS v
			FROM (SELECT * FROM calls c WHERE c.detail -> 'results' ? ids.k OFFSET 0) c
			WHERE c.session_id = ids.session_id AND ` + visible + `
			  AND (jsonb_typeof(c.detail -> 'results' -> ids.k -> 'exit') = 'array'
			       AND jsonb_array_length(c.detail -> 'results' -> ids.k -> 'exit') > 0
			    OR c.detail -> 'results' -> ids.k -> 'error' = 'true')
			ORDER BY ` + callOrder + ` LIMIT 1) r) r)`

// resultCodes is detail call x's exit codes (results).
const resultCodes = `LEFT JOIN res ON res.session_id = c.session_id AND res.k = x ->> 'id'`

// The step's commands, exit codes and patches (promptrows.go step): from its
// recorded tool calls; without them, what Claude Code's Bash events report of
// its tools (bashContent), and the running ones' too (toolContent) when they
// add to it. Each is evaluated only where it's used.
var (
	recorded   = `jsonb_array_length(` + detailCalls + `) > 0`
	detailCmds = `ARRAY(SELECT cmd FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY a(x, i),
		jsonb_array_elements_text(CASE jsonb_typeof(x -> 'commands') WHEN 'array' THEN x -> 'commands' ELSE '[]' END) WITH ORDINALITY b(cmd, j)
		ORDER BY i, j)`
	detailCodes = `ARRAY(SELECT code FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY a(x, i) ` + resultCodes + `,
		unnest(res.codes) WITH ORDINALITY u(code, j) ORDER BY i, j)`
	detailPatches = `coalesce((SELECT jsonb_agg(p ORDER BY i, j) FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY a(x, i),
		jsonb_array_elements(CASE jsonb_typeof(x -> 'patches') WHEN 'array' THEN x -> 'patches' ELSE '[]' END) WITH ORDINALITY b(p, j)
		WHERE coalesce(p ->> 'file', '') <> ''), '[]')`
	// liveAdds: the running tools add to the finished ones (toolContent).
	liveAdds = `(jsonb_array_length(tl.live) > jsonb_array_length(tl.tools)
		AND (cardinality(` + bashCmds("tl.live") + `) <> cardinality(` + bashCmds("tl.tools") + `)
		  OR cardinality(` + bashCodes("tl.live") + `) <> cardinality(` + bashCodes("tl.tools") + `)))`
)

// bashCmds is the commands Claude Code's Bash events name of tools (bashContent).
func bashCmds(tools string) string {
	return `ARRAY(SELECT x ->> 'command' FROM jsonb_array_elements(` + tools + `) WITH ORDINALITY a(x, i)
		WHERE coalesce(x ->> 'command', '') <> '' ORDER BY i)`
}

// bashCodes is their exit codes (bashContent).
func bashCodes(tools string) string {
	return `ARRAY(SELECT (x ->> 'exitCode')::bigint FROM jsonb_array_elements(` + tools + `) WITH ORDINALITY a(x, i)
		WHERE coalesce(x ->> 'command', '') <> '' AND x ->> 'exitCode' IS NOT NULL ORDER BY i)`
}

// stepDoes is what call c did (promptrows.go stepKind, the API's rows.step_kind).
const stepDoes = `CASE WHEN strpos(coalesce(c.stop_reason, ''), 'interrupt') > 0 THEN 'interrupt' ELSE (
	SELECT CASE WHEN bool_or(n IN ('apply_patch', 'Edit', 'Write', 'MultiEdit', 'NotebookEdit')) THEN 'patch'
		WHEN bool_or(n IN ('spawn_agent', 'Task', 'Agent')) THEN 'spawn'
		WHEN bool_or(u ->> 'kind' = 'mcp' AND jsonb_typeof(u -> 'kind') = 'string') THEN 'mcp'
		WHEN bool_or(n IN ('exec', 'exec_command', 'shell', 'local_shell', 'Bash', 'BashOutput')) THEN 'exec'
		WHEN count(*) > 0 THEN 'tool' ELSE 'text' END
	FROM jsonb_array_elements(CASE jsonb_typeof(c.tool_uses) WHEN 'array' THEN c.tool_uses ELSE '[]' END) u,
		LATERAL (SELECT coalesce(nullif(CASE WHEN jsonb_typeof(u -> 'name') = 'string' THEN u ->> 'name' END, ''),
			nullif(CASE WHEN jsonb_typeof(u -> 'tool') = 'string' THEN u ->> 'tool' END, ''), '?') AS n) nm
	WHERE jsonb_typeof(u) = 'object') END`

// usageMissing is why call c has no output count (process/requestrows.go
// usageMissing; a visible call is never a prewarm).
const usageMissing = `CASE WHEN coalesce(c.output_tokens, 0) <> 0 THEN NULL
	WHEN strpos(coalesce(c.stop_reason, ''), 'interrupt') > 0 THEN 'interrupted'
	WHEN c.status >= 400 THEN 'failed'
	WHEN coalesce(c.ws_turn, 0) = 0 AND coalesce(c.response_bytes, 0) > 524288 THEN 'parseBuffer'
	ELSE 'streamCut' END`

// stepCols are steps' columns.
const stepCols = `prompt_id, session_id, step, call_id, t_ns, end_ns, kind, model, request_class,
	tokens_input, tokens_cache_read, tokens_cache_write, tokens_cache_write_5m, tokens_cache_write_1h, tokens_output,
	ttft_ms, stop_reason, cost_usd, failed, reported, agent_type, tools, tools_live, does,
	commands, exit_codes, content_live, commands_live, exit_codes_live, patches, text, usage_missing`

// stepSelect is the steps of the prompts whose visible calls c `where` keeps:
// one per call, numbered within its prompt by (time, call id) (§8 difference 7;
// promptrows.go rows: steps by time and id).
func stepSelect(where string) string {
	return `WITH pc AS MATERIALIZED (SELECT * FROM calls c WHERE ` + where + ` AND c.prompt_id IS NOT NULL AND ` + visible + `),
` + results + `
SELECT c.prompt_id, c.session_id,
	row_number() OVER (PARTITION BY c.prompt_id, c.session_id ORDER BY ` + callOrder + `),
	c.call_id, ` + at + `, ` + realEnd + `,
	CASE WHEN c.request_class = 'subagent' THEN 'subagent' ELSE 'model' END,
	nullif(c.model, ''), CASE WHEN c.request_class IN ('main', 'subagent', 'auxiliary', 'compaction') THEN c.request_class END,
	nullif(c.input_tokens, 0), nullif(c.cache_read_tokens, 0), nullif(c.cache_write_tokens, 0),
	rep.cache5m, rep.cache1h, nullif(c.output_tokens, 0),
	rep.ttft, nullif(c.stop_reason, ''), rep.cost,
	coalesce(c.status, 0) = 0 OR c.status >= 400,
	coalesce(rep.found, false),
	nullif(c.agent_type, ''),
	tl.tools, CASE WHEN jsonb_array_length(tl.live) > jsonb_array_length(tl.tools) THEN tl.live END,
	` + stepDoes + `,
	CASE WHEN ` + recorded + ` THEN ` + detailCmds + ` ELSE ` + bashCmds("tl.tools") + ` END,
	CASE WHEN ` + recorded + ` THEN ` + detailCodes + ` ELSE ` + bashCodes("tl.tools") + ` END,
	NOT ` + recorded + ` AND ` + liveAdds + `,
	CASE WHEN NOT ` + recorded + ` AND ` + liveAdds + ` THEN ` + bashCmds("tl.live") + ` ELSE '{}' END,
	CASE WHEN NOT ` + recorded + ` AND ` + liveAdds + ` THEN ` + bashCodes("tl.live") + ` ELSE '{}' END,
	CASE WHEN ` + recorded + ` THEN ` + detailPatches + ` ELSE '[]' END,
	nullif(` + jsonStrOnly("c.detail -> 'text'") + `, ''),
	` + usageMissing + `
FROM pc c ` + stepReport + ` ` + stepTools
}

// --- a prompt ------------------------------------------------------------------------------------

// promptCols are prompts' columns.
const promptCols = `prompt_id, session_id, t_ns, product, has_input, prompt_index, text_chars, frustrated, steps,
	unfinished_max_ns, turn_end_ns, text, tokens_in, tokens_out, tool_counts, interrupted, destructive, turn_report`

// turnReport is agents.turn_report of a codex_turn_event's metadata m
// (promptrows.go turnReport): numbers only from JSON numbers.
func turnReport(m string) string {
	md := func(k string) string { return "(" + m + " -> '" + k + "')" }
	integer := func(k string) string { return `trunc(` + jsonNum(md(k)) + `)::bigint` }
	tool := func(k, name string) string {
		return `CASE WHEN ` + integer(k) + ` <> 0 THEN jsonb_build_object('` + name + `', ` + integer(k) + `) ELSE '{}' END`
	}
	return `jsonb_build_object('status', ` + jsonStrOnly(md("status")) + `, 'durationMs', ` + jsonNum(md("duration_ms")) + `,
		'samplingMs', ` + jsonNum(md("sampling_ms")) + `, 'requests', ` + integer("sampling_request_count") + `,
		'retries', ` + integer("sampling_retry_count") + `,
		'toolCalls', ` + tool("shell_command_count", "shell") + ` || ` + tool("mcp_tool_call_count", "mcp") + ` || ` +
		tool("file_change_count", "fileChange") + ` || ` + tool("web_search_count", "webSearch") + ` || ` +
		tool("subagent_tool_call_count", "subagent") + ` || ` + tool("dynamic_tool_call_count", "dynamic") + `,
		'steers', ` + integer("steer_count") + `,
		'compactionMs', CASE WHEN ` + jsonNum(md("compaction_ms")) + ` <> 0 THEN ` + jsonNum(md("compaction_ms")) + ` END,
		'trigger', ` + jsonStrOnly(md("turn_trigger")) + `)`
}

// promptEvent is whether event e is one of prompt $1's in its session s
// (promptrows.go joins.byPrompt).
const promptEvent = `e.prompt_id = $1 AND e.session_id = s.session_id AND e.source = 'claude_code'`

// promptSelect is prompt $1's rows, one per session it's in (an input with no
// prompt id: $2 its batch, $3 its place in it), after its steps: its first
// input, else its first step, gives its time; its steps, its events and,
// for a Codex thread's turn, its last turn report give the rest
// (promptrows.go rows).
var promptSelect = `WITH inp AS (
	SELECT DISTINCT ON (e.session_id) e.session_id, e.ts_ns, e.metadata FROM events e
	WHERE e.source = 'claude_code' AND e.name = 'tengu_input_prompt' AND e.session_id IS NOT NULL
	  AND (e.prompt_id = $1 OR e.prompt_id IS NULL AND e.batch_call_id = $2 AND e.batch_cc_n = $3)
	ORDER BY e.session_id, ` + evOrder("e") + `),
st AS (SELECT session_id, count(*) AS n, min(t_ns) AS t, max(t_ns) FILTER (WHERE end_ns IS NULL) AS unfinished,
		sum(nullif(coalesce(tokens_input, 0) + coalesce(tokens_cache_read, 0) + coalesce(tokens_cache_write, 0), 0)) AS tin,
		sum(tokens_output) AS tout
	FROM steps WHERE prompt_id = $1 GROUP BY session_id),
s AS (SELECT session_id FROM inp UNION SELECT session_id FROM st)
SELECT $1, s.session_id, coalesce(inp.ts_ns, st.t), f.product, inp.session_id IS NOT NULL,
	` + jsonTrunc(`inp.metadata -> 'prompt_index'`) + `,
	CASE WHEN inp.session_id IS NOT NULL THEN ` + jsonTrunc(`inp.metadata -> 'prompt_length'`) + ` ELSE char_length(said.text) END,
	` + pyTrue(`inp.metadata -> 'is_negative'`) + `,
	coalesce(st.n, 0), st.unfinished, ev.turn_end, said.text, st.tin, st.tout,
	coalesce(tc.counts, '{}'), coalesce(ev.cancelled, false), coalesce(ev.destructive, 0),
	CASE WHEN f.product = 'codex' THEN (SELECT ` + turnReport("e.metadata") + ` FROM events e
		WHERE e.prompt_id = $1 AND e.session_id = s.session_id AND e.source = 'codex' AND e.name = 'codex_turn_event'
		ORDER BY e.ts_ns DESC, e.event_key COLLATE "C" DESC LIMIT 1) END
FROM s LEFT JOIN inp USING (session_id) LEFT JOIN st USING (session_id)
LEFT JOIN LATERAL (SELECT ` + agent + ` AS product FROM calls c
	WHERE c.prompt_id = $1 AND c.session_id = s.session_id AND ` + visible + ` ORDER BY ` + callOrder + ` LIMIT 1) f ON true
LEFT JOIN LATERAL (SELECT c.detail ->> 'prompt' AS text FROM calls c
	WHERE c.prompt_id = $1 AND c.session_id = s.session_id AND ` + visible + `
	  AND jsonb_typeof(c.detail -> 'prompt') = 'string' AND c.detail ->> 'prompt' <> ''
	ORDER BY ` + callOrder + ` LIMIT 1) said ON true
LEFT JOIN LATERAL (SELECT max(e.ts_ns) FILTER (WHERE e.name = 'tengu_turn_end') AS turn_end,
		bool_or(e.name = 'tengu_cancel') AS cancelled,
		count(*) FILTER (WHERE e.name = 'tengu_bash_tool_command_executed'
			AND ` + pyStr(md("destructive_category")) + ` NOT IN ('none', '')) AS destructive
	FROM events e WHERE ` + promptEvent + ` AND e.name IN ('tengu_turn_end', 'tengu_cancel', 'tengu_bash_tool_command_executed')) ev ON true
LEFT JOIN LATERAL (SELECT jsonb_object_agg(n, k) AS counts FROM (
	SELECT coalesce(nullif(` + pyStr(md("toolName")) + `, ''), '—') AS n, count(*) AS k FROM events e
	WHERE ` + promptEvent + ` AND e.name IN ('tengu_tool_use_success', 'tengu_tool_use_error') GROUP BY 1) x) tc ON true`

// promptRefresh recomputes prompt $1's rows ($2, $3: an unnamed input's batch
// and place): its steps, then its prompts row from them. Both are replaced,
// since a prompt's calls can renumber and its rows can leave.
var promptRefresh = []string{
	// The planner prices the results lookup by GIN's default selectivity, which
	// puts a large prompt's statement over JIT's threshold; compiling it costs
	// seconds, running it milliseconds.
	`SET LOCAL jit = off`,
	`DELETE FROM steps WHERE prompt_id = $1`,
	`DELETE FROM prompts WHERE prompt_id = $1`,
	`INSERT INTO steps (` + stepCols + `) ` + stepSelect(`c.prompt_id = $1`),
	`INSERT INTO prompts (` + promptCols + `) ` + promptSelect,
}
