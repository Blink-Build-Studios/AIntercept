-- Shared ETL transform: raw phase-events -> transactions -> multi-resolution
-- rollups. Written in portable SQL so the SAME logic runs in DuckDB today
-- (etl/local_rollup.py, no Databricks) and Spark SQL on Databricks later.
--
-- Expects a view/table `raw_events` with the raw NDJSON columns. Produces
-- `transactions`, `rollup_1m`, and `rollup_1h`.
--
-- Cost is derived by joining `model_prices` (per-model USD per 1M tokens); a
-- missing price yields NULL cost (surfaced as "unpriced", never silently zero).

-- ── Stage 1: collapse phase-events into one row per transaction ───────────────
-- max(col) ignores NULLs, so for the identity/context fields (which appear on
-- exactly one phase) it acts as "the single non-null value", while sidestepping
-- the binder collision DuckDB raises when a FILTER clause names an output alias
-- that matches its input column.
CREATE OR REPLACE TABLE transactions AS
SELECT
    correlation_id,
    -- identity / request context (carried on request_headers)
    max(host)      AS host,
    max(method)    AS method,
    split_part(max(path), '?', 1) AS endpoint,
    max(key_hash)  AS key_hash,
    max(key_hint)  AS key_hint,
    max(ip)        AS ip,
    max(headers['user-agent']) AS user_agent,
    -- outcome (status lands on response_headers; 0 elsewhere)
    max(status) AS status,
    -- model + tokens (land on the complete event)
    max(model) AS model,
    max(input_tokens)       AS input_tokens,
    max(output_tokens)      AS output_tokens,
    max(cache_read_tokens)  AS cache_read_tokens,
    max(cache_write_tokens) AS cache_write_tokens,
    -- byte totals
    sum(CASE WHEN direction = 'inbound'  THEN bytes ELSE 0 END) AS request_bytes,
    sum(CASE WHEN direction = 'outbound' THEN bytes ELSE 0 END) AS response_bytes,
    -- timing
    min(timestamp_ns) AS ts_start_ns,
    max(timestamp_ns) AS ts_end_ns,
    (max(timestamp_ns) - min(timestamp_ns)) / 1e6 AS latency_ms,
    to_timestamp(min(timestamp_ns) / 1e9) AS ts,
    -- tool visibility (content-free arrays on the complete event)
    any_value(tools_available) FILTER (WHERE tools_available IS NOT NULL) AS tools_available,
    any_value(tool_calls)      FILTER (WHERE tool_calls      IS NOT NULL) AS tool_calls,
    -- provenance / completeness
    max(observer_instance) AS observer_instance,
    max(CASE WHEN phase = 'complete' THEN 1 ELSE 0 END) = 1 AS completed,
    max(error) AS error
FROM raw_events
GROUP BY correlation_id;

-- ── Derived dimensions on transactions (provider, client_app) ────────────────
CREATE OR REPLACE VIEW transactions_enriched AS
SELECT
    t.*,
    CASE host
        WHEN 'api.anthropic.com' THEN 'anthropic'
        WHEN 'api.openai.com' THEN 'openai'
        WHEN 'generativelanguage.googleapis.com' THEN 'google'
        ELSE 'unknown'
    END AS provider,
    CASE
        WHEN lower(coalesce(user_agent, '')) LIKE '%claude-code%' THEN 'claude-code'
        WHEN lower(coalesce(user_agent, '')) LIKE '%claude code%' THEN 'claude-code'
        WHEN lower(coalesce(user_agent, '')) LIKE '%anthropic%'   THEN 'anthropic-sdk'
        WHEN lower(coalesce(user_agent, '')) LIKE '%codex%'       THEN 'openai-client'
        WHEN lower(coalesce(user_agent, '')) LIKE '%openai%'      THEN 'openai-client'
        WHEN lower(coalesce(user_agent, '')) LIKE '%curl%'        THEN 'curl'
        WHEN coalesce(user_agent, '') = ''                        THEN 'unknown'
        ELSE split_part(lower(user_agent), '/', 1)
    END AS client_app,
    -- cost in USD, derived from tokens x per-model price (NULL if unpriced).
    -- Clients pin DATED model ids (claude-opus-4-8-20260115); the price table is
    -- keyed on the bare family, so we strip a trailing -YYYYMMDD date suffix
    -- before the join. Without this, every pinned model silently costs $0.
    (coalesce(input_tokens, 0)       * p.input_usd_per_mtok  / 1e6)
  + (coalesce(output_tokens, 0)      * p.output_usd_per_mtok / 1e6)
  + (coalesce(cache_read_tokens, 0)  * p.cache_read_usd_per_mtok  / 1e6)
  + (coalesce(cache_write_tokens, 0) * p.cache_write_usd_per_mtok / 1e6)
        AS cost_usd,
    -- whether this transaction's model matched the price table (for an honest
    -- "N calls unpriced" indicator downstream).
    (p.model IS NOT NULL) AS priced
FROM transactions t
LEFT JOIN model_prices p
    ON p.model = regexp_replace(t.model, '-[0-9]{8}$', '');

-- ── Stage 2a: finest-grain rollup (per MINUTE) ───────────────────────────────
-- The forensics console's fine zoom. Every slice dimension is in the key.
CREATE OR REPLACE TABLE rollup_1m AS
SELECT
    date_trunc('minute', ts) AS bucket,
    provider,
    model,
    client_app,
    key_hash,
    any_value(key_hint) AS key_hint,
    count(*)                       AS transactions,
    count(*) FILTER (WHERE status >= 200 AND status < 300) AS ok_count,
    count(*) FILTER (WHERE status >= 400) AS error_count,
    sum(coalesce(input_tokens, 0))       AS input_tokens,
    sum(coalesce(output_tokens, 0))      AS output_tokens,
    sum(coalesce(cache_read_tokens, 0))  AS cache_read_tokens,
    sum(coalesce(cache_write_tokens, 0)) AS cache_write_tokens,
    sum(cost_usd)                        AS cost_usd,
    sum(request_bytes + response_bytes)  AS bytes_total,
    approx_quantile(latency_ms, 0.5)     AS p50_latency_ms,
    approx_quantile(latency_ms, 0.95)    AS p95_latency_ms
FROM transactions_enriched
GROUP BY 1, 2, 3, 4, 5;

-- ── Stage 2b: hourly rollup, derived FROM the minute rollup (never re-scans raw)
CREATE OR REPLACE TABLE rollup_1h AS
SELECT
    date_trunc('hour', bucket) AS bucket,
    provider,
    model,
    client_app,
    key_hash,
    any_value(key_hint) AS key_hint,
    sum(transactions)       AS transactions,
    sum(ok_count)           AS ok_count,
    sum(error_count)        AS error_count,
    sum(input_tokens)       AS input_tokens,
    sum(output_tokens)      AS output_tokens,
    sum(cache_read_tokens)  AS cache_read_tokens,
    sum(cache_write_tokens) AS cache_write_tokens,
    sum(cost_usd)           AS cost_usd,
    sum(bytes_total)        AS bytes_total
FROM rollup_1m
GROUP BY 1, 2, 3, 4, 5;

-- ── Stage 3: explode tool usage into a per-(transaction, tool) fact ───────────
-- Screens A & B are powered by tool-level facts. The driving signal is
-- `tools_available` — every tool DECLARED on a request, i.e. "which MCP
-- servers/tools are in use this window" (Screen A's core question). We left-join
-- the per-request call counts from `tool_calls` (historical tool_use), so a tool
-- that is available carries call_count when the model actually invoked it and 0
-- otherwise. One row per (correlation_id, tool). Slice dimensions ride along so
-- the facts filter identically to every other view.
CREATE OR REPLACE TABLE tool_facts AS
WITH calls AS (
    SELECT t.correlation_id, tc.name AS tool_name, sum(tc.count) AS call_count
    FROM transactions_enriched t, UNNEST(t.tool_calls) AS u(tc)
    WHERE t.tool_calls IS NOT NULL
    GROUP BY 1, 2
)
SELECT
    t.correlation_id,
    t.ts,
    t.provider,
    t.model,
    t.client_app,
    t.key_hash,
    t.key_hint,
    ta.server AS mcp_server,
    ta.tool   AS tool,
    ta.name   AS tool_name,
    ta.kind   AS tool_kind,
    coalesce(c.call_count, 0) AS call_count
FROM transactions_enriched t,
     UNNEST(t.tools_available) AS u(ta)
LEFT JOIN calls c
    ON c.correlation_id = t.correlation_id AND c.tool_name = ta.name
WHERE t.tools_available IS NOT NULL;

-- ── Stage 3a: per-MINUTE tool rollup (powers Screen A chart + Screen B) ──────
-- Keyed by every slice dimension plus server+tool. Distinct counts are exact
-- here (small cardinality); at scale these become HLL sketches.
CREATE OR REPLACE TABLE tool_rollup_1m AS
SELECT
    date_trunc('minute', ts) AS bucket,
    provider,
    model,
    client_app,
    mcp_server,
    tool,
    tool_kind,
    sum(call_count)                  AS tool_calls,
    count(DISTINCT correlation_id)   AS transactions,
    count(DISTINCT key_hash)         AS distinct_users,
    count(DISTINCT client_app)       AS distinct_client_apps
FROM tool_facts
GROUP BY 1, 2, 3, 4, 5, 6, 7;

-- ── Stage 3b: hourly tool rollup, derived from the minute grain ──────────────
-- tool_calls / transactions sum correctly across sub-buckets. A DISTINCT count
-- (users) cannot be derived from per-minute distincts — summing over-counts,
-- max() under-counts — so we expose max() as `distinct_users_lb` (a lower bound,
-- honestly named) rather than a wrong `distinct_users`. An exact hourly distinct
-- must be recomputed from tool_facts (which the console does for live views).
CREATE OR REPLACE TABLE tool_rollup_1h AS
SELECT
    date_trunc('hour', bucket) AS bucket,
    provider, model, client_app, mcp_server, tool, tool_kind,
    sum(tool_calls)      AS tool_calls,
    sum(transactions)    AS transactions,
    max(distinct_users)  AS distinct_users_lb
FROM tool_rollup_1m
GROUP BY 1, 2, 3, 4, 5, 6, 7;
