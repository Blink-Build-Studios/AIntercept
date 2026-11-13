-- The prompt list and the session marks rebuilt from the streaming processor's
-- stored rows (aintercept/internal/stream/state prompts.go): positions numbered,
-- and the values every row used to copy joined from prompt_session and
-- mark_prompt_index. The views have today's prompt_rows, prompt_steps and
-- session_marks columns, so the endpoints' SQL reads them unchanged.
--
-- Stored tables: prompt_items, prompt_steps, prompt_step_tools, prompt_session,
-- session_marks, mark_prompt_index. A step's tools are its request id's
-- (prompt_step_tools, joined on tools_rid), stored once however many steps share
-- the id; so are its commands and exit codes when its recorded bodies name none. Views: v_prompt_rows, v_prompt_steps, v_session_marks.

-- The stored rows keep today's row as a nested group (row, step, mark) beside
-- their ordering and lookup columns; these views flatten them.
CREATE OR REPLACE VIEW f_prompt_items AS SELECT unnest("row"), * EXCLUDE ("row") FROM prompt_items;
CREATE OR REPLACE VIEW f_prompt_steps AS SELECT unnest(step), * EXCLUDE (step) FROM prompt_steps;
CREATE OR REPLACE VIEW f_prompt_step_tools AS
SELECT session_id, tools_request_id, unnest(tools) FROM prompt_step_tools;
CREATE OR REPLACE VIEW f_session_marks AS SELECT unnest(mark), * EXCLUDE (mark) FROM session_marks;

CREATE OR REPLACE VIEW v_prompt_items AS
SELECT i.*,
       row_number() OVER (PARTITION BY i.session_id
                          ORDER BY i.t_ns, i.type, i."group", i.ord_corr, i.ord_seq, i.ord_row_ns,
                                   i.ord_name, i.ord_md, i.ord_key) AS seq_n
FROM f_prompt_items i;

CREATE OR REPLACE VIEW v_prompt_rank AS
SELECT session_id, prompt_id,
       row_number() OVER (PARTITION BY session_id ORDER BY t_ns, ord_key) AS prank,
       count(*) OVER (PARTITION BY session_id) AS pcount
FROM f_prompt_items
WHERE type = 'prompt';

CREATE OR REPLACE VIEW v_prompt_rows AS
SELECT i.session_id,
       i.seq_n AS seq,
       i.t_ns,
       i.type,
       CASE WHEN i.type = 'prompt' THEN i.has_input OR coalesce(s.product, '') = 'codex' ELSE false END AS typed,
       coalesce(s.last_ns, 0) AS session_last_ns,
       i.prompt_id,
       CASE
           WHEN i.type <> 'prompt' OR i.has_input THEN i.prompt_index
           WHEN s.product = 'codex' THEN p.prank
       END AS prompt_index,
       i.duration_turn_ms,
       CASE WHEN s.product = 'codex' THEN i.duration_report_ms END AS duration_report_ms,
       CASE
           WHEN i.type = 'prompt' THEN p.prank = p.pcount AND NOT i.has_turn_end
               AND NOT (coalesce(s.product, '') = 'codex' AND i.report_status)
           ELSE false
       END AS open_tail,
       i.unfinished_max_ns, i.text_retained, i.text, i.text_chars, i.pasted_bytes, i.images, i.steps,
       i.tokens_in, i.tokens_out, i.tool_counts_json, i.frustrated, i.interrupted, i.destructive,
       CASE WHEN s.product = 'codex' THEN i.turn_report_json END AS turn_report_json,
       i.hidden_json, i.between_kind, i.detail, i.request_id, i.tokens_from, i.tokens_to, i.wait_ms
FROM v_prompt_items i
LEFT JOIN prompt_session s ON s.session_id = i.session_id
LEFT JOIN v_prompt_rank p ON p.session_id = i.session_id AND p.prompt_id = i.prompt_id;

CREATE OR REPLACE VIEW v_prompt_steps AS
SELECT st.session_id,
       it.seq_n AS seq,
       row_number() OVER (PARTITION BY st.session_id, st.prompt_id ORDER BY st.t_ns, st.request_id) AS step,
       st.request_id, st.t_ns, st.end_ns, st.kind, st.model, st.request_class, st.tokens_input,
       st.tokens_cache_read, st.tokens_cache_write, st.tokens_cache_write_5m, st.tokens_cache_write_1h,
       st.tokens_output, st.tokens_accuracy, st.duration_ms, st.ttft_ms, st.stop_reason, st.cost_usd, st.failed,
       NOT st.has_success
           AND (NOT coalesce(s.has_prompt_event, false) OR s.last_prompt_event_ns <= st.pend_end) AS events_pending_if_live,
       st.agent_type,
       coalesce(t.tools_json, st.tools_json) AS tools_json,
       CASE WHEN t.tools_request_id IS NULL THEN st.tools_live_json ELSE t.tools_live_json END AS tools_live_json,
       st.does,
       CASE WHEN t.tools_request_id IS NULL OR st.detail_calls THEN st.commands ELSE t.commands END AS commands,
       CASE WHEN t.tools_request_id IS NULL OR st.detail_calls THEN st.exit_codes ELSE t.exit_codes END AS exit_codes,
       CASE WHEN t.tools_request_id IS NULL OR st.detail_calls THEN st.content_live ELSE t.content_live END AS content_live,
       CASE WHEN t.tools_request_id IS NULL OR st.detail_calls THEN st.commands_live ELSE t.commands_live END AS commands_live,
       CASE WHEN t.tools_request_id IS NULL OR st.detail_calls THEN st.exit_codes_live ELSE t.exit_codes_live END AS exit_codes_live,
       st.patches_json, st.text, st.usage_missing
FROM f_prompt_steps st
JOIN v_prompt_items it ON it.session_id = st.session_id AND it.type = 'prompt' AND it.prompt_id = st.prompt_id
LEFT JOIN prompt_session s ON s.session_id = st.session_id
LEFT JOIN f_prompt_step_tools t
       ON st.tools_rid <> '' AND t.session_id = st.session_id AND t.tools_request_id = st.tools_rid;

-- The marks: calls, reports, friction and compactions in their stored order,
-- then a permission segment per run of one mode, then a network segment per run
-- of one client IP over the calls; each segment ends where the next starts.
CREATE OR REPLACE VIEW v_mark_base AS
SELECT m.*,
       row_number() OVER (PARTITION BY m.session_id
                          ORDER BY m."group", m.t_ns, m.ord_corr, m.ord_seq, m.ord_row_ns, m.ord_name, m.ord_md,
                                   m.ord_key, m.sub) - 1 AS seq_n
FROM f_session_marks m
WHERE m."group" < 4;

CREATE OR REPLACE VIEW v_mark_perm AS
WITH pts AS (
    SELECT session_id, t_ns, mode,
           lag(mode) OVER w AS before,
           row_number() OVER w AS n
    FROM f_session_marks
    WHERE "group" = 4
    WINDOW w AS (PARTITION BY session_id ORDER BY t_ns, ord_corr, ord_seq, ord_row_ns, ord_name, ord_md, ord_key)
), segs AS (
    SELECT session_id, t_ns, mode, n FROM pts WHERE before IS NULL OR before <> mode
)
SELECT session_id, t_ns, mode,
       lead(t_ns) OVER w AS to_ns,
       lag(mode) OVER w AS prev_mode,
       row_number() OVER w - 1 AS k
FROM segs
WINDOW w AS (PARTITION BY session_id ORDER BY n);

CREATE OR REPLACE VIEW v_mark_net AS
WITH pts AS (
    SELECT session_id, t_ns, point_ip AS ip,
           lag(point_ip) OVER w AS before,
           row_number() OVER w AS n
    FROM f_session_marks
    WHERE "group" = 0 AND point_ip <> ''
    WINDOW w AS (PARTITION BY session_id ORDER BY t_ns, ord_key)
), segs AS (
    SELECT session_id, t_ns, ip, n FROM pts WHERE before IS NULL OR before <> ip
)
SELECT session_id, t_ns, ip,
       lead(t_ns) OVER w AS to_ns,
       row_number() OVER w - 1 AS k
FROM segs
WINDOW w AS (PARTITION BY session_id ORDER BY n);

CREATE OR REPLACE VIEW v_mark_counts AS
SELECT session_id,
       count(*) FILTER (WHERE "group" < 4) AS base
FROM f_session_marks
GROUP BY session_id;

CREATE OR REPLACE VIEW v_session_marks AS
SELECT b.session_id, b.seq_n AS seq, b.kind, b.t_ns, b.to_ns, b.request_id, b.request_class, b.model, b.context,
       b.output,
       CASE WHEN b.prompt_ref <> '' THEN x.prompt_index ELSE b.prompt_index END AS prompt_index,
       b.input, b.cache_read, b.cache_write, b.prompt_id, b.unreported, b.friction_kind, b.detail, b.tokens_from,
       b.tokens_to, b.idle, b.wait_ms, b.mode, b.prev_mode, b.ip, b.ipv6
FROM v_mark_base b
LEFT JOIN mark_prompt_index x ON x.session_id = b.session_id AND x.prompt_id = b.prompt_ref
UNION ALL
SELECT p.session_id, c.base + p.k AS seq, 'permission' AS kind, p.t_ns, p.to_ns, NULL, NULL, NULL, NULL, NULL, NULL,
       NULL, NULL, NULL, NULL, false, NULL, NULL, NULL, NULL, false, NULL, p.mode, p.prev_mode, NULL, false
FROM v_mark_perm p
JOIN v_mark_counts c ON c.session_id = p.session_id
UNION ALL
SELECT n.session_id, c.base + coalesce(pc.n, 0) + n.k AS seq, 'network' AS kind, n.t_ns, n.to_ns, NULL, NULL, NULL,
       NULL, NULL, NULL, NULL, NULL, NULL, NULL, false, NULL, NULL, NULL, NULL, false, NULL, NULL, NULL, n.ip,
       contains(n.ip, ':') AND NOT starts_with(lower(n.ip), '::ffff:') AS ipv6
FROM v_mark_net n
JOIN v_mark_counts c ON c.session_id = n.session_id
LEFT JOIN (SELECT session_id, count(*) AS n FROM v_mark_perm GROUP BY session_id) pc ON pc.session_id = n.session_id;
