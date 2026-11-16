package refresh

// Work (handoff §11R): what a turn, a step, a minute and a session did, from
// metadata first (tool names and Claude Code's analytics; Codex's tool and
// command events). Each rollup that holds work computes it with the same
// rules: workItems classes an event as pieces of work, keyed (kind, key, n);
// workJSON adds pieces up into the API's TurnWork. The API adds stored works
// together (api/src/aintercept_api/work.py) and reads minutes' and hours'
// pieces, kept as session_minute_keys and session_keys rows (kinds "w:…"), with
// the same rules (work.py from_keys).

// programOf is a Bash command's program as the work line names it: argv0 with
// its subcommand (`npm test`, `git diff`), or for `cd … && x` the last program
// (Claude Code's bash_argv0, bash_subcommand and bash_last_argv0).
func programOf(argv0, sub, last string) string {
	return `(CASE WHEN ` + argv0 + ` = 'cd' AND coalesce(` + last + `, '') <> '' THEN ` + last + `
		WHEN coalesce(` + sub + `, '') <> '' THEN ` + argv0 + ` || ' ' || ` + sub + ` ELSE nullif(` + argv0 + `, '') END)`
}

// fileTools are the tools whose fileExtension is a file's (a Bash tool's can
// hold command fragments).
const fileTools = `('Read', 'Edit', 'Write', 'MultiEdit', 'NotebookEdit', 'NotebookRead')`

// fileType is a file tool's extension, lower case; NULL for other tools.
func fileType(tool, ext string) string {
	return `(CASE WHEN ` + tool + ` IN ` + fileTools + ` THEN nullif(lower(` + ext + `), '') END)`
}

// toolKind is the kind of work a tool call is, by the tool's name n and
// whether it's an MCP tool (CompTurns: Work line vocabulary), for Claude
// Code's tools and Codex's; NULL for Codex's exec, the script that runs the
// tools it lists after it.
func toolKind(n, mcp string) string {
	return `(CASE WHEN ` + mcp + ` THEN 'mcp'
		WHEN ` + n + ` = 'exec' THEN NULL
		WHEN ` + n + ` IN ('Read', 'Grep', 'Glob', 'LS', 'NotebookRead', 'view', 'fs/read_text_file', 'fs/read_multiple_files',
			'fs/list_directory') THEN 'read'
		WHEN ` + n + ` IN ('Edit', 'Write', 'MultiEdit', 'NotebookEdit', 'apply_patch', 'fs/write_file') THEN 'edit'
		WHEN ` + n + ` IN ('Bash', 'exec_command', 'shell', 'local_shell') THEN 'run'
		WHEN ` + n + ` IN ('WebSearch', 'WebFetch', 'web_search', 'web_fetch', 'web__run') THEN 'web'
		WHEN ` + n + ` IN ('Task', 'Agent', 'spawn_agent', 'collaboration.spawn_agent') THEN 'spawn'
		ELSE 'tool' END)`
}

// webKey is a web tool's key: fetch, else search.
func webKey(n string) string {
	return `(CASE WHEN ` + n + ` IN ('WebFetch', 'web_fetch') THEN 'fetch' ELSE 'search' END)`
}

// serverOf is an MCP tool's server: its mcpServerName, else its name's
// (mcp__<server>__<tool>).
func serverOf(server, n string) string {
	return `coalesce(nullif(` + server + `, ''), CASE WHEN starts_with(` + n + `, 'mcp__') AND cardinality(string_to_array(` + n + `, '__')) >= 3
		THEN split_part(` + n + `, '__', 2) END, '')`
}

// workEvents are the events that show work: Claude Code's tool calls, file
// operations and changes, shell commands, git operations and PR links;
// Codex's commands, file changes, accepted lines, MCP calls, searches and
// subagent calls.
const workEvents = `('tengu_tool_use_success', 'tengu_tool_use_error', 'tengu_file_operation', 'tengu_file_changed',
	'tengu_bash_tool_command_executed', 'tengu_git_operation', 'tengu_session_linked_to_pr',
	'codex_command_execution_event', 'codex_file_change_event', 'codex_accepted_line_fingerprints',
	'codex_mcp_tool_call_event', 'codex_web_search_event', 'codex_collab_agent_tool_call_event')`

// workItems is the pieces of work the events e `where` keeps show, one row each
// (sid, at, b, bn: the event's session and order; kind, key, n; exit: a
// command's exit code):
//   - w:read, w:edit (key: the file type), w:web (search, fetch), w:mcp (the
//     server), w:spawn (the subagent type), w:tool: Claude Code's tool calls
//     (its Bash calls are counted by their commands), and Codex's file
//     changes, MCP calls, searches and subagent calls;
//   - w:read_file, w:edit_file (key: the hashed path, counted distinct);
//   - w:lines_added, w:lines_removed (n: lines);
//   - w:run, w:run_failed (key: the program; Codex reports none);
//   - w:commit, w:push, w:pr (key: its number).
func workItems(where string) string {
	m := func(k string) string { return f("m", k) }
	tool := `e.name IN ('tengu_tool_use_success', 'tengu_tool_use_error')`
	run := `e.name IN ('tengu_bash_tool_command_executed', 'codex_command_execution_event')`
	// Each event's values, read once (only the ones its kind has).
	vals := `SELECT CASE WHEN ` + tool + ` THEN ` + pyStr(m("toolName")) + ` END AS tn,
		CASE WHEN ` + run + ` THEN coalesce(` + programOf(pyStr(m("bash_argv0")), pyStr(m("bash_subcommand")), pyStr(m("bash_last_argv0"))) + `, '') END AS prog,
		CASE WHEN ` + run + ` THEN ` + jsonTrunc(m("exit_code")) + ` END AS exit,
		CASE e.name WHEN 'tengu_file_changed' THEN ` + jsonTrunc(m("lines_added")) + `
			WHEN 'codex_accepted_line_fingerprints' THEN ` + jsonTrunc(m("accepted_added_lines")) + ` END AS added,
		CASE e.name WHEN 'tengu_file_changed' THEN ` + jsonTrunc(m("lines_removed")) + `
			WHEN 'codex_accepted_line_fingerprints' THEN ` + jsonTrunc(m("accepted_deleted_lines")) + ` END AS removed,
		CASE WHEN e.name = 'tengu_file_operation' THEN ` + pyStr(m("filePathHash")) + ` END AS hash,
		CASE WHEN e.name IN ('tengu_file_operation', 'tengu_git_operation') THEN ` + pyStr(m("operation")) + ` END AS op,
		CASE WHEN e.name = 'tengu_session_linked_to_pr' THEN ` + pyStr(m("prNumber")) + ` END AS pr`
	tn := "v.tn"
	mcp := `(coalesce(` + m("isMcp") + ` #>> '{}' = 'true', false) OR ` + tn + ` = 'mcp_tool' OR starts_with(` + tn + `, 'mcp__'))`
	return `SELECT e.session_id AS sid, e.ts_ns AS at, e.batch_call_id AS b, e.batch_cc_n AS bn, x.kind, x.key, x.n, x.exit
	FROM events e CROSS JOIN LATERAL ` + fields("e.metadata", "m", "toolName", "isMcp", "mcpServerName", "fileExtension", "subagent_type",
		"operation", "filePathHash", "lines_added", "lines_removed", "accepted_added_lines", "accepted_deleted_lines",
		"bash_argv0", "bash_subcommand", "bash_last_argv0", "exit_code", "prNumber", "mcp_server_name") + `
	CROSS JOIN LATERAL (` + vals + ` OFFSET 0) v
	CROSS JOIN LATERAL (SELECT CASE WHEN ` + tn + ` IS NOT NULL THEN ` + toolKind(tn, mcp) + ` END AS tk OFFSET 0) k
	CROSS JOIN LATERAL (VALUES
		(CASE WHEN k.tk <> 'run' THEN 'w:' || k.tk
		      WHEN e.name = 'codex_file_change_event' THEN 'w:edit' WHEN e.name = 'codex_mcp_tool_call_event' THEN 'w:mcp'
		      WHEN e.name = 'codex_web_search_event' THEN 'w:web' WHEN e.name = 'codex_collab_agent_tool_call_event' THEN 'w:spawn' END,
		 CASE WHEN e.name = 'codex_mcp_tool_call_event' THEN coalesce(` + pyStr(m("mcp_server_name")) + `, '')
		      WHEN e.name = 'codex_web_search_event' THEN 'search'
		      WHEN k.tk IN ('read', 'edit') THEN coalesce(` + fileType(tn, pyStr(m("fileExtension"))) + `, '')
		      WHEN k.tk = 'web' THEN ` + webKey(tn) + `
		      WHEN k.tk = 'mcp' THEN ` + serverOf(pyStr(m("mcpServerName")), tn) + `
		      WHEN k.tk = 'spawn' THEN coalesce(` + pyStr(m("subagent_type")) + `, '') ELSE '' END, 1::bigint, NULL::bigint),
		(CASE WHEN coalesce(v.hash, '') <> '' THEN CASE WHEN v.op = 'read' THEN 'w:read_file' WHEN v.op IN ('write', 'edit') THEN 'w:edit_file' END END,
		 v.hash, 1, NULL),
		(CASE WHEN v.added IS NOT NULL THEN 'w:lines_added' END, '', v.added, NULL),
		(CASE WHEN v.removed IS NOT NULL THEN 'w:lines_removed' END, '', v.removed, NULL),
		(CASE WHEN v.prog IS NOT NULL THEN 'w:run' END, v.prog, 1, v.exit),
		(CASE WHEN v.prog IS NOT NULL AND v.exit <> 0 THEN 'w:run_failed' END, v.prog, 1, NULL),
		(CASE WHEN e.name = 'tengu_git_operation' THEN CASE WHEN v.op IN ('commit', 'commit_amend') THEN 'w:commit'
		      WHEN v.op = 'push' THEN 'w:push' END END, '', 1, NULL),
		(CASE WHEN v.pr ~ '^[0-9]+$' THEN 'w:pr' END, v.pr, 1, NULL)
	) AS x(kind, key, n, exit)
	WHERE ` + where + ` AND e.name IN ` + workEvents + ` AND x.kind IS NOT NULL`
}

// stepItems is step c's pieces of work: its tool calls as Claude Code's
// analytics report them (tl.tools, with each Bash call's program and exit
// code and each file tool's type), else the tools its response asked for
// (c.tool_uses: Codex's, or a call with no analytics), in order (at).
var stepItems = `SELECT x.o AS at, ''::text AS b, 0::bigint AS bn, w.kind, w.key, w.n, w.exit
	FROM (SELECT o, x ->> 'name' AS name, x ->> 'mcpServer' IS NOT NULL AS mcp, coalesce(x ->> 'mcpServer', '') AS server,
			coalesce(x ->> 'fileType', '') AS type, coalesce(x ->> 'program', '') AS program, (x ->> 'exitCode')::bigint AS exit,
			1::bigint AS n
		FROM jsonb_array_elements(tl.tools) WITH ORDINALITY t(x, o)
		UNION ALL
		SELECT o, nm.n, u ->> 'kind' = 'mcp', ` + serverOf(`u ->> 'server'`, `nm.n`) + `, '', '', NULL,
			CASE WHEN jsonb_typeof(u -> 'count') = 'number' AND (u ->> 'count')::numeric >= 1 THEN trunc((u ->> 'count')::numeric)::bigint ELSE 1 END
		FROM jsonb_array_elements(CASE WHEN jsonb_array_length(tl.tools) = 0 AND jsonb_typeof(c.tool_uses) = 'array' THEN c.tool_uses ELSE '[]' END)
			WITH ORDINALITY u(u, o),
			LATERAL (SELECT coalesce(nullif(CASE WHEN jsonb_typeof(u -> 'name') = 'string' THEN u ->> 'name' END, ''),
				CASE WHEN jsonb_typeof(u -> 'tool') = 'string' THEN u ->> 'tool' END, '') AS n) nm
		WHERE jsonb_typeof(u) = 'object') x
	CROSS JOIN LATERAL (SELECT ` + toolKind("x.name", "x.mcp") + ` AS tk) k
	CROSS JOIN LATERAL (VALUES
		('w:' || k.tk, CASE k.tk WHEN 'read' THEN x.type WHEN 'edit' THEN x.type WHEN 'run' THEN x.program
			WHEN 'web' THEN ` + webKey("x.name") + ` WHEN 'mcp' THEN x.server ELSE '' END, x.n, x.exit),
		(CASE WHEN k.tk = 'run' AND x.exit <> 0 THEN 'w:run_failed' END, x.program, 1, NULL)
	) AS w(kind, key, n, exit)
	WHERE w.kind IS NOT NULL`

// workObj is the API's TurnWork of the pieces in relation rel (workItems'
// columns): counts summed, files counted distinct, types and programs and
// servers listed, most first. With last, each command's last run's exit code
// (by at, b, bn); a session's (from its keys) keeps none.
func workObj(raw string, last bool) string {
	return `(WITH k AS MATERIALIZED (SELECT kind, key, sum(n)::bigint AS n FROM ` + raw + ` GROUP BY kind, key) SELECT ` +
		workOf("k", raw, last) + `)`
}

// workOf is workObj over k, the pieces summed per (kind, key), and raw, the
// pieces themselves (for the last exits).
func workOf(rel, raw string, last bool) string {
	sum := func(kind string) string {
		return `(SELECT coalesce(sum(n), 0)::bigint FROM ` + rel + ` WHERE kind = '` + kind + `')`
	}
	files := func(kind string) string {
		return `(SELECT CASE WHEN count(*) > 0 THEN count(DISTINCT key) END FROM ` + rel + ` WHERE kind = '` + kind + `')`
	}
	types := func(kind string) string {
		return `(SELECT coalesce(jsonb_object_agg(key, n), '{}') FROM (SELECT key, sum(n)::bigint AS n FROM ` + rel + `
			WHERE kind = '` + kind + `' AND key <> '' GROUP BY key) x)`
	}
	lines := func(kind string) string {
		return `(SELECT sum(n)::bigint FROM ` + rel + ` WHERE kind = '` + kind + `')`
	}
	listed := func(kind, name, count string, null bool) string {
		k := `key`
		if null {
			k = `nullif(key, '')`
		}
		return `(SELECT coalesce(jsonb_agg(jsonb_build_object('` + name + `', ` + k + `, '` + count + `', n) ORDER BY n DESC, key COLLATE "C"), '[]')
			FROM (SELECT key, sum(n)::bigint AS n FROM ` + rel + ` WHERE kind = '` + kind + `' GROUP BY key) x)`
	}
	lastExit := `NULL::bigint`
	if last {
		lastExit = `(SELECT l.exit FROM ` + raw + ` l WHERE l.kind = 'w:run' AND l.key = r.key
			ORDER BY l.at DESC, l.b COLLATE "C" DESC, l.bn DESC LIMIT 1)`
	}
	web := func(key string) string {
		return `(SELECT coalesce(sum(n), 0)::bigint FROM ` + rel + ` WHERE kind = 'w:web' AND key = '` + key + `')`
	}
	return `jsonb_build_object(
		'reads', ` + sum("w:read") + `, 'readFiles', ` + files("w:read_file") + `, 'readTypes', ` + types("w:read") + `,
		'edits', ` + sum("w:edit") + `, 'editedFiles', ` + files("w:edit_file") + `, 'editTypes', ` + types("w:edit") + `,
		'linesAdded', ` + lines("w:lines_added") + `, 'linesRemoved', ` + lines("w:lines_removed") + `,
		'commands', (SELECT coalesce(jsonb_agg(jsonb_build_object('program', nullif(r.key, ''), 'runs', r.n, 'failed', coalesce(fl.n, 0),
				'lastExit', ` + lastExit + `) ORDER BY r.n DESC, r.key COLLATE "C"), '[]')
			FROM (SELECT key, sum(n)::bigint AS n FROM ` + rel + ` WHERE kind = 'w:run' GROUP BY key) r
			LEFT JOIN (SELECT key, sum(n)::bigint AS n FROM ` + rel + ` WHERE kind = 'w:run_failed' GROUP BY key) fl ON fl.key = r.key),
		'subagents', ` + listed("w:spawn", "name", "count", true) + `,
		'web', jsonb_build_object('searches', ` + web("search") + `, 'fetches', ` + web("fetch") + `),
		'mcp', ` + listed("w:mcp", "server", "calls", false) + `,
		'otherTools', ` + sum("w:tool") + `, 'commits', ` + sum("w:commit") + `, 'pushes', ` + sum("w:push") + `,
		'prs', (SELECT coalesce(jsonb_agg(DISTINCT key::bigint ORDER BY key::bigint), '[]') FROM ` + rel + ` WHERE kind = 'w:pr'))`
}

// isTest is whether a program (programOf's) is a test run (the OutcomeSlot's
// last test): a test runner, or a runner's test subcommand.
func isTest(p string) string {
	return `(` + p + ` ~ '^(pytest|jest|vitest|mocha|rspec|phpunit|tox|nox|ctest)( |$)' OR ` + p + ` ~ ' (test|tests|vitest|jest|pytest)$')`
}
