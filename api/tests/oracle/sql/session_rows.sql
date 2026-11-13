-- session_rows as the API reads them, rebuilt from the streaming processor's
-- per-session tables (internal/stream/state/sessionrows.go):
--   session_rows           one stored row per session (its own values; own_title)
--   session_prompt_ids     its prompt ids        → prompt_ids
--   session_devices        its devices           → devices
--   session_call_accounts  its calls' accounts   → call_accounts
--   session_missed         its uncaptured reports → cov_window_* (15-minute gaps split)
--   session_edges          a child's edge         → the parent's children_json
--   session_title_calls    a title thread's calls → Codex titles and the labels
-- Each table holds a key's newest rows (the newest-row-wins view). The Go
-- rebuild in sessionrows_test.go (sessionRowsOf) is this query step for step.
WITH RECURSIVE
lists AS (
    SELECT s.session_id,
        coalesce((SELECT list(prompt_id ORDER BY prompt_id) FROM session_prompt_ids p WHERE p.session_id = s.session_id), []::VARCHAR[]) AS prompt_ids,
        coalesce((SELECT list(device_id ORDER BY device_id) FROM session_devices d WHERE d.session_id = s.session_id), []::VARCHAR[]) AS devices,
        coalesce((SELECT list(account_id ORDER BY account_id) FROM session_call_accounts a WHERE a.session_id = s.session_id), []::VARCHAR[]) AS call_accounts
    FROM session_rows s
),
missed AS (
    SELECT session_id, ts_ns,
        sum(CASE WHEN prev IS NULL OR ts_ns - prev >= 900000000000 THEN 1 ELSE 0 END)
            OVER (PARTITION BY session_id ORDER BY ts_ns ROWS UNBOUNDED PRECEDING) AS w
    FROM (SELECT session_id, ts_ns, lag(ts_ns) OVER (PARTITION BY session_id ORDER BY ts_ns) AS prev FROM session_missed)
),
windows AS (
    SELECT session_id, list(lo ORDER BY lo) AS f, list(hi ORDER BY lo) AS t, list(n ORDER BY lo) AS c
    FROM (SELECT session_id, w, min(ts_ns) AS lo, max(ts_ns) AS hi, count(*) AS n FROM missed GROUP BY session_id, w)
    GROUP BY session_id
),
children AS (
    SELECT parent_session_id AS session_id,
        '{"subagent":' || count(*) FILTER (WHERE kind = 'subagent') || ',"review":' || count(*) FILTER (WHERE kind <> 'subagent')
            || ',"sessionIds":' || to_json(list(child_session_id ORDER BY child_first_ns, child_session_id)) || '}' AS children_json
    FROM session_edges GROUP BY parent_session_id
),
-- Codex titles: a title thread's title call names the same account's Codex thread
-- (one without a title of its own) that started last before it, within five
-- minutes; the calls in time order, each naming a thread not yet named.
cand AS (
    SELECT s.session_id, s.start_ns FROM session_rows s
    WHERE s.product = 'codex' AND s.own_title IS NULL AND s.model_calls > 0
),
eligible AS (
    SELECT t.ts_ns, t.call_id, t.title, c.session_id, c.start_ns
    FROM session_title_calls t
    JOIN session_call_accounts a ON a.account_id = t.account_id
    JOIN cand c ON c.session_id = a.session_id
    WHERE t.ts_ns - c.start_ns BETWEEN 0 AND 300000000000
),
titles AS (
    SELECT row_number() OVER (ORDER BY ts_ns, call_id) AS n, ts_ns, call_id, title
    FROM (SELECT DISTINCT ts_ns, call_id, title FROM eligible)
),
assign(n, named) AS (
    SELECT 0::BIGINT, []::STRUCT(session_id VARCHAR, title VARCHAR)[]
    UNION ALL
    SELECT a.n + 1, CASE WHEN p.session_id IS NULL THEN a.named
        ELSE list_append(a.named, {'session_id': p.session_id, 'title': t.title}) END
    FROM assign a
    JOIN titles t ON t.n = a.n + 1
    LEFT JOIN LATERAL (
        SELECT e.session_id FROM eligible e
        WHERE e.call_id = t.call_id
          AND NOT list_contains(list_transform(a.named, lambda x: x.session_id), e.session_id)
        ORDER BY e.start_ns DESC, e.session_id LIMIT 1
    ) p ON true
),
named AS (
    SELECT x.session_id, x.title
    FROM (SELECT unnest(named) AS x FROM assign WHERE n = (SELECT max(n) FROM assign))
),
alone AS (
    SELECT session_id, arg_min(title, (ts_ns, call_id)) AS title FROM eligible GROUP BY session_id
),
titled AS (
    SELECT s.session_id,
        coalesce(s.own_title, (SELECT title FROM named n WHERE n.session_id = s.session_id)) AS title,
        coalesce(s.own_title, (SELECT title FROM alone a WHERE a.session_id = s.session_id)) AS title_alone
    FROM session_rows s
)
SELECT
    s.session_id, s.start_ns, s.last_ns, nullif(s.product, '') AS product, s.agent, s.client, s.client_version,
    s.origin_kind, s.origin_session_id, s.origin_json, ch.children_json, s.hidden_calls,
    CASE WHEN ti.title <> '' THEN ti.title ELSE 'Session ' || left(s.session_id, 8) END AS label_text,
    CASE WHEN ti.title <> '' THEN 'title' ELSE 'none' END AS label_source,
    CASE WHEN ti.title_alone <> '' THEN ti.title_alone ELSE 'Session ' || left(s.session_id, 8) END AS label_text_alone,
    CASE WHEN ti.title_alone <> '' THEN 'title' ELSE 'none' END AS label_source_alone,
    s.repo, s."commit",
    s.prompts, s.captured_from_prompt, s.model_calls, s.events, s.frustrated, s.interrupted, s.destructive,
    s.bypass_pct, s.cov_reported, s.cov_captured, s.cov_not_captured,
    CASE WHEN s.events IS NULL THEN []::BIGINT[] ELSE coalesce(w.f, []::BIGINT[]) END AS cov_window_from,
    CASE WHEN s.events IS NULL THEN []::BIGINT[] ELSE coalesce(w.t, []::BIGINT[]) END AS cov_window_to,
    CASE WHEN s.events IS NULL THEN []::BIGINT[] ELSE coalesce(w.c, []::BIGINT[]) END AS cov_window_count,
    s.fields_missing, s.person_id, s.device_id, s.device_platform, s.device_terminal, s.device_shell,
    s.networks, s.effort, s.tokens_input, s.tokens_cache_read, s.tokens_cache_write, s.tokens_output,
    s.tokens_accuracy, s.tool_calls, s.has_facts, s.facts_cwd, s.facts_approval, s.facts_sandbox,
    s.limits_json, s.limits_ns,
    l.prompt_ids, l.devices, l.call_accounts, s.outputs_known, s.queries, s.bypassed,
    s.cli_version, s.cli_version_ns, s.client_version_ns, s.rl_status, s.rl_util, s.rl_overage, s.rl_ns,
    s.sandbox, s.sandbox_ns, s.custom_ca
FROM session_rows s
JOIN lists l USING (session_id)
JOIN titled ti USING (session_id)
LEFT JOIN windows w USING (session_id)
LEFT JOIN children ch USING (session_id)
ORDER BY s.session_id
