# Data pipeline (v8)

Status: **proposed, for the owner's sign-off.** v7 is built and deployed
(2026-11-14). v8 precomputes the session and person pages. Everything else is
v7's, as built. The open decisions are listed in "Owner decisions" below.

**Why.** On the real capture, v7's heaviest pages take 300 to 450 ms warm
(p95): a session's state, timeline and events, and a person's summary, timeline
and requests. Almost all of it is per request work:
- the rows are copied from Postgres into an in-memory DuckDB and rebuilt into
  today's tables;
- today's endpoint code derives the page from them;
- DuckDB's setup alone costs ~18 ms.

**What v8 changes:**
- **The API reads finished rows** (§5). For the session and person pages it
  filters rows by the request's range and filters, pages them, and merges
  precomputed cells, all in Postgres. It applies `now`. It derives nothing else,
  and it has no DuckDB.
- **The worker renders what those pages show** (§3.3, §3.8 to §3.10):
  - *cells*: each session's and each credential's sums per minute and per hour;
  - one *request-log row* per request, which both request logs read;
  - the session row's *labels and children*, finished;
  - *unit-start flags and first-occurrence marks* on the rows the pages count
    and page through.
- **Other keys' values are read at render time,** and the commits that change
  them flag the reader dirty, as v7 does for the drawer's batch parts (§2, §4).

Unchanged from v7, as built:
- ingest and discovery (§1, the tail);
- claims, the guarded commit and quarantine (§2);
- the state engine and its S3 checkpoint, expiry, retention;
- the session render (`state.RenderSession`) and the drawer (§3.7);
- the directory (`/people`, `/overview`) and `/status` (§5.6);
- deployment (§6).

v7's text is kept where it's still true. `docs/STREAMING_PIPELINE.md` is kept
only as history.

## Owner decisions

Open, for the owner. Each is explained where it's used.

1. **`bin` must be a multiple of 60** (§5.3). Today any integer ≥ 60 is
   accepted. The UI only sends the ladder (60 s to 1 d), all multiples of 60.
   Cells can't give exact bins for any other width, so v8 answers 422.
2. **Four folds that depend on order are still computed per request** (§5.5,
   which lists every per-request computation with its bound): the `q` text
   search, the person timeline's band packing, and presence and not-captured
   windows across a person's sessions. `q` scans the range's rows for its matches; the
   other three read only the page's bands, segments or missed calls, never
   events. The alternatives are contract changes.
3. **The render's CPU stays O(session) per run** (§2, Scaling budget), as v7
   accepted: measured, that's ~40 cores at the target. v8 adds no O(session) writes: every new table is diffed. An
   incremental render is a separate project.
4. **The directory keeps DuckDB** (§5.6). It's out of scope, so `/people` and
   `/overview` still run v7's loader. Porting their SQL to Postgres (no new
   precompute) would take DuckDB and ADBC out of the API image.
5. **psycopg 3 replaces ADBC** for the pages v8 serves (§5.7). Without DuckDB
   nothing reads ADBC's Arrow. It's an exact pin, 14 days old, with a bundled
   libpq.
6. **Float sums are merged per cell** (quirks). Cost and hook overhead may
   differ from today's in the last bits before rounding. They're shown rounded
   (cents, 0.1 ms); the side-by-side tests and the gate must find them equal on
   every dataset and the capture, or this comes back to the owner.
7. **The deploy starts Postgres empty** (Rollout), as v7's did: the schema
   changes, and there are no migrations.

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
- **The worker precomputes, the API reads** (v8).
  - Write time does everything that depends on the data: a key's own, and other
    keys' where a value needs them. Those are read at render time, and their
    commits flag the reader dirty (§4).
  - The API reads finished rows (§5.1). It filters them by the request's range
    and filters, orders and pages them, and merges cells. It applies `now` and
    resolves who a person is. Everything computed per request is listed with
    its bound (§5.5): merges over finished rows, and four folds that depend on
    order (decision 2).
  - One fetch per thing a page needs: a fixed number of statements per page,
    never one per row.
- **Delta in, correct eventual state out.** A commit writes only the rows that
  changed, never O(history). v8's tables are rendered and diffed like v7's, so a
  run writes only the cells, request rows and labels its records changed. The
  render's CPU stays O(session) per run, as v6 and v7 accepted (decision 3).
- **One transaction per commit.** A key's rows and its next commit number land
  together or not at all. The number orders a key's commits, so a stalled or
  duplicate worker can't write. This replaces the create-only commit objects,
  the ETag fencing and the Redis locks.
- **Response bodies don't change.** Once the pipeline has settled (no flags
  left), every endpoint returns byte-identical JSON on the CI datasets and the
  real capture, and the URLs stay the same. The
  exceptions are in "Decisions and quirks"; the only ones a client can see are
  the in-flight drawer (v7, the owner's call) and a `bin` that isn't a multiple
  of 60 (decision 1).
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
                                   │ one transaction: keys.n + 1, rows, cells, flags
                                   ▼
                               Postgres ◀── API: one connection, one snapshot, a few indexed statements
                                             → the page's finished rows and merged cells → the response
                                             (+ calls/<id>.ndjson.gz from S3 for /raw and /events/{id};
                                              the directory: v7's loader, §5.6)
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
3. **Nothing new, no expiry due and no `render` on the flag:** consume the flag
   with `DELETE FROM dirty_keys WHERE key = $1 AND seq = $seq0`, its own
   statement, and stop. The state isn't loaded. Data time for expiry is read
   once per tick, not per run. A flag with `render` (§3.7) goes on to load,
   render and commit.
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
   whole, and for every key its drawer rows (§3.7). First the run refreshes
   the render's external inputs: the detail objects of calls that ended, and
   the other keys' `batch_parts` of its batches (one statement on a run
   connection).
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
      - a flag with `render` (§3.7) runs even when the LIST finds nothing new;
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

*As built (step 2):* `/lag` also reports `quarantined_keys`, and `/metrics`
serves both as gauges. *Step 8:* `aintercept rollup` is this worker only, and
refuses to start without `ROLLUP_POSTGRES_URL`.

### v8: what a run renders and flags

- **Render (Run 6) adds v8's rows:**
  - cells (§3.8);
  - request-log rows and their cells (§3.9);
  - the finished labels and children, and title assignments (§3.10);
  - presence runs and gap windows, and the new columns on `event_rows`,
    `session_marks`, `prompt_rows`, `session_rows` and `batch_parts` (§3.3).

  They're hashed and diffed like v7's rows, so a run writes only the ones that
  changed.
- **The render's reads of other keys** stay one statement per run, with more
  parts:
  - the other keys' parts of its batches, with their event times when a batch
    spans keys;
  - its children's edges;
  - the title calls of its accounts in its first 5 minutes, and the assignment
    naming it;
  - for a key holding title calls: the candidate threads in the 5 minutes
    before each, and the assignments of the calls in those 5 minutes.
- **Flags.** A commit that changes a value another key renders flags that key
  with `render`, in the same transaction, as §3.7 does for batch parts:

  | The commit changes | It flags (render) |
  |---|---|
  | a batch part (v7) | the batch's other keys |
  | a `session_edges` row | the parent session's key |
  | a title call (`session_title_calls`) | the candidate threads of its account started in the 5 minutes before it (their alone label), and the keys of the title calls in the 5 minutes after it (§3.10) |
  | a thread's `title_candidates` rows (its start, accounts or candidacy) | the keys of its accounts' title calls in its first 5 minutes |
  | a session's credential rows, when the session holds batch parts | the batch's other keys (their `in_tab`, §3.9) |
  | a title assignment (§3.10) | the thread it named before and the one it names now; the keys of the title calls in the 5 minutes after it |

  Each flag set is the few rows around one instant, never a session's or a
  person's history. Retention flags by the same rules when it forgets these rows
  (§3.6).
- **Cost.** The render stays O(session) CPU per run (v7's accepted cost,
  decision 3). v8 adds one pass over the rendered rows for the cells and
  flags: a few ms beside the render itself (v7 measured ~0.4 s a run on the
  test datasets' largest session, its drawer rows included: ~40 cores at the
  target, Scaling budget).

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
  - Quarantine (§2) is the backstop for anything else: for example an account
    id, which canon doesn't cap (it comes from the provider, not the client),
    over ~2.7 KB would fail its key row's insert, and only its key would stop.
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
| `dirty_keys` | key, seq, since `timestamptz`, failures, last_error, failed_at `timestamptz`, render | key; (since) |
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
order and seq. Two changes keep commits O(changed), and the API's reads fill
both back, so it sees today's rows:
- **`prompt_rows.session_last_ns` is stored as 0.** It's kept once per session,
  as `session_rows.prompt_last_ns`. The render copies it onto every prompt row,
  so storing it there would rewrite them all on every run. A session with prompt
  rows and no session row (the state never makes one) keeps it on its prompt
  rows, so the read takes `coalesce(prompt_last_ns, session_last_ns)`.
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
| `session_rows` | session_id | ~6k new; each active session's row updated on every run | own_title, prompt_last_ns, key_day (its key's `day`). v7: labels hold the session's own (`SessionRowsOf`) and children_json is NULL; v8: both final (§3.10) |
| `prompt_rows` | (session_id, seq) | ~0.2M | — |
| `prompt_steps` | (session_id, seq, step) | ~1.5M | — |
| `session_marks` | (session_id, seq) | ~3.5M | — |
| `event_rows` | (session_id, seq) | ~75M | batch (the `event_id` prefix: the summary's count) |
| `presence_segments` | (session_id, seq) | small | — |
| `session_tools` | (session_id, kind, server, tool) | small | — |
| `call_tool_uses` | (session_id, ts_ns, kind, server, tool) | ~3M | — |
| `session_request_rows` | — | — | **Gone in v8 (step 8):** replaced by `request_log_rows` (§3.9), which both request logs read |

- **Extra indexes:**
  - `session_rows (key_day)`;
  - partial `session_marks (session_id) WHERE kind = 'missed'`;
  - partial `event_rows (session_id, name, ts_ns) WHERE name IN
    ('tengu_tool_search_mode_decision', 'tengu_skills_sync_success')`, for the
    summary's `_LATEST` carry.
- **Every other read is by the session prefix of the primary key,** filtered by
  time in Postgres. v8 puts the time in `event_rows`' and `prompt_rows`' keys
  (below).

**v8 additions to the session tables.** They serve the reads in §5.3.
- **`event_rows`:**
  - its primary key becomes (session_id, ts_ns, batch, rank), where rank is
    the event's number in its batch (the `event_id` suffix). That's the
    render's order by construction (it sorts a session's events by time,
    batch, rank), and it's checked equal to `seq` on the capture. The key never
    changes for an event, so a late event is one insert, not a renumbering of
    every row after it, and range reads need no second index on the largest
    table;
  - `seq` stays, out of the key, only for the v7 loader until step 8, then
    goes;
  - `head_all`, `head_pub`: the row starts a unit in the Events tab with ticks
    folded, over all rows and over the non-internal ones. A unit is a row that
    isn't a tick, or a run of consecutive tick rows (`tengu_tool_use_progress`)
    in that sequence: a tick starts one unless the row before it in that
    sequence is a tick. A late event changes its own flags and the next row's;
  - `batch_first`: it's its batch's first event in the session (in key order);
    otherwise `batch_prev_ns` is the time of the batch's event before it in the
    session;
  - partial indexes:
    - (session_id, ts_ns) WHERE `batch_prev_ns` < `ts_ns` − `ts_ns` mod 60 s:
      only the rows whose previous batch event lies in an earlier minute;
    - (session_id, ts_ns, head_sha) WHERE head_sha IS NOT NULL: `/state`'s
      first and last head. Without it, a session with no head would scan its
      whole range;
    - (session_id, ts_ns) WHERE recap: `/state`'s recaps.
- **`session_marks`:**
  - call marks get `prompt_first` and `prompt_prev_ns`, the same per prompt id,
    with the same partial index;
  - an index (session_id, kind, t_ns);
  - network marks get `bypass_from_ns`: the earliest start of a not-captured
    window that ends at or after the segment's start (the segment is bypassed
    when its end reaches it). A closed segment also gets `not_captured`, its
    missed calls.
- **`prompt_rows`:** v7's key and dense, time-ordered `seq` (a tested render
  property, §Testing), plus indexes (session_id, t_ns) for the range's ends and
  (session_id, prompt_index) WHERE type = 'prompt' for the prompt jump. A late
  prompt row renumbers the session's later prompt rows: a few.
- **`session_rows`:**
  - `batch_gap_ns`, `prompt_gap_ns`: the longest time between two consecutive
    events of one batch, and between two consecutive calls of one prompt, in
    the session;
  - `unattributed`: the session is visible (`dim_session_visible`) and no
    account names it (`dim_session_account`). Both are written by the session's
    own key (checked on the capture), so the flag is rendered within the key.
    `unattributed`'s scope is then a partial index (session_id) WHERE
    unattributed, not an anti-join in every statement. Every visible session
    has a session row (checked);
  - the labels and children, final (§3.10).
- **`batch_parts`:** `last_ns`; `spans`, when another key holds a part of the
  batch; for a spanning part, `ns`, its events' times. Index (key, first_ns)
  WHERE spans.
- **New, per session.** Each is rendered whole every run: a few rows.

  | Table | Primary key | What |
  |---|---|---|
  | `presence_runs` | (session_id, live, seq) | the session's presence fold (`timeline.PRESENCE_SQL` over its own segments): from_ns, to_ns, state. `live` false: today's fold for a session that isn't live. `live` true: the fold with [last_ns, ∞) focused added, which the API cuts at `now` (§5.3) |
  | `gap_windows` | (session_id, seq) | the session timeline's not-captured lane over the whole session: from, to, count, reported, captured, cause |

**No value that grows with its prompt or session is stored on a row.** A row's
values may only change when its own data does. A value like a step's "of M", a
prompt's call count, or "N events" while its batch fills is kept once, on a
per-prompt (or per-session, per-batch) row, and joined in at read.
- v7's drawer breaks this as built: `drawer_requests.data` embeds the prompt's
  `stepCount`. Every new call rewrites every earlier call's row in its prompt:
  222 rows a call on the capture's longest prompt, at one call per commit.
- The fix: the drawer reads the count from the prompt's row in its one
  statement. The worker change is in progress; this spec only holds the rule.
- The write-amplification gate (§Testing) asserts the rule per table.

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
| `request_txs` | the request logs (v8: the render of `request_log_rows`) | (key_hash, row_ts_ns), (key_hash, prompt_id), (call_id) |
| `prompt_index`, `codex_turns`, `session_last` | the request logs (v8: the render; `session_last` also a person log's lastDataAt) | (session_id, prompt_id) |
| `request_reports` | the request logs (v8: the render) | (req_id), (client_req_id) |
| `request_missed` | the request logs (v8: the render), the reported drawer | (session_id), (row_request_id) |
| `session_call_accounts` | labels (v8: the render, §3.10) | (account_id), (session_id) |
| `session_edges` | children_json (v8: the render) | (parent_session_id) |
| `session_title_calls` | labels (v8: the render) | (account_id, ts_ns) |
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

*As built (step 8, `internal/worker/retention.go`):*
- each key is forgotten in **one transaction**: its key row is locked first
  (`FOR UPDATE`, `lock_timeout` 1 s) and re-checked against the `n` and day the
  pass read, then its rows go (≤ 5k a statement) and its key row last. A key
  that moved keeps everything, and a page never sees half a key. A lock timeout
  skips the key until the next pass;
- account keys (`day` empty) are never forgotten;
- the recent index is trimmed a day behind the tail's cursor on every pass, raw
  cap or not: nothing else ever deletes it;
- the raw capture is written as `<prefix>/dt=…/` (and full interception's
  `<prefix>/capture/dt=…/`), not under `raw/`: those are the day folders the
  cap deletes;
- the settings are `ROLLUP_RETENTION_DAYS` and `ROLLUP_RETENTION_RAW_DAYS`; the
  `calls/` part runs when the store is a local directory (`ROLLUP_LOCAL_DIR`).

**v8: retention flags the readers of what it forgets.** Some rows a key owns are
read by other keys' renders: `batch_parts`, `session_edges`,
`session_title_calls`, `title_assignments`, `title_candidates` and
`session_call_accounts`. Forgetting them changes what those keys render: a
batch's count, a parent's children, a label. So the transaction that forgets a
key also flags (`render`) the readers of each of these rows it deletes, by the
same rules a commit uses when it changes them (§2). A reader that's being
forgotten in the same pass is skipped.

### 3.7 Request drawer: precomputed

The drawer is rendered by the worker, like the session tables. Opening it is one
indexed lookup; nothing about it is derived at request time.

| Table | Primary key; index | What |
|---|---|---|
| `drawer_requests` | (request_id, key); (key) | per drawer id: `data`, the finished `/requests/{id}` object, and `details`, the finished `/details` object, each as the JSON today's models serialize |
| `drawer_events` | (event_id, key); (key) | per Claude Code event: `/events/{id}` without its `raw` (its time, name, session, and the batch and event key that find its raw row) |

- **Which rows.** Every key renders a row for each of its ended calls (a
  WebSocket turn as `<connection>~<turn>`), each of its sessions' `reported:`
  ids (its `request_missed` rows), and each of its Claude Code events as
  `<batch>:<rank>`. A call in flight has no row: the drawer 404s until it ends.
- **The derivation is `request_detail.py`'s, ported to Go**
  (`internal/drawer`), over the key's own facts: what today's drawer loads
  (the call's keys) is the call's own key for every call, plus the batch's
  other keys for an analytics batch, which only give its session (below).
  Step N of M, prev/next and retries change as the session grows, so every
  run renders the key's drawer rows whole and writes the ones whose hash
  changed (§2 Run 6), as for the session tables.
- **What only the call's detail object holds** (headers, the whole `complete`
  row, body chunk times, retained bodies) is read once: the run GETs
  `calls/<id>.ndjson.gz` for each call that ended since, and keeps what the
  drawer reads of it (the wire summary, never the bodies) as an *external
  input* of the key's state: journaled with its records, so a cold load has it
  without a GET. An object missing for a call that ended under 2 minutes ago
  (wall clock; `Config.DetailWait`, which the rollup service sets) is retried:
  the run keeps its flag, with `render`. After that the call is rendered from
  its `requests` row, as today's drawer does for a missing object. (Ingest
  writes the object when the call ends, before the key file holding its
  ending.)
- **Cross-key inputs are read at render time** from the other keys' stored
  rows, and also kept as external inputs, so the render stays a function of
  the state and a cold load's baseline is what was committed. Today that's
  `batch_parts`: an analytics batch's count, name and session are the sums
  over every key's part (the drawer's session of a batch call, and the
  Requests tab's "N events" of an ended batch spanning sessions). Each run
  reads the other keys' parts of every batch it holds: one statement.
- **Dirtiness crosses keys through `dirty_keys`.** A commit whose
  `batch_parts` change takes `pg_advisory_xact_lock` on each changed batch (in
  order), then flags every other key holding a part of it (or its call) with
  `render = true`, and itself when the other keys' parts it now sees aren't the
  ones its render read. A run of a flag with `render` renders and commits even
  with no new file. The lock orders two commits of one batch, so the second
  sees the first's part: neither misses the other.
- **The API** reads the row by its primary key, in one statement with
  `/status`'s values (`lastDataAt` is global, quirk 7), and validates the JSON
  through today's model. `/raw` reads the row (existence) with the call's
  `requests` row (the fallback view) in one statement, and formats the detail
  object as today. `/events/{id}` reads its row the same way and formats its
  raw row from the batch's detail object: storing every event's raw row would
  copy the analytics metadata again (the largest table, §Scaling budget).
- **Retention** forgets both tables with the key (by `key`).

### 3.8 Cells: sums per minute and per hour

A cell holds what the pages merge of one unit's rows in one minute or one hour:
counts, sums, the keys seen, the first of something. The API merges whole cells
and reads only the rows of the range's partial minutes (§5.3).

- **Two levels:** `lvl` 60 (minutes) and 3600 (hours). `t` is the cell's
  start, `t_ns − t_ns mod lvl` in Unix ns: aligned to the epoch, as the
  timelines' bins are.
- **Written by the rows' own key,** rendered from its rendered rows and diffed
  like every rendered table (§2 Run 6). A run that adds rows to the current
  minute writes that minute's and that hour's cells for the keys it touched,
  and nothing else.
- **Values merge** (§5.1):
  - counts and sums add;
  - a sum is NULL when no row had a value, so a sum over cells is NULL exactly
    when today's sum over the rows is;
  - `first_*` carries the earliest (t, seq) and its value.

  Floats are summed per cell (decision 6).
- **Keys are text, and a primary key can't hold NULL.** Each nullable key
  column (`key`, `key2`; `agent`, `client` in `request_cells`) has a boolean
  `<column>_null` beside it in the key, and holds '' when it's set. So NULL and
  '' stay two keys, as they are today.
- **`key_num`** holds a key's number where the read sorts numerically: the
  `pr` dim's PR numbers, where today's `k.isdigit()` holds (Python's rule,
  ported and tested on every code point), and `pr_link`'s.

**`session_cells`:** primary key (session_id, lvl, t, dim, key, key_null, key2,
key2_null), written by the session's key. Columns: `n`; `s1` to `s5` (bigint
sums); `f1` (a double sum); `first_t`, `first_seq`, `first_val`; `key_num`.

| dim | Rows (filter) | key, key2 | Values | Read by |
|---|---|---|---|---|
| `event` | `event_rows` | name, unnamed | n; s1 rows with `head_all`; s2 rows with `head_pub`; s3 non-internal rows | the analytics lane (count, top 3 names a bin); the summary's names and event types; `/state`'s events and pool changes; the Events tab's total and shown counts (§5.3) |
| `tool`, `mcp_tool` | `tengu_tool_use_success` with a tool name, by `is_mcp` | tool name | n | the summary's toolsByName, toolCalls, builtinUsed |
| `mcp_pair` | MCP tools named `mcp__server__tool` in analytics (parsed by the worker); `call_tool_uses` of kind mcp | server, tool | n | the summary's MCP servers, mcpToolsUsed |
| `builtin` | `call_tool_uses` not mcp | tool | n | the summary's builtinUsed |
| `word` | `tengu_bash_tool_command_executed` | argv0 | n | topCommandWords |
| `destructive` | the same, category ≠ none | category | n | the destructive counts |
| `git` | `tengu_git_operation` | operation | n | the summary's commits, pushes |
| `pr` | `tengu_session_linked_to_pr` | pr_number (and `key_num`) | n | the summary's PRs, in number order |
| `head` | head_sha set | head_sha | n | headChanges (the summary, `/state`) |
| `hook_name` | `tengu_run_hook` | hook_name | n | the summary's hooks |
| `hook`, `git_op`, `pr_link`, `recap` | `hook` set; `git_op` set; `pr` set; recap, recap_key | the value (`pr_link` also `key_num`) | n | `/state`'s hooks, commits, pushes, PRs (in number order), recaps |
| `api_success` | `tengu_api_success` | — | s1 cache_5m, s2 cache_1h, f1 cost_usd | the summary's cache writes and cost |
| `hook_ms` | `tengu_tool_use_success` with hook_ms | — | n, f1 | hookOverheadMs (f1 / n) |
| `outside_cwd` | bash commands outside the cwd | — | n | outsideCwd |
| `batch_first` | rows with `batch_first` | — | n | analyticsBatches (§5.3) |
| `call` | call marks | request_class | n, s1 input, s2 cache_read, s3 cache_write, s4 output, s5 context | the model-call lanes; the summary's calls and tokens; `/state`'s modelCalls |
| `call_unreported`, `prompt_first` | call marks: unreported; with `prompt_first` | — | n | estimatedCalls; the summary's prompts (§5.3) |
| `reported`, `missed`, `compaction` | those marks | — | n | coverage; the lanes' not-captured counts; `/state` |
| `friction` | friction marks | friction kind, detail | n, first (t, seq, prompt_index) | the friction lane: count, first prompt, details by first occurrence |
| `prompt` | `prompt_rows` of type prompt | — | n | `/state`'s prompts |

**`request_cells`:** primary key (unit, lvl, t, kind, hidden, captured, agent,
agent_null, client, client_null, sessionless, missed, in_tab, key), with an
index (key) for retention: Postgres 17 has no skip scan, so the reads' unit
must lead. Column `n`. Written by the request-log row's key, so several keys
write cells of one unit. `unit` is:
- `s:<session>` for every request-log row with a session (its own, or its
  batch's) and every missed row;
- `h:<credential>` for every transaction with a credential.

A person's log merges its credentials' `h:` cells and its sessions' missed `s:`
cells. A session's tab merges its `s:` cells with `in_tab` (§3.9); a Codex
thread's also merges its credentials' sessionless Codex cells of its client
(§5.4).

**Size on the capture.** The heaviest session (53k events over 48 h) has ~1.5k
hour cells and ~10k minute cells over its event dims, and ~0.8k minute cells
over its marks. The heaviest person (13 sessions, 71k events) has ~3.3k and
~17k. The busiest minute of one session holds 3.3k events (p99 520).

### 3.9 Request-log rows: both logs, one row per request

`request_log_rows` replaces `session_request_rows`, which stays beside it until
step 8 because the v7 loader still reads it. It holds one row per request
either log lists, written by the key that holds the request:
- every transaction with a credential (`request_txs` with `is_tx` and a key
  hash), in a session's key or a `c:` key;
- every not-captured call (`request_missed`, its `reported:` id), in its
  session's key.

Primary key (request_id, key). Indexes:
- (key_hash, ts_us, request_id) and (key_hash, kind, ts_us, request_id): the
  transactions, for a person's log without and with `kind`;
- (session_id, ts_us, request_id) and (session_id, kind, ts_us, request_id):
  every row with a session, for a session's tab;
- (key), for retention (§3.6), as the drawer's tables have.

**Columns:** today's request-log columns as the logs show them
(`stream_sql.COLUMNS`, but not person_id), plus:
- `key_hash`; `ts_us` (`ts_ns / 1000`: the logs' order and their range);
  `sessionless`; `missed`;
- `in_tab`: its session's tab lists it. Today's session tab keeps a row only if
  its credential is one of its session's (`dim_session_credential`). On the
  capture 24 rows fail that: 6 hidden Codex title calls (in 6 sessions) and 18
  analytics batch calls. Without the flag v8 would count them in the tab's
  total, its hidden count and `/state`'s request count. Rendered within the
  key: every row with a session is held by its session's key (checked on the
  capture). A batch call in flight in a `c:` key reads its session's
  credential row with the batch parts, and a session's commit that adds a
  credential flags its batches' other keys, as a part change does;
- `session_id`, final: the call's own session, or its analytics batch's (the
  batch parts across keys, read at render time as the drawer's are, §3.7). A
  batch call still in flight in a `c:` key gets its session this way, so the
  session's tab finds it by session;
- **the three values a person's scope decides.** Each comes from the call's
  own session (I1, I2, §5.3), so the read tests `session_id` against the scope.
  No separate source column is kept; the periodic check (§5.3) is the alarm if
  that ever stops holding:
  - `prompt_index`: the prompt's number, from its session's `prompt_index`
    (Claude Code) or `codex_turns` (Codex) rows;
  - `ttft_ms`, `tokens_cache_write_5m`, `tokens_cache_write_1h` (model calls):
    from the first report of its request id;
  - `step_index`: its number among its prompt's model calls, in today's order
    (time, then request id). When the prompt's calls used more than one
    credential (a token refreshed mid-prompt: 2 prompts on the capture), also
    `step_creds` and `step_before`: for each credential, how many of the
    prompt's calls with it come before this one.

**Why the sources.** Today's person log takes these values from the person's
own sessions and credentials only (`stream_sql` over the scope). On the
capture, credentials are shared across email groups: a person's log holds
thousands of model calls from sessions outside their scope, shown without a
prompt number or a time to first token. The read shows each value only when the
call's session is in the scope (§5.3), so one row serves every person and every
session.

**What the render reads of other keys:** batch parts (v7), nothing else. A
prompt's calls, its numbers and its reports are in its session's key (I1 to I3,
§5.3).

**Writes:** a new call adds its row and nothing renumbers: the order is read
from `ts_us` and `request_id`. A late call early in its prompt rewrites the
later calls' steps in that prompt.

### 3.10 Labels and children: finished on the session row

v7 rendered a session's own label and joined the rest at read time
(`sql/labels.sql`). v8 renders all five columns on `session_rows`, reading the
other keys' rows at render time:

| Columns | From | Read at render | Flagged by |
|---|---|---|---|
| `children_json` | the `session_edges` naming it as parent, each written by the child's key | its edges | the child's commit that changes its edge |
| `label_text_alone`, `label_source_alone` | its own title; else, for a Codex thread with a model call, the earliest title call (by time, then call id) of its accounts in its first 5 minutes (`labels.sql`'s `alone`); else `Session <id prefix>` | those title calls | the title call's commit (§2) |
| `label_text`, `label_source` | its own title; else the title of the call that names it in `title_assignments`; else `Session <id prefix>` | the assignment naming it | the assignment's commit |

**`title_assignments`** (call_id, key) → session_id, title, ts_ns, with indexes
(session_id) and (key): `labels.sql`'s assignment, made by the key holding the
title call.
- **The rule (today's).** In time order (time, then call id), each title call
  names one *candidate*: a Codex thread with no title of its own and a model
  call, with the call's account, started at most 5 minutes before it. It names
  the one that started last (then the least session id) among those no earlier
  call named.
- **`title_candidates`** (account_id, start_ns, session_id), the primary key:
  each candidate thread writes one row per account of its calls. It changes
  only when the thread's start, accounts or candidacy do.
- **What a call's render reads.** Its choice depends only on the candidates
  started in the 5 minutes before it, and on which of them earlier calls named.
  An earlier call can name one of them only if it's in those same 5 minutes. So
  the render reads just those: the candidates (`title_candidates`, an index
  range by account and start) and those calls' assignments.
- **A change runs forward.** A late call, or a candidate that changes, can
  change the choice of the calls in the 5 minutes after it. Its commit flags
  them (§2), and each choice that changes flags its own next 5 minutes, until a
  choice stays the same. In the live case a new call is the newest, and flags
  nothing after it.
- **The same as today's.** Today the assignment runs over an account-connected
  component, in the same time order. Calls interact only through shared
  candidates, so splitting the work by time changes nothing.

## 4. Cross-key values

v7 joined most cross-key values at read time, bounded by the page's entity. v8
moves them to write time: the reader's render reads the other keys' stored rows,
and the commits that change those rows flag it dirty (§2). What stays at read
time is who a person is, and the merges over that person's rows.
- v6 precomputed these with the merge, the person loop, `pdirty`, dispatch and
  group objects. None of that comes back: each value is rendered by one key,
  from rows other keys already store, and the flags name the keys to render
  again.
- A value that depends on the request (a range, a person's scope) can't be
  rendered whole. Its parts are, and the read merges them (§5.3).

| Value | v7 | v8 | How |
|---|---|---|---|
| Person scope: the email group, its sessions and credentials; `unattributed` | read | read; `unattributed` by a session-row flag (§3.3) | Identity, not data: the group's dimension rows by the `lower(email)` index, its sessions by `dim_session_account`, its credentials by `dim_session_credential`, in a CTE each statement repeats (v7 §5). At write time, an account linking would rewrite its group |
| Person facts: `person_sql`'s picks, linked accounts, the header's rollup | read | read | merges over the group's rows (§5.1) |
| A batch's session, count and name | write | write | v7 §3.7. Spanning parts also carry their event times (§3.3) |
| A request's prompt number, first report and step across a person's sessions (quirk 3) | read (`stream_sql`) | write, shown by scope at read | `request_log_rows` carry each, from the call's own session (§3.9); the read shows it when that session is in the scope (§5.3) |
| A session's Requests tab: other keys' rows | read (`stream_sql` over candidates) | write; a filter at read | A batch call in flight gets its session at render (§3.9). A Codex thread's unthreaded calls are read by the thread's credentials, client and window: a filter, nothing derived |
| `children_json`, `label_text_alone`, Codex `label_text` (quirk 2) | read | write | §3.10 |
| Presence and not-captured windows across a person's sessions | read | read (§5.5) | folds over the range's few segments and missed calls |
| `/people`, `/overview` | read | read | v7 §5, unchanged |
| `/status`; expiry's data time | read | read | unchanged |

## 5. API: reads finished rows, scaled on CPU

v7's API read each page's rows into an in-memory DuckDB and ran today's endpoint
code over them. v8 replaces that for the session and person pages. The
directory keeps it (§5.6).

### 5.1 What the API does

For each request it takes one pooled connection, opens one READ ONLY,
REPEATABLE READ snapshot, and runs a fixed number of statements (§5.4). Each is
bounded by the page's entity, its range and an index. In them it:
- **reads finished rows.** A row the page shows is read as stored. A count or a
  sum is merged from cells: the whole cells inside the range, plus the rows of
  its partial minutes, merged the same way (§5.3);
- **filters, orders and pages:** the range, `kind`, `captured`, `hidden`,
  `agent`, `internal`, the sessions a range shows, `page`;
- **merges.** A merge is associative and commutative: count, sum, min, max, the
  value at the least or greatest (time, id), any, set union. A merge over cells
  equals the merge over their rows however they're grouped: that's what makes
  cells exact. Person pages merge per-session rows and cells over the person's
  sessions;
- **applies `now`:** live (`now − last_ns ≤ 5 min`), a prompt in flight, a step
  streaming (today's expressions over the stored thresholds), and a range, band
  or segment that ends now;
- **resolves the person** (§4).

Python then maps the rows onto today's models, as the routers do now: field
copies, today's rounding, ns to times. There's no DuckDB, and no cache.

Every computation that stays, each with its bound, is listed in §5.5; the four
that depend on order are decision 2.

### 5.2 Inventory: what each page computes today

The UI's parameters are the ones `ui/src/api/hooks` sends. The rest are
accepted, and the parity URLs use some of them.

| Endpoint (v7, warm p95 on the capture) | Parameters the UI sends (also accepted) | What today's code computes per request, from | Other keys | `now` |
|---|---|---|---|---|
| `/sessions/{id}` (55 ms) | — | the session row; its alone label from its accounts' title calls; its children from edges | title calls; edges | live, end |
| `/sessions/{id}/timeline` (400 ms) | `bin`, always, on the ladder; the range is the whole session | from `session_marks`, `event_rows`, `prompt_rows`, `presence_segments`: call and missed bins (count, tokens, classes); event bins (count, top 3 names); friction per (bin, kind); not-captured windows (15-minute gaps, reported count, bypass); the presence fold; typed prompts; context points; compactions; permission and network segments (bypass, not-captured count) | the alone label | live; the range's end; open segments; presence; prompts in flight |
| `/sessions/{id}/state` (444 ms) | `from`, `to`: a brush, any ms | over [lo, hi] of `event_rows`: events, commits, pushes, PRs, recaps, pool changes, first, last and distinct heads; up to hi: distinct hooks, the latest MCP total and skills; `session_tools` first used by hi; the counts of prompts, calls and compactions; the Requests tab's count | through the Requests tab | the default hi |
| `/sessions/{id}/events` (396 ms) | `from`, `to`, `q`, `internal`, `collapseTicks` (always), `page`, `pageSize` 20 | `event_rows` in range, filtered; consecutive ticks folded (`lag` over the filtered rows), counted, paged by unit | — | the default hi |
| `/sessions/{id}/requests` (131 ms) | `from`, `to`, `kind`, `q`, `captured=false`, `hidden=true`, `page`, `pageSize` 20 (`order`) | the rendered rows, and other keys' rows `stream_sql` builds from candidates; counts (hidden, total, per kind); order; page | a Codex thread's unthreaded calls; batch calls in flight in `c:` keys | — |
| `/sessions/{id}/prompts` (105 ms) | `from`, `to`, `page`, `pageSize` 20, `prompt` | `prompt_rows` in range, numbered; the page holding prompt N; its `prompt_steps` | — | live, in flight, streaming |
| `/people/{id}` (86 ms) | — | `person_sql` over the group's dimension rows; a rollup over all its session rows; agents; calls per day (`session_call_days`); linked accounts | the group's dimension rows | live |
| `/people/{id}/sessions` (91 ms) | `from`, `to`, `agent`, `page`, `pageSize` 20 | the sessions a range shows; agent; nesting under a shown parent; paging by root; labels over the account component (`labels.sql`) | Codex titles; edges | live; shown |
| `/people/{id}/summary` (369 ms) | `from` (now − 24 h, 7 d or 14 d, in ms; no `to`), or a brush | over the shown sessions in [lo, hi]: events and 7 dimensions; distinct batches, types, heads and hooks; hook overhead, cost, cache writes; the latest MCP and skills to hi; call tokens, subagents, unreported calls, distinct prompts; reported and missed; missed windows; tools from responses; account calls (from the person's log) | the log's credentials | shown; the default hi |
| `/people/{id}/timeline` (355 ms) | `from` (now − span), `bin` 300 or 3600 (none for All) | bands: the shown sessions, packed into rows; over every scope session in range, the session timeline's lanes; presence over the bands | Codex titles | live; ends; hi |
| `/people/{id}/requests` (296 ms) | `from`, `to`, `kind`, `q`, `captured`, `hidden`, `agent`, `page`, `pageSize` 20 (`order`) | `stream_sql` over: the scope credentials' transactions in range, the same prompts' others, their reports, prompt numbers, Codex turns, missed calls, batch parts. Then filters, counts, order, page; lastDataAt | every key using the credentials | — |
| `/coverage?personId=` (the UI never calls it) | `personId`, `sessionId`, `from`, `to` | the marks' counts in range; the log's missed rows; missed windows | — | — |
| `/people` (61 ms), `/overview` (66 ms), `/status` (27 ms), the drawer (~35 ms) | | out of scope (§5.6) | | |

Ranges are often arbitrary instants: a brush is any ms, and a person's preset
is `now − span` at ms. Bins are always on the ladder: 60, 300, 900, 3600,
10800, 21600, 43200, 86400 s.

### 5.3 The read primitives, and why each is exact

**Finished rows.** Rows shown as they're stored. Their order is a stored key,
and paging takes a page of it.

**Ranges: cells and edges.** Each endpoint's range is a half-open interval
[L, H) in ns, from its own predicate:
- events and marks, `BETWEEN lo AND hi`: [lo, hi + 1);
- prompts, `t_ns // 1000 * 1000 BETWEEN lo AND hi`: [⌈lo⌉, ⌊hi⌋ + 1 µs),
  rounding to whole µs;
- the request logs, `ts_ns // 1000` against `lo // 1000` and `hi // 1000`:
  [⌊lo⌋, ⌊hi⌋ + 1 µs);
- the summary's account calls, `ts_ns BETWEEN lo AND hi` over the person's log:
  [lo, hi + 1) in ns, not µs. Its edge rows are tested on `ts_ns` too.

The rows in [L, H) are a disjoint union of three parts:
1. the whole hours inside [L, H): hour cells;
2. the whole minutes inside [L, H) that aren't in such an hour: minute cells,
   at most 59 at each end;
3. the rows of the partial minutes at each end, tested with the endpoint's own
   predicate: the *edge rows*.

A merge over the parts equals the merge over the rows. The edge rows are merged
by SQL that a test holds equal to every minute's cell (Testing).

**Bins.** A timeline bin is `t − t mod bin`, aligned to the epoch.
- Every bin the UI sends is a multiple of 60, and those of an hour or more are
  multiples of 3600.
- A bin of b seconds reads hour cells when 3600 divides b, else minute cells.
  Each whole cell lies in one bin.
- The range's first and last bins are cut as above.
- A `bin` that isn't a multiple of 60 has no exact cells: 422 (decision 1).

**Keys seen in a range** (event names and types, heads, hooks, tools, PRs): the
union of the cells' and the edge rows' keys. A key occurs in the range if and
only if it occurs in one of the parts.

**Things that span time, counted once** (an analytics batch's events, a
prompt's calls): first occurrence.
- Within a session, the entity's rows are in time order. A row is *first* if
  it's the entity's first in the session. Otherwise it carries `prev`, the
  time of the entity's row before it.
- The entities with a row in [L, H) are counted by: the first rows in [L, H),
  plus the rows in [L, H) whose `prev` < L.
- Each entity with a row in range is counted exactly once, by its first row in
  range. That row is either its first overall, or its previous row is before L.
  No other row counts: a later row in range has its previous row in range.
- The first term is a cell sum (`batch_first`, `prompt_first`).
- The second term's rows lie in [L, min(H, L + G + 1)), where G is the
  session's longest gap between consecutive rows of one entity (`batch_gap_ns`,
  `prompt_gap_ns`). One whose previous row is in the same minute is in L's
  partial minute, already read as an edge row. One whose previous row is in an
  earlier minute is in the partial index (§3.3), which holds only those. On the
  capture G is 17 minutes for batches (p99 8 s), and the index holds about one
  row in six batches.
- A person counts per session, and adds. A prompt's calls are in one key (I3,
  below). A batch can span sessions (6 of 4.2k for the capture's heaviest person). The
  sum counts it once per session with an event in range, so the read subtracts
  k − 1 for each spanning batch with events in range in k ≥ 2 of the sessions
  it counts. The spanning parts' event times give k: `batch_parts.ns`, through
  the (key, first_ns) index from L − G.

**The Prompts tab: positions.** A session's `prompt_rows` are in time order
with a dense `seq` (v7; a tested render property). The rows in a range are one
contiguous run of it, so its total is a `seq` difference, and so is the place
of prompt N in it (the page holding it).

**The Events tab: unit starts, counted in cells.** Today's SQL folds a tick into
the row before it when that row is also a tick (`lag` over the rows the view
keeps). Let v be the view: all rows, or the non-internal ones; its sequence is
those rows in key order (§3.3).
- Within a range, a row's previous row in v is its previous row overall,
  except for the range's first row of v: it has none, so it always starts a
  unit. Every other row starts a unit in the range iff it starts one overall
  (`head_all`, `head_pub`).
- **Total** (every event in range): the `event` cells' `n`, plus the edge rows.
- **Shown:** with ticks folded, the `event` cells' head counts for v (s1 or s2)
  plus the edge rows' heads, plus 1 if the range's first row of v isn't a head
  overall (one lookup). Unfolded, every row of v is a unit: `n`, or s3 for the
  non-internal rows.
- **The page:** the units of v in range start at its rows with a head flag,
  plus its first row. Two `ORDER BY key OFFSET … LIMIT 1` reads over those
  give the page's first unit start and the next page's. The page's rows lie
  between them, and each unit shows its first row, with `collapsedCount` the
  number of its rows there. That's bounded by the page's units: their rows,
  plus the offset's index entries for a later page.
- This replaces the per-row positions an earlier draft stored (`pos_pub`,
  `grp_all`, `grp_pub`). Those renumbered every later row on a late event;
  head flags change only on the late row and the one after it.

**`now`.** Every value that depends on `now` is one of three kinds:
- **a threshold on stored values:** live (`now − last_ns ≤ 5 min`), in flight,
  streaming, and the live tools and exit codes. These are today's expressions,
  run in Postgres over the same stored columns;
- **`now` itself:** a default range end; a band's or an open segment's end; an
  open network segment's bypass (its end ≥ `bypass_from_ns`) and its
  not-captured count (missed cells over the segment);
- **presence while live.** Today's fold caps each open segment at `now` and adds
  [last_ns, now] focused. Before `now`, every instant is covered by the same
  segments as in the fold with [last_ns, ∞) focused added; after `now`, nothing
  survives today's fold. So the live fold is the stored `live` runs, cut at
  `now`.

These hold when `now` is before the newest row too (a client clock ahead of
the API's): the range ends at `now`, and the runs are cut there.

**The scope's values on request-log rows** (§3.9). A row in a person's log
shows:
- its prompt number and report values if its `session_id` is one of the
  person's sessions;
- its step as 1 + the sum of `step_before` over its credentials that are the
  person's (`step_index` when there's one credential).

That's today's `stream_sql` exactly when:
- **I1:** a prompt's or turn's numbers are all in the session of its calls;
- **I2:** a request id's reports are all in its call's session;
- **I3:** a prompt's model calls are all in one key.

They're also why the render needs no other key for these values: the call's
own key holds them.

All three hold on the capture (0 violations, checked in Postgres for this
spec), and follow from how the ids are made: the client numbers prompts and
sends reports per session, and a call carries its session. Tests assert them on
every dataset.
- **A periodic check alarms if one breaks.** Every hour, beside retention, a
  worker singleton runs one SQL check per invariant over the keys committed
  since its last run (`keys.updated_ns`), never over all history. It exports
  `invariant_violations{invariant}` on `/metrics`, and a nonzero value alarms,
  as `quarantined_keys` does.
- If one broke, the person's log would show the row's own session's value where
  today's shows the scope's.
- A session's tab is the scope of one session, so its rows (those with `in_tab`)
  show all three.

### 5.4 Each page in v8

The numbers are the capture's heaviest session and person, estimated from their
measured cells; the gate measures them.

| Endpoint | Statements, in order, in one snapshot | Rows read |
|---|---|---|
| `/sessions/{id}` | 1. the session row | 1 |
| `/sessions/{id}/timeline` | 1. the session row. 2. the lanes' cells at the bin's level (`call`, `missed`, `event`, `friction`), merged per bin. 3. its rows, tagged: typed prompts, context points, compactions, permission and network segments, gap windows, presence runs | at bin 900 (its 48 h): ~9k minute cells and ~2k rows, ~1.7k of them context points. Today: 53k events and 2.5k marks |
| `/sessions/{id}/state` | 1. the session row. 2. cells and edge rows over [lo, hi] (up to hi for hooks; every hour for recaps), and the request cells (`in_tab`) of the Requests tab's range. 3. lookups: the latest MCP and skills counts to hi, the first and last head in range, the recaps in range, `session_tools` first used by hi | the whole session: ~2k hour cells, at most ~1.2k minute cells, at most 2 minutes of rows |
| `/sessions/{id}/events` | 1. the session row. 2. the total and shown counts (`event` cells and edge rows, the range's first row of the view); the page's first unit start and the next page's; the rows between, grouped by unit | the range's cells, a few lookups and the page's rows. A later page also reads the index entries of the unit starts before it. With `q`: the range's matches (§5.5) |
| `/sessions/{id}/requests` | 1. the session row. 2. request cells and edge rows: hidden, total, per kind. 3. the page, by (`ts_us`, `request_id`): the session's rows with `in_tab`, and a Codex thread's unthreaded rows by its credentials, client and window | ~0.3k cells and 20 rows |
| `/sessions/{id}/prompts` | 1. the session row. 2. the page, its total and its number (positions), with `now` applied. 3. the page's steps | at most 20 prompts and their steps |
| `/people/{id}` | 1. the group's dimension rows: facts, devices, linked accounts. 2. its session rows, merged: the rollup, agents, each linked account's devices. 3. calls per day | the group's rows: tens to hundreds |
| `/people/{id}/sessions` | 1. the scope's session rows: shown, agent, nested, paged; the total and lastDataAt | the scope's session rows |
| `/people/{id}/summary` | 1. the shown sessions. 2. their cells, edge rows, straddling rows and spanning parts over [lo, hi]. 3. each shown session's latest MCP and skills counts to hi. 4. the account calls' cells; the missed windows (§5.5) | the 14 d preset: ~3.3k hour cells, at most ~2.5k minute cells, at most 2 minutes of rows per session |
| `/people/{id}/timeline` | 1. the scope's sessions and the bands. 2. cells per bin and edge rows. 3. typed prompts in range. 4. the bands' presence segments (§5.5). 5. the missed windows (§5.5) with their cells | 24 h at 300 s: ~6k minute cells. Today: 24k events |
| `/people/{id}/requests` | 1. the scope's credentials and sessions. 2. request cells and edge rows. 3. the page: for each credential (and each requested kind, by the kind index), and each session's missed rows, its first offset + size rows in order, merged | the cells, and k·(offset + 20) rows for k credentials |
| `/coverage` | 1. the scope. 2. mark cells and edge rows; the missed rows. 3. the windows (§5.5) | the missed calls in range |

### 5.5 Everything still computed per request

Every computation left in a request, with its bound. "The page's rows" means
the rows the response shows. None of these reads a range's rows except where
the bound says so.

**Merges and filters over finished rows** (§5.1: exact however they're grouped):

| Computation | Where | Bound |
|---|---|---|
| The person's scope: the group by email, its sessions, its credentials; `unattributed` by its flag | every person page | the group's dimension rows |
| The person header's merges: the rollup's sums and newest picks, the union of devices, agents, `person_sql`'s picks, each linked account's devices | `/people/{id}` | the group's dimension and session rows |
| The shown-sessions filter, nesting under a shown parent, paging by root | person sessions, summary, timeline | the scope's session rows |
| Cell merges: sums, counts, the first by (time, seq), key unions (names, heads, hooks, tools, PRs) | every count and lane | the range's cells: whole hours, and at most 59 minute cells at each end, per session or credential, per key |
| Edge rows, merged by the cells' own rules | every range | the rows of at most 2 partial minutes per session or credential (the capture's busiest minute: 3.3k events) |
| Per bin: the top 3 event names; friction's count, first prompt and details by first occurrence | both timelines | the bins' cell keys |
| First-occurrence straddlers, and the spanning-batch correction (k − 1) | the summary's batches and prompts | the rows in [L, L + G] whose previous row is in an earlier minute (a partial index); the spanning parts from L − G |
| The Events tab's unit math: the first-row adjustment, the page's two unit starts, `collapsedCount` | `/events` | a lookup, the page's rows, the offset's index entries |
| The Prompts tab's positions and the prompt jump | `/prompts` | two `seq` lookups |
| The request logs' scope values: the session test for prompt numbers and reports; the step as a sum over the scope's credentials | request logs | the page's rows; a credential list per row only after a mid-prompt token refresh |
| A Codex thread's unthreaded calls: credentials, client, kind and the thread's window | a Codex session's tab and `/state` | cells and the page, by index |
| The person log's page: a k-way merge of each credential's (and kind's) first offset + size rows | person requests | k·(offset + size) rows |
| `now`: thresholds, a range or segment ending now, the live presence cut, the open network segment's counts from cells | session pages, person timeline | per row; the session's few runs and segments |
| Presentation: field copies, today's rounding, number order of PRs, the summary's top 8 words | every page | the response |

**Folds that depend on order** (decision 2: each runs in Postgres, in the page's
statements, as today's SQL does):

| What | Where | Bound | Why it isn't a merge |
|---|---|---|---|
| `q`: a substring of an event's name and summary, or of a request's path and detail | the Events and Requests tabs; a person's requests | the range's rows of the session or the credentials (a scan with `strpos`), and for the Events tab, its tick fold over the matches (one window function) | free text can't be precomputed |
| Band rows (`cc.pack_rows`) | the person timeline | the bands shown | greedy, and its input depends on which sessions the range and `now` show |
| Presence across sessions | the person timeline | the bands' segments in range | the priority fold needs every session's raw segments: a session's own fold drops time only it sleeps through, which still makes another session's time asleep |
| Not-captured windows across sessions, with each window's reported count and bypass from cells | the person summary, timeline and `/coverage` | the missed calls in range (none on the capture), and per window its cells | a window's edges depend on the missed calls around it, in every session |

Only `q` reads events, to match them. The alternatives are contract changes:
drop the tick fold while searching; pack bands in the UI; show presence and
windows per session.

### 5.6 Pages v8 leaves alone

- **`/status`, the drawer and `/events/{id}`:** one statement each (v7 §3.7,
  §5), now through v8's reader instead of the DuckDB loader.
- **`/people` and `/overview`:** v7 §5 as built: the v7 loader into DuckDB, and
  today's directory code (decision 4).

### 5.7 Code

- **The routers call a page reader.** V1Store's reader is today's SQL: it's
  the oracle. PgStore's is v8's statements. Both return what the routers map
  onto the models, so the side-by-side tests compare the two readers through the
  same router.
- **Gone from the API's Postgres path:**
  - the v7 loader for these pages: `pgstore.py`'s session loads, the other
    keys' candidates and `_request_rows`, and `pgstore_person.py`'s loads;
  - `sql/labels.sql`, now the worker's (§3.10);
  - every use of `stream_sql`, `state_sql`, `person_sql`, `rollups.py`'s folds,
    and `session.py`'s and `timeline.py`'s endpoint SQL. They stay for V1Store
    and the oracle.
- **Driver:** with no DuckDB to read Arrow, the API reads Postgres with
  psycopg 3: an exact pin, 14 days old (decision 5). The directory keeps ADBC
  and DuckDB until it's ported (decision 4).
- `agent_calls_in` (`/overview`) and `org_kind` are unchanged.

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

None, as in v7 (decision 7). The schema changes, and there are no migrations,
so on deploy:
- everything under `v1/` is deleted;
- Postgres starts empty;
- ingest and the workers start from new traffic.

No backfill, no side-by-side run, no migration.

## Scaling budget

**Today:** a handful of people, a few hundred thousand rows a day. One small
Postgres and two workers carry it with room to spare. In v8 every page reads its
own rows and the cells of its range: tens of milliseconds at most.

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
| Worker run | per record: apply (#19). Per run: the whole session rendered and hashed, its drawer rows and cells too (O(session) CPU, as v6 and v7: ~40 cores at the target, below; decision 3), the changed rows written. A key with no input costs a key-row read, a LIST and a DELETE | the key's state (with each ended call's wire summary, a few KB) and a hash per rendered row and cell, while warm | key (advisory lock), heartbeat share | S3 LIST and GET, the commit | ~100 commits/s at peak, ~135 rows each (v7: ~100): ~11k rows/s at peak, ~2.7k/s on average, ~240M rows/day; one read of other keys' rows per run; a few flags per commit | ~4 per run, plus one GET per call that ended (its detail object, once): ~650/s at peak |
| Retention | per forgotten key: batched deletes | none | one holder | — | ~240M rows/day deleted at the target (Not now: partitions) | the forgotten keys' objects |
| API | per request: at most 5 statements, reading the page's rows, the range's cells and at most 2 minutes of edge rows per session (§5.4); the drawer one primary-key lookup | none | pods, on CPU | Postgres; S3 GET (`/raw`, `/events/{id}`) | reads only | those GETs |
| Postgres | — | the working set | one primary (vertical) | disk | ~270 GB/day measured per row (below); ~4 TB at 15 days | — |

**What a commit writes, v7 and v8.** A typical run on the capture's heaviest
session: ~100 events, a few calls and ~10 requests over a minute (estimated
from its measured cells: ~10 event-dim keys per active minute).

| Table | v7 as built | v8 |
|---|---|---|
| `event_rows`, `prompt_rows`, `prompt_steps`, `session_marks`, … | ~120 | the same rows, a few more columns; a late event is one insert and one head-flag update, not a renumbering |
| `drawer_requests` | the new calls' rows, plus every earlier call's row in their prompts (`stepCount`, §3.3): up to 222 rows a call on the capture's longest prompt | the new calls' rows and their prompt's row |
| `session_request_rows` → `request_log_rows` | ~10 | ~10 (both, until step 8) |
| `session_cells` | — | ~25: the minute's and the hour's cells of the keys the run touched |
| `request_cells` | — | ~10 |
| `presence_runs`, `gap_windows`, labels, title rows | — | 0 to a few |
| A commit | ~130, and O(prompt) drawer rows | ~165 |

**What may grow per commit, and what may not.** v8's rule (§3.3): a row
changes only when its own data does. Two costs still grow with something:
- a late prompt row renumbers the session's later prompt rows (a few);
- a late title call reruns the assignments of the calls after it, until one
  stays the same.

Everything else is bounded per record. v7's drawer as built isn't: its fix is in
progress. The write-amplification gate (§Testing) asserts the rule table by
table, in rows and bytes per record, rather than trusting this table.

**What a page reads** (the capture's heaviest, from §5.4; v7 read every row of
the entity in the range):

| Page | v7 rows read | v8 rows read |
|---|---|---|
| a session's header, prompts, requests, events (page 1) | the session's rows: up to 53k events | 1 to ~300: the page, its cells |
| a session's state, whole range | 53k events, 2.5k marks, 3.4k requests | ~3k cells, at most 2 minutes of rows |
| a session's timeline, bin 900 | 53k events, 2.5k marks | ~9k minute cells, ~2k rows |
| a person's summary, 14 d | 71k events and their marks | ~6k cells, at most 2 minutes of rows a session |
| a person's timeline, 24 h at 300 s | 24k events | ~6k minute cells |
| a person's requests, page 1 | the log's inputs for the range | the cells, and 20 rows per credential |

**Storage, measured.** Bytes per row with indexes, from the capture in the v7
gate's Postgres, times the target's rows a day:

| Table | Bytes a row | Rows/day | GB/day |
|---|---|---|---|
| `analytics` | 1,354 | 75M | ~101 |
| `event_rows` | 701 (v8: ~20 more) | 75M | ~53 |
| `drawer_events` | 633 | 75M | ~47 |
| `drawer_requests` | 5,358 | 7.5M | ~40 |
| `requests`, `request_txs`, `request_log_rows` | 1,350, 966, ~530 | 7.5M each | ~21 |
| `session_marks`, `prompt_steps`, `call_tool_uses`, `request_reports`, `agent_calls`, `batch_parts` | 250 to 815 | 1.5M to 4.5M each | ~7 |
| `session_cells`, `request_cells` | ~200 (estimated; none written yet) | ~17M | ~4 |
| **All** | | | **~270** |

That's ~4 TB at 15 days. v7's budget (~110 GB/day) underestimated it: the four
tables of one row per event or per request dominate. The owner decides the cuts
(narrower `analytics`, drawer rows, retention); this spec doesn't.

- **Postgres at the target.** One primary takes ~11k inserted rows/s at peak
  (v7: ~8k) and ~270 GB/day. An r6g.2xlarge with 2 TB of gp3 doesn't hold
  15 days of that; the class and the disk follow the owner's cuts.
  - Retention's deletes match the inserts (~240M rows/day), and so does the
    vacuum behind them. That's where day partitions start to pay (Not now,
    with the numbers).
  - Hour cells are updated in place while their hour is active: one row
    rewritten per key and run, which the vacuum absorbs.
  - The next steps for reads: a read replica for the API (Not now).
- **Connections:** 3 per worker, and one per API request in flight (a pool of 8
  per pod). That's ~140 at the maximum replica counts.
- **Advisory locks:** one per warm key, ~5k at peak, well under
  `max_locks_per_transaction` × `max_connections`.
- **CPU:** the render is O(session) per run. v7 measured ~0.4 s a run on the
  test datasets' 21k-event session, drawer rows included. At ~100 runs/s of
  sessions that size, that's ~40 cores across the workers, not v7's ~5. KEDA
  scales them on lag. v8's cells add a pass over the rendered rows, a few
  percent. This is decision 3's cost, stated as measured.
- **S3:** ~800 ops/s in all at peak. AWS S3's limits are far above that; DO
  Spaces' 800/s per bucket fits the demo.

**Expected latency** in the cluster, warm (the gate measures it):

| Page | Rows read (the heaviest) | Expected |
|---|---|---|
| `/status`, the drawer, a session's header | 1 | ~5 ms |
| a session's events, prompts, requests | a page, and a few hundred cells | ~10 ms |
| a session's state | ~3k cells, at most 2 minutes of rows | ~15 ms |
| a session's timeline | ~11k cells and rows at the finest bin the UI asks of it | ~30 ms |
| a person's header, sessions | the group's rows | ~10 ms |
| a person's summary (14 d) and timeline (24 h) | ~6k cells each | ~20 ms |
| a person's requests | the cells, 20 rows per credential | ~15 ms |
| `/people`, `/overview` at 1,500 people, 24 h and 14 d | ~6k and ~90k sessions, with their dimension rows and day counts | ~0.1 s and ~1 s (v7, unchanged) |

**The cold-cache limit,** recorded honestly.
- v7's person summary and timeline folded raw event rows at read time, so at
  1,500 people a heavy person's 7 days were ~16k random page reads from disk.
- v8 reads hour cells for the whole hours (O(hours), written together per key)
  and at most two minutes of rows per session at each edge. A heavy person's
  7 days are then a few hundred pages.
- The directory keeps v7's folds over session rows and day counts.

The final gate measures cold p95 on the capture, with Postgres restarted and the
OS cache dropped once. The capture is 0.9 GB and fits in any cache, so that
can't prove cold ≤ 300 ms at the target. A target-shaped database will come
from the load-test harness, planned separately, and the cold gate runs there
too.

## Testing: small tests, written first

Each step writes its failing tests first. **The oracle is today's code, frozen**
(`api/tests/oracle`, #42), and stays the reference through the final gate.

- **Go layers, on a Postgres fake.** The writer's transaction interface (the
  guarded key update, upserts, deletes, the flag) has an in-memory
  implementation with Postgres's semantics: one transaction, and the guard
  re-checked after a concurrent commit. The layers run on it:
  - *As built (step 2):* the worker's layers (`internal/worker`) run on a real
    Postgres, a fresh database per test (`pgtest.Fresh`), which is stronger
    than a fake and as fast: the whole package takes ~15 s (~45 s under
    `-race`). Their check writes the one pass with the commit's own writer
    into a `ref` schema and compares every stored and rendered table with it
    in SQL (`EXCEPT ALL` both ways). `render/rendertest.Fake` covers the
    render's layers.
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
- **The drawer, byte for byte** (`test_v7_drawer.py`, §3.7): on each dataset
  (the mixed builders and demo, a dataset of every drawer shape, the spanning
  batch in flight), every request id (the logs' and every one the worker
  rendered: WebSocket turns, `reported:` ids) and every event id: their
  `/requests/{id}`, `/details`, `/raw` and `/events/{id}` responses are the
  oracle's bytes. `make check` holds a request id of each kind and every
  tenth, two events a batch and every tenth; `-m full` holds every one. An
  ended batch spanning sessions is compared at the table level
  (`test_v6_session_rows.py`) and the response level. The worker's own tests
  hold the cross-key flags (two commits of one batch at once), the detail
  object read once and kept, and the wait for a late one.
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

### v8's tests

v7's tests stay, except that for the pages v8 serves, the table-level
comparisons of what the API loads are replaced by the ones below. v8 adds
these, each written before the code it holds. The oracle stays today's code,
frozen: V1Store's reader (§5.7).

- **The worker's layers** (v7's harness: a fresh database per test, one pass
  written into a `ref` schema, compared with `EXCEPT ALL` both ways) cover every
  new table and column: cells, `request_log_rows` and `request_cells`, labels,
  title assignments, head flags, first-occurrence marks, presence runs and gap
  windows.
  - Every order of up to 5 records, one run per record; and generated
    sequences, shuffled, duplicated, late across hours, reloaded cold halfway.
    After each run the tables equal one pass, and the run wrote only the rows
    and cells that changed (counted).
  - **Named cases:**
    - a late event inside a tick run: only its own and the next row's head
      flags change, and no other event row is written;
    - a batch whose events span two sessions, its two keys committing in both
      orders, and while one of its calls is in flight in a `c:` key;
    - a token refreshed mid-prompt: the step arrays;
    - a late title call before one already assigned: the change runs forward
      and stops;
    - a thread that becomes a candidate (its first model call) and one that
      stops being one (its own title);
    - a child's edge;
    - a commit's flags never name more than the rows around one instant.
- **Render properties:** `prompt_rows`' `seq` is dense and in time order;
  `event_rows`' key order is the render's event order; every row's head flags
  equal the oracle's `lag` over its view.
- **Write amplification, rows and bytes.** Counting rows whose hash changed
  can't catch a value that changes on every run, so the gate counts what's
  written. The capture, and each CI dataset, is replayed in 5 s and in 30 s
  windows, with its files spread over 2 to 4 simulated pods. For every table,
  the rows and the bytes written per record applied must stay under a fixed
  bound, and must not grow with the session's or the prompt's length:
  - the bounds are set from the budget (§Scaling budget) and checked in each
    of steps 2 to 5 for that step's tables;
  - v7's drawer as built fails it (§3.3: 222 rows a call); the drawer fix must
    pass it.
- **Cells, row for row.** On every dataset:
  - every minute and hour cell equals the oracle's merge of its rows;
  - the API's edge SQL over every whole minute equals that minute's cell. This
    holds the Go cells and the SQL edges to one definition.
- **Invariants I1 to I3** (§5.3) are asserted on every dataset, and at the gate.
  The periodic check's SQL is tested on a dataset with one violation of each:
  its gauge reports it.
- **The read primitives against the oracle's tables.** Positions and units equal
  the oracle's window SQL over its rows; first-occurrence counts equal
  `count(DISTINCT)` for every generated range; the request log with each
  person's scope applied equals `stream_sql`'s rows for every person and range.
- **Response level, side by side.** Old is V1Store's reader (today's SQL), new
  is PgStore's, through the same router. For every endpoint in §5.2:
  - every URL of `endpoints.py`, which gains what the UI really sends: `kind` on
    a session's requests; the "other" chip's kinds with `skills`;
    `collapseTicks` always; `q` with a range;
  - generated ranges whose ends fall:
    - on a minute or hour boundary, and ±1 ns and ±1 µs around one;
    - inside a minute; both inside one minute;
    - inside a tick run; between two events of one batch (in one session, and
      across two); between two calls of one prompt; inside a presence segment;
      inside a not-captured window;
    - before and after all data; open at either end;
  - every ladder bin with each of those ranges (partial first and last bins),
    and `bin` 90 (422);
  - a session's tab and `/state` for the sessions with rows outside their own
    credentials (`in_tab`);
  - `kind`-filtered logs, which read the kind indexes;
  - pages 1, 2 and past the end, and the prompt jump to the first, a middle and
    a missing prompt.

  `make check` runs the small datasets with 3 URLs per group plus every cut
  for one session and one person; `-m full` runs everything.
- **Settled, then compared.** Responses are byte-identical once the pipeline
  has settled: cross-key values land when their render flags run. Every
  comparison first drains `dirty_keys`, render flags included.
- **Replay checkpoints, for running sessions.** Each dataset is replayed in
  pieces, cut at fixed points and mid-batch, mid-prompt and mid-tick-run. At
  each checkpoint, once settled, every in-scope URL is compared with the oracle
  over the same prefix:
  - with `now` at the checkpoint;
  - with `now` at each threshold, ±1 ns: live (5 min), streaming (10 min), in
    flight;
  - with `now` a second before the newest row (a client clock ahead).
- **Reads:** `test_flat_reads.py` per endpoint:
  - the statements in §5.4, on one connection;
  - every statement's plan (EXPLAIN) uses an index on the big tables;
  - the rows read don't change with 8× more history outside the range, nor
    with more rows in the session outside the page (the Events tab: its page
    and 4 lookups).
- **No DuckDB:** the session and person pages run with DuckDB unimportable (a
  test).
- **CI time.** The new layer tests run on the per-test databases v7 uses; the
  side-by-side cuts add under a minute across the parallel jobs.
- **The real capture,** once, at the gate (step 9).

## Build order

v7's steps 1 to 10 are done: v7 is built and deployed. v8's steps follow. Each
is one PR, tests first: its failing tests land in the PR before the code.

**What runs in parallel.** Steps 1 to 5 can be built at once: each owns its
tables, and the shared edits (`schema.sql`, `render.Version`) merge
mechanically. Steps 6 and 7 are one PR per page, each starting when its inputs
have landed.

| Step | What | Tests first | Gate | Needs |
|---|---|---|---|---|
| 1 | **The reader.** psycopg 3 (decision 5): the pool, one snapshot, the statements, the trace. The routers' page-reader seam (§5.7), with V1Store's reader today's SQL and PgStore's still v7's loader behind it. `/status`, the drawer and `/events/{id}` on the new reader. The harness: generated cuts, bins with cuts, replay checkpoints with `now` at each threshold | every v7 response unchanged through the seam; the harness's own tests (each cut lands where it claims) | `make check`; the harness green on v7, which is byte-identical today | — |
| 2 | **Session cells:** `session_cells` rendered and diffed; the API's edge SQL for every dim | every minute and hour cell equals the oracle's merge; the edge SQL equals every minute's cell; the write-amplification bounds for cells | `make check` | — |
| 3 | **Session-table columns:** `event_rows`' stable key and head flags (`seq` kept, out of the key, for the v7 loader); the partial indexes; batch and prompt first-occurrence marks and gaps; `batch_parts`' spans and times; the `unattributed` flag; `presence_runs`, `gap_windows`, network bypass | each equals the oracle's window SQL over its rows; the render properties; the named cases (a late tick, a spanning batch both ways, a client clock ahead); the write-amplification bounds | `make check` | — |
| 4 | **Request-log rows:** `request_log_rows` and `request_cells` with `in_tab`, the kind indexes, the `c:` keys' rows, batch sessions; the periodic I1 to I3 check and its gauge. `session_request_rows` stays, for the v7 loader, until step 8 | for every person and range of every dataset, the log with the scope applied equals `stream_sql`'s; every session's tab equals today's (the `in_tab` rows included); a token refreshed mid-prompt; a batch call in flight getting its session; I1 to I3 and the gauge; the write-amplification bounds | `make check` | — |
| 5 | **Labels and children:** `title_candidates`, `title_assignments`, alone labels, children; the flags of §2, and retention's (§3.6) | `session_rows`' labels and children equal `labels.sql`'s over every dataset's components; the named label cases; flags bounded; forgetting a key flags its readers | `make check` | — |
| 6 | **Session pages on v8's reads,** one PR each: `/sessions/{id}` (5), `/prompts` (1), `/events` (3), `/requests` (4), `/state` (2, 3, 4), `/timeline` (2, 3, 5) | the page's side-by-side URLs and cuts, its checkpoints, its statements test | byte-identical on the CI datasets (`make check`: the subset; `-m full`: all) | 1, and the steps named |
| 7 | **Person pages on v8's reads,** one PR each: `/people/{id}` (1), `/sessions` (5), `/requests` (4), `/summary` (2, 3, 4), `/timeline` (2, 3, 5), `/coverage` (2, 4) | as step 6 | as step 6 | 1, and the steps named |
| 8 | **Delete** the v7 loader for these pages (§5.7), then what only it read: `session_request_rows` (v7 `pgstore.py:137` and `:754`) and `event_rows.seq`. If decision 4 says so, the directory's SQL in Postgres, and DuckDB and ADBC out of the API image. `SETUP.md`, `PRODUCTION.md` | the no-DuckDB test; the image test, if chosen | `make check`; `make up && make verify` | 6, 7 |
| 9 | **The gate,** on the real capture, settled | — | every endpoint byte-identical (v7's 4,248 URLs, plus the new cuts and checkpoints); the write-amplification gate (rows and bytes per record per table, 5 s and 30 s windows, 2 to 4 pods); warm p95 ≤ 50 ms and cold p95 ≤ 300 ms for every in-scope page of the heaviest person and session; rows read per page measured against the budget. The capture is 0.9 GB, so its cold numbers can't prove the target's: the cold gate also runs on the target-shaped database the load-test harness builds (planned separately) | 8 |
| 10 | **Deploy** to the DigitalOcean cluster: Postgres starts empty (Rollout), `helm upgrade` (never uninstall) | — | pages load on the cluster's new traffic; `/lag` settles under 30 s; a request through the proxy shows up end to end | 9 |

v7 planned to delete the oracle after its step 10. It's still here, and v8's
tests need it, so it goes after v8's step 10 instead, with the rest of that
list: `v1layout`'s v1 output, `merge.go`, `mergeloop.go`, `Commit` and
`WriteOnePass`, and V1Store with today's endpoint SQL. From then on,
`process.Derived` and the UI samples are the oracles.

## Deleted

**Deleted in v7:**

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
  - the drawer's read-time loading (`pgstore.py`'s call keys, their facts and
    the batch's analytics): the worker renders the drawer (§3.7).
    `request_detail.py` stays while V1Store (`AINTERCEPT_STORE=v1`) and
    the oracle derive the drawer at request time, and for `/raw` and
    `/events/{id}`'s raw rows;
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

**Deleted in v8:**
- **Postgres,** in step 8, once the v7 loader that reads them is gone:
  `session_request_rows` (replaced by `request_log_rows`, §3.9) and
  `event_rows.seq` (replaced by the stable key, §3.3).
- **Go:** the render of `session_request_rows` and of `event_rows.seq` (step 8).
- **API** (§5.7):
  - the v7 loader for the session and person pages: `pgstore.py`'s session
    loads, `_OTHER_KEYS` and `_request_rows`; `pgstore_person.py`'s loads;
  - `sql/labels.sql`, now the worker's;
  - the runtime use of `stream_sql`, `state_sql`, `person_sql`, `rollups.py`'s
    folds, and `session.py`'s and `timeline.py`'s endpoint SQL. They stay for
    V1Store and the oracle until after v8's step 10;
  - per decisions 4 and 5: `adbc-driver-postgresql` and `duckdb` from the
    API's image, once the directory is ported.

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

**v8, against the owner's brief** (2026-11-14: precompute the session and
person pages):

12. **"The timeline as finished rows."** A session's timeline merges its own
    cells per bin, as a person's does, rather than storing finished bins for
    each ladder width. That's one mechanism for both timelines, any bin that's
    a multiple of 60, and no 8× write per row. It reads ~11k narrow rows at the
    finest bin the UI asks of the capture's heaviest session (§5.4).
13. **"State as a finished object."** `/state` takes any range (the brush), so
    it's a merge of cells and four lookups. The default range is the same
    query.
14. **"No computing anything on the fly."** §5.5 lists everything still
    computed per request, with its bound. Most are merges over finished rows.
    Four folds depend on order (decision 2): `q`, band packing, and presence and not-captured windows
    across a person's sessions. `q` scans the range's rows for its matches;
    the other three read only the page's bands, segments or missed calls.
15. **"Re-rendering a whole session per commit is not OK."** Writes are
    O(changed): every v8 table is diffed. The render's CPU is still O(session)
    per run, as v7 built it (decision 3).
16. **"Per-session hourly rollups, summed."** Yes, and minute cells and edge
    rows too: the UI's ranges are arbitrary instants (a preset is `now − span`
    at ms, a brush is any ms), so whole hours alone can't be exact.
17. **Who a person is stays a read-time resolution,** as in v7: a few index
    lookups per statement. Rendering it would rewrite every session of a group
    whenever an account links.

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
- **The drawer is precomputed** (§3.7):
  - its rows are the ids the request logs list: a WebSocket turn as
    `<connection>~<turn>` (its `#` form isn't served), and a `reported:` id
    its session's `request_missed` rows name;
  - an event id is looked up by its rank (`b:007` is `b:7`'s, as
    `request_detail.event` reads it);
  - a call whose ending another key held a moment ago (an expired row its
    ending's copy replaces) reads the first key's row until the copy lands;
  - the summary and details keep what the call's detail object held when the
    worker read it: an object the local raw cap deletes later leaves `/raw`
    unavailable, as today, but not the summary;
  - the render's cost per commit is O(session): on the test datasets' largest
    session (21k events, 1.7k calls) ~0.3 s for its drawer rows, ~0.1 s for
    its 9 tables (Not now: an incremental render).
- **A Codex thread's unthreaded calls** are read from the other keys' rows at
  request time, never copied: in v8 by a filter on `request_log_rows` (§5.4).
  An in-flight batch call gets its session at render time (v8, §3.9).
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

**v8's decisions and quirks:**
- **`bin` must be a multiple of 60** (decision 1). Any other width is a 422.
  Today any integer ≥ 60 is accepted; the UI sends only the ladder.
- **Float sums are merged per cell** (decision 6): `cost_usd` and `hook_ms` are
  summed in each cell, then across cells. Today's single sum runs in an order
  DuckDB picks. The last bits can differ; they're shown rounded (cents, 0.1 ms),
  and the tests compare the responses byte for byte. A sum within ~1e-12 of a
  rounding boundary could show one cent differently.
- **One request-log row serves both logs** (§3.9). Both logs must stay
  byte-identical to today's on every dataset and the capture (steps 4, 9). By
  construction they'd differ from today's person log only if I1 or I2 broke
  (§5.3): the row would show its own session's value.
- **A request id held by two keys at once** (a call whose ending another key
  held a moment ago, v7's drawer quirk) shows twice in the logs, as today, and
  sorts by key after (`ts_us`, `request_id`).
- **Byte-identical once settled.** A cross-key value (a batch's count, a label,
  `in_tab` after a credential lands) is right once its render flag has run.
  Until then a page can show the value as it was, for about a run.
- **The Events tab's later pages** read the index entries of the unit starts
  before them (`OFFSET`): no index serves page N directly (Not now). Page 1
  reads its own rows.
- **The person summary's batch count** relies on a batch's spanning parts
  carrying their event times. A batch spanning sessions is rare (8 of ~9.4k
  batches on the capture), so the times cost nothing measurable.

**v7's quirks, in v8:**
- quirk 2 (Codex titles over the account group): rendered by the title call's
  key (§3.10), the same assignment in the same order.
- quirk 3 (person and session request numbering differ): reproduced from one
  row. Each log applies its own scope to the row's sourced values (§5.3): the
  person's sessions and credentials, or the one session. Each matches today's
  (step 4's test).
- quirk 4 (no final tiebreaker in the person requests sort): request ids are
  unique per row except for the case above, so the order is (`ts_us`,
  `request_id`, key).
- quirk 5 (session pages derived the whole account group): nothing is read at
  request time for labels at all.
- Codex step numbers (#44): a step counts its prompt's calls in its own key
  (I3), for both logs. Today's person log counts calls that share a credential
  and a turn id across threads; real turn ids are UUIDs, and no dataset row
  differs.

## Not now: add when real traffic needs it

- **Day partitioning** of the big tables (`event_rows`, `analytics`,
  `request_txs`, `requests`, `request_log_rows`, `session_marks`,
  `session_cells`). Add it when retention's DELETE and vacuum cost too much, or
  when a table's indexes outgrow RAM.
  - At 1,500 people that's ~240M rows and ~116 GB a day deleted again after 15
    days: about 3k deletes/s around the clock, plus the vacuum behind them.
  - At today's scale it's well under 1M rows a day.
- **An incremental render** (O(changed) CPU), if workers' CPU shows up
  (decision 3).
- **A 15-minute or day cell level,** if the timelines' minute-cell reads show
  up: the capture's heaviest session reads ~9k minute cells at bin 900.
- **An index over the Events tab's unit starts,** if deep pages show up.
  Cursor paging would be a contract change.
- **A trigram index for `q`,** if searching long ranges shows up.
- **The directory precomputed** (`/people`, `/overview`): its own design, after
  v8.
- **A read replica for the API, or RDS Proxy.**
- **An API-side cache** of past days' rows.
- **Hour order keys** (v6) for the time-ordered tables, if late rows show up in
  writes/s.
- **A narrower `analytics`,** at about half the database at 1,500 people: read
  event metadata from the batch's S3 detail object, or keep it only for the
  names the drawer reads, and serve `/events/{id}` from `event_rows`.
- **A periodic full listing of `v1/keys/`,** to flag keys a lost node never
  indexed.
- **The in-flight drawer** (a `pending` table), if the owner wants it back.
- **Schema migrations.** Until then, each schema change is a fresh start.
- **A size cap,** not just an age, for local raw data.
- **Hot keys:** a shared API key used by thousands of people, or many people
  behind one NAT IP.
- **Changing response contracts.**
