# Data pipeline (v5)

Status: **proposed, for the owner's sign-off.** This replaces every earlier pipeline
design. `docs/STREAMING_PIPELINE.md` is kept only as history.

## Goals

- **Target 1,500 people.** Design for that and nothing beyond it, and keep the
  mechanisms to a minimum. Complexity gets added only when real traffic needs
  it.
- **Response bodies don't change.** Every endpoint returns byte-identical JSON,
  and the URLs stay the same, so `openapi.json`, the UI and its samples don't
  change either.
- **Every tier scales horizontally:** ingest on CPU, rollups on lag, the API
  on CPU.
- **Correct under any timing.** Retries, crashes, late data, a lock that runs
  out while its holder is still working, or Redis losing its data must never
  lose a record or count one twice.
- **Infrastructure:** data lives in S3. Redis runs inside the cluster as one
  StatefulSet with a small volume. It's used for coordination only, and
  nothing that correctness depends on lives there.
- **Cost:** S3 request charges grow with active users and are accepted. That's
  about $45k/month at 21,000 people, and much less at 1,500.

## Keys

Every record already carries a **routing key** (`records.RoutingKey`, built in
#18):

| Record | Key |
|---|---|
| has a session id (Claude Code, Codex, analytics events) | `s:<session id>` |
| an analytics request's own call | the session of the batch's earliest event |
| no session, but a credential (catalog, flags, usage polls) | `c:<credential hash>:<day>` |
| no session, no credential | `ip:<source ip>:<hour>` |
| account sighting (name, email, org) | `a:<account id>` |

A session is never split across keys.

## S3 layout

```
raw/dt=…/hour=…/instance=…/…ndjson.gz         raw capture (unchanged)
calls/<call id>.ndjson.gz                      per-call detail (unchanged)
v1/keys/<key>/in/<s3-time>-<incarnation>-<seq>.parquet   ingest output, one file per key per flush
v1/keys/<key>/out/<n>-<nonce>.parquet         a rollup run's output (rows for every table, by table)
v1/keys/<key>/state/<n>-<nonce>               the run's state checkpoint
v1/keys/<key>/commit/<n, 12 digits>.json      create-only; names the current out/state files + the input cursor
v1/recent/<s3 minute>/<incarnation>-<seq>.json   per pod per minute: which keys it flushed
v1/merge/out/<n>-<nonce>/…parquet             org-level rows (people index, per-session rows by day)
v1/merge/commit/<n, 12 digits>.json           create-only
```

- `<key>` is URL-escaped.
- `<s3-time>` is S3's clock, to the second, taken at upload. The writer keeps
  the offset between its own clock and the `Date` header of the last S3
  response.
- `<incarnation>` is the pod name, the process start time and random bytes
  (`records.NewIncarnation`), so a restarted container never reuses a name.

## 1. Ingest: the Envoy + observer pod, scaled on CPU

1. **Records.** The observer builds records exactly as today (#18), and computes
   each record's routing key.
2. **Buffers.** There's one buffer per key.
   - A buffer flushes when it reaches **`OBSERVER_KEY_FLUSH_BYTES`** (default
     1 MiB) or when its timer, **`OBSERVER_KEY_FLUSH_SECONDS`** (default 30 s,
     max 60 s), runs out. The timer starts at the buffer's first record and has
     ±10% jitter.
   - A global memory cap (`OBSERVER_KEY_BUFFER_MAX_BYTES`) applies backpressure:
     over the cap, the oldest buffers flush first.
3. **Spool.** Every buffer is also a file in the disk spool, fsynced about once a
   second.
   - A flush seals the file, uploads it as
     `v1/keys/<key>/in/<s3-time>-<incarnation>-<seq>.parquet`, then deletes it.
   - Uploads retry until they succeed, and several run in parallel.
   - On restart, every leftover spool file is uploaded.
4. **After each upload:**
   - `SADD dirty <key>`. This is fire-and-forget: if Redis is down, ingest carries
     on.
   - The pod also adds the key to its per-minute recent index. Once a minute it
     uploads `v1/recent/<s3-minute>/<incarnation>-<seq>.json`.
5. **Shutdown (SIGTERM).** Flush every buffer before Envoy's drain ends, and
   upload the recent index. Anything that couldn't be uploaded is counted,
   triggers an alarm, and stays in the spool for the next start.
6. **Unchanged:** the raw ndjson log, the `calls/` detail objects, failing open in
   observe mode and closed in enforce mode.

## 2. Rollups: the `rollup` Deployment, scaled on lag, at least 2 replicas

Each replica runs these loops.

**The key loop.**
1. Take a key from `dirty` (`SPOP`). Lock it with `SET lock:<key> <replica>
   NX PX 60000`, renewed every 20 s. If the key is already locked, put it back
   in `dirty` and move on.
2. `SREM dirty <key>`. Removing it *before* listing means a new file that
   arrives during the run re-flags the key.
3. Read the key's newest commit, then load its state from the checkpoint the
   commit names. The state stays in an in-memory LRU while this replica holds
   the lock.
4. LIST `in/` starting after (last applied name − 5 min grace). Skip any name
   the commit recorded inside the grace window.
5. Apply the new records with the per-key state engine (#19), which already
   handles ordering, duplicates and late data.
6. Write one `out/<n+1>-<nonce>.parquet` holding the rows that changed in every
   table, plus `state/<n+1>-<nonce>`.
7. Create `commit/<n+1>.json` **only if it doesn't exist yet**. It names the full
   set of current `out` and `state` files, the last applied input name and the
   names inside the grace window.
   - If the create fails, another worker got there first. Drop this run and its
     in-memory state. That's the fencing, and it covers expired locks, GC pauses
     and Redis restarts.
8. `SADD committed <key>`, which the merge reads.
9. Once a key has had no new input for 60 s, release its lock. Release all locks
   on SIGTERM.

**Compaction.** When a key's commit lists more than 32 `out` files, merge them
in tiers (the tiered compaction from #25). A file is deleted only when no
commit in the last 10 minutes names it.

**The sweep.** Every replica runs it every 10 minutes, with no lock. It reads
`v1/recent/` for the last 15 minutes and runs `SADD dirty` for every key listed.
That covers lost SADDs, Redis restarts, and pods that crashed between upload
and SADD. A second replica sweeping too does no harm.

**Scaling.** Each replica exports `oldest_unapplied_seconds` on `/lag`. KEDA
scales on it with the `metrics-api` trigger, and there are always at least 2
replicas.

## 3. Merge: org-level rows, every 30 s

- **Who runs it:** any rollup replica that holds `lock:merge`.
- **What it does:**
  1. Read the newest merge commit.
  2. Take everything in `committed` (`SPOP` in batches).
  3. For each of those keys, read its newest commit's session summary rows and
     dimension rows.
  4. Rewrite only the partitions that changed: today's and yesterday's
     per-session rows, and the people index.
  5. Create `merge/commit/<n+1>.json` only if it doesn't exist yet.
- **Fencing:** a concurrent merge loses the create and drops its run.
- **If Redis loses `committed`:** the merge rebuilds from every key with a
  commit in the last 15 minutes, taken from `v1/recent/`.

## 4. API: stateless, scaled on CPU

The API runs one DuckDB query over S3 (`httpfs`), using the files a commit names.

| Endpoint | How it reads |
|---|---|
| `/sessions/{id}`, `/timeline`, `/state`, `/prompts`, `/requests`, `/events` | key `s:<id>`: that key's newest commit, then its `out` files |
| `/requests/{id}`, `/raw`, `/details`, `/events/{id}` | the call's `calls/<id>` object, already needed for raw headers and bodies, gives its key; then that key's rows. The raw view reads S3 directly. |
| `/people/{id}`, `/summary`, `/sessions`, `/timeline`, `/requests`, `/coverage` | the people index in the merge gives the person's session keys and `c:` keys for the window; then each key's rows, or the merge's per-session rows for summaries. The same SQL as today, over these rows. |
| `/people`, `/overview` | the merge's per-session rows and people index |
| `/status` | the merge, plus the newest ingest time each pod writes to Redis |

- Values relative to now (live, in flight, activity) are computed at query time
  from stored timestamps, as today.
- The API never copies data to local disk. The mirror sidecar is deleted.

## Rollout

None. This is used internally and by one or two people outside the team. On deploy, today's processed data is
discarded and the new pipeline starts fresh from new traffic. The old `process` Deployment,
the mirror sidecar and the old processed tables are deleted in the same change. No backfill, no
side-by-side run, no migration.

## Testing: small tests, written first

- **Unit tests**, in the user's layers:
  - **Ingest buffers:** flush by size and by timer, the memory cap, the spool's
    crash and recovery, S3-clock naming, unique incarnations, SIGTERM flush.
  - **The rollup loop:**
    - a crash at each step;
    - two holders racing (the commit create);
    - SREM ordering;
    - the grace window;
    - the sweep;
    - Redis being wiped.
  - **Merge:** fencing, incremental partitions.
- **A chaos simulation on small synthetic data, in `make check`, in seconds:**
  - pods crash, restart and get scaled in;
  - locks expire mid-run, and Redis is wiped;
  - S3 calls fail;
  - clocks skew.

  The final tables must equal a single pass over the same data.
- **Endpoint parity** on the API's existing small test datasets: byte-identical,
  every endpoint.
- **The real two-day capture runs once,** as the final gate. It is never part of
  the development loop.

## Build order

| Step | What | Gate |
|---|---|---|
| 1 | Ingest buffers, spool, naming, recent index, Redis SADD | Unit tests, plus no loss or duplicates in the chaos simulation |
| 2 | Rollup loop, commit, compaction, sweep, lag | Unit tests, plus the chaos simulation equal to a single pass |
| 3 | Merge | Unit tests, plus the people index and per-session rows equal to the reference |
| 4 | API over v1, S3 raw reader | Endpoint parity on the test datasets |
| 5 | Helm: rollup Deployment, Redis StatefulSet, KEDA; delete `process` and the mirror | Chart tests |
| 6 | Final gate on the real capture | Every endpoint byte-identical |

## Not now: add when real traffic needs it

- Fewer, larger S3 files to cut cost.
- Hot keys: a shared API key used by thousands of people, or many people behind
  one NAT IP.
- Redis high availability. Losing Redis costs freshness, never correctness.
- Changing response contracts.
