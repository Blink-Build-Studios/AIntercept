-- The values a session's row takes from other keys' rows (docs/PIPELINE.md v7 §4), joined when the
-- API loads it: sql/session_rows.sql's children and alone-title parts, moved here. Each session's
-- own values are rendered by its key (own_title; label_text_alone and children_json as if it had no
-- title calls and no children); these read:
--   _lbl_sessions     the page's sessions: session_id, start_ns, product, own_title, model_calls,
--                     call_accounts (its calls' accounts, as session_call_accounts holds them)
--   _lbl_title_calls  title calls of those accounts (a title thread's, another key's)
--   _lbl_edges        the edges whose parent is one of the sessions (each written by the child's key)
-- and give each session its children_json, its alone label (the session header and timeline) and its
-- label (the person pages: the sessions list and the timeline's bands).
-- Codex titles: a title thread's title call names the same account's Codex thread (one without a
-- title of its own) that started last before it, within five minutes; the calls in time order, each
-- naming a thread not yet named. That runs over an account-connected component of sessions with
-- their accounts' title calls (pgstore_person runs it once per component, as buckets.py ran
-- session_rows.sql); a page that reads only the alone label (a session's) gives it one session and
-- the title calls of its first five minutes, and doesn't read label_text. Alone, a thread takes the
-- earliest such call.
WITH RECURSIVE
cand AS (
    SELECT s.session_id, s.start_ns, s.call_accounts FROM _lbl_sessions s
    WHERE s.product = 'codex' AND s.own_title IS NULL AND s.model_calls > 0
),
eligible AS (
    SELECT t.ts_ns, t.call_id, t.title, c.session_id, c.start_ns
    FROM _lbl_title_calls t
    JOIN cand c ON list_contains(c.call_accounts, t.account_id)
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
children AS (
    SELECT parent_session_id AS session_id,
        '{"subagent":' || count(*) FILTER (WHERE kind = 'subagent') || ',"review":' || count(*) FILTER (WHERE kind <> 'subagent')
            || ',"sessionIds":' || to_json(list(child_session_id ORDER BY child_first_ns, child_session_id)) || '}' AS children_json
    FROM _lbl_edges GROUP BY parent_session_id
),
titled AS (
    SELECT s.session_id,
        coalesce(s.own_title, (SELECT title FROM named n WHERE n.session_id = s.session_id)) AS title,
        coalesce(s.own_title, (SELECT title FROM alone a WHERE a.session_id = s.session_id)) AS title_alone
    FROM _lbl_sessions s
)
SELECT
    ti.session_id, ch.children_json,
    CASE WHEN ti.title <> '' THEN ti.title ELSE 'Session ' || left(ti.session_id, 8) END AS label_text,
    CASE WHEN ti.title <> '' THEN 'title' ELSE 'none' END AS label_source,
    CASE WHEN ti.title_alone <> '' THEN ti.title_alone ELSE 'Session ' || left(ti.session_id, 8) END AS label_text_alone,
    CASE WHEN ti.title_alone <> '' THEN 'title' ELSE 'none' END AS label_source_alone
FROM titled ti
LEFT JOIN children ch USING (session_id)
