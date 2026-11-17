package refresh

// Work (handoff §11R): what a turn, a step, a minute and a session did, from
// each step's own tool calls (prompts.go stepItems: names and counts always,
// and with recorded content their commands, files, patches and the exit codes
// their results carry back). Each rollup that holds work adds the steps'
// pieces, keyed (kind, key, n); workOf adds pieces up into the API's TurnWork. The API adds stored works
// together (api/src/aintercept_api/work.py) and reads minutes' and hours'
// pieces, kept as session_minute_keys and session_keys rows (kinds "w:…"), with
// the same rules (work.py from_keys).

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

// workObj is the API's TurnWork of the pieces in relation rel (kind, key, n):
// counts summed, files counted distinct, types and programs and servers
// listed, most first; no command's last exit code (a session's).
func workObj(rel string) string {
	return `(WITH k AS MATERIALIZED (SELECT kind, key, sum(n)::bigint AS n FROM ` + rel + ` GROUP BY kind, key) SELECT ` +
		workOf("k", "NULL::bigint") + `)`
}

// workOf is workObj over rel, the pieces summed per (kind, key), with
// lastExit each command's last run's exit code (an expression over r.key).
func workOf(rel, lastExit string) string {
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
