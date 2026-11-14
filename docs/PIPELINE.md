# Data pipeline (v8)

Status: **proposed; the owner's decisions are in.** v7 is built and deployed
(2026-11-14). v8 precomputes every page the API serves, makes the worker's
render incremental, and moves what Postgres doesn't query out of it. The
decisions are listed below, and each is applied where it's used.

**Why.** On the real capture, v7's heaviest pages take 300 to 450 ms warm
(p95): a session's state, timeline and events, and a person's summary, timeline
and requests. Almost all of it is per request work:
- the rows are copied from Postgres into an in-memory DuckDB and rebuilt into
  today's tables;
- today's endpoint code derives the page from them;
- DuckDB's setup alone costs ~18 ms.

The worker renders every session whole on every run, and v7 stores ~270 GB a
day at the target (measured bytes per row), most of it rows nothing queries.

**What v8 changes:**
- **The API reads finished rows** (§5). For every page it filters rows by the
  request's range (snapped to whole minutes) and filters, pages them, and
  merges precomputed cells, all in Postgres. It applies `now`. DuckDB and ADBC
  leave the API.
- **The worker's render is incremental** (§3.11). A run re-renders only the
  rows its records affect: its CPU and writes follow the new records, never the
  session's size. Rows carry sort keys, not ordinals. A value shared by many
  rows (a prompt's number, a call's label, a step count) lives once, on its
  prompt's, call's or session's row, and is joined at read.
- **The worker renders what the pages show** (§3.3, §3.8 to §3.10): cells (sums
  per minute and per hour); one request-log row per call, which both request
  logs read; finished labels and children; first-occurrence marks.
- **Postgres holds what's queried** (§3.12): the rows the pages read, and
  finished display rows read by key. Raw payloads stay in S3. ~270 → ~81
  GB/day at the target.
- **Other keys' values are read at render time,** and the commits that change
  them flag the reader dirty (the cross-key rule, §2).

Unchanged from v7, as built:
- ingest and discovery (§1, the tail);
- claims, the guarded commit and quarantine (§2);
- the state engine and its S3 checkpoint, expiry, retention;
- `/status` (§5.6);
- deployment (§6).

v7's text is kept where it's still true. `docs/STREAMING_PIPELINE.md` is kept
only as history.

## Owner decisions (decided)

1. **Ranges snap to whole minutes** (§5.3): `from` to its minute's start, `to`
   to its minute's end. Cells are then exact with no edge rows. Byte-identity is
   proven by giving the oracle the same snapped range. `bin` must be a multiple
   of 60.
2. **A Codex thread's label is its own title calls'** (`label_text_alone`),
   everywhere (§3.10). The assignment chain across threads, its table and its
   flags are gone.
3. **Spanning batches stay exact** (§5.3): the read corrects for each spanning
   batch from its parts that overlap the range, with no gap bound.
4. **The render is incremental: delta in at every granularity** (§3.11). Per
   commit, CPU and writes are proportional to the new records and the rows they
   affect, never the session's size. A key stays warm until 60 minutes idle or
   its session expires, under a byte cap per worker, and its checkpoint is a
   snapshot, so a cold load decodes rather than replays (§2). A CPU gate holds
   it, cold loads included.
5. **Postgres holds what's queried** (§3.12): every stored input no v8 read or
   cross-key render needs goes; raw payloads stay in S3. The target is under
   110 GB/day.
6. **Cold latency at the target:** not now (Not now). The gate measures cold
   latency on the capture only.
7. **The directory is in scope** (§5.6): `/people` and `/overview` read
   finished rows too, so DuckDB and ADBC leave the API.
8. **The bounded per-request computations in §5.5 are accepted:** filters,
   sorts and sums over finished rows, the index lookups there (labels,
   children, links), and the folds.

**Open, for the owner:** the 14-day directory at 1,500 people: ~1 s warm over
~90k sessions (§5.6). Accept it, or add per-(session, day) cells summed per
person.

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
    (snapped to whole minutes) and filters, orders and pages them, and merges
    cells. It applies `now` and resolves who a person is. Everything computed
    per request is listed with its bound (§5.5), and accepted (decision 8).
  - One fetch per thing a page needs: a fixed number of statements per page,
    never one per row.
- **Delta in, correct eventual state out, at every granularity** (decision 4).
  A commit's CPU and writes are proportional to its new records and the rows
  they affect: never the session's, the person's or history's size. The render
  is driven by the state's per-record deltas (§3.11), and a CPU gate and a
  write gate hold it (Testing).
- **One transaction per commit.** A key's rows and its next commit number land
  together or not at all. The number orders a key's commits, so a stalled or
  duplicate worker can't write. This replaces the create-only commit objects,
  the ETag fencing and the Redis locks.
- **Response bodies don't change.** Once the pipeline has settled (no flags
  left), every endpoint returns byte-identical JSON on the CI datasets and the
  real capture, for the same (snapped) range as today's code is given, and the
  URLs stay the same. What a client can see change is decided and listed in
  "Decisions and quirks": ranges snap to minutes, a `bin` must be a multiple of
  60, a Codex thread's label is its own title calls' everywhere, and the
  in-flight drawer (v7, the owner's call).
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
                                             (+ calls/<id>.ndjson.gz from S3 for /raw and /events/{id})
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
  - *v8:* each chunk is the journal's gob compressed with zstd (a chunk of plain
    gob, from before, still loads). The records repeat their event metadata
    and headers, so on the capture's heaviest session (57,874 records, 3,430
    wires) the chunk is 6.2 MB instead of 114 MB: the records were 99 MB of
    it, the wires 15. Compaction keeps each external input's last value once
    (`state.Merge`), so a value that changes every run (other keys' batch
    parts) doesn't pile up.
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
   input for 60 minutes (v7 had 60 s), under a byte cap per worker (§2's v8
   block, as built), then it's unlocked. All keys are unlocked on
   SIGTERM (v8: after writing their snapshots).
5. **If the lock connection drops, the worker forgets every key it holds,**
   along with their warm states. Postgres has freed the locks, so another worker
   may already hold them.
6. **The lock is for affinity only.** It saves duplicate work and cold loads (a
   cold load replays the key's journal; v8: decodes its snapshot). Correctness
   never depends on it: the guarded commit (step 8) does.

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
     them. *v8:* the newest snapshot, decoded, and the delta chunks after it.
   - *v7 only:* for a session key, a cold load also renders the state and
     hashes the rows: the baseline. v8 has no baseline (§2's v8 block).
5. **GET the files and apply them** with the state engine (#19), sorted by time
   and record id. Expire due calls (below). Collect the run's state deltas,
   keeping each (table, id)'s last change.
6. **Render**, for a session key: `state.RenderSession` (#44), every table
   whole, and for every key its drawer rows (§3.7). *v8:* only the rows the
   run's deltas affect (§2's v8 block, §3.11). First the run refreshes
   the render's external inputs: the detail objects of calls that ended, and
   the other keys' `batch_parts` of its batches (one statement on a run
   connection).
   - *v7 only:* hash each row (xxhash64 of its encoding) and compare with the
     baseline. Changed and new rows become upserts, rows that are gone become
     deletes. The render's hashes become the next baseline, in memory only.
     *v8:* the rows the deltas affect are upserted or deleted, unhashed.
   - If `keys.render_version` differs from this worker's, delete the session's
     rendered rows and write them all.
   - Every key also renders its directory folds and agent calls (§3.5), and its
     batch parts (§3.4).
7. **Write the state chunk** `state/<n+1>-<nonce>` and compact, as v5 (the
   `out/` half is gone). *v8:* a delta chunk, or a snapshot when the deltas
   since the last one hold as many records as it does (§2's v8 block).
8. **Commit in one transaction,** holding a run connection only now:
   1. **The guard first:** `UPDATE keys SET n = n + 1, state = …, last_applied =
      …, grace = …, garbage = …, expires_ns = …, newest_row_ns = …, day = …,
      first_request_ns = …, earliest_ns = …, content_retained = …,
      render_version = …, updated_ns = … WHERE key = $1 AND n = $2`. A new key's
      first commit instead runs `INSERT … n = 1 ON CONFLICT DO NOTHING`.
      - If no row matches, another worker committed. Roll back before writing
        anything, drop the warm state (v7: and its baseline), and move on.
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

### v8: the incremental run, and the cross-key rule

**The run is driven by deltas** (§3.11). v7's Run 6 renders every table whole,
hashes every row and diffs against a baseline. v8 replaces that:
- `Apply` already returns each record's delta: the state rows it changed, by
  (table, id).
- Each display table has a dependency rule (§3.11): which of its rows a changed
  state row or input can affect. The run renders exactly those rows from the
  state, and upserts them, or deletes those that are gone.
- There's no baseline and no hashing of every row. `Tables.Baseline`, which
  rendered a whole key after a cold load, goes. A key's very first run, and a
  run after a `render_version` change, render every row once.
- **Running values come from deltas too.** v7 recomputes these every run:
  - `FactsOf`: the key row's `day`, first request, earliest and newest row
    times;
  - `refreshParts`: the other keys' parts of its batches;
  - `refreshWires`: the detail objects of calls that ended.

  v8 keeps each as a running value, updated by the run's deltas.
- **`dirty_inputs`** (key, kind, id) names exactly what changed for a reader.
  It's written with the flag, in the same statement. The reader re-reads just
  those inputs (one batch's parts, one call's wire), never all of its batches.
- **The drawer's rows follow the same rule** (§3.7, the worker's incremental
  drawer).

**What a warm key keeps, and how it loads cold.**
- **The warm state** is everything the incremental render keeps between runs:
  - the state;
  - its dependency indexes (the rows by minute, by request id, by prompt);
  - its memoized minute cells (§3.8);
  - the drawer's recorded lookups and memo (§3.7).
- **Stay warm.** A key stays warm until it's been idle 60 minutes, or its
  session has expired (§2's expiry), whichever comes first. v7 unloaded after
  60 s. The worker agent lands this, with the byte cap and the replay gate, on
  v7 first (Build order); v8's snapshots build on it.
  - Each worker caps its warm bytes, evicting the least recently used key.
  - *As built on v7:* 60 minutes idle, and a cap of 512 MiB per worker
    (`ROLLUP_WARM_BYTES`, the chart's `rollup.warmBytes`), which fits the
    rollup pod's 1 GiB limit.
    - A warm state's bytes are estimated as its records times 7,000.
    - v7 has no session expiry earlier than that: a call in flight expires
      6 h after its start. So the 60 minutes decide.
  - **Warm bytes per key, measured:** ~7 KB of heap per record held, for
    the state and its render baseline together. The capture's heaviest
    session is 402 MB warm (57,874 records); the next is 199 MB.
  - **The replay gate isn't met on v7.** Records replayed by cold loads, ×
    the 1.12M applied, at the capture's real gaps. This is simulated per key,
    a run per in/ file at its S3 time, with one worker holding every key:

    | Idle | No cap | 4 GiB | 2 GiB | 512 MiB |
    |---|---|---|---|---|
    | 60 s (v7) | 12.2× | — | 12.6× | 15.9× |
    | 60 min | 1.15× | 1.23× | 1.80× | **10.2×** (as built) |
    | 3 h | 0.74× | 0.86× | 1.55× | — |

    At 512 MiB the heaviest keys crowd each other out. The window can't fix
    that; the bytes per record are the problem. v8 step 2 has to answer two
    questions: what the incremental render needs in memory per record held,
    and what a snapshot decode costs per record held.
- **Snapshot checkpoints.** The checkpoint stops being a journal of records.
  It's a snapshot of the warm state, so a cold load decodes it and never
  replays or renders. The records since the last snapshot are kept as delta
  chunks, as today's chunks are.
  - A key writes a snapshot when it goes cold (idle, evicted, or on SIGTERM).
  - It also writes one when its delta chunks hold as many records as the
    snapshot does, so a load replays at most that tail.
  - The CPU gate's bound: replayed ≤ 1× applied.
  - This builds on #57's smaller checkpoints (merged): zstd chunks, and
    compaction that keeps each external input's last value once.
- **Measured, why:** one cold load of the capture's heaviest session replays
  its journal in 2,581 ms (~45 µs a record). With a 5-minute warm window, the
  capture's real gaps cause 1,506 loads. Those replay 6.47M records against
  202k applied (32×): ~290 s of CPU, against ~69 s for the incremental render
  itself.
- **A render flag on a key with no state** (a parent or a batch's key that
  hasn't committed yet) is consumed without writing a key row. A key row is
  only ever written with a real `day`, never `''`.

**What a run renders:**
- the session tables (§3.3);
- cells (§3.8);
- calls' rows and their cells (§3.9);
- the drawer's rows (§3.7);
- the directory's cells (§3.5).

**The cross-key rule.** In v8 nothing heals a stale value later: there's no
whole render. So a render that reads another key's rows must never miss a
change to them.
1. **The writer flags the reader.** The writer's commit flags (`render`) every
   key whose render reads the rows it changed, and writes `dirty_inputs` for
   them.
2. **A lock orders a writer and a new reader.** The writer can only flag the
   readers it can see. A reader can appear by committing a row the writer
   looks up. So both commits take `pg_advisory_xact_lock` on the shared entity.
   Under the lock, the reader re-reads what its render read (READ COMMITTED: it
   sees what committed before it got the lock), and flags itself if it changed.
   Whichever commits second sees the first.
3. **No deadlocks: one order.** A commit takes all its advisory locks right
   after its guard, in one global order sorted by (namespace, id). Then it
   writes every flag, its own and the others', in one statement sorted by key.
   Retention takes locks and writes flags in the same order.
4. **Retention is a writer too.** When it forgets rows another key reads, it
   flags the readers under the same locks (§3.6).

There is now one kind of cross-key render read:

| A render reads | Written by | The writer flags | Lock |
|---|---|---|---|
| the other keys' parts of its batches: a batch's count, least name and session (v7), and, for a spanning batch, their event times | each key holding the batch's events | the batch's other keys, and the key holding its call | the batch |

- **The holder's side** (a key holding a batch call reads the parts to give the
  call its session and detail). A commit that inserts or changes a batch call's
  row takes that batch's lock, and re-checks the parts its render read.
- **Everything else another key writes is read at read time:**
  - a session's children (`session_edges` by parent) and its label (the title
    calls of its accounts in its first 5 minutes): index lookups, §3.10;
  - a session tab's credential filter, a join (§3.9);
  - a Codex thread's unthreaded calls, a filter (§5.4);
  - who a person is (§4).

  So none of them has a flag.

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
seconds. *v8 drops `seq` from every key (§3.3), so nothing renumbers.*

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
| `session_request_rows` | — | — | **Gone in v8 (step 11):** replaced by `request_log_rows` (§3.9), which both request logs read |

- **Extra indexes:**
  - `session_rows (key_day)`;
  - partial `session_marks (session_id) WHERE kind = 'missed'`;
  - partial `event_rows (session_id, name, ts_ns) WHERE name IN
    ('tengu_tool_search_mode_decision', 'tengu_skills_sync_success')`, for the
    summary's `_LATEST` carry.
- **Every other read is by the session prefix of the primary key,** filtered by
  time in Postgres. v8 puts the time in `event_rows`' and `prompt_rows`' keys
  (below).

**v8: one canonical row per fact, keys not ordinals** (decisions 4 and 5).
- **Each fact is stored once, in its canonical row:**
  - an analytics event's in `event_rows`;
  - a call's (or a not-captured call's) in `request_log_rows` (§3.9);
  - a prompt's in its `prompt_rows` item;
  - a session's in `session_rows`.

  Everything else references those rows by id, and the read joins them in the
  same statement. A value many rows share (a prompt's number, a call's label, a
  step count) is a fact of its prompt or call, so it's stored there and joined,
  never copied onto the rows that show it.
- **Keys, not ordinals.** v7's `seq` columns are dense ranks: one late row
  renumbers every row after it. v8 keys every row by the state's own stable id
  or sort key, and ranks at read.
- §3.11 has each table's dependency rule, and §3.12 lists every table.

The session tables in v8:
- **`event_rows`, an analytics event's one row** (the Events tab and
  `/events/{id}`):
  - primary key (batch, rank): the event's own id (`batch:rank`), so
    `/events/{id}` is a primary-key read;
  - index (session_id, ts_ns, batch, rank) for the session pages. That's the
    render's order: `seq` sorts by time, batch, rank, then further columns, but
    (batch, rank) is unique, so those never decide. On the capture it equals
    `seq` on every row;
  - `seq` isn't stored. Until step 11 the v7 loader numbers the events with
    `row_number()` over this order;
  - an event with no session has a row too, with `session_id` NULL, which no
    page lists, and `owner_key`, the key that wrote it. `owner_key` is set only
    then, with a partial index (owner_key) WHERE session_id IS NULL, so
    retention deletes those rows by key. Every other row is deleted by its
    session. `drawer_events` goes;
  - the columns a page reads, and no others (§3.12): session_id, ts_ns, name,
    internal, summary, marker, `link_kind` and `link_ref` (the raw request id,
    tool use id, device or session it links to), `prompt_id`, `event_key`
    (where its raw row is in the batch's S3 object), `head_all`, `head_pub`,
    `batch_first`, `batch_prev_ns`, `recap`;
  - `name_lower`, `summary_lower`: only where Python's `lower()` (today's)
    differs from Postgres's (the dotted İ, a final sigma), else NULL. `q` reads
    `coalesce(summary_lower, lower(summary))`. On the capture every one is NULL;
  - `head_all`, `head_pub`: the row starts a unit in the Events tab with ticks
    folded, over all rows and over the non-internal ones. A unit is a row that
    isn't a tick, or a run of consecutive tick rows
    (`tengu_tool_use_progress`) in that sequence. A late event changes its own
    flags and the next row's;
  - `batch_first`: it's its batch's first event in the session, in key order;
    otherwise `batch_prev_ns` is the time of the batch's event before it in the
    session;
  - the straddlers' index (§5.3): a GiST index on `int8range(batch_prev_ns,
    ts_ns)` WHERE `batch_prev_ns` < `ts_ns` − `ts_ns` mod 60 s. It covers only
    the pairs that cross a minute boundary, the only ones a snapped range's
    start can fall inside;
  - a partial index (session_id, ts_ns) WHERE recap;
  - joined at read, not stored: its prompt's number (from the prompt's item),
    and a request link's target and label (from the call's row: the newest
    visible call of that request id in the session, its class, model family
    and stop reason). The count columns (head, hooks, tools, git, PRs, cost,
    cache, counts) go: the cells hold them, rendered from the state.
- **`prompt_rows`, a prompt's one row** (its `p:<prompt id>` item), and the
  between items:
  - primary key (session_id, item_id), the state's id (`p:`, `b:`, `c:`, `r:`),
    plus its sort columns, and a unique index (session_id, prompt_id) on the
    prompt items. No `seq`: the Prompts tab pages by the sort key, counts by
    cells (`prompt_row`), and numbers its rows at read;
  - the prompt item holds the prompt's facts that other rows show: its three
    Claude Code numbers (today's code has three: the latest
    `tengu_input_prompt` by (time, key), for events and requests; the newest by
    event order, for marks; the first input, for prompt rows), its Codex first
    call time, and `drawer_step_count`: the drawer's own `stepCount`, the
    number of the prompt's calls the drawer numbers "Step N of M" (§3.7). It
    isn't `steps`, the Prompts tab's count: they differ on 1 of 390 prompts on
    the capture. Events, marks, calls and drawer rows reference the item by
    (session_id, prompt_id). A late input prompt rewrites this one row;
  - three values that depend on the whole session are stored raw and finished
    at read: `typed`, `duration_report_ms` and `turn_report_json` from the
    session's `prompt_product` (its first step's product, on `session_rows`);
    `open_tail` (the last prompt by start); and a Codex prompt's number, its
    rank by start among the session's prompt items.
- **`prompt_steps` folds into the call's row.** A step is a model call. Its
  facts (tokens, model, timings, tools, commands, exit codes, patches, text) go
  on the call's `request_log_rows` row (§3.9), and the Prompts tab reads a
  page's steps from there by (session_id, prompt_id). A step's number in its
  prompt is `row_number()` at read. Its tools are built by appending as tool
  and tick events arrive (v7 rebuilt them from every tick of the request on each
  tick). A tick updates one tool's entry: one row, of the request's tools' size.
  `events_pending` is stored as `has_success` and `pend_end`, and compared at
  read with the session's `last_prompt_event_ns`.
- **`session_marks`:** primary key (session_id, kind, mark_id), the state's id,
  with its sort columns. The API orders a kind by them, as it ordered by `seq`.
  - A call or friction mark stores its `prompt_id`; its number is joined.
  - A call mark also carries `prompt_first` (the first call of its prompt in
    the session, in key order), or else `prompt_prev_ns` (the time of the
    prompt's call before it). The straddlers' GiST index covers its
    minute-crossing pairs too, as `event_rows`' does (§5.3).
  - Permission and network segments are recut locally when a point arrives,
    as presence is: the state keeps their ordered points.
- **`presence_segments`:** primary key (session_id, grp, from_ns), the state's
  ids. The session timeline folds them at read, as today (§5.5).
- **`session_rows`, a session's one row:** rewritten by most commits (one row).
  - Its arrays become child rows or counts:
    - `devices` → `session_devices` (session_id, device_id);
    - `call_accounts` → `session_call_accounts`, already stored;
    - `prompt_ids` → `prompt_id_count`: a person's distinct prompts are the
      sum, by I3 (§5.3);
    - `cov_window_*` go: a session's coverage windows are folded at read from
      its missed marks (few), as the timeline's not-captured lane already is
      (§5.5).
  - New columns: `prompt_product`, `last_prompt_event_ns`, `has_prompt_event`,
    `unattributed`, and the label's inputs (`own_title`, and whether it's a
    Codex thread with a model call). Labels and children are read, not
    rendered (§3.10).
  - `session_last` goes: it's the session's span's last time, as `last_ns` is
    (equal on every session of the capture), so a person log's lastDataAt is
    the newest `last_ns`.
  - `unattributed` means visible (`dim_session_visible`) with no account
    (`dim_session_account`). Both inputs are written by the session's own key
    (checked on the capture), so the flag is rendered within the key.
  - `key_day` is kept as a running maximum, not by scanning the key.
- **`batch_parts`** (batch_id, key), each key's part of a batch: v7's count,
  least name and first (ns, session), plus:
  - `last_ns`;
  - `spans`, set when another key holds a part of the batch;
  - `ns bigint[]`, its events' times, for a spanning part only.

  Index (key, last_ns) WHERE spans: the spanning parts that overlap a range
  (§5.3). A key whose events are all its own never writes `ns`.

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

- **Facts.**
  - `fact_pending` isn't stored: the drawer 404s for a call in flight (owner).
  - *v8:* `fact_events` (the analytics events, the table `analytics`) isn't
    stored. Nothing reads it from Postgres since the drawer is precomputed
    (§3.7):
    - the drawer and `/events/{id}` read their own rows;
    - a batch's other keys come from `batch_parts`.

    On the capture it was the largest table: 216k rows, 292 MB of 916 MB.
    Every table now has a reader, an API statement or the worker's own
    (`api/tests/test_pg_readers.py`).
  - `fact_probes` folds into `keys.earliest_ns`.
- **`batch_members` isn't stored:** it's a row per event.
  - Instead each key writes its **batch parts**: `batch_parts (batch_id, key, n,
    min_name, first_ns, first_session)` holds the key's events of that batch:
    their count, their least non-empty name, and the earliest (ns, session)
    that has a session.
  - Primary key (batch_id, key); index (key).
  - Read in §4. #43's stamps on the batch call stay, unused.

**v8:** these rows were stored so read-time SQL could run over them. v8 reads
finished rows, so each one stays only if a page or a cross-key render reads it.
- **Stay:** the dimensions (a person's scope and header, the directory),
  `session_call_accounts` (a linked account's devices), `session_edges` and
  `session_title_calls` (cross-key renders), `batch_parts`.
- **Go:** `request_txs`, `requests`, `analytics`, `prompt_index`, `codex_turns`,
  `request_reports`, `request_missed`, `session_last`, `dim_session_visible`.
  Their values live in the call's, the prompt's or the session's row.

§3.12 lists each table with its readers.

### 3.5 Directory folds

Every key writes these from its render or its facts:

| Table | Primary key; index | What | Replaces |
|---|---|---|---|
| `session_call_days` | (session_id, day) | per session and UTC day: its call marks' count, first and last `t_ns`. The day is computed as `_ACTIVITY` computes it: floor(t_ns / 1e9 / 86400) in float64 | the directory's reads of every call mark |
| `agent_calls` | (key, call_id); (ts_ns) | each Claude Code and Codex call's time, calls in flight included (v5's `merge_agent_calls` rows) | the merge's day partitions |
| `agent_call_hours` | (hour, key) | the count per UTC hour | the whole hours of `/overview`'s count |

*v8:* `agent_calls` and `agent_call_hours` become `agent_call_cells` (key, lvl,
t): counts per minute and per hour, plus one row per key for calls with no time
(§3.8). With snapped ranges, `/overview`'s count is a merge of whole cells.
`session_call_days` stays.

### 3.6 Retention

The retention singleton runs every hour, by day: it works through the keys whose
`day` is older than `retention.days` (default 15; the UI's widest preset is 14 d
back from an hour). For each key:
1. **Delete its rows in small batches:** ≤ 5k rows per statement, by the key or
   session prefix of each table's primary key (v8: or of an index, as for
   `event_rows`), with `lock_timeout` 1 s. A batch
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

**v8: retention flags the readers of what it forgets.**
- One kind of row a key owns is read by other keys' renders: its
  `batch_parts`. Forgetting it changes what those keys render: a batch's count,
  name and session.
- So the transaction that forgets a key also flags (`render`) the batch's other
  keys, and writes their `dirty_inputs`. It takes the batch locks in the global
  order and writes the flags in one sorted statement, as a commit does (§2). A
  reader being forgotten in the same pass is skipped.
- Rows read only at read time (edges, title calls) need no flag. The page
  simply stops finding them.
- A key's sessionless events go by `owner_key`, its session rows by session,
  and its drawer templates by key.

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
  run renders the key's drawer rows whole (v7; v8: §3.7's v8 block) and writes the ones whose hash
  changed (§2 Run 6), as for the session tables.
- **What only the call's detail object holds** (headers, the whole `complete`
  row, body chunk times, retained bodies) is read once: the run GETs
  `calls/<id>.ndjson.gz` for each call that ended since, and keeps what the
  drawer reads of it (the wire summary, never the bodies) as an *external
  input* of the key's state: journaled with its records, so a cold load has it
  without a GET. *v8:* of its headers the wire keeps only the ones the render
  reads (the request's prompt, class, client request id and session; the
  response's request id, unified rate limits and Codex headers), and of its
  bodies only the drawer's views of them (content rows with their 4,000-char
  previews, the stream's event count, the tools' input rows).
  - **Redaction stays whole-body and exact.** `request_detail.redact` runs over
    the decoded body before it's parsed, and that order shows: a redacted
    `"access_token": "…"` pair leaves the JSON invalid (no content rows), a
    secret is matched in the body's JSON-escaped text, not in the decoded
    strings, and the content rows' sizes are the redacted text's. Redacting
    only the emitted previews would change all three, so the worker still
    redacts the whole body, but finds candidates by the literal each
    alternative starts with (`sk-ant-`, `Bearer`, `eyJ`, `gh`, `xox`, `ya29.`,
    the four quoted keys) with `strings.Index`, and runs an alternative's
    regular expression only where its literal is. Only one alternative can
    match at any position, so this is the same leftmost-first scan; a fuzz
    test holds it to the regular expression's. It was 55% of the drain's CPU. An object missing for a call that ended under 2 minutes ago
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

**v8: the drawer.**
- **Rendered incrementally.** The worker's incremental drawer (branch
  `v8-worker`) derives a row from its own call and what it reads of the key's
  other rows, through fixed lookups, and records what each lookup read:
  - the events about it: by its request or client request id;
  - its stall summary;
  - its query: the last `tengu_api_query` of its prompt near its time;
  - a failed call's retry, near its end;
  - its tools' bash runs;
  - its prompt's steps up to its place, and its next step;
  - the message-id chain;
  - its prompt's number;
  - an analytics call's batch session;
  - a reported call's success;
  - the sandbox setting up to its time.

  A run files every changed input under what it was and what it is, and
  re-derives only the rows that recorded something filed.
- **v8 makes all of it follow the run's records.** The scope's indexes and
  the recorded lookups are part of the warm state, updated by deltas and
  stored in the snapshot (§2). So neither a run nor a cold load rebuilds them,
  and no row is hashed whole.
- **Values that grow are filled at read,** from placeholders in the stored
  values:
  - `stepCount`, from the prompt item's `drawer_step_count` (§3.3): the
    number of the prompt's steps as the drawer counts them (the calls it
    numbers "Step N of M" in that prompt). It isn't the Prompts tab's
    `steps`: on the capture 1 of 390 (session, prompt) groups differs (849
    against 850). `v8-worker` keeps it in `drawer_step_counts`; normalized,
    it's a column of the prompt's row, and that table goes;
  - a call's step number in its prompt: its prompt's steps before it;
  - its "delivered in step N", its next step's place among the session's
    steps in the drawer's order (time, then call id). That's the `step` cells
    (§3.8) before its next step's minute, plus that minute's steps before it.

  `v8-worker` stores the last two in the row. Read time is what this spec asks
  for, so a late step rewrites only its own and its neighbours' rows, not every
  later row of the prompt or the session.
- **The rows are a template and their values,** as `v8-worker` built them
  (`internal/drawer/template.go`), but scoped per key (decided):
  - A row's `data` and `details` are each a template (the JSON's shape, with
    `%s` for its values) and its values joined by `\x1f`. The drawer's
    statement rebuilds the bytes with `format()`.
  - `drawer_templates` (key, id): the owning key is in the primary key, a
    template is written in the same commit as the first row that uses it, and
    retention deletes it with its key. `v8-worker`'s global, never-deleted
    templates become per key.
  - The drawer row carries one foreign-key column per template it references
    (`data_template`, `details_template`), each a DEFERRABLE foreign key to
    `drawer_templates (key, id)`.
  - **Measured on the capture** (`v8-worker`): `drawer_requests` 222 → 104 MB,
    plus 3.5 MB in 1,137 global templates. Scoped per key, an approximation of
    the shape gives ~1.5× the templates and ~20% more template bytes: ~3.5 →
    ~4.2 MB, under 1% of the rows. That's the dedupe lost.
  - What's still payload-like in the values is the `fields` section's verbatim
    `complete` row and analytics metadata (41.5 MB of JSON), and the content
    previews (18.7 MB). Moving them to S3 would mean deriving at read, so they
    stay (Not now).
- **One drawer row per call, and no input copies:**
  - `/raw`'s fallback view (a WebSocket turn's, or a call whose detail object is
    missing) is rendered into the row (`raw_fallback`) instead of being read
    from a `requests` row, so `requests` goes;
  - `/events/{id}` reads the event's own row (`event_rows` by its primary key,
    (batch, rank)), and its raw row from the batch's S3 object by `event_key`,
    so `drawer_events` goes;
  - the raw formatter (`raw.py`, and the parts of `request_detail.py` that
    `/raw` and `/events/{id}` use) is split from today's derivation, so it
    imports no DuckDB (before step 11).
- **Why a call keeps a log row and a drawer row, not one merged row:**
  - **different reads:** the logs read narrow rows by range through their
    indexes; the drawer reads one wide row by key;
  - **different lifetimes:** a call in flight has a log row and no drawer row;
  - **different updates:** a drawer row changes with its neighbours (its next
    step, a retry), and the log row almost never does. Merged, each drawer
    update would rewrite a row the log's indexes point at.

### 3.8 Cells: sums per minute and per hour

A cell holds what the pages merge of one unit's rows in one minute or one hour:
counts, sums, the keys seen, the first or last of something. Every range is
whole minutes (§5.3), so the API merges whole cells and never reads rows to
count a range. Cells are aggregates, not copies: summing the rows at read would
read every row of the range.

- **Two levels:** `lvl` 60 (minutes) and 3600 (hours). `t` is the cell's
  start, `t_ns − t_ns mod lvl` in Unix ns: aligned to the epoch, as the
  timelines' bins are.
- **Written by the rows' own key, from deltas** (§3.11). The minute cells are
  memoized in the warm state (and in its snapshot, §2):
  - a changed row's old contribution comes out of its minute's cells, and its
    new one goes in;
  - a first or last pick is recomputed from the minute's rows only when its
    current pick is removed;
  - an hour cell takes the same deltas.

  A run's cost is its rows', never the minute's or the session's. It writes
  the cells it changed: the run's minutes, and their hours, ~10 to 25 hour
  cells a run.
- **Values merge** (§5.1):
  - counts and sums add;
  - a sum is NULL when no row had a value, so a sum over cells is NULL exactly
    when today's sum over the rows is;
  - `first_*` and `last_*` carry the earliest and latest (t, seq), and the
    value there.

  Floats are summed per cell (quirks).
- **Keys are text, and a primary key can't hold NULL.** Each nullable key
  column (`key`, `key2`; `agent`, `client` in `request_cells`) has a boolean
  `<column>_null` beside it in the key, and holds '' when it's set. So NULL and
  '' stay two keys, as they are today.
- **Numeric keys** (PR numbers) sort by their value at read: a key of ASCII
  digits, cast (quirks).

**`session_cells`:** primary key (session_id, lvl, t, dim, key, key_null, key2,
key2_null), written by the session's key. Columns: `n`; `s1` to `s5` (bigint
sums); `f1` (a double sum); `first_t`, `first_seq`, `first_val`, `last_t`,
`last_val`.

| dim | Rows (filter) | key, key2 | Values | Read by |
|---|---|---|---|---|
| `event` | events | name, unnamed | n; s1 rows with `head_all`; s2 rows with `head_pub`; s3 non-internal rows | the analytics lane (count, top 3 names a bin); the summary's names and event types; `/state`'s events and pool changes; the Events tab's total and shown counts (§5.3) |
| `tool`, `mcp_tool` | `tengu_tool_use_success` with a tool name, by `is_mcp` | tool name | n | the summary's toolsByName, toolCalls, builtinUsed |
| `mcp_pair` | MCP tools named `mcp__server__tool` in analytics (parsed by the worker); the calls' MCP tool uses | server, tool | n | the summary's MCP servers, mcpToolsUsed |
| `builtin` | the calls' other tool uses | tool | n | the summary's builtinUsed |
| `word` | `tengu_bash_tool_command_executed` | argv0 | n | topCommandWords |
| `destructive` | the same, category ≠ none | category | n | the destructive counts |
| `git` | `tengu_git_operation` | operation | n | the summary's commits, pushes |
| `pr` | `tengu_session_linked_to_pr` | pr_number | n | the summary's PRs, in number order |
| `head` | events with a head | head_sha | n; `first_t`, `last_t` | headChanges (the summary, `/state`); `/state`'s first and last head: the least (first_t, sha), the greatest (last_t, sha) |
| `latest` | `tengu_tool_search_mode_decision`, `tengu_skills_sync_success` with a count | name | `last_t`, `last_val`: the latest (t, count) | the latest MCP total and skills, up to hi (`/state`, the summary) |
| `hook_name` | `tengu_run_hook` | hook_name | n | the summary's hooks |
| `hook`, `git_op`, `pr_link`, `recap` | `hook` set; `git_op` set; `pr` set; recap, recap_key | the value | n | `/state`'s hooks, commits, pushes, PRs (in number order), recaps |
| `api_success` | `tengu_api_success` | — | s1 cache_5m, s2 cache_1h, f1 cost_usd | the summary's cache writes and cost |
| `hook_ms` | `tengu_tool_use_success` with hook_ms | — | n, f1 | hookOverheadMs (f1 / n) |
| `outside_cwd` | bash commands outside the cwd | — | n | outsideCwd |
| `batch_first` | rows with `batch_first` | — | n | analyticsBatches (§5.3) |
| `call` | call marks | request_class | n, s1 input, s2 cache_read, s3 cache_write, s4 output, s5 context | the model-call lanes; the summary's calls and tokens; `/state`'s modelCalls |
| `call_unreported`, `prompt_first` | call marks: unreported; with `prompt_first` | — | n | estimatedCalls; the summary's prompts (§5.3) |
| `reported`, `missed`, `compaction` | those marks | — | n | coverage; the lanes' not-captured counts; `/state` |
| `friction` | friction marks | friction kind, detail | n; first: its (t, sort key) and its `prompt_id`, whose number is joined | the friction lane: count, first prompt, details by first occurrence |
| `prompt`, `prompt_row` | prompt items; every prompt row | — | n | `/state`'s prompts; the Prompts tab's total and a prompt's place |
| `codex_prompt`, `codex_turn` | a Codex prompt item, by its start; a Codex turn, by its first visible call | — | n | a Codex prompt's and turn's number: the count before it (§5.3) |
| `step` | the drawer's steps (its set: the session's Claude Code calls) | — | n | the drawer's "delivered in step N" |

**`request_cells`:** primary key (unit, lvl, t, kind, hidden, captured, agent,
agent_null, client, client_null, sessionless, missed, key_hash, key), with an
index (key) for retention: Postgres 17 has no skip scan, so the reads' unit
must lead. Column `n`. Written by the call's key, so several keys write cells
of one unit. `unit` is:
- `s:<session>` for every call with a session (its own, or its batch's) and
  every missed row;
- `h:<credential>` for every call with a credential.

A person's log merges its credentials' `h:` cells and its sessions' missed `s:`
cells. A session's tab merges its `s:` cells whose `key_hash` is one of the
session's credentials, a join at read (§3.9). A Codex thread's tab also merges
its credentials' sessionless Codex cells of its client (§5.4).

**`agent_call_cells`**: primary key (lvl, t, key), and an index (key) for
retention. Column `n`: the Claude Code and Codex calls per minute and per hour,
for `/overview` (§5.6), plus one row per key for calls with no time. They
replace `agent_calls` and `agent_call_hours`.

**Size on the capture.** The heaviest session (53k events over 48 h) has ~1.5k
hour cells and ~10k minute cells over its event dims, and ~0.8k minute cells
over its marks. The heaviest person (13 sessions, 71k events) has ~3.3k and
~17k. About one minute cell per 5 events.

### 3.9 Request-log rows: a call's one row

`request_log_rows` is a call's canonical row: one per call, and one per
not-captured call (its `reported:` id), written by the key that holds it. It
replaces `session_request_rows`, `request_txs`, `requests` and `prompt_steps`,
four copies of the same call in v7. (`session_request_rows` and the other
inputs stay until step 11, because the v7 loader reads them.) It serves:
- both request logs: a person's and a session's;
- the Prompts tab's steps;
- an event's link to its call;
- the drawer's lookup of the key holding a batch call.

**Rows:** every call, with or without a credential (a call with none appears in
no log: the logs select by credential), and every not-captured call.

**Primary key (request_id, key).** Indexes:
- (key_hash, kind, ts_us, request_id): a person's log;
- (session_id, kind, ts_us, request_id): a session's tab;
- (session_id, prompt_id, ts_ns, request_id) WHERE it's a step: the Prompts
  tab's steps, and step numbers;
- (session_id, req_id): an event's link;
- (key): retention (§3.6).

A log without `kind` merges the kinds' index ranges, so no index without `kind`
is needed.

**Columns:** today's request-log columns as the logs show them
(`stream_sql.COLUMNS`, but not person_id), plus:
- `key_hash`; `ts_us` (`ts_ns / 1000`: the logs' order and their range);
  `sessionless`; `missed`; `req_id`;
- `session_id`, final: the call's own session, or its analytics batch's (the
  batch parts across keys, read at render time as the drawer's are, §3.7). A
  batch call still in flight in a `c:` key gets its session this way, so the
  session's tab finds it;
- `prompt_id`, and its prompt's number joined from the prompt's row, not
  stored (§3.3);
- for a step (a model call of a prompt), its Prompts-tab facts: request class,
  model family, timings, tokens, tools, commands, exit codes, patches, text;
- `ttft_ms`, `tokens_cache_write_5m`, `tokens_cache_write_1h` (model calls),
  from the first report of its request id: its own session's (I2, §5.3).

**What a scope decides, at read** (§5.3):
- **Prompt number and report values** are shown when the call's `session_id` is
  in the scope: the person's sessions, or the one session.
- **The step** is 1 + the number of its prompt's earlier model calls (by
  `ts_ns`, then request id; visible, with a prompt id) whose credential is in
  the scope's credentials. That's one index range on (session_id, prompt_id,
  …). It's exact with no stored arrays, even when a token was refreshed
  mid-prompt (2 prompts on the capture), and a late call renumbers nothing.
- **A session's tab** lists its calls whose credential is one of the session's
  (`dim_session_credential`): today's filter, a join at read. On the capture
  24 calls fail it (6 hidden Codex title calls, 18 analytics batch calls).
  Stored as a flag, a session's first call with a new credential would flip
  every row with that credential.

**Why the call's facts live here.** Today's person log takes these values from
the person's own sessions and credentials only (`stream_sql` over the scope).
On the capture, credentials are shared across email groups: a person's log
holds thousands of model calls from sessions outside their scope, shown without
a prompt number or a time to first token. Joining by scope at read lets one row
serve every person and every session.

**Render:** from the call's key's own state, plus batch parts (v7). A prompt's
calls, its numbers and its reports are in its session's key (I1 to I3). A
commit that inserts or changes a batch call's row takes that batch's lock and
re-checks its parts (§2).
**Writes:** a new call adds its row. Its retry fills one earlier row (the failed
call's). Nothing renumbers.

### 3.10 Labels and children: read, by index

A session's label and children come from rows other keys write. v8 reads them
at read time, by index lookups, as an event's link to its call is read (§5.5,
accepted). Nothing is rendered or flagged for them.

**One label** (decision 2). A Codex thread's label is its own title calls'
(`labels.sql`'s `alone` rule), everywhere: the session header and timeline, and
now the person pages too. Today's person pages ran an assignment over the
account's whole component (quirk 2). That chain is gone, so a thread's label
never depends on another thread's.

| Value | Read from | Bound |
|---|---|---|
| children | `session_edges` by `parent_session_id`, each written by the child's key | the session's children |
| the label | its own title (`session_rows.own_title`). Else, for a Codex thread with a model call, the earliest title call (by time, then call id) of its accounts (`session_call_accounts`) in its first 5 minutes: `session_title_calls` by (account, time). Else `Session <id prefix>` | its accounts × 5 minutes of title calls |

A page of sessions does these lookups once per shown session, in one statement.
`title_candidates`, the account lock and their flags aren't needed, and aren't
built.

### 3.11 The incremental render: each table's dependencies

A run renders exactly the rows its records' deltas affect (§2). This section
gives each table's rule and bound.

**Why every bound holds.** Every column of every rendered table is one of three
kinds:
1. a function of the row's own state row (its record);
2. a function of a bounded group of state rows: one request id's rows, a
   prompt's neighbouring items, the ≤ 3 segments of a local recut, one
   minute's rows, one batch part;
3. a value shared by an unbounded group (a prompt's number, a call's label, a
   step count, a session's product). Each of these is stored once, on the
   group's canonical row (§3.3), and joined at read.

Rows carry stable keys, and ranks are computed at read. So a record changes a
constant number of rows per table, and the cells of its minute and hour, by
delta. The state engine already returns each record's delta by (table, id),
and the run already collects them; the render is the only part that used to
read everything.

| Table | Key | A record re-renders | Bound |
|---|---|---|---|
| `event_rows` | (batch, rank) | its own row; in each view, the next row's head flags | ≤ 3 rows |
| `prompt_rows` | (session, item id) | its prompt's item (numbers, aggregates, step count) and up to two neighbours (hidden and pasted runs); a between item in its compaction window | the prompt and its neighbours |
| `request_log_rows` | (request id, key) | the call's row; on a request id's first report, that id's rows; on a retry, the failed call's row; a tool or tick event, its step's row (one tool entry); an analytics call, when its batch's parts change | one request id's rows |
| `session_marks` | (session, kind, mark id) | the new mark; a call mark's `unreported` on its request's first report; ≤ 3 permission or network segments around a new point (a local recut) | constant |
| `presence_segments` | (session, grp, from) | the recut stretch (`state/presence.go` already recuts locally) | the stretch the record moved |
| `session_rows` | session | its one row (running aggregates) | 1 |
| `session_devices`, `session_call_accounts`, `session_tools` | (session, value) | a new value's row | 1 |
| `session_cells` | (session, lvl, t, dim, keys) | the cells of the changed rows' minutes and hours, by delta (§3.8) | the run's rows |
| `request_cells`, `agent_call_cells` | as §3.8 | the call's minute and hour cells | the run's calls |
| `batch_parts` | (batch, key) | the key's part of the event's batch: count, least name, first, last; its times when spanning | 1 |
| `session_call_days` | (session, day) | the call's day | 1 |
| `drawer_requests` | (request id, key) | the rows whose recorded lookups had an input filed (§3.7): the call's own; the previous step's (its next); a failed call's (its retry); the rows sharing its request id, prompt place, chain link or time bucket | the few rows sharing one id, place, link or bucket |
| `drawer_templates` | (key, id) | a new shape | 1 |
| `session_title_calls`, `session_edges`, the dimensions | as v7 | the changed rows | 1 |
| `dirty_inputs` | (key, kind, id) | written with a flag; consumed by the reader's run | per flag |

**Late and out-of-order records** follow the same rules:
- a late event: its row, and the next row's flags;
- a late input prompt: one prompt row;
- a late call: its row (step numbers are read time);
- a late title call: its own row (labels are read at read time).

**What stays O(session):** a key's first run, and a run after a
`render_version` change, render everything once. Nothing else does:
- **A cold load decodes a snapshot** (§2): the state, its indexes, its minute
  cells and the drawer's memo. It never renders and never rebuilds a baseline,
  and it replays at most the delta chunks since the snapshot. Over a key's
  life, the records replayed stay ≤ the records applied.
- **Keys stay warm** until 60 minutes idle or their session expires, under a
  byte cap per worker. With v7's journal, a 5-minute window would replay 32×
  the applied records on the capture.
- **Restarts and scale-in** write each warm key's snapshot before its lock is
  released (SIGTERM), so the next holder decodes it.

The CPU gate holds all of this, cold loads included (Testing).

### 3.12 Every table: writer, readers, size

**The rule** (decision 5): one canonical row per fact. Postgres holds what's
queried (the rows the pages read, and finished display rows read by key). Raw
payloads stay in S3, read by pointer. A table with no reader is deleted.
Denormalized values are kept only where a join can't meet the latency target,
each justified below.

Sizes are measured bytes per row with indexes on the capture (v7 gate's
Postgres) where the table exists, else estimated (est.). Rows a day are at the
target (1,500 people).

| Table | Writer | Readers | Bytes/row | Rows/day | GB/day |
|---|---|---|---|---|---|
| `event_rows` | the event's key | the Events tab (page, lookups); `/events/{id}` (primary key); `/state`'s recaps; the summary's straddlers | ~615 (est.; v7 701) | 75M | ~46 |
| `drawer_requests` | the call's key | the drawer (primary key) | ~2,500 (`v8-worker`'s templates, measured: 104 MB / 41,769 rows; v7 5,358) | 7.5M | ~19 |
| `request_log_rows` | the call's key | both logs; the Prompts tab's steps; an event's link; the worker's batch-call holder lookup | ~1,100 (est.) | 7.5M | ~8 |
| `session_cells` | the session's key | every count and lane (§5.4) | ~270 (est.) | ~17M | ~4.6 |
| `request_cells` | the call's key | the logs' counts; `/state`; the summary's account calls | ~200 (est.) | ~2M | ~0.4 |
| `session_marks` | the session's key | the timelines; the Prompts tab's between rows | 310 | 3.5M | ~1.1 |
| `batch_parts` | each key holding a batch's events | the render of a batch's other keys (§2); the summary's spanning correction | 365 | ~3M | ~1.1 |
| `prompt_rows` | the session's key | the Prompts tab; the prompt lanes; joins for numbers and step counts | ~400 | 0.2M | ~0.1 |
| `presence_segments`, `session_tools`, `session_devices`, `session_call_accounts`, `session_call_days`, `session_rows`, `drawer_templates`, `session_title_calls`, `session_edges`, `agent_call_cells`, `dirty_inputs` | the session's (or call's, title's, child's) key; `dirty_inputs`, the flag's writer | the pages and renders named in §3.3 to §3.10, §5.4, §5.6 | 150 to 1,500 | under 1M each | ~0.5 together |
| the dimensions (`dim_account`, `dim_session_account`, `dim_session_credential`, `dim_session_person`, `dim_person_device`, `dim_account_events`, `dim_account_device`, `dim_codex_plan`) | each key, its partial rows | a person's scope and header; the directory | small | per session or account | ~0.1 |
| `keys`, `dirty_keys`, `workers`, `tail`, `ingest_pods`, `schema_version` | the workers | claims, runs, `/lag`, `/status`, retention | small | per key | ~0 |
| **All** | | | | | **~81** |

**Deleted: no reader once v8 reads finished rows** (v7 bytes/row → GB/day at
the target):

| Table | v7 bytes/row | v7 GB/day | Why it goes |
|---|---|---|---|
| `analytics` | 1,354 | 101 | no reader since the drawer was precomputed (#55); removed in #59 (`api/tests/test_pg_readers.py` checks every table has a reader) |
| `drawer_events` | 633 | 47 | `/events/{id}` reads the event's own `event_rows` row |
| `requests` | 1,350 | 10 | `/raw`'s fallback view is rendered into the drawer row; the batch-call holder lookup reads `request_log_rows` |
| `request_txs` | 966 | 7 | read only by v7's read-time `stream_sql`; the call's row holds its facts |
| `session_request_rows` | 526 | 4 | replaced by `request_log_rows`, after step 11 |
| `prompt_steps` | 814 | 1.2 | a step's facts are its call's row |
| `request_reports`, `request_missed`, `prompt_index`, `codex_turns`, `session_last` | 775, —, 866, —, — | ~1.5 | read only by read-time SQL; their values are the call's, the prompt's or the session's row (`session_last` is `session_rows.last_ns`) |
| `call_tool_uses` | 250 | 0.8 | read only by the summary; the cells hold it |
| `agent_calls`, `agent_call_hours` | 361, 249 | 1.6 | `agent_call_cells` |
| `dim_session_visible` | — | ~0 | read only by `unattributed`'s anti-join; `session_rows.unattributed` replaces it |
| `drawer_step_counts` (on `v8-worker`) | — | ~0 | the drawer's step count is a column of the prompt's row (`drawer_step_count`) |

v7 stored ~270 GB/day at the target; v8 stores ~81 (decision 5: under 110). The
three largest left are each one row per thing: an event's row, a call's drawer
row, and a call's row. A further cut is possible: an event's raw position in
place of its 79-byte `event_key` (~6 GB/day less; Not now). Never built:
`drawer_parts` and `title_candidates` (an earlier draft's).

**Denormalized, and why:**
- **cells:** aggregates of rows. Merging the rows of a range at read would read
  every row of the range.
- **`event_rows`' head flags and first-occurrence marks:** a row's relation to
  its neighbours, so counts can be cell sums. Each changes only with the row or
  its neighbour.
- **`drawer_requests`:** a finished display object, read by key. Its
  derivation reads many rows of the key, so doing it per open would miss the
  latency target. Its growing values are referenced, not stored (§3.7).
- **`session_rows`' aggregates:** the session's own running totals, which every
  session and person page reads.
- **`batch_parts`:** each key's part of a batch, merged by its readers.
- **`name_lower`, `summary_lower`:** only where Postgres's `lower()` differs
  from today's.

**Foreign keys.** Declared where parent and child are written in the same
commit by the same key, so they're cheap and catch render bugs. Each is
DEFERRABLE INITIALLY DEFERRED, so a commit can write children before parents
and retention can delete in any order inside its transaction:
- each table's `key` column → `keys (key)`: `request_log_rows`,
  `drawer_requests`, `drawer_templates`, `request_cells`, `agent_call_cells`,
  `batch_parts`, `session_title_calls`, the dimensions, and `event_rows`'
  `owner_key`;
- `drawer_requests (request_id, key)` → `request_log_rows (request_id, key)`;
- `drawer_requests (key, data_template)` and `(key, details_template)` →
  `drawer_templates (key, id)`: one column per template it references.

**The session tables have no foreign key to `session_rows`:** a session's rows
can exist with no session row (the state doesn't make one for every session,
v7 §3.3). Nor are they given a `key` column only to hold one: on `event_rows`
that would cost ~3 GB/day.

**References that cross keys** can't be enforced foreign keys:
- a batch call's `session_id` (the session's rows are another key's);
- `batch_parts` across a batch's keys;
- `session_edges`' parent;
- `session_title_calls`' accounts;
- the dimensions' account and person ids;
- an event's link to a call (a raw request id, joined at read).

Their two sides are written by independent commits in any order (the
referenced row can land later, or never: a call that wasn't captured), and
retention forgets each key on its own schedule. The cross-key rule (§2) keeps
their readers right instead.

## 4. Cross-key values

v7 joined most cross-key values at read time, bounded by the page's entity. v8
renders the ones that must be precomputed: a batch's values, read by its other
keys' renders and flagged by the cross-key rule (§2). It reads the rest at read
time, by index lookups or filters: who a person is, a session's children and
label, a session tab's credentials, a Codex thread's unthreaded calls.
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
| A session's Requests tab: other keys' rows | read (`stream_sql` over candidates) | write; filters at read | A batch call in flight gets its session at render (§3.9). The tab's credential filter, and a Codex thread's unthreaded calls (by the thread's credentials, client and window), are filters at read |
| children; a Codex thread's label (its own title calls everywhere, decision 2) | read | read, by index lookups | §3.10 |
| Presence and not-captured windows across a person's sessions | read | read (§5.5) | folds over the range's few segments and missed calls |
| `/people`, `/overview` | read (DuckDB) | read (merges in Postgres) | §5.6 |
| `/status`; expiry's data time | read | read | unchanged |

## 5. API: reads finished rows, scaled on CPU

v7's API read each page's rows into an in-memory DuckDB and ran today's endpoint
code over them. v8 replaces that for every page, the directory included (§5.6).

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

Every computation that stays is listed in §5.5 with its bound, and accepted
(decision 8).

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
| `/people` (61 ms), `/overview` (66 ms) | `from` (floored to the hour), `to`, `sort`, `agent`, `q`, `page`, `pageSize` | v7's loader: the window's keys' dimension rows, session rows, call days and missed marks into DuckDB; `person_sql`, `rollups.py` and `directory.py` over them; for `/overview`, agent calls in the range | every key in the window | live |
| `/status` (27 ms), the drawer (~35 ms) | — | one statement each, through the DuckDB loader | — | — |

Ranges are often arbitrary instants: a brush is any ms, and a person's preset
is `now − span` at ms. Bins are always on the ladder: 60, 300, 900, 3600,
10800, 21600, 43200, 86400 s.

### 5.3 The read primitives, and why each is exact

**Finished rows.** Rows shown as they're stored. Their order is a stored key,
and paging takes a page of it.

**Ranges snap to whole minutes** (decision 1). The router snaps every range
bound before any reader sees it, in ns:
- `lo` → its minute's start: `lo − lo mod 60 s`;
- `hi` → its minute's last ns: `hi − hi mod 60 s + 60 s − 1`.

This applies to every range a page filters by, explicit or default:
- a `from` or `to` in the URL;
- a session's default `lo`, its start, and default `hi`, its last activity or
  `now` while live;
- a person's default `lo` (first activity, or `now` − 14 d) and default `hi`
  (the later of `now` and the last activity);
- the directory's `from` and `to`.

The UI's ranges map like this:
- a person preset sends `now − span` at ms precision and no `to`. Its `lo`
  becomes the minute's start, and its default `hi` the end of `now`'s minute;
- a brush's ends snap outward, so a brush narrower than a minute shows that
  whole minute;
- the People page's `from` is already floored to the hour, so it doesn't move.

Pages that don't filter by a range don't snap. That's the session header and
timeline: the timeline's lanes cover every row, and its `from`/`to` are the
session's span. The response's meta echoes the snapped bounds: `from` is the
minute's start (`…:00Z`), and `to` is its minute's last µs (`…:59.999999Z`),
since times are shown to the µs.

**Why snapped ranges are exact.** Each endpoint's own predicate keeps exactly
the rows in [L, H), where L is the snapped `lo` and H = the snapped `hi` + 1
ns, a minute boundary:
- events and marks, `BETWEEN lo AND hi`: [L, H);
- prompts, `t_ns // 1000 * 1000 BETWEEN lo AND hi`: [L, H), since L and H are
  whole µs;
- the request logs, `ts_ns // 1000` against `lo // 1000` and `hi // 1000`:
  [L, H);
- the summary's account calls, `ts_ns BETWEEN lo AND hi`: [L, H).

[L, H) is whole minutes: the whole hours inside it are hour cells, and the
other minutes (at most 59 at each end) are minute cells. There are no partial
minutes, so no rows are read to count a range, and a merge over the cells
equals the merge over the rows.

**Byte-identity is proven on the snapped range.** The oracle (today's code,
V1Store's reader) gets the same snapped ns bounds from the same router, so
every response must equal today's code's for that range. What snapping changes
is visible and decided: a range's ends move to its minutes' ends, and its
counts are its minutes'.

**Bins.** A timeline bin is `t − t mod bin`, aligned to the epoch.
- `bin` must be a multiple of 60 (422 otherwise). The UI sends only the ladder
  (60 s to 1 d), and those of an hour or more are multiples of 3600.
- A bin of b seconds reads hour cells when 3600 divides b, else minute cells.
  Each cell lies in one bin.
- A snapped range's first and last bins are whole minutes too.
- With no `bin`, the person timeline's auto bin comes from the span the request
  asked for (`hi − lo` before snapping), so snapping never changes which bin
  the UI gets (owner). The session timeline doesn't snap: its auto bin comes
  from its span, as today.

**Keys seen in a range** (event names and types, heads, hooks, tools, PRs): the
union of the cells' keys. A key occurs in the range if and only if it occurs in
one of its minutes.

**Things that span time, counted once** (an analytics batch's events, a
prompt's calls): first occurrence.
- Within a session, the entity's rows are in key order. A row is *first* if
  it's the entity's first in the session. Otherwise it carries `prev`, the
  time of the entity's row before it.
- The entities with a row in [L, H) are counted by the first rows in [L, H),
  plus the rows in [L, H) whose `prev` < L.
- Each entity with a row in range is counted exactly once, by its first row in
  range. That row is either its first overall, or its previous row is before L.
  No other row counts: a later row in range has its previous row in range.
- The first term is a cell sum (`batch_first`, `prompt_first`).
- The second term, the *straddlers*: the rows in [L, H) whose previous row is
  before L. Each is the far end of a pair (previous, row) that contains L.
  - L is a minute boundary, so such a pair crosses one.
  - The GiST index (§3.3) holds exactly the minute-crossing pairs, as
    `int8range(prev, ts)`. The straddlers are its pairs that contain L, kept
    when their row is before H and in the scope's sessions.
  - That's bounded by the pairs open at the instant L: the batches and prompts
    in flight then, across all sessions. No gap bound (G) is needed.
- **A person counts per session, and adds.**
  - A prompt's calls are in one key (I3, below).
  - A batch can span sessions (6 of 4.2k for the capture's heaviest person).
    The sum counts it once per session with an event in range, so the read
    subtracts k − 1 for each spanning batch with events in range in k ≥ 2 of
    the sessions it counts.
  - k comes from the spanning parts that overlap the range: `first_ns ≤ hi AND
    last_ns ≥ lo`, with no gap bound (a part's events can span any time).
    The (key, last_ns) WHERE spans index reads them. A part counts in k when
    its event times (`ns`) hold one in range.
  - Spanning parts are rare (8 of ~9.4k batches on the capture), so the read
    is a handful of rows.

**The Prompts tab: sort keys and cells.** A session's `prompt_rows` are keyed
by the state's ids, with their sort columns (§3.3).
- The total in range is the `prompt_row` cells' sum.
- A page is an `ORDER BY` the sort key with `OFFSET … LIMIT`.
- Prompt N's place, and so the page holding it, is the `prompt_row` cells
  before its minute plus its minute's rows before it: a minute of prompt rows
  at most.

**A Codex prompt's or turn's number** (S8). Today it's a rank:
- a prompt by its start among the session's prompts;
- a turn by its first visible call.

v8 counts the ones before it: the session's `codex_prompt` (or `codex_turn`)
hour cells before its hour, the minute cells of its hour before its minute, and
its minute's items before it. That's at most the session's hours plus 59 cells,
plus a minute's items, per distinct prompt on the page.

**The Events tab: unit starts, counted in cells.** Today's SQL folds a tick into
the row before it when that row is also a tick (`lag` over the rows the view
keeps). Let v be the view: all rows, or the non-internal ones; its sequence is
those rows in key order (§3.3).
- Within a range, a row's previous row in v is its previous row overall,
  except for the range's first row of v: it has none, so it always starts a
  unit. Every other row starts a unit in the range iff it starts one overall
  (`head_all`, `head_pub`).
- **Total** (every event in range): the `event` cells' `n`.
- **Shown:** with ticks folded, the `event` cells' head counts for v (s1 or
  s2), plus 1 if the range's first row of v isn't a head overall (one lookup).
  Unfolded, every row of v is a unit: `n`, or s3 for the non-internal rows.
- **The page:** the units of v in range start at its rows with a head flag,
  plus its first row. Two `ORDER BY key OFFSET … LIMIT 1` reads over those
  give the page's first unit start and the next page's. The page's rows lie
  between them, and each unit shows its first row, with `collapsedCount` the
  number of its rows there. That's bounded by the page's units: their rows,
  plus the offset's index entries for a later page.
- Head flags change only on a late row and the one after it; stored positions
  would renumber every later row.

**`now`.** Every value that depends on `now` is one of two kinds:
- **a threshold on stored values:** live (`now − last_ns ≤ 5 min`), in flight,
  streaming, events pending, and the live tools and exit codes. These are
  today's expressions, run in Postgres over the stored columns (§3.3);
- **`now` itself:** a default range end (snapped), a band's end, and the folds
  over a session's own segments that end now. Those are its presence, its
  permission and network segments (the last ends at the range's end), and
  their not-captured windows. These run as today's SQL does, over the session's
  stored segments and missed marks: a few rows (§5.5).

They hold when `now` is before the newest row too (a client clock ahead of the
API's): the range ends at `now`'s minute, and the folds cut at `now` as today's
SQL does.

**The scope's values on request-log rows** (§3.9). A row in a person's log
shows:
- its prompt number and report values if its `session_id` is one of the
  person's sessions;
- its step as 1 + the number of its prompt's earlier visible model calls whose
  credential is one of the person's. That's one index range, and no stored
  arrays.

That's today's `stream_sql` exactly when:
- **I1:** a prompt's or turn's numbers are all in the session of its calls;
- **I2:** a request id's reports are all in its call's session;
- **I3:** a prompt's model calls are all in one key.

They're also why the render needs no other key for these values: the call's
own key holds them.

All three hold on the capture (0 violations, checked in Postgres for this
spec), and follow from how the ids are made: the client numbers prompts and
sends reports per session, and a call carries its session. They're tests on
every dataset and at the gate (decided: no runtime check), with two more: a
prompt id never repeats across sessions, and `unattributed`'s inputs come from
the session's own key.
- If one broke, the person's log would show the row's own session's value where
  today's shows the scope's.
- A session's tab is the scope of one session, so its rows (those whose
  credential is the session's) show all three.

### 5.4 Each page in v8

The numbers are the capture's heaviest session and person, estimated from their
measured cells; the gate measures them. Every range is snapped (§5.3).

| Endpoint | Statements, in order, in one snapshot | Rows read |
|---|---|---|
| `/sessions/{id}` | 1. the session row, its devices, its children (`session_edges`) and its label (its accounts' title calls), by index | a few |
| `/sessions/{id}/timeline` | 1. the session row. 2. the lanes' cells at the bin's level (`call`, `missed`, `event`, `friction`), merged per bin. 3. its rows, tagged: typed prompt items, context points, compactions, permission and network segments, presence segments, missed marks; the presence, network and not-captured folds over them (§5.5) | at bin 900 (its 48 h): ~9k minute cells and ~2k rows, ~1.7k of them context points. Today: 53k events and 2.5k marks |
| `/sessions/{id}/state` | 1. the session row. 2. cells over [lo, hi] (up to hi for hooks, the latest counts and heads; every hour for recap totals), and the request cells of the Requests tab's range with the session's credentials. 3. the recaps in range; `session_tools` first used by hi | ~2k hour cells and at most ~1.2k minute cells for the whole session |
| `/sessions/{id}/events` | 1. the session row. 2. the total and shown counts (`event` cells, the range's first row of the view); the page's first unit start and the next page's; the rows between, grouped by unit, with their prompt numbers and links joined | the range's cells, a few lookups and the page's rows. A later page also reads the index entries of the unit starts before it. With `q`: the range's matches (§5.5) |
| `/sessions/{id}/requests` | 1. the session row. 2. request cells: hidden, total, per kind; for a Codex thread, its unthreaded calls' window counted exactly (§5.5). 3. the page, by (`ts_us`, `request_id`): the session's calls whose credential is the session's, and a Codex thread's unthreaded calls by its credentials, client and window; prompt numbers and steps at read | ~0.3k cells and 20 rows, plus each row's prompt's earlier calls |
| `/sessions/{id}/prompts` | 1. the session row. 2. the page by sort key, its total and number (`prompt_row` cells), with `now` applied. 3. the page's steps: its prompts' calls | at most 20 items and their steps |
| `/people/{id}` | 1. the group's dimension rows: facts, devices, linked accounts. 2. its session rows, devices and call accounts, merged: the rollup, agents, each linked account's devices. 3. calls per day | the group's rows: tens to hundreds |
| `/people/{id}/sessions` | 1. the scope's session rows: shown, agent, nested, paged; the total and lastDataAt; the page's sessions' coverage windows (§5.5) | the scope's session rows |
| `/people/{id}/summary` | 1. the shown sessions. 2. their cells, straddling rows and spanning parts over [lo, hi]. 3. the account calls' cells; the missed windows (§5.5) | the 14 d preset: ~3.3k hour cells and at most ~2.5k minute cells per session |
| `/people/{id}/timeline` | 1. the scope's sessions and the bands. 2. cells per bin. 3. typed prompt items in range. 4. the bands' presence segments (§5.5). 5. the missed windows (§5.5) with their cells | 24 h at 300 s: ~6k minute cells. Today: 24k events |
| `/people/{id}/requests` | 1. the scope's credentials and sessions. 2. request cells. 3. the page: for each credential and kind (the kind index), and each session's missed rows, its first offset + size rows in order, merged; prompt numbers and steps at read | the cells, and k·(offset + 20) rows for k credentials and kinds |
| `/coverage` | 1. the scope. 2. mark cells; the missed rows. 3. the windows (§5.5) | the missed calls in range |
| `/people`, `/overview` | §5.6 | the window's session rows |

### 5.5 Everything still computed per request (accepted, decision 8)

Every computation left in a request, with its bound. "The page's rows" means
the rows the response shows. None of these reads a range's rows, except where
the bound says so.

**Merges, joins and filters over finished rows** (§5.1: exact however they're
grouped):

| Computation | Where | Bound |
|---|---|---|
| The person's scope: the group by email, its sessions, its credentials; `unattributed` by its flag | every person page | the group's dimension rows |
| The person header's merges: the rollup's sums and newest picks, the union of devices, agents, `person_sql`'s picks, each linked account's devices | `/people/{id}` | the group's dimension and session rows |
| The directory's merges: the same, per person, over the window's sessions; the overview's totals | `/people`, `/overview` | the window's session rows (§5.6) |
| The shown-sessions filter, nesting under a shown parent, paging by root | person sessions, summary, timeline | the scope's session rows |
| Cell merges: sums, counts, the first or last by (time, seq), key unions (names, heads, hooks, tools, PRs) | every count and lane | the range's cells: whole hours, and at most 59 minute cells at each end, per session or credential, per key |
| Per bin: the top 3 event names; friction's count, first prompt and details by first occurrence | both timelines | the bins' cell keys |
| First-occurrence straddlers, and the spanning-batch correction (k − 1) | the summary's batches and prompts | the GiST index's pairs that contain L (the batches and prompts in flight at L); the spanning parts that overlap the range |
| The Events tab's unit math: the first-row adjustment, the page's two unit starts, `collapsedCount` | `/events` | a lookup, the page's rows, the offset's index entries |
| The Prompts tab's page and prompt jump | `/prompts` | the page's rows; a minute of prompt rows |
| Joins for shared facts: a prompt's number (from its item; a Codex one counted from cells, §5.3), an event's link (the request id's newest visible call), a step count; a session's children (`session_edges` by parent) and label (its accounts' title calls in its first 5 minutes) | events, logs, prompts, the drawer; session headers and lists | one indexed row per shown row; a Codex number, the session's hours plus 59 cells and a minute's items; a label, its accounts × 5 minutes of title calls |
| Step numbers: in the logs, 1 + the prompt's earlier visible model calls with credentials in scope; in the Prompts tab, `row_number()` in the prompt; the drawer's step N and "delivered in step N" | logs, prompts, the drawer | the prompt's calls; for "delivered in", the session's `call` cells before the call's minute |
| The request logs' scope test: a call's session in scope for its prompt number and reports; a session tab's credential filter | request logs | the page's rows, and the scope's credentials |
| A Codex thread's unthreaded calls: credentials, client, kind, and the thread's window [start_us, (last_ns + 60 s) / 1000], which isn't whole minutes. Counted exactly: the cells of the minutes strictly inside the window, plus the rows of its two edge minutes from the (key_hash, kind, ts_us) index | a Codex session's tab and `/state` | the window's cells, and two minutes of the credentials' rows; the page by index |
| The person log's page: a k-way merge of each credential's and kind's first offset + size rows | person requests | k·(offset + size) rows |
| `now`: thresholds, and a range or band ending now | session pages, person timeline | per row |
| Presentation: field copies, today's rounding, number order of PRs (keys of ASCII digits), the summary's top 8 words, `/raw`'s and `/events/{id}`'s formatting of the S3 object | every page | the response |

**Folds that depend on order** (today's SQL, in Postgres, in the page's
statements):

| What | Where | Bound |
|---|---|---|
| `q`: a substring of an event's name and summary, or of a request's path and detail, and for the Events tab its tick fold over the matches (one window function) | the Events and Requests tabs; a person's requests | the range's rows of the session or the credentials (a scan with `strpos`) |
| Band rows (`cc.pack_rows`) | the person timeline | the bands shown |
| Presence: a session's own segments, or the person's bands' segments, with sleep's priority; live, open until `now` | both timelines | the segments in range |
| A session's permission and network segments (the last ending at the range's end), with their bypass and not-captured counts | the session timeline | the session's segments and missed marks |
| Not-captured windows (15-minute gaps), with each window's reported count and bypass | the session row's coverage, the session timeline, the person summary and timeline, `/coverage`, the directory | the missed calls in range (none on the capture) |

Only `q` reads events, to match them.

### 5.6 The directory, `/status` and the drawer

**`/status`, the drawer and `/events/{id}`** are one statement each, now
through v8's reader instead of the DuckDB loader:
- the drawer reads `drawer_requests` by its primary key, joined with its
  `drawer_templates` and its prompt item, with the growing values filled in
  (§3.7);
- `/raw` reads the same row (its `raw_fallback`), and GETs the call's detail
  object;
- `/events/{id}` reads the event's `event_rows` row, and GETs the batch's
  object.

**`/people` and `/overview`** (decision 7) read finished rows like every other
page, so DuckDB and ADBC leave the API. They need no new precompute: a person's
directory row is the header's merges (§5.5) over the window's sessions. The
statements, all bounded by the window's keys through `key_day` (v7 quirk 1, as
built):
1. The window's dimension rows (keys with `day` ≥ day(`from`), and every `a:`
   key): `person_sql`'s leaders and members, as Postgres SQL. It's a merge
   (min, newest pick) over the group's rows.
2. The window's session rows with their person, merged per person: the
   rollup's sums, maxima and newest picks, devices (`session_devices`),
   prompts (the sum of `prompt_id_count`, by I3), agents, `live` by `now`.
   Then the filters (`agent`, `q` over names, emails, ids and devices), the
   sort and the page.
3. Each row's calls per day over the last 14 days (`session_call_days`).
4. Each row's not-captured windows: the fold over its sessions' missed marks
   (§5.5).
5. For `/overview`: the totals over those rows; its requests from
   `agent_call_cells` over [lo, hi] (whole minutes and hours; with no range,
   every cell and the calls with no time); sessions per product; and the
   unattributed sessions (`session_rows.unattributed`, with their call days).

Ranges snap like every other (§5.3). The UI's `from` is already floored to the
hour. The read is bounded by the window's sessions: on the capture a few dozen;
at the target ~6k for 24 h and ~90k for 14 d (v7's numbers). That makes the
14-day directory the heaviest read left: ~1 s warm at the target. Accepting
that, or adding per-(session, day) cells summed per person, is open for the
owner.

### 5.7 Code

- **The routers call a page reader.** V1Store's reader is today's SQL: it's
  the oracle. PgStore's is v8's statements. Both return what the routers map
  onto the models, so the side-by-side tests compare the two readers through the
  same router, on the same snapped range.
- **Gone from the API:**
  - the v7 loader: Arrow into DuckDB, `_into`, `_finish`, and the session,
    person and directory loads in `pgstore.py`, `pgstore_person.py` and
    `pgstore_directory.py`;
  - `sql/labels.sql` (the worker renders labels, §3.10);
  - DuckDB and ADBC as runtime dependencies. The API reads Postgres with
    psycopg 3 (an exact pin, 14 days old).
- **Split before step 11:** the raw formatter that `/raw` and `/events/{id}`
  use (`raw.py`, and the parts of `request_detail.py` it needs) moves into a
  module that imports no DuckDB. The rest of `request_detail.py` is the
  oracle's.
- **Kept only as the oracle,** moved under `api/tests/oracle` with DuckDB as a
  dev dependency: V1Store and BucketStore, `store.py`, `v1.py`, `buckets.py`,
  `state_sql`, `stream_sql`, `person_sql`, `rollups.py`'s folds,
  `directory.py`'s SQL, and `session.py`'s and `timeline.py`'s endpoint SQL.
  `AINTERCEPT_STORE=v1` leaves the app; the tests construct V1Store directly.
- `org_kind` still reads `CUSTOMER_ORG_IDS` at request time.

*As built (step 1):*
- **The seam** is `api/src/aintercept_api/pages.py`. A router parses, snaps
  (`ranges.py`), calls `pages.read(request, "<page>")` and maps what comes
  back. `SqlPages` is today's SQL; a store without a picker (V1Store, the
  oracle) reads every page with it. PgStore's `page_reader` picks `PgPages`
  (`pgstore.py`, on the psycopg reader in `reader.py`) for the names in
  `PgPages.PAGES`, and `SqlPages` over v7's loader for the rest. A page moves to
  v8 by adding its name there and its method, with `SqlPages`' signature.
- **On v8's reader so far:** `/status`, `/requests/{id}`, `/details`, `/raw`
  and `/events/{id}`, one statement each. `/raw`'s fallback view still runs
  `request_detail`'s `calls` SQL, over a one-row DuckDB made only when the
  view is needed (step 8 renders it).
- **Two pools until step 11:** v7's loader keeps its ADBC connections, so a pod
  holds up to 8 of each (8 + 8; step 11's row).
- **The auto bin comes from the span asked for, before snapping** (owner). The
  default 14 days snap to 14 d + 60 s − 1 ns, which would otherwise make them
  daily.
- **The harness** is `api/tests/side_by_side.py`: every page, one registration
  each, walked by `test_v8_side_by_side.py` (each page), `test_v8_cuts.py`
  (every cut, one session and one person) and `test_v8_checkpoints.py`. A
  checkpoint is a prefix of the dataset's raw rows, laid out by the v7 worker
  into a database of its own and settled (no flag left, render flags
  included). The worker's own incremental runs are held in Go (incremental
  equals whole). The API's clock is whole µs, so "±1 ns" around a threshold is
  the µs either side.
- `aintercept v1layout` now records each key's newest raw row in its one-pass
  commit (`Commit.NewestRowNS`), as the rollup loop does. Without it, the
  oracle's `/status` and drawer `lastDataAt` left out a call still in flight
  at a checkpoint.

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

None, as in v7. The schema changes, and there are no migrations,
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
| Worker run | per record: apply (#19), then render the rows its delta affects (§3.11): O(the run's records), never O(session). A key with no input costs a key-row read, a LIST and a DELETE. A cold load decodes the key's snapshot (§2) | the warm state (§2): the state, its indexes, its minute cells, the drawer's memo; until 60 min idle or the session expires, under a byte cap per worker | key (advisory lock), heartbeat share | S3 LIST and GET, the commit | ~100 commits/s at peak. ~1.5 rows a record (an event's row; a call's two; cells; neighbours): ~4.5k rows/s at peak (v7: ~10k), ~125M rows/day; a few flags per commit | ~4 per run, plus one GET per call that ended (its detail object, once): ~650/s at peak |
| Retention | per forgotten key: batched deletes, and its readers' flags | none | one holder | — | ~125M rows/day deleted at the target (Not now: partitions) | the forgotten keys' objects |
| API | per request: at most 5 statements, reading the page's rows and the range's cells (§5.4); the drawer one primary-key lookup | none | pods, on CPU | Postgres; S3 GET (`/raw`, `/events/{id}`) | reads only | those GETs |
| Postgres | — | the working set | one primary (vertical) | disk | ~81 GB/day (§3.12); ~1.2 TB at 15 days | — |

**What a commit writes, v7 and v8.** A typical run on the capture's heaviest
session: ~100 events, a few calls and ~10 requests over a minute (estimated
from its measured cells: ~10 event-dim keys per active minute).

| Table | v7 as built | v8 |
|---|---|---|
| events | ~100 `event_rows`, ~100 `analytics`, ~100 `drawer_events` | ~100 `event_rows`, and the next row's flags |
| calls | ~10 each in `request_txs`, `requests`, `session_request_rows`, `prompt_steps` (steps), plus the drawer rows: every earlier call's in their prompts (`stepCount`), up to 222 rows a call on the capture's longest prompt | ~10 `request_log_rows`, ~10 `drawer_requests` (and a template when a shape is new), and the previous step's and a retried call's rows |
| prompts, marks, segments, session row | the rows whose hash changed, after rendering everything (v7) | the prompt item and its neighbours, the new marks, the recut segments, the session row |
| cells | — | ~25 session cells (the run's minute cells, and ~10 to 25 hour cells) and ~10 request cells |
| a late event | every later `event_rows` row renumbered (`seq`) | one row, and its neighbour's flags |
| A commit | ~350, plus O(prompt) drawer rows | ~170 |

The write gate (Testing) asserts these per table, in rows and bytes per record,
instead of trusting this table.

**What a page reads** (the capture's heaviest, from §5.4; v7 read every row of
the entity in the range):

| Page | v7 rows read | v8 rows read |
|---|---|---|
| a session's header, prompts, requests, events (page 1) | the session's rows: up to 53k events | 1 to ~300: the page, its cells |
| a session's state, whole range | 53k events, 2.5k marks, 3.4k requests | ~3k cells |
| a session's timeline, bin 900 | 53k events, 2.5k marks | ~9k minute cells, ~2k rows |
| a person's summary, 14 d | 71k events and their marks | ~6k cells |
| a person's timeline, 24 h at 300 s | 24k events | ~6k minute cells |
| a person's requests, page 1 | the log's inputs for the range | the cells, and 20 rows per credential and kind |

**Storage** (§3.12 has every table): ~270 GB/day at the target in v7 (measured
bytes per row) → ~81 in v8.

| | v7 GB/day | v8 GB/day |
|---|---|---|
| an analytics event | 101 (`analytics`) + 53 (`event_rows`) + 47 (`drawer_events`) | ~46 (`event_rows`) |
| a call | 10 (`requests`) + 7 (`request_txs`) + 4 (`session_request_rows`) + 1 (`prompt_steps`) + 40 (`drawer_requests`) | ~8 (`request_log_rows`) + ~19 (`drawer_requests`, templates per key) |
| cells | — | ~5 |
| everything else | ~7 | ~3 |
| **All** | **~270** | **~81** |

- **Postgres at the target.** One primary takes ~4.5k inserted rows/s at peak
  and ~81 GB/day: ~1.2 TB at 15 days. An r6g.2xlarge with 2 TB of gp3 at 12k
  IOPS holds it.
  - Retention's deletes match the inserts, and so does the vacuum behind them.
    That's where day partitions start to pay (Not now, with the numbers).
  - Hour cells are updated in place while their hour is active: ~10 to 25
    rewritten per run, which the vacuum absorbs.
  - The next step for reads: a read replica for the API (Not now).
- **Connections:** 3 per worker, and one per API request in flight (a pool of 8
  per pod). That's ~140 at the maximum replica counts.
- **Advisory locks:** one per warm key, plus a commit's batch locks while it
  runs. Keys stay warm up to 60 minutes idle (§2), which holds more of them
  than v7's 60 s did. The byte cap bounds them, well under
  `max_locks_per_transaction` × `max_connections`.
- **CPU, measured.** On the capture's heaviest session (57,874 records,
  replayed one in/ file per run: 234 runs, ~250 records each), from
  `v8-worker`:
  - v7's whole render and diff: p50 966 ms a run. At the target's ~100 runs/s,
    that's ~40 cores at 0.4 s a run, and it grows with each session;
  - with `v8-worker`'s incremental drawer: p50 204 ms. Of that, ~120 ms is the
    O(session) work v8 removes: the scope's indexes and filing (~50 ms), the
    session tables' whole render (~27 ms), and hashing every row (~43 ms);
  - what stays scales with the run's records: ~84 ms for ~250 records, ~0.34
    ms a record (apply not included);
  - cold loads, measured: one load of that session replays its journal in
    2,581 ms (~45 µs a record). With a 5-minute warm window, the capture's real
    gaps cause 1,506 loads: 6.47M records replayed against 202k applied (32×),
    ~290 s of CPU against ~69 s for the incremental render. v8's 60-minute
    window and snapshots (§2) bound replay at ≤ 1× applied, and a snapshot is
    a decode.

  At the target's ~83M records a day (75M events, 7.5M calls), ~2.9k a second
  at peak, that's ~1 core for the render, plus apply, commits and decodes. The
  CPU gate measures the real number, cold loads amortized per record included,
  and holds it flat as a session grows (Testing). KEDA scales the workers on
  lag.
- **Warm memory.** A warm key holds its state, indexes, cells and memo, for up
  to 60 minutes idle. Each worker caps that total and evicts the least
  recently used key (writing its snapshot first). The bytes per key are
  measured with the warm-window PR; the capture's heaviest session's journal is 114 MB
  before compression (#57).
- **S3:** ~800 ops/s in all at peak. AWS S3's limits are far above that; DO
  Spaces' 800/s per bucket fits the demo.

**Expected latency** in the cluster, warm (the gate measures it on the
capture):

| Page | Rows read (the heaviest) | Expected |
|---|---|---|
| `/status`, the drawer, a session's header | 1 to a few | ~5 ms |
| a session's events, prompts, requests | a page, and a few hundred cells | ~10 ms |
| a session's state | ~3k cells | ~10 ms |
| a session's timeline | ~11k cells and rows at the finest bin the UI asks of it | ~30 ms |
| a person's header, sessions | the group's rows | ~10 ms |
| a person's summary (14 d) and timeline (24 h) | ~6k cells each | ~20 ms |
| a person's requests | the cells, 20 rows per credential and kind | ~15 ms |
| `/people`, `/overview` | the window's sessions: on the capture a few dozen | ~20 ms; at 1,500 people, ~6k and ~90k sessions for 24 h and 14 d, ~0.1 s and ~1 s |

**Cold latency:** measured on the capture only, with Postgres restarted and the
OS cache dropped once. The capture is 0.9 GB and fits in any cache, so it says
little about the target. A cold gate at the target is Not now (decision 6).

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

v7's tests stay, except two kinds. For the pages v8 serves, the table-level
comparisons of what the API loads are replaced by the ones below. And v7's
checks against the hashed baseline (a run "wrote only the rows whose hash
changed", "the baseline is the render of the replayed state") become the
incremental-equals-whole check and the write gate: v8 has no baseline. v8 adds
these, each written before the code it holds. The oracle stays today's code,
frozen: V1Store's reader (§5.7), given the same snapped ranges.

- **Incremental equals whole.** After every run of every layer test, the rows
  the incremental render wrote equal a whole render of the same state, table by
  table (as `v8-worker`'s `VerifyIncremental` holds the drawer). That's v7's
  harness: a fresh database per test, one pass written into a `ref` schema,
  compared with `EXCEPT ALL` both ways.
  - Every order of up to 5 records, one run per record; and generated
    sequences, shuffled, duplicated, late across hours, reloaded cold halfway.
  - **A test per dependency rule** (§3.11) changes one kind of input alone, and
    fails if a row that reads it isn't re-rendered.
  - **Named cases:**
    - a late event inside a tick run: only its own row and the next row's head
      flags are written;
    - a late input prompt: one prompt row is written;
    - a call ending: its row and its link's events are unchanged, but the read
      shows the new label;
    - a batch whose events span two sessions, its two keys committing in both
      orders, and while one of its calls is in flight in a `c:` key;
    - a token refreshed mid-prompt: the steps by scope;
    - a batch call inserted while its batch's parts change, in both orders:
      the holder's lock and re-check (§2);
    - two commits sharing two batches, locking in the global order: no
      deadlock;
    - a render flag for a key with no state writes no key row;
    - `dirty_inputs`: the reader re-reads only what it names;
    - forgetting a key flags its batches' other keys;
    - a commit's flags never name more than one batch's keys.
- **Render properties:** `event_rows`' key order is the render's event order;
  every row's head flags equal the oracle's `lag` over its view; a prompt item
  holds its three numbers as today's three rules give them.
- **Snapshots:** a decoded snapshot equals the warm state it was written from
  (the state, indexes, cells, drawer memo). A run after a cold load writes what
  the same run on the warm key writes.
- **The CPU gate.** The capture's heaviest session is replayed one in/ file per
  run, and again in 5 s windows, at the capture's real gaps between files. Each
  run's CPU is measured: apply, the running values (facts, parts, wires), the
  render, the commit's build, and any load.
  - CPU per record must stay flat, and per commit must be about equal at 1k and
    at 50k events: within 1.5×, for runs of the same size.
  - Cold loads count, amortized per record: records replayed ≤ 1× records
    applied, through the warm window's evictions, a worker restart, and
    scale-in (keys moving to another worker).
  - A whole-session pass anywhere in a run fails it.
  - Steps 2 to 8 hold the gate on their own tables' render path. The whole
    run's gate is held at steps 11 and 12.
- **The write gate: rows and bytes per record.** Counting rows whose hash
  changed can't catch a value that changes on every run, so the gate counts
  what's written.
  - The capture, and each CI dataset, is replayed in 5 s and in 30 s windows,
    with its files spread over 2 to 4 simulated pods.
  - For every table, the rows and the bytes written per record applied must
    stay under a fixed bound, and must not grow with the session's or the
    prompt's length.
  - The bounds come from the budget (§Scaling budget), and each of steps 2 to 6
    checks them for its tables. v7's drawer as built fails it (222 rows a
    call), and the fix must pass.
- **Cells, row for row.** On every dataset, every minute and hour cell equals
  the oracle's merge of its rows.
- **Invariants, as tests** on every dataset and at the gate (no runtime gauge):
  - I1 to I3 (§5.3);
  - a prompt id never repeats across sessions (a person's distinct prompts are
    the sum of its sessions');
  - `unattributed`'s inputs (`dim_session_visible`, `dim_session_account`) are
    written by the session's own key.
- **The read primitives against the oracle's tables.**
  - first-occurrence counts equal `count(DISTINCT)` for every generated range,
    spanning batches included;
  - the request log with each person's scope applied equals `stream_sql`'s rows
    for every person and range;
  - each session's tab (its credential filter) equals today's.
- **Snapping.** The router snaps every bound as §5.3 says: ends on a minute
  boundary, ±1 ns, ±1 µs and ±1 ms around one, inside a minute, both in one
  minute, open at either end, defaults included. The oracle and v8 get the same
  snapped bounds.
- **Response level, side by side.** Old is V1Store's reader (today's SQL), new
  is PgStore's, through the same router. For every endpoint in §5.2:
  - every URL of `endpoints.py`, which gains what the UI really sends: `kind` on
    a session's requests; the "other" chip's kinds with `skills`;
    `collapseTicks` always; `q` with a range;
  - generated ranges whose ends fall:
    - inside a tick run; between two events of one batch (in one session, and
      across two); between two calls of one prompt; inside a presence segment;
      inside a not-captured window;
    - before and after all data; open at either end;
  - every ladder bin with each of those ranges, and `bin` 90 (422);
  - `kind`-filtered logs, which read the kind indexes;
  - pages 1, 2 and past the end, and the prompt jump to the first, a middle and
    a missing prompt;
  - the directory for every `from` (presets, unaligned, none), with every
    `sort`, `agent` and `q` the parity URLs use.

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
    with more rows in the session outside the page.
- **No DuckDB, no ADBC:** the API runs with neither importable (a test). Only
  the oracle's tests import DuckDB.
- **Foreign keys** (§3.12) are declared in `schema.sql`, so every layer test
  runs under them.
- **CI time.** The new layer tests run on the per-test databases v7 uses; the
  side-by-side cuts add under a minute across the parallel jobs.
- **The real capture,** once, at the gate (step 12).

## Build order

v7's steps 1 to 10 are done: v7 is built and deployed. v8's steps follow. Each
is one PR, tests first: its failing tests land in the PR before the code.

**Rules for every step:**
- **A table or column is deleted in the PR that removes its last reader,** or
  in step 11 with the v7 loader.
- **No step stores a dense `seq`.** Until step 11, the v7 loader computes
  `seq` as `row_number()` over the v8 sort key (per kind for marks), as it
  already numbers a session's request rows (`pgstore.py:763`).
- **Steps 2 to 8 hold the CPU gate and the write gate on their own tables'
  render path.** The whole run's gates are held at steps 11 and 12.

**What runs in parallel.**
- Steps 1 and 2 start at once. Before step 2, the worker agent lands the
  60-minute warm window, the LRU byte cap and the "replayed ≤ 1× applied" gate
  on v7, as they are.
- Steps 3 to 7 each own their tables, and run at once once step 2 lands.
  Their shared edits (`schema.sql`, `render.Version`) merge mechanically.
- Step 8 is the worker agent's drawer, finished on 4, 5 and 6.
- Steps 9 and 10 are one PR per page, each starting when its inputs have
  landed.
- The worker agent's PR that drops `analytics` (no reader) has landed (#59).

| Step | What | Tests first | Gate | Needs |
|---|---|---|---|---|
| 1 | **The reader and snapping.** psycopg 3: the pool, one snapshot, the statements, the trace. The routers' page-reader seam (§5.7), with V1Store's reader today's SQL and PgStore's still v7's loader behind it. The router snaps every range (§5.3), for both readers. `/status` and the drawer on the new reader. The harness: snapped cuts, bins with cuts, replay checkpoints with `now` at each threshold, settling before each comparison | the snapping tests; every response through the seam equals the oracle's on the same snapped range | `make check`; the harness green on v7 | — |
| 2 | **The incremental render, and snapshots.** Deltas name the affected rows (§3.11); render and upsert exactly those. `Tables.Baseline` and the hashing of every row go. Running values (`FactsOf`, parts, wires) come from deltas, with `dirty_inputs`. Snapshot checkpoints encode v8's warm state: the state, its indexes, its minute cells, the drawer's memo. v7's baseline hashes and memo go away. The warm window, the LRU cap and the replay gate are already on v7 (the worker agent's PR); this step keeps them. It covers v7's tables whose keys are already stable (`session_tools`, `session_call_days`, the dimensions) | incremental equals whole; a test per rule; a decoded snapshot equals the warm state; a flag on a key with no state writes no key row | `make check`; its render path's CPU gate, cold loads included (≤ 1× replay) | — |
| 3 | **Events:** `event_rows`' key (batch, rank) and session index, its new columns (§3.3), head flags, first-occurrence marks, the straddlers' GiST index, `event_key`, `owner_key`. `seq` isn't stored: the v7 loader numbers events. `/events/{id}` reads `event_rows`, and `drawer_events` goes, its last reader | the render properties; the named event cases | `make check`; its render path's gates | 1, 2 |
| 4 | **Prompts, marks, segments, the session row:** prompt items with their numbers and `drawer_step_count`; marks by id with local recuts and `prompt_first`/`prompt_prev_ns`; presence by its ids; the session row's child rows, label inputs and `unattributed`. The v7 loader numbers them | as step 3, for these tables | as step 3 | 2 |
| 5 | **Calls:** `request_log_rows` for every call, with steps folded in, and `request_cells`. The worker's batch-call holder lookup reads `request_log_rows`. `session_request_rows`, `request_txs`, `request_reports`, `request_missed`, `prompt_index`, `codex_turns` and `session_last` stay until their last reader goes (step 11) | the log with each scope applied equals `stream_sql`'s; each session's tab equals today's; steps by scope after a token refresh; I1 to I3 as tests | as step 3 | 2 |
| 6 | **Cells:** `session_cells` and `agent_call_cells`, memoized from deltas (§3.8). `call_tool_uses`, `agent_calls` and `agent_call_hours` stay until step 11 | every cell equals the oracle's merge | as step 3 | 2 |
| 7 | **The cross-key rule:** batch locks in the global order right after the guard; flags in one sorted statement; the holder's lock and re-check; retention's flags in the same order | both commit orders of a batch call and its parts; no deadlock; forgetting a key flags its readers | as step 3 | 2 |
| 8 | **The drawer** (`v8-worker`'s incremental drawer, finished): its indexes and memo in the warm state and snapshot; step N and "delivered in step N" filled at read (`step` cells); `stepCount` from the prompt item (`drawer_step_counts` goes); templates per key; `raw_fallback`, so `/raw` stops reading `requests`, which goes with the holder lookup moved in step 5; the raw formatter split from DuckDB. The step count and next link, read at read time, count in-flight calls from the call rows as the oracle does, and the harness's in-flight-neighbour excuse (`side_by_side.py`, the temporary quirk) is deleted | `VerifyIncremental`; the drawer byte for byte (v7's `test_v7_drawer.py`); the replay checkpoints with no in-flight-neighbour excuse | as step 3 | 2, 4, 5, 6, 7 |
| 9 | **Session pages on v8's reads,** one PR each: `/sessions/{id}` (4), `/prompts` (4, 5; `prompt_steps` goes), `/events` (3, 4, 5, 6), `/requests` (5, 6), `/state` (3, 5, 6), `/timeline` (4, 6) | the page's side-by-side URLs and cuts, its checkpoints, its statements test | byte-identical on the CI datasets (`make check`: the subset; `-m full`: all) | 1, and the steps named |
| 10 | **Person pages on v8's reads,** one PR each: `/people/{id}` (4), `/sessions` (4), `/requests` (5, 6), `/summary` (3, 5, 6), `/timeline` (4, 6), `/coverage` (4, 5, 6). The oracle is patched to the one label rule (decision 2) first, so it compares | as step 9 | as step 9 | 1, and the steps named |
| 11 | **The directory, and DuckDB out:** `/people` and `/overview` on v8's reads (§5.6). Then the v7 loader goes, with every table and column only it read: `session_request_rows`, `request_txs`, `request_reports`, `request_missed`, `prompt_index`, `codex_turns`, `session_last`, `call_tool_uses`, `agent_calls`, `agent_call_hours`, `dim_session_visible`, `event_rows`' old columns. DuckDB and ADBC leave the API, and the oracle moves under `api/tests`. With ADBC goes the loader's own pool: until then a pod holds up to 8 + 8 connections (psycopg's and ADBC's, step 1). `SETUP.md`, `PRODUCTION.md` | the directory side by side for every `from`, `sort`, `agent` and `q`; the no-DuckDB test; the image builds without either | `make check`; `make up && make verify`; the whole run's CPU and write gates | 6, 8, 9, 10 |
| 12 | **The gate,** on the real capture, settled | — | every endpoint byte-identical on the snapped ranges (v7's 4,248 URLs, plus the new cuts and checkpoints); the CPU gate (flat per record and per commit, 1k vs 50k events, cold loads ≤ 1× replay, restarts and scale-in); the write gate (rows and bytes per record per table, 5 s and 30 s windows, 2 to 4 pods); warm p95 ≤ 50 ms and cold p95 ≤ 300 ms for every page of the heaviest person and session, on the capture; bytes per row against §3.12 | 11 |
| 13 | **Deploy** to the DigitalOcean cluster: Postgres starts empty (Rollout), `helm upgrade` (never uninstall) | — | pages load on the cluster's new traffic; `/lag` settles under 30 s; a request through the proxy shows up end to end | 12 |

v7 planned to delete the oracle after its step 10. It's still here, and v8's
tests need it. So it goes after v8's step 13 instead, with the rest of that
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

**Deleted in v8,** each in the step that removes its last reader (Build
order):
- **Postgres:**
  - `drawer_events` (step 3);
  - `requests` (step 8);
  - `prompt_steps` (step 9);
  - in step 11, with the v7 loader: `session_request_rows`, `request_txs`,
    `request_reports`, `request_missed`, `prompt_index`, `codex_turns`,
    `session_last`, `dim_session_visible`, `call_tool_uses`, `agent_calls`,
    `agent_call_hours`, and `event_rows`' old columns;
  - `analytics` (#59, landed);
  - `drawer_step_counts` (on `v8-worker`), which folds into the prompt item in
    step 8.

  No `seq` is stored from step 3 on.
- **Go:** the whole render, `Tables.Baseline` and the hashed baseline (§2);
  journal replay on load (snapshots); the render of every table above; the
  label assignment chain.
- **API** (§5.7): the v7 loader and every DuckDB and ADBC import;
  `sql/labels.sql`. Today's endpoint SQL, V1Store and BucketStore move under
  `api/tests/oracle`.
- **Never built:** `title_candidates`, the account lock and `drawer_parts`
  (earlier drafts of this spec).

## Deviations from the meeting and the accepted recommendations

1. **Discovery (recommendation 2: "ingest inserts the key into `dirty_keys`
   after each upload").** Ingest never talks to Postgres. A singleton tails
   ingest's recent index, now uploaded every 5 s, into `dirty_keys`. Ingest
   then can't block or fail on the database, and an outage can't lose a flag.
2. **Claiming (recommendation 2: `FOR UPDATE SKIP LOCKED` "or an advisory
   lock").**
   - An advisory lock, held while the key is warm, for affinity: a cold load
     replays the key's journal (v8: decodes its snapshot).
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

   *v8 supersedes this:* the endpoint code no longer runs in the API (§5); it's
   the oracle.
4. **"Workers write what's needed for joins and queries" (the meeting).** These
   are joined at read time, not written as v6 planned:
   - person-wide request values;
   - Codex labels;
   - person scope;
   - other keys' request rows.

   Each join is bounded and indexed, and there's no cross-key write path.

   *v8 supersedes this:* person-wide values are rendered on the call's row and
   applied by scope at read, other keys' request rows are filtered from
   `request_log_rows`, and labels are read by index (§4). Person scope stays a
   read-time resolution.
5. **"Write time does the data-only work" (the owner).** The person summary and
   timeline are still read-time folds over raw event rows. That's exact and
   fast at today's scale. At 1,500 people it hits the cold-cache limit above.
   *v8 fixes this:* they merge cells (§3.8).
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
   metadata isn't a body; `env_json` is dropped. *v8:* `requests` and
   `analytics` go (§3.12); an event's raw row is read from S3 by its pointer.
9. **Size (recommendation 4: "partition by day + retention").** No partitions
   yet. Retention forgets whole keys past the window with batched DELETEs.
   Partitions pay only at volumes we don't have (Not now, with the numbers).
10. **Retention (recommendation 5's "small retention job").** It's a worker
    singleton, not a new component, and it also applies the local raw cap.
11. **The DigitalOcean demo:** Postgres in the cluster, not DO Managed Postgres.

**v8, against the owner's brief** (2026-11-14, and the decisions since):

12. **"The timeline as finished rows."** A session's timeline merges its own
    cells per bin, as a person's does, rather than storing finished bins for
    each ladder width. That's one mechanism for both timelines, and any bin that
    is a multiple of 60, with no 8× write per row. It reads ~11k narrow rows at
    the finest bin the UI asks of the capture's heaviest session (§5.4).
13. **"State as a finished object."** `/state` takes any range (the brush), so
    it's a merge of cells. The default range is the same query.
14. **A session's own folds stay at read:** its presence, its permission and
    network segments, and its not-captured windows (§5.5). Rendering them would
    store a second copy of the segments and marks they fold, and a session holds
    a few of them.
15. **Delta in at every granularity, cold loads included** (decision 4). A
    load decodes a snapshot. Replay is bounded at ≤ 1× the records applied, and
    keys stay warm up to 60 minutes idle (§2).
16. **Who a person is stays a read-time resolution,** as in v7: a few index
    lookups per statement. Rendering it would rewrite every session of a group
    whenever an account links.
17. **Labels and children are read, not rendered** (decided, §3.10): index
    lookups, accepted like an event's link. That needs no cross-key flags or
    locks for them.

## Decisions and quirks

- **Delta in, eventual state out.**
  - Stored inputs are the state's deltas.
  - v7: rendered rows are recomputed whole and written where their hash
    changed. v8: only the rows the run's deltas affect are rendered and
    written (§3.11). An O(session) render per run is no longer accepted
    (decision 4).
  - Everything is an upsert or a delete by primary key, so rewriting is
    harmless.
- **One owner per row.** The guarded `n` orders each key's commits.
- **v7's accepted costs, gone in v8:** the O(session) render per run, and a late
  row renumbering the rows after it (v8 keys rows by stable ids, §3.3).
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
  - v7: the render's cost per commit was O(session): on the test datasets'
    largest session (21k events, 1.7k calls) ~0.3 s for its drawer rows, ~0.1
    s for its 9 tables. v8 renders only what a commit's records affect
    (§3.11).
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
   over the same component, at read time. *v8: gone:* one label, the thread's
   own title calls' (decision 2).
3. **Person and session request numbering differ:** the person log is today's
   `stream_sql` over the person's scope; the session tab is the render's. *v8:*
   one call row, with each log's scope applied at read (§3.9).
4. **No final tiebreaker in the person requests sort:** `stream_sql`'s output
   order, loaded with one thread, as today.
5. **Session pages derived the whole account group:** not reproduced as a cost.
   Only the labels' component is read, and only for person pages. *v8:* no
   component at all: a label reads its own accounts' title calls.
6. **`/people/{id}` facts are the URL account's:** today's code over the
   group's rows.
7. **The drawer's `lastDataAt` is global:** `/status`'s statement, in the
   drawer's snapshot.
8. **`org_kind` reads the environment at request time:** unchanged.

**v8's decisions and quirks:**
- **Ranges snap to whole minutes** (decision 1): `from` to its minute's start,
  `to` to its minute's last ns, defaults included. A response's meta shows the
  snapped bounds (`…:00Z`, `…:59.999999Z`), and its counts are those minutes'.
  The session header and timeline don't filter by a range and don't snap.
- **`bin` must be a multiple of 60** (decision 1). Any other width is a 422.
  Today any integer ≥ 60 is accepted; the UI sends only the ladder. With no
  `bin`, the auto bin comes from the span asked for, before snapping, so it's
  today's (owner).
- **Temporary, until step 8: the in-flight drawer leaves its neighbours'
  links and counts out.** An ended call whose next step in its prompt is still
  in flight has no `nextRequestId` (nor "delivered in step N") for it, and its
  `stepCount` doesn't count it; the oracle links and counts it. It heals when
  the call ends. v7's drawer has no row for a call in flight (owner) and
  renders its neighbours without it. The side-by-side harness excuses exactly
  this (`api/tests/side_by_side.py`, `_in_flight`). In v8 the step count and
  the next link are read at read time, so step 8 counts in-flight calls from
  the call rows, as the oracle does, and deletes that excuse.
- **A Codex thread's label is its own title calls'** (decision 2), on the
  person pages too: today they ran an assignment across the account's threads.
  Where the two differed, the person pages now show what the session page
  always showed.
- **Float sums are merged per cell:** `cost_usd` and `hook_ms` are summed in
  each cell, then across cells. Today's single sum runs in an order DuckDB
  picks. The last bits can differ; they're shown rounded (cents, 0.1 ms), and
  the tests compare responses byte for byte. A sum within ~1e-12 of a rounding
  boundary could show one cent differently.
- **One row per call serves both logs, the Prompts tab and event links**
  (§3.9). Both logs must stay byte-identical to today's on every dataset and the
  capture (steps 5, 12). By construction they'd differ from today's person log
  only if I1 or I2 broke (§5.3): the row would show its own session's value.
- **A request id held by two keys at once** (a call whose ending another key
  held a moment ago, v7's drawer quirk) shows twice in the logs, as today, and
  sorts by key after (`ts_us`, `request_id`).
- **Byte-identical once settled.** A cross-key value (a batch's count, a label,
  a child) is right once its render flag has run. Until then a page can show
  the value as it was, for about a run.
- **The Events tab's later pages** read the index entries of the unit starts
  before them (`OFFSET`): no index serves page N directly (Not now). Page 1
  reads its own rows.
- **The person summary's batch count** relies on a batch's spanning parts
  carrying their event times. A batch spanning sessions is rare (8 of ~9.4k
  batches on the capture), so the times cost nothing measurable.
- **PR numbers sort by value when their key is ASCII digits.** Today's summary
  takes Python's `isdigit()`, which also accepts other scripts' digits. No such
  key has been seen, and a test holds the two equal on every dataset.
- **A label and children are read at request time** (§3.10), so a title call or
  a child edge shows as soon as it commits, with no render flag.

**v7's quirks, in v8:**
- quirk 2 (Codex titles over the account group): gone. One label, the thread's
  own title calls', read by index (decision 2, §3.10).
- quirk 3 (person and session request numbering differ): reproduced from one
  row. Each log applies its own scope at read (§5.3): the person's sessions and
  credentials, or the one session. Each matches today's (step 5's test).
- quirk 4 (no final tiebreaker in the person requests sort): request ids are
  unique per row except for the case above, so the order is (`ts_us`,
  `request_id`, key).
- quirk 5 (session pages derived the whole account group): nothing is read at
  request time for labels at all.
- Codex step numbers (#44): a step counts its prompt's calls in its own
  session's key (I3), for both logs. Today's person log counts calls that share
  a credential and a turn id across threads; real turn ids are UUIDs, and no
  dataset row differs.

## Not now: add when real traffic needs it

- **A cold-latency gate at the target** (decision 6): on the target-shaped
  database the load-test harness will build.
- **Per-(session, day) cells for the directory,** summed per person, if the
  owner doesn't accept ~1 s for the 14-day directory at 1,500 people (open,
  §5.6).
- **Day partitioning** of the big tables (`event_rows`, `drawer_requests`,
  `request_log_rows`, `session_cells`). Add it when retention's DELETE and
  vacuum cost too much, or when a table's indexes outgrow RAM.
  - At 1,500 people that's ~125M rows and ~81 GB a day deleted again after 15
    days, plus the vacuum behind them.
  - At today's scale it's well under 1M rows a day.
- **An event's raw position in place of its `event_key`** (79 bytes): ~6 GB/day
  less, once the worker reads batch bodies to find positions.
- **The drawer's verbatim payload values in S3** (the `fields` section's
  `complete` row and metadata, the content previews: ~60 MB of JSON on the
  capture). That would mean deriving at read, so not without the owner.
- **A 15-minute or day cell level,** if the timelines' minute-cell reads show
  up: the capture's heaviest session reads ~9k minute cells at bin 900.
- **An index over the Events tab's unit starts,** if deep pages show up.
  Cursor paging would be a contract change.
- **A trigram index for `q`,** if searching long ranges shows up.
- **A read replica for the API, or RDS Proxy.**
- **A periodic full listing of `v1/keys/`,** to flag keys a lost node never
  indexed.
- **The in-flight drawer** (a `pending` table), if the owner wants it back.
- **Schema migrations.** Until then, each schema change is a fresh start.
- **A size cap,** not just an age, for local raw data.
- **Hot keys:** a shared API key used by thousands of people, or many people
  behind one NAT IP.
- **Changing response contracts.**
