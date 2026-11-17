package refresh

import (
	"fmt"
	"strings"
)

// The step and prompt rollups (docs/PIPELINE.md §5.2). The unit is the step:
// one visible model call with a prompt, refreshed from that call alone (its
// row, and the results of its tool calls that later calls carry back, one
// index lookup per tool call). A step adds its keys (step_keys) to its
// prompt's (prompt_keys), taking away what it added before, and its prompt's
// row is recomputed from those sums and a few indexed lookups on its steps:
// a new call in a long prompt never reads the prompt's other steps.
//
// Everything comes from the wire: analytics events aren't loaded (§3.2).

// realEnd is a call's end as today's row has it: none for one still open or
// one the sweep closed (the start alone).
const realEnd = `(CASE WHEN c.swept THEN NULL ELSE c.end_ns END)`

// ordOf is a call's place among calls at the same time: its id with '#'
// written '~' (process/requestrows.go callFromRequest: the row's request id),
// compared byte-wise.
const ordOf = `replace(c.call_id, '#', '~')`

// callOrder is the order of a session's calls: time, then ordOf.
const callOrder = at + `, ` + ordOf + ` COLLATE "C"`

// jsonTrunc is session.py _int (promptrows.go pyIntOf): int(float(v)), toward
// zero, for a number or a numeric string; NULL otherwise (or out of range).
func jsonTrunc(v string) string {
	return fmt.Sprintf(`(CASE jsonb_typeof(%[1]s)
		WHEN 'number' THEN CASE WHEN abs((%[1]s #>> '{}')::numeric) < 9e18 THEN trunc((%[1]s #>> '{}')::numeric)::bigint END
		WHEN 'string' THEN CASE WHEN btrim(%[1]s #>> '{}') ~ %[2]s AND abs(btrim(%[1]s #>> '{}')::numeric) < 9e18
		                        THEN trunc(btrim(%[1]s #>> '{}')::numeric)::bigint END END)`, v, numeric)
}

// jsonStrOnly is a JSON string's value (promptrows.go jsonStr); NULL otherwise.
func jsonStrOnly(v string) string {
	return fmt.Sprintf(`(CASE WHEN jsonb_typeof(%[1]s) = 'string' THEN %[1]s #>> '{}' END)`, v)
}

// --- a step --------------------------------------------------------------------------------------

// detailCalls is call c's recorded tool calls (events.CallDetail's calls).
const detailCalls = `(CASE jsonb_typeof(c.detail -> 'calls') WHEN 'array' THEN c.detail -> 'calls' ELSE '[]' END)`

// recorded is whether call c's tool calls were recorded (full interception).
var recorded = `jsonb_array_length(` + detailCalls + `) > 0`

// results is the exit codes of the steps' recorded tool calls, by tool call
// id: for each, the session's first visible call, in order, that carries its
// result back with codes or as an error. That call can be a later prompt's
// (the prompt after an interrupt), so it's found through the index on
// detail's results, one lookup per tool call (OFFSET 0 keeps the planner from
// mixing in the session's index), and its flush marks this step (flush:
// ResultIDs). codes is the exit codes ({1} for an error with none), err
// whether the client marked it an error.
var results = `res AS MATERIALIZED (SELECT ids.session_id, ids.k, r.codes, r.err
	FROM (SELECT DISTINCT c.session_id, x ->> 'id' AS k FROM pc c, jsonb_array_elements(` + detailCalls + `) x
		WHERE x ->> 'id' IS NOT NULL) ids
	CROSS JOIN LATERAL (SELECT CASE WHEN jsonb_typeof(v -> 'exit') = 'array' AND jsonb_array_length(v -> 'exit') > 0
			THEN ARRAY(SELECT jsonb_array_elements_text(v -> 'exit')::bigint) ELSE ARRAY[1::bigint] END AS codes,
			coalesce(v -> 'error' = 'true', false) AS err
		FROM (SELECT c.detail -> 'results' -> ids.k AS v
			FROM (SELECT * FROM calls c WHERE c.detail -> 'results' ? ids.k OFFSET 0) c
			WHERE c.session_id = ids.session_id AND ` + visible + `
			  AND (jsonb_typeof(c.detail -> 'results' -> ids.k -> 'exit') = 'array'
			       AND jsonb_array_length(c.detail -> 'results' -> ids.k -> 'exit') > 0
			    OR c.detail -> 'results' -> ids.k -> 'error' = 'true')
			ORDER BY ` + callOrder + ` LIMIT 1) r) r)`

// fileExt is a path's extension, lower case (” for none).
func fileExt(p string) string {
	return `coalesce(lower(substring(` + p + ` from '\.([^./]+)$')), '')`
}

// stepTools is call c's tool calls, one row each in order (o): its recorded
// ones (detail's calls: name, id, commands, file, patches, subagent, and the
// result a later call carried back), else the ones its response's metadata
// names (tool_uses: name, MCP server, count).
var stepTools = `SELECT x.o, nullif(x.x ->> 'id', '') AS id, coalesce(x.x ->> 'tool', '') AS name,
		starts_with(coalesce(x.x ->> 'tool', ''), 'mcp__') AS mcp, ` + serverOf(`''`, `coalesce(x.x ->> 'tool', '')`) + ` AS server,
		ARRAY(SELECT jsonb_array_elements_text(CASE jsonb_typeof(x.x -> 'commands') WHEN 'array' THEN x.x -> 'commands' ELSE '[]' END)) AS cmds,
		nullif(` + jsonStrOnly(`x.x -> 'file'`) + `, '') AS file,
		CASE jsonb_typeof(x.x -> 'patches') WHEN 'array' THEN x.x -> 'patches' ELSE '[]' END AS patches,
		coalesce(` + jsonStrOnly(`x.x -> 'subagent'`) + `, '') AS subagent, res.codes, coalesce(res.err, false) AS err, 1::bigint AS n
	FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY x(x, o)
	LEFT JOIN res ON res.session_id = c.session_id AND res.k = x.x ->> 'id'
	WHERE jsonb_typeof(x.x) = 'object'
	UNION ALL
	SELECT o, NULL, nm.n, u ->> 'kind' = 'mcp', ` + serverOf(`u ->> 'server'`, `nm.n`) + `, '{}', NULL, '[]', '', NULL, false,
		CASE WHEN jsonb_typeof(u -> 'count') = 'number' AND (u ->> 'count')::numeric >= 1 THEN trunc((u ->> 'count')::numeric)::bigint ELSE 1 END
	FROM jsonb_array_elements(CASE WHEN NOT ` + recorded + ` AND jsonb_typeof(c.tool_uses) = 'array' THEN c.tool_uses ELSE '[]' END)
		WITH ORDINALITY u(u, o),
		LATERAL (SELECT coalesce(nullif(CASE WHEN jsonb_typeof(u -> 'name') = 'string' THEN u ->> 'name' END, ''),
			CASE WHEN jsonb_typeof(u -> 'tool') = 'string' THEN u ->> 'tool' END, '') AS n) nm
	WHERE jsonb_typeof(u) = 'object'`

// toolObj is the API's ToolCallData of tool row t (stepTools'), its kind tk:
// what the wire says (no duration, result size or destructive mark: those
// were analytics').
var toolObj = `jsonb_build_object(
	'toolUseId', t.id, 'name', coalesce(nullif(t.name, ''), '—'), 'mcpServer', nullif(t.server, ''),
	'target', t.file,
	'status', CASE WHEN t.err OR 0 <> ANY(coalesce(t.codes, '{}')) THEN 'error' ELSE 'ok' END,
	'durationMs', NULL, 'exitCode', t.codes[1], 'command', t.cmds[1],
	'program', CASE WHEN t.tk = 'run' THEN t.cmds[1] END,
	'fileType', ` + fileType(`t.name`, fileExt(`t.file`)) + `,
	'resultBytes', NULL, 'destructive', NULL)`

// stepItems is step c's pieces of work, from its tool rows t (with tk, their
// kind), one row each (at: its place in the step; kind, key, n; exit: a
// command's exit code):
//   - w:read, w:edit (key: the file type), w:web (search, fetch), w:mcp (the
//     server), w:spawn (the subagent type), w:tool: one per tool call (n: its
//     count), but for a shell call, counted by its commands;
//   - w:read_file (key: the file a read tool reads), w:edit_file (each file a
//     patch changes), counted distinct;
//   - w:lines_added, w:lines_removed (n: a patch's lines);
//   - w:run, w:run_failed (key: the program; ” when not recorded): each
//     command a shell call (or a Codex exec script) runs, with the exit code
//     its result carried back at its place;
//   - w:commit, w:push: a git commit or push command.
var stepItems = `SELECT t.o * 1000 AS at, 'w:' || t.tk AS kind,
		CASE WHEN t.tk IN ('read', 'edit') THEN coalesce(` + fileType(`t.name`, fileExt(`t.file`)) + `, '')
			ELSE CASE t.tk
			WHEN 'web' THEN ` + webKey("t.name") + ` WHEN 'mcp' THEN t.server
			WHEN 'spawn' THEN t.subagent ELSE '' END END AS key, t.n, NULL::bigint AS exit
	FROM t WHERE t.tk IS NOT NULL AND t.tk <> 'run'
	UNION ALL
	SELECT t.o * 1000, 'w:read_file', t.file, 1, NULL FROM t WHERE t.tk = 'read' AND t.file IS NOT NULL
	UNION ALL
	SELECT t.o * 1000 + p.j, x.kind, x.key, x.n, NULL FROM t, jsonb_array_elements(t.patches) WITH ORDINALITY p(p, j),
		LATERAL (VALUES ('w:edit_file', p.p ->> 'file', 1::bigint),
			('w:lines_added', '', ` + jsonTrunc(`p.p -> 'added'`) + `), ('w:lines_removed', '', ` + jsonTrunc(`p.p -> 'removed'`) + `)) x(kind, key, n)
	WHERE jsonb_typeof(p.p) = 'object' AND coalesce(p.p ->> 'file', '') <> '' AND x.n IS NOT NULL
	UNION ALL
	SELECT t.o * 1000 + r.j, x.kind, x.key, x.n, x.exit
	FROM t CROSS JOIN LATERAL (SELECT j, cmd, t.codes[j] AS code FROM unnest(t.cmds) WITH ORDINALITY u(cmd, j)
			UNION ALL SELECT 1, '', t.codes[1] WHERE cardinality(t.cmds) = 0 AND t.tk = 'run') r,
		LATERAL (VALUES ('w:run', r.cmd, t.n, r.code),
			(CASE WHEN r.code <> 0 THEN 'w:run_failed' END, r.cmd, 1, NULL),
			(CASE WHEN r.cmd = 'git commit' THEN 'w:commit' WHEN r.cmd = 'git push' THEN 'w:push' END, '', 1, NULL)) x(kind, key, n, exit)
	WHERE (t.tk = 'run' OR t.tk IS NULL AND t.name = 'exec') AND x.kind IS NOT NULL`

// stepKinds are the pieces a step's kind is named by (its first of them).
const stepKinds = `('w:read', 'w:edit', 'w:run', 'w:web', 'w:mcp', 'w:spawn', 'w:tool')`

// stepWork is call c's tools and work (handoff §11R): its tool list (the API's
// ToolCallData), its tool counts by name, its pieces ([kind, key, n, exit], in
// order) and what the first of them is (the step's kind: a new group starts
// where it, or its program or server, changes). The API adds a group's steps'
// pieces up (work.py from_keys, the same rules as workObj).
var stepWork = `LEFT JOIN LATERAL (WITH t AS MATERIALIZED (SELECT t.*, ` + toolKind("t.name", "t.mcp") + ` AS tk
			FROM (` + stepTools + `) t),
		i AS MATERIALIZED (` + stepItems + `)
	SELECT (SELECT coalesce(jsonb_agg(` + toolObj + ` ORDER BY t.o), '[]') FROM t) AS tools,
		(SELECT coalesce(jsonb_object_agg(name, n), '{}') FROM (SELECT coalesce(nullif(t.name, ''), '—') AS name, sum(t.n)::bigint AS n
			FROM t GROUP BY 1) x) AS tool_counts,
		(SELECT substr(kind, 3) FROM i WHERE kind IN ` + stepKinds + ` ORDER BY at, kind COLLATE "C" LIMIT 1) AS kind,
		(SELECT key FROM i WHERE kind IN ` + stepKinds + ` ORDER BY at, kind COLLATE "C" LIMIT 1) AS key,
		(SELECT coalesce(jsonb_agg(jsonb_build_array(kind, key, n, exit) ORDER BY at, kind COLLATE "C"), '[]') FROM i) AS pieces) sw ON true`

// The step's commands, exit codes and patches: from its recorded tool calls.
var (
	detailCmds = `ARRAY(SELECT cmd FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY a(x, i),
		jsonb_array_elements_text(CASE jsonb_typeof(x -> 'commands') WHEN 'array' THEN x -> 'commands' ELSE '[]' END) WITH ORDINALITY b(cmd, j)
		ORDER BY i, j)`
	detailCodes = `ARRAY(SELECT code FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY a(x, i)
		LEFT JOIN res ON res.session_id = c.session_id AND res.k = x ->> 'id',
		unnest(res.codes) WITH ORDINALITY u(code, j) ORDER BY i, j)`
	detailPatches = `coalesce((SELECT jsonb_agg(p ORDER BY i, j) FROM jsonb_array_elements(` + detailCalls + `) WITH ORDINALITY a(x, i),
		jsonb_array_elements(CASE jsonb_typeof(x -> 'patches') WHEN 'array' THEN x -> 'patches' ELSE '[]' END) WITH ORDINALITY b(p, j)
		WHERE coalesce(p ->> 'file', '') <> ''), '[]')`
)

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
const stepCols = `call_id, prompt_id, session_id, t_ns, ord, end_ns, kind, model, request_class,
	tokens_input, tokens_cache_read, tokens_cache_write, tokens_output, stop_reason, failed, agent_type,
	tools, tool_counts, does, commands, exit_codes, patches, text, usage_missing, interrupted, follows_interrupt, prompt_chars,
	work_kind, program, server, agent_id, work_pieces`

// stepSelect is the steps of the visible calls c with a prompt that `where`
// keeps: each from its own call (and its tool calls' results).
func stepSelect(where string) string {
	return `WITH pc AS MATERIALIZED (SELECT * FROM calls c WHERE ` + where + ` AND c.prompt_id IS NOT NULL AND ` + visible + `),
` + results + `
SELECT c.call_id, c.prompt_id, c.session_id, ` + at + `, ` + ordOf + `, ` + realEnd + `,
	CASE WHEN c.request_class = 'subagent' THEN 'subagent' ELSE 'model' END,
	nullif(c.model, ''), CASE WHEN c.request_class IN ('main', 'subagent', 'auxiliary', 'compaction') THEN c.request_class END,
	nullif(c.input_tokens, 0), nullif(c.cache_read_tokens, 0), nullif(c.cache_write_tokens, 0), nullif(c.output_tokens, 0),
	nullif(c.stop_reason, ''), coalesce(c.status, 0) = 0 OR c.status >= 400, nullif(c.agent_type, ''),
	sw.tools, sw.tool_counts, ` + stepDoes + `,
	CASE WHEN ` + recorded + ` THEN ` + detailCmds + ` ELSE '{}' END,
	CASE WHEN ` + recorded + ` THEN ` + detailCodes + ` ELSE '{}' END,
	CASE WHEN ` + recorded + ` THEN ` + detailPatches + ` ELSE '[]' END,
	nullif(` + jsonStrOnly("c.detail -> 'text'") + `, ''),
	` + usageMissing + `,
	strpos(coalesce(c.stop_reason, ''), 'interrupt') > 0,
	coalesce(c.detail -> 'interrupted' = 'true', false),
	CASE WHEN jsonb_typeof(c.detail -> 'said') = 'array' AND jsonb_array_length(c.detail -> 'said') > 0
		THEN coalesce(` + jsonTrunc(`c.detail -> 'promptChars'`) + `, 0) END,
	coalesce(sw.kind, 'think'), CASE WHEN sw.kind = 'run' THEN nullif(sw.key, '') END,
	CASE WHEN sw.kind = 'mcp' THEN nullif(sw.key, '') END, nullif(c.agent_id, ''), sw.pieces
FROM pc c ` + stepWork
}

// stepKeys is what the steps s `where` keeps add to their prompts, per (kind,
// key): s:steps, s:tokens_in (input, cache read and cache write, when any is
// known), s:tokens_out, s:interrupted, s:follows_interrupt; tool (its tool
// calls by name); and its work pieces summed per key, with a command's last
// run (its place and exit code).
func stepKeys(where string) string {
	return `SELECT s.call_id, s.prompt_id, s.session_id, k.kind, k.key, k.n, s.t_ns, s.ord, k.i, k.exit
	FROM steps s CROSS JOIN LATERAL (
		SELECT 's:steps' AS kind, '' AS key, 1::bigint AS n, 0::bigint AS i, NULL::bigint AS exit
		UNION ALL SELECT 's:tokens_in', '', nullif(coalesce(s.tokens_input, 0) + coalesce(s.tokens_cache_read, 0) + coalesce(s.tokens_cache_write, 0), 0), 0, NULL
		UNION ALL SELECT 's:tokens_out', '', s.tokens_output, 0, NULL
		UNION ALL SELECT 's:interrupted', '', 1, 0, NULL WHERE s.interrupted
		UNION ALL SELECT 's:follows_interrupt', '', 1, 0, NULL WHERE s.follows_interrupt
		UNION ALL SELECT 'tool', t.key, t.value::bigint, 0, NULL FROM jsonb_each_text(s.tool_counts) t
		UNION ALL SELECT kind, key, sum(n)::bigint, max(i), (array_agg(exit ORDER BY i DESC))[1]
		FROM (SELECT p ->> 0 AS kind, p ->> 1 AS key, (p ->> 2)::bigint AS n, (p ->> 3)::bigint AS exit, i
			FROM jsonb_array_elements(s.work_pieces) WITH ORDINALITY w(p, i)) x GROUP BY kind, key) k
	WHERE ` + where + ` AND k.n IS NOT NULL`
}

// --- a prompt ------------------------------------------------------------------------------------

// promptCols are prompts' columns.
const promptCols = `prompt_id, session_id, t_ns, product, text_chars, steps, unfinished_max_ns, text, parts, tokens_in, tokens_out,
	tool_counts, interrupted, follows_interrupt, ended_ns, work`

// pk is a prompt key's sum for prompt k (k.prompt_id, k.session_id).
func pk(kind string) string {
	return `(SELECT n FROM prompt_keys x WHERE x.prompt_id = k.prompt_id AND x.session_id = k.session_id AND x.kind = '` + kind + `' AND x.key = '')`
}

// promptSelect is the rows of the prompts ($1, an array) from their keys
// (prompt_keys: a few hundred rows at most, its distinct tools, files and
// commands) and indexed lookups on their steps: the first (its time, its
// product), the first main-class one whose new messages say something (its
// text and typed parts), the last open one and the last end. Its work's commands' last exit codes are one indexed lookup
// each on step_keys.
var promptSelect = `SELECT k.prompt_id, k.session_id, f.t_ns, f.product, said.chars, k.n, un.t, said.text, said.parts,
	` + pk("s:tokens_in") + `, ` + pk("s:tokens_out") + `,
	coalesce((SELECT jsonb_object_agg(x.key, x.n) FROM prompt_keys x
		WHERE x.prompt_id = k.prompt_id AND x.session_id = k.session_id AND x.kind = 'tool'), '{}'),
	coalesce(` + pk("s:interrupted") + ` > 0, false), coalesce(` + pk("s:follows_interrupt") + ` > 0, false), en.e,
	(WITH w AS MATERIALIZED (SELECT x.kind, x.key, x.n FROM prompt_keys x
		WHERE x.prompt_id = k.prompt_id AND x.session_id = k.session_id AND starts_with(x.kind, 'w:'))
	SELECT ` + workOf("w", `(SELECT l.exit FROM step_keys l WHERE l.prompt_id = k.prompt_id AND l.session_id = k.session_id
			AND l.kind = 'w:run' AND l.key = r.key ORDER BY l.t_ns DESC, l.ord DESC, l.i DESC LIMIT 1)`) + `)
FROM prompt_keys k
CROSS JOIN LATERAL (SELECT s.t_ns, ` + agent + ` AS product FROM steps s JOIN calls c USING (call_id)
	WHERE s.prompt_id = k.prompt_id AND s.session_id = k.session_id ORDER BY s.t_ns, s.ord LIMIT 1) f
LEFT JOIN LATERAL (SELECT s.t_ns AS t FROM steps s WHERE s.prompt_id = k.prompt_id AND s.session_id = k.session_id AND s.end_ns IS NULL
	ORDER BY s.t_ns DESC LIMIT 1) un ON true
LEFT JOIN LATERAL (SELECT coalesce(s.end_ns, s.t_ns) AS e FROM steps s WHERE s.prompt_id = k.prompt_id AND s.session_id = k.session_id
	ORDER BY coalesce(s.end_ns, s.t_ns) DESC LIMIT 1) en ON true
LEFT JOIN LATERAL (SELECT nullif(` + jsonStrOnly(`c.detail -> 'prompt'`) + `, '') AS text, s.prompt_chars AS chars, c.detail -> 'said' AS parts
	FROM steps s JOIN calls c USING (call_id)
	WHERE s.prompt_id = k.prompt_id AND s.session_id = k.session_id AND s.prompt_chars IS NOT NULL
	  AND (s.request_class = 'main' OR s.request_class IS NULL)
	ORDER BY s.t_ns, s.ord LIMIT 1) said ON true
WHERE k.prompt_id = ANY($1) AND k.kind = 's:steps' AND k.key = '' AND k.steps > 0`

// addKeys adds the step keys a statement returns as rel (sign 1) or takes
// them away (-1) in their prompts' prompt_keys.
func addKeys(rel string, sign int) string {
	return fmt.Sprintf(`INSERT INTO prompt_keys (prompt_id, session_id, kind, key, steps, n)
	SELECT prompt_id, session_id, kind, key, %[1]d, %[1]d * n FROM %[2]s ORDER BY 1, 2, 3, 4
	ON CONFLICT (prompt_id, session_id, kind, key) DO UPDATE SET steps = prompt_keys.steps + excluded.steps, n = prompt_keys.n + excluded.n`, sign, rel)
}

// stepRefresh recomputes step $1 (a call id): its step_keys taken away from
// its prompt's keys, its row and keys recomputed from its call, and added back.
// Its prompts' rows (it was in and is in now: $2) are recomputed after it
// (promptRows). The prompts are locked first (lockPrompts), so two steps of
// one prompt refresh one after the other.
var stepRefresh = []string{
	`WITH o AS (DELETE FROM step_keys WHERE call_id = $1 RETURNING *) ` + addKeys("o", -1),
	`DELETE FROM steps WHERE call_id = $1`,
	`INSERT INTO steps (` + stepCols + `) ` + stepSelect(`c.call_id = $1`),
	`WITH k AS (INSERT INTO step_keys ` + stepKeys(`s.call_id = $1`) + ` RETURNING *) ` + addKeys("k", 1),
}

// promptRows recomputes the rows of prompts $1 (an array) from their keys.
var promptRows = []string{
	`DELETE FROM prompt_keys WHERE prompt_id = ANY($1) AND steps = 0`,
	`DELETE FROM prompts WHERE prompt_id = ANY($1)`,
	`INSERT INTO prompts (` + promptCols + `) ` + promptSelect,
}

// promptFull recomputes prompt $1 from all its calls: its steps, their keys,
// its keys and its rows. Only retention uses it (a prompt that lost calls), and
// the from-scratch check.
var promptFull = []string{
	`DELETE FROM step_keys WHERE prompt_id = $1`,
	`DELETE FROM steps WHERE prompt_id = $1`,
	`DELETE FROM prompt_keys WHERE prompt_id = $1`,
	`INSERT INTO steps (` + stepCols + `) ` + stepSelect(`c.prompt_id = $1`),
	`INSERT INTO step_keys ` + stepKeys(`s.prompt_id = $1`),
	`INSERT INTO prompt_keys (prompt_id, session_id, kind, key, steps, n)
	SELECT prompt_id, session_id, kind, key, count(*), sum(n) FROM step_keys WHERE prompt_id = $1 GROUP BY 1, 2, 3, 4`,
}

// lockPrompts is the transaction-scoped lock on each prompt a refresh changes
// ($1, sorted, so two refreshes take them in one order).
const lockPrompts = `SELECT count(pg_advisory_xact_lock(hashtextextended(p, 0))) FROM unnest($1::text[]) p`

func promptList(ids ...string) []string {
	var out []string
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return sortedUnique(out)
}

func sortedUnique(ss []string) []string {
	if len(ss) == 0 {
		return ss
	}
	out := append([]string(nil), ss...)
	for i := 1; i < len(out); i++ { // small: insertion sort
		for j := i; j > 0 && strings.Compare(out[j-1], out[j]) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	w := 1
	for i := 1; i < len(out); i++ {
		if out[i] != out[w-1] {
			out[w] = out[i]
			w++
		}
	}
	return out[:w]
}
