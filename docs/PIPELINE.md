# Data pipeline (v7)

Status: **proposed, for the owner's sign-off; revised after the independent
review.** This follows the owner's and co-founder's decision of 2026-11-13 to
keep the queryable data in Postgres.

Already built and kept:
- §1 ingest, from v5, except how it announces new files;
- the per-key state engine (#19) and its S3 checkpoint;
- 6 h expiry (#39);
- the Python oracle (#42);
- the session render functions, `state.RenderSession` (#44).

Postgres replaces the rest of v5 and v6:
- the key commit objects and their create-only fencing;
- each key's `out/` tables;
- the merge;
- Redis;
- v6's view objects, person loop and dispatch (specified, never built).

`docs/STREAMING_PIPELINE.md` is kept only as history.

## Goals

- **Solve today's problems.** Today that's a handful of people. 1,500 is the
  design target, and the budget below is checked against it, but no mechanism
  gets built for it before real traffic needs it. Keep the mechanisms to a
  minimum.
- **Raw data in S3, queryable rows in Postgres.**
  - Ingest writes the per-key files, the calls' detail objects and a recent
    index to S3. They never collide, and their layout isn't shaped by the
    queries.
  - Workers turn them into rows in Postgres.
  - Request and response bodies stay in S3. Postgres holds the call id that
    names them.
- **Ingest never talks to Postgres.** It has no connection, DSN or login.
  Workers discover new files by tailing the recent index (§2).
- **The worker precomputes, the API reads.**
  - Write time does everything that depends only on the data of one key.
  - The API does what depends on the request: the range, filters, sort, page,
    bin, and values relative to now.
  - Values that need other keys' rows are joined at read time when the join is
    bounded by the page's entity and indexed (§4).
  - The API runs today's endpoint code over the rows it reads.
- **Delta in, correct eventual state out.** A commit writes only the rows that
  changed, never O(history). Rendering a session costs O(session) CPU, as
  accepted in v6.
- **One transaction per commit.** A key's rows and its next commit number land
  together or not at all. The number orders a key's commits, so a stalled or
  duplicate worker can't write. This replaces the create-only commit objects,
  the ETag fencing and the Redis locks.
- **Response bodies don't change.** Every endpoint returns byte-identical JSON,
  and the URLs stay the same. The exceptions are in "Decisions and quirks"; the
  only one the UI sees is the in-flight drawer (the owner's call).
- **Every tier scales horizontally:** ingest on CPU, workers on lag, the API on
  CPU. Postgres is the one component that scales vertically: one primary,
  sized in the scaling budget.
- **Correct under any timing.** None of these may ever lose a record or count
  one twice:
  - retries and crashes;
  - late or duplicate data;
  - a stalled worker;
  - a dropped Postgres connection;
  - Postgres being down. Ingest doesn't notice, and the workers catch up from
    S3.
- **Infrastructure:**
  - S3 holds raw data, per-key files, state checkpoints, the recent index and
    call bodies.
  - Postgres runs on RDS in production, in the cluster on the DigitalOcean demo,
    and in Docker Compose locally.
  - Redis is gone.

## Keys

Unchanged. Every record carries a **routing key** (`records.RoutingKey`, #18):

| Record | Key |
|---|---|
| has a session id (Claude Code, Codex, analytics events) | `s:<session id>` |
| an analytics request's own call | the session of the batch's earliest event |
| no session, but a credential (catalog, flags, usage polls) | `c:<credential hash>:<day>` |
| no session, no credential | `ip:<source ip>:<hour>` |
| account sighting (name, email, org) | `a:<account id>` |

A session is never split across keys, so every row a session's tables hold is
written by its own key.

A **person** is an email group (today's `person_map`). The API's person id stays
the group's leader account, as today.

## Data flow

```
Envoy + observer (ingest) ── per-key files, call detail, recent index (5 s) ──▶ S3
                                                                                 │
   workers: the tail (one holder) ── LIST/GET v1/recent/ from its cursor ───────┤
        │ flags + cursor, one transaction                                        │
        ▼                                                                        │
Postgres dirty_keys ◀── claim ── workers (`rollup`) ◀── LIST/GET in/, state chunks
                                   │ one transaction: keys.n + 1, rows, flag
                                   ▼
                               Postgres ◀── API: one connection, one snapshot, a few indexed statements
                                             → in-memory DuckDB → today's endpoint code
                                             (+ calls/<id>.ndjson.gz from S3 for the drawer)
```

## S3 layout

```
raw/dt=…/hour=…/instance=…/…ndjson.gz          raw capture (unchanged; nothing reads it)
calls/<call id>.ndjson.gz                       per-call detail (unchanged): the drawer's raw view
v1/keys/<key>/in/<s3-time>-<incarnation>-<seq>.parquet   ingest output, one file per key per flush (unchanged)
v1/keys/<key>/state/<n>-<nonce>                 the key's state checkpoint chunks (unchanged; keys.state names them)
v1/recent/<s3-time>-<incarnation>-<seq>.json    per pod every 5 s: the keys whose files it uploaded, its newest upload time
```

- **The recent index changes in two ways.**
  - Its objects are named by S3 time to the second, like `in/` files. They used
    to sit in a folder per minute.
  - A pod uploads one every 5 s instead of every minute.

  The time-ordered names let one cursor tail them (§2).
- **Gone:**
  - each key's `commit/` and `out/`;
  - `v1/merge/`, `v1/reported/` and `v1/content_retained`;
  - `calls/<id>.keys/`;
  - `v2/` (planned, never written).
- **The state checkpoint stays in S3.** It's the key's journal of applied
  records (`state.Encode`), compacted in tiers of 8 as built.
  - Its size is the key's records, and only the worker loading the key reads it.
  - Postgres holds the chunk names in the key's row.
  - Moving it into Postgres would copy every record into the database for no
    query.

## 1. Ingest: the Envoy + observer pod, scaled on CPU

Unchanged from v5, except step 4:
- records and their routing keys;
- per-key buffers (1 MiB or 30 s);
- the spool;
- uploads named by S3 time;
- shutdown;
- the raw ndjson and `calls/` objects;
- failing open in observe mode and closed in enforce mode.

4. **After each upload, the key goes into the pod's recent index.**
   - **The pod journals the key in its spool before the upload,** fsynced with
     the spool. The key goes into the next index object once the upload has
     completed.
   - **It uploads the index every 5 s,** as
     `v1/recent/<s3-time>-<incarnation>-<seq>.json`: the keys, plus the pod's
     newest upload time for `/status`.
     - It also uploads at shutdown.
     - At start, it uploads whatever a crash left in the journal. A key named
       for a file that never landed is harmless: its run finds nothing and
       clears the flag (§2).
   - **Ingest has no Postgres connection, DSN or login, and no Redis.** An
     outage of either can't touch traffic or lose a flag.
   - **The one gap:** losing the node, and with it the spool, within 5 s of an
     upload. That key isn't flagged until its next file. The file isn't lost:
     it's in S3, waiting (Not now: a periodic full listing).

## 2. Workers: the `rollup` Deployment, scaled on lag, at least 2 replicas

The name stays: renaming gains nothing. Each replica holds:
- one lock connection, which holds its advisory locks and its heartbeat;
- at most two run connections, shared by its runs (`parallel`, default 8).

**A run holds a connection only for its key-row read and its commit,** never
during S3 I/O or rendering.

**Claim.** A flag is *claimable* unless its key is quarantined:
`failures < 5 OR failed_at < now() − 1 h`.
1. **Run the keys it already holds that are flagged:** `SELECT key, seq FROM
   dirty_keys WHERE key = ANY($held) AND <claimable>`.
2. **Then take new keys,** at most ceil(dirty keys / live workers) per tick.
   - The candidates: `(SELECT key FROM dirty_keys WHERE <claimable> ORDER BY
     since LIMIT 4) UNION (SELECT key FROM dirty_keys WHERE <claimable> ORDER BY
     hashtext(key || $worker) LIMIT 64)`, minus the keys it holds.
   - It tries each with `pg_try_advisory_lock(<the key's 64-bit hash>)` on its
     lock connection, one statement for the batch.
   - The per-worker order spreads workers over different keys, so they don't
     herd onto the same few. The oldest few keep the oldest key from starving.
3. **Live workers come from heartbeats.** Each worker upserts its row in
   `workers` every 10 s, and live means seen in the last 30 s. No `pg_locks` or
   `pg_stat_activity` reads.
4. **A held key stays warm,** its state kept in memory, until it has had no
   input for 60 s, then it's unlocked. All keys are unlocked on SIGTERM.
5. **If the lock connection drops, the worker forgets every key it holds,**
   along with their warm states. Postgres has freed the locks, so another worker
   may already hold them.
6. **The lock is for affinity only.** It saves duplicate work and cold loads (a
   cold load replays the key's journal). Correctness never depends on it: the
   guarded commit (step 8) does.

**Run** (one key, `parallel` at once):
1. **Read the key row and the flag in one round:** `SELECT … FROM keys WHERE key
   = $1` and `seq0 = SELECT seq FROM dirty_keys WHERE key = $1`. With no row,
   `n` is 0; with no flag, `seq0` is 0. The flag is read before the LIST, so a
   file flagged after this point is seen either by this LIST or by the next run.
2. **LIST** `in/` after (last applied − 5 min grace), skipping the grace-window
   names the row records, and keep at most 500 files (v5 §2 step 4, unchanged).
3. **Nothing new and no expiry due:** consume the flag with `DELETE FROM
   dirty_keys WHERE key = $1 AND seq = $seq0`, its own statement, and stop. The
   state isn't loaded. Data time for expiry is read once per tick, not per run.
4. **Load the state.**
   - A warm state at the same `n` is used as is.
   - Otherwise, load cold: GET the chunks the row names and `state.Decode`
     them.
   - For a session key, a cold load also renders the state and hashes the rows:
     the baseline. Those are the rows committed with that state, as long as
     `keys.render_version` matches this worker's. Postgres is never read for a
     baseline.
5. **GET the files and apply them** with the state engine (#19), sorted by time
   and record id. Expire due calls (below). Collect the run's state deltas,
   keeping each (table, id)'s last change.
6. **Render**, for a session key: `state.RenderSession` (#44), every table
   whole.
   - Hash each row (xxhash64 of its encoding) and compare with the baseline.
     Changed and new rows become upserts, rows that are gone become deletes.
   - If `keys.render_version` differs from this worker's, delete the session's
     rendered rows and write them all.
   - Every key also renders its directory folds and agent calls (§3.5), and its
     batch parts (§3.4).
   - The render's hashes become the next baseline. They're kept in memory only.
7. **Write the state chunk** `state/<n+1>-<nonce>` and compact, as v5 (the
   `out/` half is gone).
8. **Commit in one transaction,** holding a run connection only now:
   1. **The guard first:** `UPDATE keys SET n = n + 1, state = …, last_applied =
      …, grace = …, garbage = …, expires_ns = …, newest_row_ns = …, day = …,
      first_request_ns = …, earliest_ns = …, content_retained = …,
      render_version = …, updated_ns = … WHERE key = $1 AND n = $2`. A new key's
      first commit instead runs `INSERT … n = 1 ON CONFLICT DO NOTHING`.
      - If no row matches, another worker committed. Roll back before writing
        anything, drop the warm state and its baseline, and move on.
      - The guard takes the key row's lock first, so concurrent commits
        serialize there.
   2. **The rows:** the stored-input deltas and the rendered diff. Each table
      gets one `INSERT … SELECT * FROM unnest($1, $2, …) ON CONFLICT (primary
      key) DO UPDATE` and one `DELETE … WHERE (primary key) IN (SELECT * FROM
      unnest(…))`, pipelined as one pgx batch.
   3. **The flag:**
      - the LIST reached the end and `seq` hasn't moved: `DELETE FROM dirty_keys
        WHERE key = $1 AND seq = $seq0`;
      - the 500-file cap cut the run short: keep the flag, with `since` set to
        the S3 time of the first file left;
      - a flag came in during the run (`seq` moved): keep it, with `since` set to
        the run's start.
   4. **`COMMIT`,** and reset the key's failure count.
9. **Delete due garbage** (superseded chunks, 10 minutes after they were
   replaced), as v5. A worker deletes only names read from the key row.

**Failures and quarantine.** A run that fails with an error records it in its
own statement: `UPDATE dirty_keys SET failures = failures + 1, last_error = $e,
failed_at = now()`. A lost guard isn't an error.
- After 5 consecutive failures the key is **quarantined**:
  - claims skip it;
  - `/lag` ignores it;
  - the `quarantined_keys` gauge alarms.
- A quarantined key is retried once an hour, and a successful commit releases
  it.
- So one bad key can never hold up the rest or pin the lag.

**Timing.** Each case ends in the rows one pass would write.
- **A crash before COMMIT.** Postgres aborts the transaction and frees the lock.
  The flag is still there. Another worker loads the row's `n` cold and applies
  the same files again. The orphaned chunk is deleted at the next compaction
  (v5's orphans).
- **A crash after COMMIT.** The rows and `n + 1` are in. The garbage waits for
  the key's next run.
- **A COMMIT whose result is unknown** (the connection dropped). The worker
  drops the warm state and flags the key again. The next run reads whichever
  `n` landed.
- **A stalled worker** (a GC pause or a partition). Keepalives (§6) make
  Postgres close its lock connection within about 30 s. Another worker takes the
  key, and whichever commits second fails the guard.
- **Postgres down.** Runs and the tail fail and retry with backoff. Nothing
  commits and nothing is lost: the files and the recent index stay in S3, and
  the tail's cursor resumes where it was.
- **Late, duplicate or out-of-order files:** as v5 (the grace window and the
  order-free state).

**Calls that never end** (#39, as built). A call with no ending is written
without one once data time passes its start + 6 h (a WebSocket connection's:
7 d).
- Data time is `max(keys.newest_row_ns)`, read through an index once per tick.
- The key row keeps `expires_ns`.

**Singletons.** Each runs on whichever worker holds its advisory lock (a fixed
id). No new component.
- **The tail, every 2 s:** the discovery. It replaces v5's 10-minute sweep and
  ingest's flags.
  - It LISTs `v1/recent/` after (its cursor − 5 min grace), skipping the names
    its row records inside the grace window (the key cursor's rule), and GETs
    each new index object.
  - In one transaction it:
    - upserts a flag for every key named (`INSERT … ON CONFLICT (key) DO UPDATE
      SET seq = dirty_keys.seq + 1`);
    - upserts each pod's newest upload time into `ingest_pods`;
    - advances its cursor (the `tail` row: last name, grace names,
      `checked_at`).
  - The cursor advances only together with its flags, so an outage or a crash
    makes it read again, never skip.
- **Every 30 s, due calls:** `INSERT INTO dirty_keys SELECT key FROM keys WHERE
  expires_ns > 0 AND expires_ns < <data time> ON CONFLICT … seq + 1`. It
  replaces the merge's flagging.
- **Every hour, retention** (§3.6).

**Scaling.** `/lag` is the larger of two ages:
- `now() − min(since)` over the flags with `failures < 5` (an index);
- `now() − tail.checked_at`, so a stuck tail shows as lag too.

It's the same number on every replica. KEDA's `metrics-api` trigger reads it,
unchanged: target 30 s, at least 2 replicas. If Postgres is unreachable,
`/lag` errors and KEDA holds the replica count.

## 3. Postgres schema

### 3.1 Conventions

- **PostgreSQL 17, built-in `C.UTF-8` locale** (`LOCALE_PROVIDER builtin`).
  - Text sorts in code-point order, which is UTF-8 byte order, as in Go and
    DuckDB. So `min(name)` and `ORDER BY session_id` agree everywhere.
  - `lower()` uses Unicode simple case mapping, as DuckDB's does, so an email
    group found in Postgres is the group `person_sql` finds. A test checks
    `lower()` against DuckDB on every assigned code point.
- **Types:**
  - Times stay `bigint` Unix ns, never timestamp types, so nothing converts
    between Go, Postgres and DuckDB.
  - Floats are `double precision`, exact through the binary protocol.
  - Lists are arrays (`text[]`, `bigint[]`).
  - Today's nested structs are flattened into columns (`row_<column>`,
    `plan_v`, …) and rebuilt as structs when the API loads them:
    `request_txs.row`, `request_missed.row`, and the dimension tables' picks
    `STRUCT(v, ts, ok)`.
- **No NUL and no oversized ids reach Postgres.** Postgres text can't hold NUL,
  and a btree entry over about 2.7 KB fails the insert, so either would wedge a
  key.
  - The state's row ids are `bytea`. The state joins id parts with NUL, which
    the values never contain after the next rule.
  - Canon (records) replaces NUL in every string with U+FFFD.
  - Canon also replaces any client-controlled id over 512 bytes with
    `sha256:<hex>`: session, request, call, event and prompt ids.
  - Both rules apply where records are built, so the state, the oracle and the
    API all see the same values. Only pathological input changes.
  - Quarantine (§2) is the backstop for anything else.
- **Columns are today's names and types:**
  - `process/tables.go` and `state.TableTypes`;
  - the oracle's `_EMPTY` shapes.

  A few extra columns are never part of an API table: `key` (the owner), and
  the per-table extras listed below.
- **One owner per row.** Every row belongs to one routing key, and only that
  key's commit writes it.
  - Stored inputs are keyed by (key, the state's row id).
  - A session's rendered rows are its own key's.
  - Batch parts are keyed by (batch, key).

  No two keys ever write one row.
- **No partitioning:** plain tables with indexes (Not now: partitions by day,
  with the numbers that call for them).
- **One embedded schema file,** `aintercept/internal/pg/schema.sql`.
  - The workers apply it to an empty database, under an advisory lock, and
    record its hash in `schema_version`.
  - The workers and the API refuse to start on a mismatch.
  - There are no migrations. A schema change means a fresh database, which the
    owner accepts (Not now).
- **Two logins:**
  - `aintercept_worker` owns the schema;
  - `aintercept_api` has `SELECT`.

### 3.2 Coordination

| Table | Columns | Primary key; indexes |
|---|---|---|
| `keys` | key, n, state `text[]`, state_levels `int[]`, last_applied, grace `text[]`, garbage `jsonb`, render_version, updated_ns, newest_row_ns, expires_ns, day, first_request_ns, earliest_ns, content_retained | key; (day); (newest_row_ns); partial (expires_ns) where > 0, (first_request_ns) and (earliest_ns) where > 0, (content_retained) where true |
| `dirty_keys` | key, seq, since `timestamptz`, failures, last_error, failed_at `timestamptz` | key; (since) |
| `workers` | worker, seen `timestamptz` | worker |
| `tail` | one row: last_name, grace `text[]`, checked_at `timestamptz` | — |
| `ingest_pods` | incarnation, last_ns, at (the index object's S3 time) | incarnation |
| `schema_version` | hash | — |

`keys` is v5's commit JSON (n, the state chunks and their levels, the cursor,
the grace names, the garbage). It also holds what the merge used to fold, each
computed by the key's run at commit:
- `day`: `DayOf`, the day of the key's newest `last_ns`/`ts_ns` over its rows
  (`merge.go:492`'s rule);
- `first_request_ns`, `earliest_ns` and `content_retained`, for `/status`;
- `newest_row_ns`: the newest raw row its files carried.

### 3.3 Session tables: rendered

The session's key writes the rows of `state.RenderSession`: today's columns,
order and seq. Two changes keep commits O(changed), and both are filled back
when the API loads the rows, so the API sees today's rows:
- **`prompt_rows.session_last_ns` is stored as 0.** It's kept once per session,
  as `session_rows.prompt_last_ns`. The render copies it onto every prompt row,
  so storing it there would rewrite them all on every run.
- **`session_marks.seq` is re-keyed per block** as `block << 32 | rank in the
  block`. The blocks follow the render: calls; reported and missed; friction;
  compaction; permission; network.
  - The render numbers marks across blocks, so each new call would renumber
    every later block.
  - The new key sorts as today. The API only orders by it, and only within a
    kind.

The other tables keep the render's dense seq. They're in time order, so new
rows append. A late row renumbers the rows after it: normally the last few
seconds (Not now: hour order keys).

| Table | Primary key | Rows/day at 1,500 people | Columns the API doesn't load |
|---|---|---|---|
| `session_rows` | session_id | ~6k new; each active session's row updated on every run | own_title, prompt_last_ns, key_day (its key's `day`). Labels hold the session's own (`SessionRowsOf`); children_json is NULL (§4) |
| `prompt_rows` | (session_id, seq) | ~0.2M | — |
| `prompt_steps` | (session_id, seq, step) | ~1.5M | — |
| `session_marks` | (session_id, seq) | ~3.5M | — |
| `event_rows` | (session_id, seq) | ~75M | batch (the `event_id` prefix: the summary's count) |
| `presence_segments` | (session_id, seq) | small | — |
| `session_tools` | (session_id, kind, server, tool) | small | — |
| `call_tool_uses` | (session_id, ts_ns, kind, server, tool) | ~3M | — |
| `session_request_rows` | (scope_session_id, seq) | ~7.5M | The session's own rows; other keys' are joined at read time (§4) |

- **Extra indexes:**
  - `session_rows (key_day)`;
  - partial `session_marks (session_id) WHERE kind = 'missed'`;
  - partial `event_rows (session_id, name, ts_ns) WHERE name IN
    ('tengu_tool_search_mode_decision', 'tengu_skills_sync_success')`, for the
    summary's `_LATEST` carry.
- **Every other read is by the session prefix of the primary key,** filtered by
  time in Postgres.

### 3.4 Stored inputs: the state's own rows

These are written straight from the run's state deltas: an upsert for a row, a
delete for a tombstone.
- **No diff.** The state already names only the rows that changed (its
  `rowSet`), so this is O(changed).
- **They're exactly the per-key rows the oracle reads today** (`st_*`), keyed
  by (key, the state's row id). Read-time SQL over them sees today's inputs.

| Table (state table) | Read by | Extra indexes |
|---|---|---|
| `dim_account`, `dim_session_account`, `dim_session_visible`, `dim_session_person`, `dim_person_device`, `dim_session_credential`, `dim_account_events`, `dim_account_device`, `dim_codex_plan` | person scope (§4) | (account_id) or (person_id); `dim_account (lower(email))`; (session_id) on `dim_session_account` and `dim_session_credential` |
| `request_txs` | the request logs | (key_hash, row_ts_ns), (key_hash, prompt_id), (call_id) |
| `prompt_index`, `codex_turns`, `session_last` | the request logs | (session_id, prompt_id) |
| `request_reports` | the request logs | (req_id), (client_req_id) |
| `request_missed` | the request logs, the reported drawer | (session_id), (row_request_id) |
| `session_call_accounts` | labels | (account_id), (session_id) |
| `session_edges` | children_json | (parent_session_id) |
| `session_title_calls` | labels | (account_id, ts_ns) |
| `requests` (`fact_requests`) | the drawer | (call_id) |
| `analytics` (`fact_events`) | the drawer, `/events/{id}` | (correlation_id), (key, name), both for the drawer |

- **Facts.**
  - `fact_pending` isn't stored: the drawer 404s for a call in flight (owner).
  - `analytics` keeps every event for now. Schema changes are wipes, and the
    owner accepts discarding data, so narrowing it later costs nothing (Not
    now).
  - `analytics.env_json` isn't stored: no API code reads it (`store.py` exposes
    it as `env`, which no query selects).
  - `metadata_json` is most of an analytics row. It's compressed in line (lz4,
    `toast_tuple_target = 128`).
  - `fact_probes` folds into `keys.earliest_ns`.
- **`batch_members` isn't stored:** it's a row per event.
  - Instead each key writes its **batch parts**: `batch_parts (batch_id, key, n,
    min_name, first_ns, first_session)` holds the key's events of that batch:
    their count, their least non-empty name, and the earliest (ns, session)
    that has a session.
  - Primary key (batch_id, key); index (key).
  - Read in §4. #43's stamps on the batch call stay, unused.

### 3.5 Directory folds

Every key writes these from its render or its facts:

| Table | Primary key; index | What | Replaces |
|---|---|---|---|
| `session_call_days` | (session_id, day) | per session and UTC day: its call marks' count, first and last `t_ns`. The day is computed as `_ACTIVITY` computes it: floor(t_ns / 1e9 / 86400) in float64 | the directory's reads of every call mark |
| `agent_calls` | (key, call_id); (ts_ns) | each Claude Code and Codex call's time, calls in flight included (v5's `merge_agent_calls` rows) | the merge's day partitions |
| `agent_call_hours` | (hour, key) | the count per UTC hour | the whole hours of `/overview`'s count |

### 3.6 Retention

The retention singleton runs every hour, by day: it works through the keys whose
`day` is older than `retention.days` (default 15; the UI's widest preset is 14 d
back from an hour). For each key:
1. **Delete its rows in small batches:** ≤ 5k rows per statement, by the key or
   session prefix of each table's primary key, with `lock_timeout` 1 s. A batch
   that times out is retried on the next pass.
2. **Delete its key row,** guarded on the `n` it read. A key that got new input
   meanwhile keeps its row, so it's never forgotten mid-run.
3. **Delete only what the deleted row named:** its state chunks, its garbage,
   and its `in/` files at or before its `last_applied`.

So a key never restarts from half its state, and "All" is the retained window.

**The local raw cap,** `retention.rawDays`: default 0 (off) in the chart, 3 in
Compose. The same pass deletes:
- raw day folders older than the cap;
- `in/` files older than the cap whose names sort before their key's
  `last_applied` − grace (already applied);
- `v1/recent/` objects a day behind the tail's cursor;
- on a local directory store, `calls/` objects older than the cap. The drawer
  then shows its unavailable raw view, as it does today for a missing object.

In the cloud, bucket lifecycle rules expire `raw/` and `calls/`
(`docs/PRODUCTION.md`).

## 4. Cross-key values

**Joins move to read time.** Each key does its own work at write time. A value
that needs other keys' rows is joined at read time when the join is bounded by
the page's entity and indexed.
- v6 precomputed these because S3 has no joins, which took the merge, the person
  loop, `pdirty`, dispatch and group objects. The cold-load cost it fought was
  S3 file reads and scans of every partition, not joins.
- In Postgres these joins are a few index lookups.
- Keeping them at write time would need dependency tracking and locks on groups
  of keys.

| Value | Where | Read, bounded by | Why |
|---|---|---|---|
| Person scope: `person_map`, `person_sessions`, `account_facts`, `account_devices` | read | The URL account's email group: the dimension rows whose account_id or person_id is in the group (the `lower(email)` index), and the group's accounts' `dim_session_account` rows. `unattributed`: visible sessions with no account row (an anti-join on the `session_id` index). `person_sql.py` runs unchanged over them | Its results for a group depend only on the group's rows: linked accounts, owned accounts, the leader and sessions per email; facts and devices per account. A group is tens to hundreds of rows. At write time, an account linking would rewrite its whole group |
| Directory leaders (quirk 1) | read | The dimension rows of the keys whose `day` ≥ day(`from`), plus every `a:` key's | `person_sql` over exactly the rows v5's partitions held |
| A person's request log: prompt numbers, first reports and step numbers across sessions (quirk 3); a batch's session and "N events"; `c:` transactions by credential | read | `stream_sql` (today's; its batch CTEs read batch parts) over: the scope credentials' transactions in the range, plus the transactions of the same prompts (the `(key_hash, prompt_id)` index); the reports of their ids; those prompts' `prompt_index`; the scope sessions' `codex_turns` and `session_last`; the scope's missed rows in the range; the batch parts of the batch calls among them | Every value depends on rows of the same prompt, request id, session or batch, so the range plus that context is exact. Tests prove it with ranges that cut through prompts and batches. A heavy person-day has ~5k transactions: 24 h takes ~60 ms, 7 d ~0.3 s. At write time this is v6's person loop |
| A session's Requests tab: its own rows | write | `session_request_rows` (the render) | — |
| … and other keys' rows: a Codex thread's unthreaded client calls; an analytics batch whose call is still in flight in its credential's `c:` key, with no session until it ends | read | `stream_sql`'s session builder (unchanged but for the batch CTEs) over just the candidates: the thread's credentials' transactions in [start, last + 60 s] (Codex); the transactions held by other keys of the batches this session has parts of; their batch parts. The rows it keeps join the stored ones, sorted as today (`ts_ns // 1000`, `request_id`, `seq`): the request ids differ, so `seq` never decides | The session's own key can't see them; tens to thousands of rows. Today's tab shows both |
| Codex `label_text` over the account-connected component (quirk 2): the person sessions list and the person timeline's bands | read | The page's sessions' component, closed through `session_call_accounts` (a recursive CTE over its two indexes); the candidates' `session_rows`; the component accounts' `session_title_calls`. Today's title SQL (`session_rows.sql`'s `cand` … `titled`, moved to `sql/labels.sql`) runs once per component, as `buckets.py`'s title runs do | It's order dependent across the component, which holds tens of Codex sessions and title calls. The title thread is another key |
| `label_text_alone` (the session header and the session timeline) | read | The title calls of the session's accounts in [start, start + 5 min] | one index range |
| `children_json` | read | `session_edges` where `parent_session_id` is one of the page's sessions | Each edge is written by the child's key |
| A batch spanning sessions | read | `batch_parts` by batch: the count is the sum, the name the least, the session the earliest (ns, session). The drawer and `/events` read `analytics` by `correlation_id` | Each key writes its own part |
| `/people`, `/overview` | read | §5 | — |
| `/status` | read | Aggregates over `keys`, through indexes; `ingest_pods` | — |
| Expiry's data time | read | `max(keys.newest_row_ns)` | — |

## 5. API: reads Postgres, runs today's code, scaled on CPU

**Loading.** For each request, the API:
1. **Takes one connection and opens one snapshot:** a READ ONLY, REPEATABLE
   READ transaction. Every statement sees the same commits, so a page never
   mixes two commits.
2. **Runs the endpoint's statements in order,** one round trip each: at most
   8, about 1 ms each in the cluster.
   - Each statement is bounded by the entity, and the range when there is one,
     through an index.
   - Each resolves the entity itself (a CTE over the dimension rows or the
     call).
   - The range goes to Postgres as a time filter widened to whole
     microseconds, a superset (for example, of `SHOWN`). Today's SQL applies
     the exact filter.
3. **Loads the rows into DuckDB.**
   - Rows arrive as Arrow (`adbc-driver-postgresql`, binary COPY). DuckDB reads
     ADBC's Arrow stream directly (`__arrow_c_stream__`); `pyarrow` comes in
     only if that fails.
   - They're registered in the request's in-memory DuckDB under today's table
     names and columns.
   - The flattened structs are rebuilt and `session_last_ns` is filled in.
     Labels and children are applied (§4).
   - A table a page doesn't read is today's empty shape.
4. **Runs today's endpoint code unchanged.** There's no cache.

**Columns.** Each statement selects the columns its endpoint reads, from a list
per endpoint. A missing column is an SQL error the tests hit, and `SELECT *`
sites get every column.

| Endpoint | Statements (one snapshot, in order) | What the API computes |
|---|---|---|
| `/status` | `keys`: min `first_request_ns`, min `earliest_ns`, max `newest_row_ns`, any `content_retained` (index lookups); `ingest_pods`: max `last_ns` of objects from the last 10 minutes | as today: the newest is the larger; `waitingSince` |
| `/overview`, `/people` | The dimension rows of keys with `day` ≥ day(`from`) and of every `a:` key (no `from`: all keys); those sessions' `session_rows` (the directory's columns), `session_call_days` and missed marks. `/overview`'s requests: the sum of `agent_call_hours` over the whole hours in [lo, hi], plus `count(*)` of `agent_calls` in the partial hours | `person_sql`, `rollups.py` and `directory.py` over the rows: the window, `live`, activity, filters, sort, page |
| `/people/{id}` | the group's dimension rows; its sessions' `session_rows`, `session_call_days` and missed marks | today's header, rollups and linked accounts |
| `/people/{id}/sessions` | as `/people/{id}`, plus the labels' component | `SHOWN`, nesting, paging by root |
| `/people/{id}/summary`, `/timeline` | The group's dimension rows and `session_rows`; for the timeline, the labels' component. For the sessions a superset of `SHOWN` keeps: `event_rows` (the 19 columns the person pages read, plus `batch`), `session_marks`, typed `prompt_rows`, `call_tool_uses` and `presence_segments` in the range. Per session, its newest event of each `_LATEST` name before lo (the partial index). For the summary, the `account` transactions in the range | range defaults, `SHOWN`, today's SQL, bins, `pack_rows`, live values |
| `/people/{id}/requests`, `/coverage` | the group's dimension rows; the request log's inputs for the range (§4); `session_last` | `stream_sql`, `request_filter`, counts, order, page |
| `/sessions/{id}` | its `session_rows` row; its accounts' title calls (the alone label); its children's edges | `live` |
| `/sessions/{id}/timeline`, `/prompts`, `/events`, `/state` | as `/sessions/{id}`, plus the session's `prompt_rows`, `prompt_steps`, `session_marks`, `event_rows`, `presence_segments` and `session_tools` (the range pushed down when the request has one) | as today |
| `/sessions/{id}/requests` | `session_rows`, `session_request_rows`, its `dim_session_credential` rows, the other keys' candidates (§4) | `stream_sql` over the candidates; the union; as today |
| `/requests/{id}`, `/details`, `/raw` | The call's own keys: the keys holding its fact rows, or for `reported:<id>`, the session whose `request_missed` row has that id. Their `requests` rows, and their `analytics` rows of the names the drawer's code matches (`(key, name)`) or about the call (its prompt, its request and tool-use ids). The batch's `analytics` by `correlation_id`. `/status`'s statement. Beside them: GET `calls/<id>.ndjson.gz` | `request_detail.py`, unchanged |
| `/events/{id}` | the batch's `analytics` by `correlation_id`; beside it: GET `calls/<batch>.ndjson.gz` | the event's rank and raw row |

- **Round trips:** one connection, one snapshot, the endpoint's statements in
  order. The drawer's S3 GET runs beside them. No N+1: each statement is per
  page, never per row.
- **The drawer's scope is today's.** It reads its call's own keys, as `v1.py`
  does with the `calls/<id>.keys/` markers today, without needing them.
- **Endpoint code that changes, and nothing else** (each change proven side by
  side):
  - `_ACTIVITY` (`rollups.py`), `overview()`'s `sessions_by` and
    `unattributed()` (`directory.py`) read `session_call_days` instead of every
    call mark: `count(*)` becomes `sum(n)`, and min and max become min first
    and max last;
  - `stream_sql`'s `batch_session` and `batch` CTEs read `batch_parts`;
  - the summary's distinct batch count reads `batch` instead of a regex over
    `event_id`. Measured in v6, the regex was most of the fold: 0.88 s → 0.28 s
    single-threaded at 14 d;
  - the title and children CTEs move from `session_rows.sql` to load time
    (`sql/labels.sql`);
  - the drawer 404s for a call in flight (owner).

  `agent_calls_in` stays a store method, now backed by Postgres. `org_kind`
  still reads `CUSTOMER_ORG_IDS` at request time.
- **Dependencies:** the API adds `adbc-driver-postgresql` (exact pin, 14 days
  old) and drops `redis`. Go adds `pgx/v5` and drops `go-redis` and
  `miniredis`.

## 6. Deployment

- **Production (AWS): RDS for PostgreSQL 17.** The operator creates it (support,
  don't provision): gp3 with storage autoscaling, Multi-AZ as they choose.
  - Today's load needs the smallest class. At 1,500 people, the scaling
    budget's sizing applies.
  - Every connection uses TLS (`sslmode=verify-full`, with the RDS CA bundle).
- **The DigitalOcean demo: Postgres in the cluster.** A StatefulSet with a PVC,
  as Redis is today:
  - one replica, `postgres:17.x-alpine` pinned by digest;
  - 20 Gi of do-block-storage;
  - non-root, with a read-only root filesystem and emptyDirs for the socket and
    tmp;
  - an init script that creates the database (built-in `C.UTF-8`) and the two
    logins.

  Chosen over DO Managed Postgres: it needs no provisioning outside the chart
  and no trusted-sources setup, and `helm upgrade` brings it up. Backups don't
  matter for the demo: its data is discarded on deploy anyway.
- **Server and connection settings,** the same on every target (the RDS
  parameter group, or the in-cluster config):
  - `max_locks_per_transaction = 256`. Each warm key holds an advisory lock,
    and this gives 256 × `max_connections` slots;
  - `max_connections` ≥ 200.

  The logins:
  - `aintercept_worker`: `idle_in_transaction_session_timeout = 30s`,
    `lock_timeout = 5s`, `statement_timeout = 60s`;
  - `aintercept_api`: `statement_timeout = 30s`, the same idle timeout.

  Every DSN sets TCP keepalives (`keepalives_idle=10`, `keepalives_interval=5`,
  `keepalives_count=3`) and `tcp_user_timeout=30000`, so a dead peer is noticed
  in about 30 s, either way.
- **Local:**
  - a Compose `postgres` service replaces the `redis` one: the same image, a
    named volume, a healthcheck;
  - its port is published on 127.0.0.1:15432 only, as Redis's is today;
  - its password is generated by `scripts/up.sh` into a gitignored `.env`;
  - the API runs on the host and reads Postgres there.
- **CI:** a Postgres service container (the same digest) in the Go, API and
  generated-contract jobs, as moto is today.
- **Helm values:**

  ```yaml
  postgres:
    # A Secret with one DSN per component: worker-url, api-url. In the cluster it
    # also carries the passwords its init script sets: postgres-password,
    # worker-password, api-password.
    existingSecret: aintercept-postgres
    inCluster:
      enabled: false            # true on the DigitalOcean demo: a StatefulSet with a PVC
      persistence: { size: 20Gi, storageClass: "" }
      resources:
        requests: { cpu: "250m", memory: "1Gi" }
        limits: { cpu: "2", memory: "2Gi" }
  retention:
    days: 15                    # keys whose day is older are forgotten
    rawDays: 0                  # raw/, applied in/, a local store's calls/ (0: off; lifecycle rules in the cloud)
  ```

- **Secrets** are created out of band, as the key-hash salt's is:
  `deploy/digitalocean-bootstrap.sh` generates the passwords with `openssl rand`
  and creates the Secret. Credentials never go through `--set`. Each pod mounts
  only its own component's DSN. The observers get none.
- **Gone from the chart:**
  - `redis.yaml`, `redis.*`, `images.redis`;
  - `observer.redisAddr`, `api.redisAddr`.

  A NetworkPolicy admits only the workers and the API to Postgres. KEDA is
  unchanged.
- **`helm upgrade`** removes the Redis StatefulSet and Service. Its PVC
  (`data-redis-0`) stays and is deleted once by hand. Never `helm uninstall`.

## Rollout

None. This is used internally and by one or two people outside the team. On
deploy:
- everything under `v1/` and the `calls/*.keys/` markers are deleted;
- Postgres starts empty;
- ingest and the workers start from new traffic.

No backfill, no side-by-side run, no migration.

## Scaling budget

**Today:** a handful of people, a few hundred thousand rows a day. One small
Postgres and two workers carry it with room to spare. Every page reads tens of
milliseconds of rows.

**The design target, at 1,500 people.** These numbers check that nothing in the
design breaks at the target. None of them is a reason to build anything now.
Assumptions, at the capture's rates per person:
- 75M analytics events a day (the meeting's figure);
- ~1k model calls and ~5k requests per person-day;
- a business-hours peak of about 3× the average;
- ~50 ingest pods.

| Component | Work | State held | Sharded by | Waits on | Postgres | S3 ops/s |
|---|---|---|---|---|---|---|
| Ingest | per record, as v5 | per-key buffers, the index journal | pods, on CPU | S3 PUT | none | ~350 PUTs/s at peak (key files ~100, call detail objects ~250), plus ~10 index objects |
| The tail | per index object: GET; per pass: one transaction | its cursor row | one holder | S3, Postgres | ~1 transaction every 2 s, ~100 flags/s | 1 LIST every 2 s, ~10 GETs/s |
| Worker run | per record: apply (#19). Per run: the whole session rendered and hashed (O(session) CPU, as v6), the changed rows written. A key with no input costs a key-row read, a LIST and a DELETE | the key's state and a hash per rendered row, while warm | key (advisory lock), heartbeat share | S3 LIST and GET, the commit | ~100 commits/s at peak, ~100 rows each: ~8k rows/s at peak, ~2k/s on average, ~180M rows/day | ~4 per run: ~400/s at peak |
| Retention | per forgotten key: batched deletes | none | one holder | — | ~180M rows/day deleted at the target (Not now: partitions) | the forgotten keys' objects |
| API | per request: a few statements, the rows of the entity and range | none | pods, on CPU | Postgres; S3 GET (drawer) | reads only | the drawer's GETs |
| Postgres | — | the working set | one primary (vertical) | disk | ~110 GB/day; ~1.6 TB at 15 days | — |

| Table | Rows/day | With indexes |
|---|---|---|
| `analytics` | 75M | ~55 GB (its (correlation_id) and (key, name) indexes for the drawer: ~10 GB of that) |
| `event_rows` | 75M | ~28 GB |
| `request_txs`, `requests`, `session_request_rows` | ~7.5M each | ~15 GB together |
| `session_marks`, `prompt_steps`, `call_tool_uses`, `request_reports`, `agent_calls`, `batch_parts` | 1.5M to 4M each | ~8 GB together |
| everything else | under 1M | under 1 GB |

- **Postgres at the target.** One primary handles the writes: ~8k inserted
  rows/s at peak. That takes an r6g.2xlarge class with 2 TB of gp3 at 12k IOPS.
  - Retention's deletes match the inserts (~180M rows/day), and so does the
    vacuum behind them. That's where day partitions start to pay (Not now,
    with the numbers).
  - The next steps for reads: a read replica for the API, and a narrower
    `analytics` (Not now).
- **Connections:** 3 per worker, and one per API request in flight (a pool of 8
  per pod). That's ~140 at the maximum replica counts.
- **Advisory locks:** one per warm key, ~5k at peak, well under
  `max_locks_per_transaction` × `max_connections`.
- **CPU:** the render is O(session) per run. At ~100 runs/s of sessions
  averaging 20k rows, that's ~5 cores across the workers, and KEDA scales them
  on lag.
- **S3:** ~800 ops/s in all at peak. AWS S3's limits are far above that; DO
  Spaces' 800/s per bucket fits the demo.

**Expected latency** in the cluster, with the rows in Postgres's buffer cache:

| Page | Rows read (the heaviest) | Expected |
|---|---|---|
| `/status` | 1 | ~5 ms |
| a session page, the capture's busiest session (~100k events) | Events tab: 100k rows; the other tabs: thousands | ≤ 250 ms for the Events tab, ~50 ms for the rest |
| person summary and timeline, 24 h and 7 d (the capture's heaviest person, ~60k events a day) | 60k and 420k narrow event rows | ~0.1 s and ~0.4 s |
| person requests, 24 h and 7 d | ~5k and ~35k transactions | ~60 ms and ~0.3 s |
| `/people`, `/overview` at 1,500 people, 24 h and 14 d | ~6k and ~90k sessions, with their dimension rows and day counts | ~0.1 s and ~1 s |
| the drawer | the session's calls (thousands) and its drawer events (~15k) | 50 to 100 ms |

**The cold-cache limit,** recorded honestly.
- The person summary and timeline are read-time folds over raw event rows.
- At 1,500 people the event rows (~30 GB a day) far exceed RAM. A person's
  older days are then read from disk.
- A session's rows are written a run at a time, ~25 to a page, so a heavy
  person's 7 days are ~16k random page reads: about 1 to 2 s on gp3.
- At today's scale everything fits in RAM.
- The fix is per-(session, hour) folds or 60 s cells, so these pages read O(hours)
  rows (Not now).

The final gate measures cold p95 on the capture, with Postgres restarted and the
OS cache dropped once.

## Testing: small tests, written first

Each step writes its failing tests first. **The oracle is today's code, frozen**
(`api/tests/oracle`, #42), and stays the reference through the final gate.

- **Go layers, on a Postgres fake.** The writer's transaction interface (the
  guarded key update, upserts, deletes, the flag) has an in-memory
  implementation with Postgres's semantics: one transaction, and the guard
  re-checked after a concurrent commit. The layers run on it:
  - **Atomic:** one record per case (every `state.Cases` branch). The fake's
    rows equal `RenderSession`'s and the folded deltas'.
  - **Short sequences:** every order of up to 5 records, one run per record.
    After each run the fake equals one pass, and each run wrote only the rows
    whose hash changed (counted).
  - **Generated sequences:** shuffled, duplicated, late across hours, and
    reloaded cold halfway. The fake equals one pass, and a cold load with
    nothing new writes nothing: the baseline is the render of the replayed
    state. A changed `render_version` rewrites the session.
  - **Named cases:**
    - a call that lands an hour after the event linking it;
    - a new call, which renumbers no later mark block;
    - a late event, which rewrites only the rows after it.
  - **Flags:**
    - the tail flags idle keys, and every run consumes its flag: `dirty_keys`
      ends empty and `/lag` at 0;
    - a run capped at 500 files keeps its flag, with `since` set to the first
      file left;
    - a flag landing mid-run survives, and the next run applies the file.
  - **Guard and timing:**
    - two workers on one key, the old one committing before and after the new;
    - a stale warm state;
    - a crash at each step (v5's `Hook`);
    - a COMMIT whose result is unknown;
    - the lock connection lost mid-run: the worker forgets its keys.
  - **Hostile input:** NUL in every string field, and 5 KB session, request,
    call and event ids. Every commit succeeds, and the oracle and the API see
    the same sanitized values. A key that fails 5 times is quarantined: it's
    excluded from claims and `/lag`, retried hourly, and released by a commit.
  - **The tail:**
    - Postgres down: the cursor holds and nothing is skipped;
    - an index object that becomes visible late, inside the grace window;
    - a pod crash: the journal's leftovers are uploaded at start.
  - **Chaos simulation** (v5's `sim`): workers crash, stall and race,
    connections drop, and the tail's holder dies. The fake equals one pass.
- **Postgres integration**, on CI's service container: a handful of tests, in
  seconds.
  - The schema applies once; a hash mismatch refuses to start.
  - The guard, the upsert and delete SQL (with `unnest`), and two concurrent
    guarded commits.
  - `bytea` ids and sanitized text.
  - Built-in `C.UTF-8`: the sort order matches Go's, and `lower()` matches
    DuckDB's on every assigned code point.
  - Every column type round-trips Postgres → Arrow → DuckDB equal to the
    oracle's types: floats bit for bit, NULL apart from an empty list.
  - Retention deletes only names read from the row it deletes.
- **Table level, side by side, in `make check`.** `aintercept v1layout --pg`
  runs every dataset's keys into a fresh database: `builders.py`, the demo
  fixture, the SQL fixture seeds. For each entity, the tables the API loads
  equal `oracle.tables(kind, id)` row for row (seq compared by order). The
  entities:
  - every session. `test_v6_session_rows.py` (#44) moves to the Postgres rows
    and adds `children_json`, the labels, and the other keys' request rows;
  - every person, for every range in the URL set, plus generated ranges that
    cut through prompts and batches;
  - every request id and event id;
  - the directory from every day.

  The cases include sessions with a batch still in flight and Codex threads.
- **Response level:** `test_v1_pipeline.py`, with "old" pinned to the oracle
  path and "new" the Postgres reader.
  - In `make check`, on the small datasets: every URL of the endpoints with
    read-time joins (the person pages, the directory, the drawer, a session's
    Requests tab), and 3 per group for the rest. Everything runs with
    `-m full`.
  - `endpoints.py` gains the UI's real params (the presets' `from`s, an
    unaligned `from`, a brush, ladder bins) and drops the bogus `binSeconds` and
    `sort`.
- **Reads:** `test_flat_reads.py` becomes a statements test, per endpoint:
  - a fixed number of statements, on one connection;
  - on the big tables, every statement's plan (EXPLAIN) uses an index;
  - the rows read don't change under 8× more history outside the range.
- **CI time.** The service container starts in seconds, each dataset is loaded
  once per session, and the Go layers run on the fake. Budget: under a minute
  added across the parallel jobs. Locally, `make check` starts the pinned image
  in Docker when `AINTERCEPT_TEST_PG` isn't set.
- **The real two-day capture runs once,** as the final gate
  (`~/git/aintercept-parity`). It is never part of the development loop. There
  is no synthetic scale gate.

## Build order

Each step is one PR, tests first.

| Step | What | Gate |
|---|---|---|
| 1 | Postgres in dev and CI: the Compose service (`up.sh` writes its password), CI's service containers, `internal/pg` (pool, `schema.sql` and its hash, logins, settings), the Python test fixture. Canon sanitizes NUL and caps long ids | Integration tests: schema, `bytea` ids, collation and `lower()`, the Arrow round trip; the oracle's datasets unchanged |
| 2 | The worker core on Postgres: the key row as the commit (the guard first), the flag rules, quarantine, the heartbeat claim, `/lag`, due calls, state deltas into the stored inputs and facts. Redis leaves the worker | v5's rollup tests ported; the flag, guard, timing and hostile-input tests; the chaos simulation; every dataset's stored inputs equal the oracle's `st_*` tables |
| 3 | Rendered tables: `RenderSession` into rows, the marks' block keys, `prompt_last_ns`, hashes and the diff, the render baseline, `render_version`; the directory folds, agent calls and batch parts | The Go layers on the fake; side by side for every session's 9 tables |
| 4 | Discovery: ingest's index journal and its 5 s uploads under time names; the tail singleton and `ingest_pods`. `keyflag`'s Redis goes, and ingest gets no Postgres | Tail tests (an outage, late objects, a pod crash); ingest's tests show it has no database path |
| 5 | The API loader: one connection and snapshot, the statements, Arrow into DuckDB. `/status`, the session pages (labels, children, other keys' request rows), the drawer, `/events` | Table level for every session, request id and event id; response level for their URLs |
| 6 | Person pages: scope, the request log (batch CTEs), summary and timeline (`batch`, the `_LATEST` carry, the labels' component), the header | Person tables side by side for every person and range (ranges cutting through prompts); response level |
| 7 | The directory: loads by `keys.day`, the folds, the three SQL changes, `agent_calls_in` | `/people` and `/overview` side by side for every `from` (presets, unaligned, none), the existing directory tests included |
| 8 | Switch and delete: the Postgres reader becomes the API's; "Deleted" below (the merge, `Commit` and `WriteOnePass` stay until after the gate); the chart and Compose (Postgres in, Redis out, retention); `SETUP.md`, `PRODUCTION.md`, `DEPLOY_DOKS.md` | `make check`; the chart tests; `make up && make verify` |
| 9 | The final gate, on the real capture | Every endpoint byte-identical to the v5 baseline (4,248 URLs); cold p95 under 300 ms per page for the capture's heaviest person and session (the directory included), measured once with Postgres restarted and the OS cache dropped; Postgres rows and bytes per person-day measured against the budget |
| 10 | Deploy to the DigitalOcean cluster: create the Postgres Secret (the bootstrap script), clear `v1/` and `calls/*.keys/`, `helm upgrade` (never uninstall), delete the Redis PVC | Pages load on the cluster's new traffic; `/lag` settles under 30 s; a request through the proxy shows up in the UI end to end |

After step 10, these are deleted:
- the oracle and `v1layout`'s v1 output;
- `merge.go`, `mergeloop.go`, `Commit` and `WriteOnePass`.

From then on, `process.Derived` and the UI samples are the oracles.

## Deleted

- **Go:**
  - each key's `out/` tables and their compaction (state compaction stays);
  - `rollup/reported.go`;
  - `records/keymarks.go` and ingest's marker writes;
  - every Redis path: `keyflag`'s SADD and `ingest:last`; the worker's
    `dirty`, `committed`, `held`, locks and `replicas`; `miniredis` in tests;
  - `stream.Fence` and `Store.ETag`/`PutIf` (#41): unused, since Postgres's
    guard replaces conditional writes;
  - `v1layout`'s `v2/test-sessions/` files: the harness reads Postgres.

  After the gate: the merge (`merge.go`, `mergeloop.go`), the commit objects
  (`Commit`, `ReadNewest`, `PutIfAbsent` fencing) and `WriteOnePass`. The
  oracle's layout needs them until then.
- **Never built:** the first draft's ingest-to-Postgres flags (a batcher,
  `ingest_pods` writes, an ingest login). The tail replaces them.
- **API:**
  - `v1.py`;
  - `buckets.py`: scopes, `resolve_person`, `_session_group`, the title runs,
    `read_newest`;
  - `state_sql.py`'s table build;
  - `sql/prompts.sql`;
  - `session_rows.sql`'s lists and windows. They're rendered now; its title and
    children parts move to `sql/labels.sql`;
  - the Redis client;
  - the S3 table reads (`S3Source` keeps the `calls/` GETs);
  - the legacy empty tables' fills;
  - the dead code (`cc.person_sessions`, `cc.linked_accounts`, `pp.retries`);
  - the tests whose SQL is gone (`test_buckets`, `test_stream_pipeline`,
    `test_events_sql`, `test_prompts_sql`).

  `person_sql.py`, `stream_sql.py`, `rollups.py` and `directory.py` stay: they
  run at read time.
- **S3:**
  - `commit/` and `out/`;
  - `v1/merge/`, `v1/reported/` and `v1/content_retained`;
  - `calls/<id>.keys/`;
  - the per-minute `v1/recent/<minute>/` folders, whose objects get time names.
- **Chart and Compose:** Redis.

## Deviations from the meeting and the accepted recommendations

1. **Discovery (recommendation 2: "ingest inserts the key into `dirty_keys`
   after each upload").** Ingest never talks to Postgres. A singleton tails
   ingest's recent index, now uploaded every 5 s, into `dirty_keys`. Ingest
   then can't block or fail on the database, and an outage can't lose a flag.
2. **Claiming (recommendation 2: `FOR UPDATE SKIP LOCKED` "or an advisory
   lock").**
   - An advisory lock, held while the key is warm, for affinity: a cold load
     replays the key's journal.
   - Workers take keys in their own hash order, with heartbeats sharing them
     out.
   - The flag isn't deleted when a key is claimed. The run consumes it, guarded
     by its counter, and only when the LIST reached the end. So a crash or a
     capped run never strands a key.
3. **Today's endpoint code unchanged (recommendation 3).** There are five small
   exceptions, each proven side by side:
   - the three directory statements read per-session call-day counts. At 1,500
     people a 14-day window holds ~21M call marks;
   - `stream_sql`'s batch CTEs read batch parts, since `batch_members` is a row
     per event;
   - the summary's batch count reads a stored column (v6);
   - the labels and children SQL runs at load time;
   - the in-flight drawer (the owner's call).
4. **"Workers write what's needed for joins and queries" (the meeting).** These
   are joined at read time, not written as v6 planned:
   - person-wide request values;
   - Codex labels;
   - person scope;
   - other keys' request rows.

   Each join is bounded and indexed, and there's no cross-key write path.
5. **"Write time does the data-only work" (the owner).** The person summary and
   timeline are still read-time folds over raw event rows. That's exact and
   fast at today's scale. At 1,500 people it hits the cold-cache limit above;
   the fix is in Not now.
6. **"The worker upserts exactly #44's rows" (the coordinator).** It does,
   except for two re-encodings that are filled back at load:
   - `prompt_rows.session_last_ns`, kept once per session;
   - `session_marks.seq`, keyed per block.
7. **The S3 layout (open in the meeting):**
   - raw data stays in S3 (recommendation 1);
   - so does the state checkpoint. Postgres holds its chunk names.
   - the recent index gets time names and a 5 s cadence.
8. **Bodies in S3, pointers in Postgres (the meeting).** Postgres also holds the
   call fact rows the drawer reads (`requests`, `analytics`). Analytics
   metadata isn't a body; `env_json` is dropped.
9. **Size (recommendation 4: "partition by day + retention").** No partitions
   yet. Retention forgets whole keys past the window with batched DELETEs.
   Partitions pay only at volumes we don't have (Not now, with the numbers).
10. **Retention (recommendation 5's "small retention job").** It's a worker
    singleton, not a new component, and it also applies the local raw cap.
11. **The DigitalOcean demo:** Postgres in the cluster, not DO Managed Postgres.

## Decisions and quirks

- **Delta in, eventual state out.**
  - Stored inputs are the state's deltas.
  - Rendered rows are recomputed whole and written where their hash changed.
  - Everything is an upsert or a delete by primary key, so rewriting is
    harmless.
- **One owner per row.** The guarded `n` orders each key's commits.
- **Accepted costs:**
  - O(session) render CPU per run (v6);
  - a late row renumbers the rows after it in the time-ordered tables.
- **Reads see one snapshot:** v6's "reads aren't isolated" quirk is gone.
- **"All" is the retained window** (`retention.days`).
  - A key that is still active keeps its rows older than the window until it
    goes quiet.
  - `/status`'s first request is the first one retained.
- **Sanitized input.** NUL becomes U+FFFD, and an id over 512 bytes becomes
  `sha256:<hex>`. Only pathological input changes, and the oracle sees the same.
- **Codex step numbers (#44).**
  - A Codex thread's Requests tab counts the thread's own calls.
  - Today's code, like the person log, which still runs today's SQL, counts
    calls that share a credential and a turn id across threads.
  - Real turn ids are UUIDs, and no dataset row differs.
- **The drawer** 404s for a call in flight until it ends (owner); its sample
  changes.
- **A Codex thread's unthreaded calls and in-flight batch calls** are read from
  the other keys' transactions at request time, never copied.
- **Calls that never end:** as built (#39), with data time from `keys`.
- **Discovery's one gap:** losing a node within 5 s of an upload delays that key
  until its next file (Not now).

The inventory's quirks, each reproduced:
1. **Directory partitions from day(`from`):**
   - `keys.day` is the merge's `DayOf`. The directory loads the keys whose day
     is ≥ day(`from`), plus every `a:` key, exactly as v5 loads partitions.
   - `/overview`'s request count coincides: a call at t ≥ lo is in a key whose
     day is ≥ day(lo).
2. **Codex `label_text` in time order over the account group:** today's SQL,
   over the same component, at read time.
3. **Person and session request numbering differ:** the person log is today's
   `stream_sql` over the person's scope; the session tab is the render's.
4. **No final tiebreaker in the person requests sort:** `stream_sql`'s output
   order, loaded with one thread, as today.
5. **Session pages derived the whole account group:** not reproduced as a cost.
   Only the labels' component is read, and only for person pages.
6. **`/people/{id}` facts are the URL account's:** today's code over the
   group's rows.
7. **The drawer's `lastDataAt` is global:** `/status`'s statement, in the
   drawer's snapshot.
8. **`org_kind` reads the environment at request time:** unchanged.

## Not now: add when real traffic needs it

- **Day partitioning** of the big tables (`event_rows`, `analytics`,
  `request_txs`, `requests`, `session_request_rows`, `session_marks`). Add it
  when retention's DELETE and vacuum cost too much, or when a table's indexes
  outgrow RAM.
  - At 1,500 people that's ~180M rows and ~110 GB a day deleted again after 15
    days: about 2k deletes/s around the clock, plus the vacuum behind them.
  - At today's scale it's well under 1M rows a day.
- **Per-(session, hour) folds or 60 s cells** for the person summary and
  timeline, when the cold-cache limit shows: these pages would read O(hours)
  rows instead of every event.
- **A read replica for the API, or RDS Proxy.**
- **Native Postgres SQL for the directory's folds,** when the gate or real
  traffic shows the 14-day pages slow.
- **An API-side cache** of past days' rows.
- **Hour order keys** (v6) for the time-ordered tables, if late rows show up in
  writes/s.
- **A narrower `analytics`,** at about half the database at 1,500 people: read
  event metadata from the batch's S3 detail object, or keep it only for the
  names the drawer reads, and serve `/events/{id}` from `event_rows`.
- **A periodic full listing of `v1/keys/`,** to flag keys a lost node never
  indexed.
- **The in-flight drawer** (a `pending` table), if the owner wants it back.
- **An incremental render** (O(changed) CPU).
- **Schema migrations.** Until then, each schema change is a fresh start.
- **A size cap,** not just an age, for local raw data.
- **Hot keys:** a shared API key used by thousands of people, or many people
  behind one NAT IP.
- **Changing response contracts.**
