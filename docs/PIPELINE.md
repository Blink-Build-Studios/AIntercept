# Data pipeline (v6)

Status: **proposed, for the owner's sign-off.** §1 (ingest) and §2 (rollups) are
v5, built and deployed. v6 keeps them, adds a step after §2's commit (§3.2) and
drops the key's `out/` tables, and replaces v5's merge partitions and read path:
the rollup writes **views**, the rows each page reads at fixed paths computed
from the page's id, and the API only reads them.
`docs/STREAMING_PIPELINE.md` is kept only as history.

## Goals

- **Target 1,500 people; build for the week in front of us.** Keep the
  mechanisms to a minimum. Complexity gets added only when real traffic needs
  it.
- **The rollup precomputes, the API reads.** Write time does everything that
  depends only on the data. The API does only what depends on the request: the
  range, filters, sort, page, bin, and values relative to now. It runs today's
  endpoint code over the rows it reads, and never derives a table, except the
  directory's request-time aggregation (the owner's call, §5).
- **Fixed reads.** An API call GETs objects at paths computed from its URL (and
  the time): at most 2 sequential rounds of parallel GETs, no LIST, no commit or
  manifest file. The objects read are set by the range asked for. A cold page
  loads in under ~300 ms.
- **Recompute, don't delta.** Every view object is recomputed from absolute
  inputs and overwritten whole, so writing it twice is harmless. A commit costs
  the rows it changed plus the chunk they sit in (an hour of a session, a day of
  a session or a person), never a whole session or a person's history.
- **One writer per object.** Each object has one owning entity, and only a run
  holding that entity's lock writes it.
- **Response bodies don't change.** Every endpoint returns byte-identical JSON,
  and the URLs stay the same. The exceptions are in "Decisions and quirks"; the
  only one the UI sees is the in-flight drawer (the owner's call).
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

A **person** is an email group (today's `person_map`). Its stable id `<gid>` is
`e-` and the hash of the lowercased email, or `a-<account id>` for an account
with no email, or `unattributed` for the sessions no account appears on. The
API's person id stays the group's leader account, as today.

## S3 layout

```
raw/dt=…/hour=…/instance=…/…ndjson.gz         raw capture (unchanged)
calls/<call id>.ndjson.gz                      per-call detail (unchanged): the drawer's raw view
v1/keys/<key>/in/<s3-time>-<incarnation>-<seq>.parquet   ingest output, one file per key per flush
v1/keys/<key>/state/<n>-<nonce>               the run's state checkpoint
v1/keys/<key>/commit/<n, 12 digits>.json      create-only; names the state files and the input cursor, and
                                               carries what the merge reads (§4)
v1/recent/<s3 minute>/<incarnation>-<seq>.json   per pod per minute: the keys it flushed, or a rollup
                                               replica's: the keys it committed
v1/merge/index/<n>-<nonce>.parquet            the merge's state (§4); nothing else reads it
v1/merge/commit/<n, 12 digits>.json           create-only; names the index; its newest_ns is the data clock
v1/content_retained                            empty; ingest logged a row with a body (the retention mode /status shows)

                                               written by (holding that entity's lock)
v2/s/<sid>/head.parquet                        session key run: the session's whole-session rows
v2/s/<sid>/h/<YYYYMMDDTHH>.parquet             session key run: its rows with event time in that hour
v2/s/<sid>/pd/<YYYY-MM-DD>.parquet             session key run: its part of a person's day
v2/s/<sid>/p/<prompt id>.parquet               session key run: a prompt's rows the drawer reads
v2/s/<sid>/kids/<child sid>.json               the child's key run: its edge
v2/s/<sid>/x.parquet                           merge: a Codex session's own title label
v2/r/<request id>.parquet                      the call's home key run: the drawer's rows of the call
v2/r/<request id>/<key>.parquet                that key's run: its events of an analytics batch homed elsewhere
v2/c/<hash>/<YYYY-MM-DD>.parquet               c: key run: the credential's request rows that day
v2/kd/<key>.parquet                            the key's run: its rows for the directory
v2/pa/<account id>.parquet                     merge: the account's group (a copy per linked account)
v2/g/<gid>/group.parquet                       merge: the group (the same content)
v2/g/<gid>/head.parquet                        person run: the person's whole-history rows
v2/g/<gid>/d/<YYYY-MM-DD>.parquet              person run: the person's day
v2/g/<gid>/rq/<YYYY-MM-DD>.parquet             person run: the person's request log for a day
v2/org/status.json                             merge: /status, and the directory's first day
v2/org/days/<YYYY-MM-DD>.parquet               merge: the directory rows of every key last active that day
v2/org/accounts.parquet                        merge: every account's own row (v5's index partition)
```

- `<key>`, `<sid>`, `<prompt id>`, `<request id>` and `<account id>` are
  URL-escaped. Hours and days are UTC.
- `<s3-time>` is S3's clock, to the second, taken at upload. The writer keeps
  the offset between its own clock and the `Date` header of the last S3
  response.
- `<incarnation>` is the pod name, the process start time and random bytes
  (`records.NewIncarnation`), so a restarted container never reuses a name.
- Gone: `calls/<id>.keys/`, `v1/reported/`, the merge's day and index
  partitions, and each key's `out/` tables (nothing reads them any more; §2
  step 6 writes only `state/`, build step 11).

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
6. Write the run's output directory, `out/<n+1>-<nonce>/`, with one
   `<table>.parquet` per table that changed, plus `state/<n+1>-<nonce>`.
7. Create `commit/<n+1>.json` **only if it doesn't exist yet**. It names the full
   set of current `out` and `state` files, the last applied input name and the
   names inside the grace window.
   - If the create fails, another worker got there first. Drop this run and its
     in-memory state. That's the fencing, and it covers expired locks, GC pauses
     and Redis restarts.
8. `SADD committed <key>`, which the merge reads.
9. Once a key has had no new input for 60 s, release its lock. Release all locks
   on SIGTERM.

**Calls that never end.** A call whose ending never comes is written without one,
as the old processor did, once data time passes its start + 6 h (a WebSocket
connection's: 7 d). Data time is global: the newest merge commit's `newest_ns`,
which a run reads (listing only the merge commits after the last it saw). The
written row is an ending built from the call's start record
(`canon.RequestFromPending`), applied like any record, so it's in the checkpoint.
An ending that arrives later replaces it, as if it had come first: a call has one
row, and the tables stay order free. A run with no new input still commits when
a call is due. Each commit records `expires_ns`, when its first waiting call
expires (0: none).

**Compaction.** Every run folds the newest 8 output directories (and state
chunks) of one tier into one of the next tier, so each row is rewritten
O(log N) times and a commit names O(8 · log N) of each. The folded files, and
the previous commit, go on the commit's garbage list and are deleted 10 minutes
later. A losing run's files are deleted at the next fold. The exact format is
`aintercept/internal/rollup/layout.go`.

**Reclaiming a dead holder's keys.** A replica adds a key to the Redis `held` set when it takes the
key's lock, and removes it before releasing the lock. Every tick, each replica re-flags any key in
`held` whose lock has expired: a holder that was killed mid-run, after taking the key off `dirty`,
would otherwise strand the key until new input arrived.

**The sweep.** Every replica runs it every 10 minutes, with no lock. It reads
`v1/recent/` for the last 15 minutes and runs `SADD dirty` for every key listed.
That covers lost SADDs, Redis restarts, and pods that crashed between upload
and SADD. A second replica sweeping too does no harm.

**Scaling.** Each replica exports `oldest_unapplied_seconds` on `/lag`. KEDA
scales on it with the `metrics-api` trigger, and there are always at least 2
replicas.

## 3. Views: what the rollup writes for the API

### 3.1 Objects and fencing

**Format.** A view object is one Parquet file (zstd) with one row. Each table is
one column: a list of structs in the table's Go row type (`process/tables.go`,
today's columns and types), its rows in today's table order. The API GETs an
object whole and unnests each column into the table today's SQL reads: one GET
per object, no footer reads, no row groups.

**Order keys, not dense numbers.** A chunked table stores, in its `seq` column,
an int64 that sorts as today's order: (hour, rank within the hour). A late row
renumbers only its own hour, so it changes no other hour's hash. No response
carries `seq` (no model has the field;
the SQL only sorts, joins `prompt_steps` to `prompt_rows` and numbers rows by
it). Values a chunk would copy from the whole session (`session_last_ns` on
prompt rows) are filled at read time from the head.

| Object | Tables (today's names and columns) |
|---|---|
| `s/head` | `session_rows` (one row, its own label), `presence_segments`, `session_tools`; the hours that hold rows; the commit `n` it reflects; each `pd/` day's hash; credentials and their days |
| `s/h` | `event_rows`, `session_marks`, `prompt_rows`, `prompt_steps` (with their prompt's hour), `session_request_rows` (the session's own), `call_tool_uses` |
| `s/pd` | the session's rows of that day for its person: `event_rows` narrowed to the columns the person summary and timeline read (`batch` for `event_id`); `session_marks` of kind call, reported, missed and friction; typed `prompt_rows`; `call_tool_uses`; its request rows before the person-wide values, with `prompt_index`, `codex_turns` (ranked), `request_reports` and `request_missed`; the carry: its newest `tengu_tool_search_mode_decision` and `tengu_skills_sync_success` before the day. A session has a part for every day from its first to its last active day; an idle day's holds only the carry |
| `s/p` | the prompt's `requests`, `pending` and `analytics` rows the drawer reads (§3.3) |
| `s/x` | `label_text_alone`, `label_source_alone` |
| `r` | the call's own rows and its picks (§3.3), its session and prompt ids, the batch's other sessions |
| `c` | the request rows of the key's transactions, before the person-wide values |
| `pa`, `g/group` | the group: member accounts and their `person_map`, `accounts`, `account_facts`, `account_devices` rows; the leader; credentials; each member session's start, last, active days, accounts and Codex labels; the other sessions that used its credentials, with their active days |
| `g/head` | `session_rows` and `presence_segments` of every session of the person (labels from the group); `person_sessions` and the identity tables; `person_last`; the `ru.rollups` row (`live` aside); the `dr.linked_accounts` rows; the `pd/` hash it folded per session and day |
| `g/d` | the member sessions' `pd` parts of that day (their request rows aside), concatenated; `request_rows` narrowed to the day's account calls (the summary's count) |
| `g/rq` | `request_rows` of that day: the request rows of the parts of members and of sessions that used the group's credentials, and of the `c:` views, with the person-wide values |
| `kd` | the key's rows the directory reads: its finished `session_rows` row (labels aside), its rows of the 9 dimension tables, its `session_marks` of kind call and missed (narrowed to the columns the directory reads), its Claude Code and Codex call times (v5's `merge_agent_calls`) |
| `org/days` | the `kd` rows of every key whose `DayOf` is that day, with the merge's labels: v5's day partition, cut to what `/people` and `/overview` read. A day with no object had no activity |
| `org/accounts` | every account's `dim_account` row (v5's index partition) |

A head names data at fixed paths (which hours hold rows), never a version of a
file. Chunks are written before the head, so a head never names a chunk that
isn't there.

**Fencing.**
- A run captures the ETag of every object it'll write: from its own last write
  while it's warm, else a `HEAD` (absent: it'll write with `If-None-Match: *`).
  It writes with `If-Match: <ETag>` (unquoted). Any 412: drop the run and its
  cached state, and flag the entity dirty again.
- **The capture comes before a newer holder can exist.** The session run and the
  merge capture before their commit create, and the create orders runs: a newer
  one commits only after the older one's commit exists. The person run has no
  commit, so after capturing it reads its lock back (one Redis GET) and drops if
  the lock isn't its own or the read fails.
- **So the stale run loses.** The newer holder captures after the older one did.
  If the older run writes second, it gets 412. If it writes first, the newer run
  either captured after that write and overwrites it, or captured before it,
  gets 412, and its retry overwrites it.
- No version metadata, no manifests, no leases on objects.

### 3.2 Session views: the key loop, around step 7

1. **Render.** After applying the input, render every object of the session in
   memory from the state: every hour's rows, every day's `pd/` part, every
   prompt's `p/`, every call's `r/`, its `kd` part, the head. The functions are today's Go
   mirrors of the SQL, moved from `state/*_test.go` into package code
   (`eventRowsOf`, `promptViewOf`, `sessionRowsOf` without titles,
   `rqView.sessionRows`, `callToolUsesOf`, `presenceRows`, `sessionToolsOf`).
   They're whole-session functions, so every value that crosses hours (link
   labels, prompt numbers and Codex turn ranks, permission and network `to_ns`,
   `open_tail`, `events_pending_if_live`, a call's chain neighbours and sandbox)
   is right with no dependency tracking. CPU is O(session) per run (accepted).
2. **Diff.** Hash each object and compare with the last render's hashes, kept
   in memory with the state. Only objects whose hash changed are written, so
   writes stay O(changed). The head records only its commit `n` and each `pd/`
   day's hash (the person loop reads those).
3. **Capture** the ETags of those objects and the head.
4. **Commit** (step 7). A lost create drops the run, as today.
5. **Write** the changed objects, then the head (with its commit `n` and the
   `pd/` hashes). Then `SADD committed <key>` (step 8, after the views) and add the
   key to the replica's recent index, so recovery sees commits ingest didn't
   cause.

- **Children.** A child writes `s/<parent>/kids/<child>.json` (kind,
  `child_first_ns`) with its other views: after its commit, so it's fenced, and
  before `SADD committed`, so the merge only flags the parent once it's there.
  The parent's run LISTs `kids/` (on the write path) for `children_json`.
- A run flagged with no new input (a new child, a call due to expire) commits
  `n+1` with no records and writes what changed.
- **Cold load.** A run that loads a key cold reads its head and renders once.
  If the head's `n` equals the commit's, every object matches that render (the
  head is written last), so its hashes are the baseline. If the head is older (a
  crash between commit and views), the baseline is empty: the run writes every
  object of every kind and creates its own commit. This happens before §2's
  no-input early return (`rollup.go:230`). A warm run whose write fails drops
  its cached state, so its next run loads cold.
- **The commit carries, absolute, what the merge reads:** every account and
  credential the key has had, its dimension rows, its start, last and active
  days, its last-active day (`DayOf`: the newest `last_ns`/`ts_ns` over all its
  tables, `merge.go:492`), its parent edge, its Codex title inputs (product, own title,
  model calls, call accounts; a title thread's title calls), its first-request,
  earliest and content-retained values, `newest_row_ns`, `expires_ns`.

**Cost:** CPU O(session) per run; writes the changed objects only. **State:**
v5's, plus a hash per object in memory.

### 3.3 Request objects: the drawer

- **Home key.** A call's object is written by the key holding its ending under
  the ending's own routing key. (Ingest also copies an ending to its start's key
  when they differ, `records.Assembler.endKeys`; that copy's holder writes
  nothing.) A reported call (`reported:<id>`): the key holding the report. `c:`
  keys write their calls' objects too.
- **When.** When its finished row appears or its rows change. An in-flight call
  has none, and the drawer 404s until it ends (owner).
- **`r/<id>`:** the call's own `requests`, `pending` and `analytics` rows; its
  session and prompt ids; the calls its message ids chain to (prev and next);
  the stall summary naming its request id; the session's last
  `tengu_bash_tool_command_executed` at or before t + 1 s with a non-empty
  `sandbox_enabled` (sandbox); an analytics batch's events in the home session.
  Every `r/` of the session is re-rendered each run (§3.2), so a new call in the
  chain or a late Bash event rewrites the calls it changes, and only those.
- **`s/<sid>/p/<prompt>`:** the prompt's calls and its events of the names the
  drawer reads (api query, success, retry, tool use, input prompt). Its calls
  share it, so a new row changes one prompt object, never one object per call
  of the prompt.
- **A batch spanning sessions.** Ingest stamps on the batch call its event count,
  its first event's name and its other sessions; each of those writes
  `r/<batch>/<key>` with its events of the batch.
- The drawer reads `r/<id>`, then the prompt object and any parts, and
  `request_detail.py` runs unchanged over their rows.

### 3.4 `c:`, `ip:` and `a:` keys

- A `c:` key writes `c/<hash>/<day>`, its transactions' request rows, and its
  calls' request objects.
- Every key writes its `kd` part. `ip:` and `a:` keys write no other view:
  their probe times and dimension rows also reach the merge in the commit.

### 3.5 Person views: the person loop

Every replica runs it next to the key loop, and the oldest waiting person counts
in `/lag`.

1. **Take.** `SPOP pdirty` gives a `<gid>`; lock `lock:p:<gid>` as keys are
   locked (renewed; `held` reclaims a dead holder's and flags it `all`). `SPOP`
   every member of `pdirty:<gid>` **before reading anything**: session keys,
   `c:` keys, or `all`. A source flagged during the run flags the person again.
2. **Read** `g/<gid>/group` (the merge's), the old head, and each changed
   session's head. A day is touched when one of its sessions' `pd/` hash differs
   from the one the person head records as folded, or a `c:` source names it;
   `all` touches every day. For each touched day, read the `pd/` parts of every
   session the group lists for it (members, and others that used its
   credentials) and its credentials' `c:` views for that day.
3. **Capture** the ETags of what it'll write, then read its lock back. An error
   reading it counts as not ours.
4. **Write,** each recomputed whole: `d/<day>` and `rq/<day>` for each touched
   day, then the head (with the hashes it folded). On any failure, it puts its
   sources and the gid back.

- **Person-wide values,** computed here and never at read time: request rows'
  prompt numbers, first reports and step numbers across the person's sessions
  (quirk 3), from the parts' `prompt_index`, `codex_turns` (ranked by the key
  run), `request_reports` and `request_missed`; an analytics row's session,
  count and name (stamped by ingest); the rows of the group's credentials' `c:`
  keys. A prompt crossing midnight brings its other day's parts.
- **The head** is rebuilt by replacing parts: the changed sessions' rows come
  from their heads, the others are kept from the old head (re-reading every
  session's head each run would cost the person's whole history), and sessions
  the group no longer lists are dropped. Everything else in it is folded from
  those rows.

**Cost:** per touched day, the parts of the sessions active that day (the
capture's busiest person-day: 12) and two objects written; the changed
sessions' heads; the head. **State:** none between runs.

### 3.6 Person ranges: narrow raw rows

The open point. The person summary and timeline fold events over a range the UI
sets to the millisecond (`from` = now − span, or the brush). Measured on the
capture's busiest person (12 sessions) on its busiest UTC day, as Parquet, zstd,
sorted by time:

| One person-day | Rows | Bytes |
|---|---|---|
| `event_rows`, today's columns | 111,747 | 6.9 MB |
| `event_rows`, the 19 columns the person summary and timeline read | 111,747 | 430 KB |
| marks: call, reported, missed, friction | 11,523 | 224 KB |
| request rows | 6,934 | 292 KB |

The person's day objects (`g/d`) hold the narrow rows. Folding them with today's SQL (DuckDB 2.0
on a laptop, the objects already in memory), if every day were that busiest
day:

| Window | Rows | Bytes | Load | Summary SQL (4 threads / 1) | Timeline lane | Cold, with 2 rounds |
|---|---|---|---|---|---|---|
| 7 d | 782k | 3 MB + 1.6 MB marks | 0.11 s | 0.06 s / 0.14 s | 0.01 s | ≈ 0.3 s |
| 14 d | 1.56M | 6 MB + 3.1 MB marks | 0.22 s | 0.11 s / 0.28 s | 0.02 s | ≈ 0.45 s |

- **Acceptable.** 7 d is on target. 14 d misses only if all 14 days are as busy
  as this person's busiest: their real two days averaged 60k events a day,
  which puts 14 d near 0.3 s. The final gate measures it.
- **Exact by construction:** no cells, no edges; the one carried value is
  `_LATEST`'s (below). The SQL runs
  unchanged except one line: the distinct batch count reads a stored `batch`
  column instead of a regex over `event_id`, which was most of the fold
  (0.88 s → 0.28 s single-threaded at 14 d).
- `_LATEST` has no lower bound: each part carries its session's newest value of
  the two names before its day, and the API keeps the carry rows of the first
  day it reads only.
- If the gate misses, minute cells with raw edges are the next step (Not now).

## 4. Merge: identity, dispatch and org views, every 30 s

- **Who runs it:** any rollup replica that holds `lock:merge` (unchanged). Its
  create-only commit fences it, and the commit's `newest_ns` is the data clock
  expiry reads (§2, unchanged).
- **What it does, each run:**
  1. **Take** everything in `committed` (`SPOP` in batches), plus the keys the
     newest merge commit lists if this replica loaded it cold (a merge that
     died after its commit). For each key, GET its newest commit JSON and its
     `kd` part, never its tables.
  2. **Index** (`v1/merge/index/`): per account its email and so its `<gid>`,
     its dimension rows folded newest-by-time; per credential its groups;
     `merge_keys`, each key's day (as v5) and, for keys with a waiting call,
     `expires_ns`.
  3. **Compute** (in memory, from absolute inputs):
     - for each group a committed key touches: its `group` object (read, then
       that key's entry replaced);
     - Codex labels, over the account-connected component of the sessions a
       committed Codex thread or title thread touches (today's
       `buckets._session_group` closure, followed through the group objects):
       title calls matched in time order, `sql/session_rows.sql`'s assignment,
       and `label_text_alone` per session. The merge is their one writer;
     - the day objects, as v5's merge rewrites its day partitions: each
       committed key's `kd` rows (with its labels) replace its old ones in the
       object of its `DayOf`, and leave the day `merge_keys` had it in. That's
       today's object, plus a day a resumed session moved out of.
  4. **Capture** the ETags of the objects it'll write; **create the commit,**
     listing the keys it took. A lost create puts the keys back and drops the
     run.
  5. **Write** the fixed-path objects, only now: `g/<gid>/group` and
     `pa/<account id>` for each member account, `s/<sid>/x` where a session's own
     label changed, the changed `org/days/<day>` objects, `org/accounts`,
     `org/status.json`.
  6. **Dispatch,** after the writes, so a person run reads the new group:
     - a session's groups (its accounts' or `unattributed`, and its
       credentials'): `SADD pdirty <gid>`, `SADD pdirty:<gid> s:<sid>`;
     - a `c:` key: the groups of its credential, with `c:<hash>:<day>`;
     - a credential newly linked to a group: that group, with each of the
       credential's `c:` days (a LIST of `v2/c/<hash>/`, on this rare write) and
       every other session that used it;
     - any commit whose dimension rows change an account's name, email, org,
       plan or devices (`a:`, `s:`, `c:` or `ip:`): the account's groups, `all`;
     - an account that changed groups: both groups, `all`;
     - a child's commit: `SADD dirty s:<parent>`;
     - a key whose call is due: `SADD dirty <key>` (below).
- **The directory is time buckets, not windows.** A day object holds every key
  last active that day, so `/people` and `/overview` for any `from` read the
  days from day(from) through today: exactly the partitions v5 loads, so
  quirk 1 (the window's leader, `account_facts`, `unattributed()`, sessions by
  product) holds by construction. No per-window objects, nothing written ahead.
- **Calls that never end:** `merge_keys` (in the index) keeps each key's
  `expires_ns`, as built. After each commit,
  the merge runs `SADD dirty` for every key whose `expires_ns` is before its
  `newest_ns`, so a key that gets no more data is revisited and its call
  expires. The deadlines live in S3; a flag Redis loses is set again by the next
  merge.
- **Newest data:** each ingest file carries the newest raw row its pod had
  seen (Parquet metadata), including rows that complete no record yet, such
  as an in-flight stream's chunks. A key's commit keeps the maximum, and the
  merge takes the maximum across commits into `status.json`: the API's
  `lastDataAt`.
- **The merge's sweep.** Every 10 minutes it also takes every key in
  `v1/recent/` from the last 15 minutes as if committed. Everything it computes
  is absolute, so redoing a key is harmless. That one pass covers a Redis wipe,
  a merge that crashed after taking keys, and a lost `pdirty`.

## 5. API: reads views, scaled on CPU

A round is parallel whole-object GETs (`S3Source.get`). The tables go into the
request's in-memory DuckDB, and today's endpoint code runs over them. A missing
object is empty (404): an `x` for a session with no title thread, a day with
nothing. The API never LISTs and never copies data to local disk.

| Endpoint | Round 1 | Round 2 | What the API computes |
|---|---|---|---|
| `/status` | `org/status.json`; Redis `ingest:last:*` | — | newest = the larger; `waitingSince` |
| `/overview`, `/people` | `org/days/<day>` from day(from) through today (24 h: 2, 7 d: 8, 14 d: 15), `org/accounts`. No `from`: `org/status.json` first | no `from`: every day from the first | today's `person_sql`, `rollups.py` and `directory.py` SQL over the loaded rows, unchanged: the window, `live`, activity, filters, sort, page |
| `/people/{id}` | `pa/<id>` | `g/<gid>/head` | `live`; today's header building, with this account's facts (quirk 6) |
| `/people/{id}/sessions` | `pa/<id>` | `g/<gid>/head` | `SHOWN`, agent, nesting, paging by root |
| `/people/{id}/summary`, `/timeline` | `pa/<id>` | `g/<gid>/head`; `g/<gid>/d/` for the days from day(lo) through day(hi) | range defaults (now); `SHOWN`; the first day's carry only; today's SQL; bins; `pack_rows`; in-flight and live values |
| `/people/{id}/requests`, `/coverage` | `pa/<id>` | `g/<gid>/head`; `g/<gid>/rq/` for the range's active days (`/coverage`: and `d/`) | `request_filter`, counts, order, page |
| `/sessions/{id}` | `s/<id>/head`, `s/<id>/x` | — | `live` |
| `/sessions/{id}/timeline`, `/prompts`, `/events` | head, `x` | every hour chunk the head names | as today |
| `/sessions/{id}/requests`, `/state` | head, `x` | the hour chunks; a Codex thread also `c/<hash>/<day>` for its credentials over [start, last + 60 s] | as today; Codex: today's filter on those rows (no thread, agent codex, not model, same client, in time) |
| `/requests/{id}`, `/details`, `/raw` | `r/<id>`, `calls/<call id>.ndjson.gz`, `org/status.json` | `s/<sid>/p/<prompt>`; batch parts | `request_detail.py` over the rows |
| `/events/{id}` | `r/<batch>`, `calls/<batch>.ndjson.gz` | batch parts | the event's rank and raw row |

- `x` replaces the head's `label_text_alone` and `label_source_alone`.
- **Endpoint code that changes, and nothing else:** the summary's distinct
  batch count reads `batch` (§3.6); `/people/{id}` reads the precomputed
  `ru.rollups` and `dr.linked_accounts` rows; the drawer 404s for a call in
  flight.
- **The directory aggregates at request time** (the owner's call): `/people` and
  `/overview` run today's person, rollup and directory SQL over the day
  objects' finished rows. It's the one request-time aggregation left; any
  `from` works.
- `org_kind` still reads `CUSTOMER_ORG_IDS` at request time.

## Cross-key values and how a change reaches them

| Value | Depends on | Computed by | A change reaches it through |
|---|---|---|---|
| a person's views | its sessions' heads and `pd/` parts; the parts of other sessions that used its credentials; its credentials' `c:` views | person run | the merge: `pdirty:<gid>` |
| who a person is (accounts, leader, credentials, account facts) | every commit's dimension rows | the merge | the merge: `all` to the groups, and their directory rows |
| request rows' numbers and first reports across the person | the person's other sessions | person run | the same run |
| a batch's count, name and other sessions | the batch body | ingest, stamped on the batch call | — |
| Codex labels | the title threads and Codex sessions of the account-connected component | the merge | its own run |
| `children_json` | the children's `kids/` objects | the parent's key run | the merge: `SADD dirty s:<parent>` |
| a Codex thread's unthreaded requests | `c:<hash>:<day>` | the API, at read time | nothing: read, never copied |
| a batch's events in other sessions | those sessions' keys | each key, its part | nothing: `r/` names the parts |
| `/people`, `/overview` | every key's `kd` part; labels; accounts | the merge (day objects), then the API's SQL | commits |
| `/status` | keys' first, earliest and newest times, retention | the merge | commits |
| expiry | the merge's `newest_ns` | the key run (as built) | the merge: `SADD dirty` |

Redis is never needed for correctness. Every flag is a hint: the key loop's
input cursor, the merge's sweep over `v1/recent/` and `held` recover from losing
any of them. Losing Redis costs freshness.

## Scaling budget

Ops are S3 requests, estimated per active session or person from the steps
above.

| Component | Work | State held | Sharded by | Waits on | S3 ops/s |
|---|---|---|---|---|---|
| Ingest | per record, as v5 | per-key buffers | pods, on CPU | S3 PUT | ~0.2 per active session (its files and call detail objects) |
| Key run | per record: apply (v5). Per run: the whole session rendered in memory (O(session) CPU), the changed objects written | v5's, plus a hash per object in memory | key (`lock:<key>`) | S3 | ~0.7 per active session (~20 per run with its HEADs, a run per 30 s) |
| Person run | per touched day: the day's session parts read, `d/` and `rq/` written; the changed sessions' heads; the head | none between runs | person (`lock:p:<gid>`) | the merge's dispatch (≤ 30 s, as v5's person pages) | ~1 per active person |
| Merge | per committed key: its commit and `kd` GETs; per touched group: its group, `pa/` and labels; per touched day: its object (today's, mostly) | the index: accounts, credentials, `merge_keys` | none: one `lock:merge` holder | `lock:merge` | ~0.3 per active person, plus the sweep |
| API | per request: ≤ 2 rounds, the rows of the range asked | none | pods, on CPU | S3 GET | ~3 per person page polled |

- About 3 ops/s per active person with two active sessions. A handful of
  people: under 20 ops/s. DO Spaces' 800 ops/s per bucket is about 250 active
  people; 1,500 is about 5,000 ops/s. Production is AWS S3, whose limits are per
  prefix and far above this. Nothing is added for Spaces now.
- The merge's 10-minute sweep redoes the last 15 minutes' keys: about half
  again on the key-driven rows above, in bursts.
- The merge is the one single point, and its run grows with what changed in
  30 s, never with history.

## Rollout

None. This is used internally and by one or two people outside the team. On
deploy, everything under `v1/` (ingest files, key state, the merge) and the
`calls/*.keys/` markers are deleted; ingest, the rollup and `v2/` start fresh
from new traffic. No backfill, no side-by-side run, no migration.

## Testing: small tests, written first

v5's tests (ingest buffers, the rollup loop, the chaos simulation) stay. Each
v6 step writes its failing tests first.

**The oracle is today's code.** The Python derivation (`state_sql`,
`stream_sql`, `sql/*.sql`, `buckets._session_rows`, `person_sql`, the aggregate
SQL of `rollups.py` and `directory.py`) moves to `api/tests/oracle/` and runs
over the v1 layout, merge partitions included, that `aintercept v1layout` keeps
writing until the switch. `v1layout` also writes the v6 views: every key's run,
then the person loop until idle, then the merge. Both sides run DuckDB with
`threads=1`.

- **Go unit tests, in the user's layers.** Through step 4 they call the view
  functions directly, with no writer:
  - **Atomic:** one record per case (every `state.Cases` branch); each table's
    rows equal `process.Derived`'s.
  - **Short sequences:** every order of up to 5 records; the views after each
    prefix, and at the end, equal one pass.
  - **Generated sequences:** shuffled, duplicated, late across hours, reloaded
    from a checkpoint halfway; the views equal one pass, and only objects whose
    rows changed are written. Named cases: a call that lands an hour after the
    event linking it; a prompt's input event after its calls; a new call in a
    message-id chain (the earlier call's `r/` gets its next); a late Bash event
    (later calls' sandbox).
  - **Fencing:** two holders race (old writes before and after new), a first
    write, a person run whose lock lapsed before it captured, an error reading
    the lock back. `MemStore` and `DirStore` get ETags and conditional PUTs.
  - **Repair:** a crash between commit and views, then a run with no new input:
    every object kind is rewritten and the views equal one pass.
  - **Person loop:** sessions committed in any order, only changed days
    refolded, an account changing groups, a crash at each step, Redis wiped:
    equal one pass.
  - **Merge:** dispatch per key kind, the sweep after a wipe and after a crash,
    a session resumed on a later day (it leaves its old day object), labels.
- **Table level, side by side, in `make check`:** for every API test dataset
  (`builders.py`, the demo fixture, the SQL fixture seeds), each entity's v6
  tables (each session's, person's, request's) equal the oracle's for that
  entity, row for row (`seq` compared by order).
- **Drawer:** for every request id of every dataset, the drawer over its keys'
  whole facts (the oracle) and over `r/`, the prompt object and parts are
  byte-identical.
- **Response level:** `test_v1_pipeline.py` with "old" pinned to the oracle path
  and "new" the v6 reader. In `make check`, the full URL set on the small
  datasets for every endpoint that changes (person, directory, drawer), 3 per
  group for the rest; everything with `-m full`. `endpoints.py` gains the UI's
  real params (the UI's `from`s, an unaligned `from`, a brush, ladder bins) and
  drops the bogus `binSeconds` and `sort`.
- **Reads:** `test_flat_reads.py` counts rounds, objects and LISTs per endpoint:
  at most 2 rounds, no LIST, the same objects under 8× more history outside the
  range.
- **The chaos simulation** (v5) checks the views too: after crashes, lock
  expiry, Redis wipes and S3 failures they equal one pass.
- **CI time:** all on the small datasets, in seconds; the SQL-fixture tests it
  replaces go. Budget: under a minute added across the parallel jobs.
- **The real two-day capture runs once,** as the final gate
  (`~/git/aintercept-parity`). It is never part of the development loop.

## Build order

v5's steps are built and deployed. Each v6 step is one PR.

| Step | What | Gate |
|---|---|---|
| 1 | Store: ETags and conditional PUTs (S3, `MemStore`, `DirStore`); the fencing rule | Unit tests; the S3 emulator test; once, two concurrent `If-Match` PUTs raced against real DO Spaces on a scratch path |
| 2 | Oracle to `api/tests/oracle/`; the table-level harness; `v1layout` writes `v2/` beside v1 | The API tests pass on the oracle path |
| 3 | Session view functions: the mirrors into package code, hour chunks, order keys, `pd/` parts, object hashes | Go layers against `process.Derived`, called directly; side by side for every session table |
| 4 | Session writer in the key loop: render, diff, capture, commit, write, `kids/`, repair, the commit's fields, recent entries | Chaos simulation: views equal one pass; fencing and repair tests |
| 5 | `c:` views; request and prompt objects; ingest stamps batch count, name and sessions | Drawer side by side, every request id |
| 6 | Merge v6: index, groups, labels, calls, `status.json`, dispatch, sweep; its partitions stay for `v1layout` only | Merge units; Redis wipe; `/status` side by side |
| 7 | Person loop: `d/`, `rq/`, head | Person layers; side by side for person tables, request rows, labels |
| 8 | Directory: `kd` parts and the merge's day objects | Day objects equal v5's day partitions' directory tables; `/people` and `/overview` side by side for any window (the existing tests included) and none |
| 9 | API: the v6 reader and the endpoint changes | Every URL byte-identical; the reads test; `make regen` changes only the in-flight drawer sample |
| 10 | Final gate on the real capture | Every endpoint byte-identical to the v5 baseline (4,248 URLs); cold p95 < 300 ms for the heaviest person (7 d and 14 d) and session |
| 11 | Delete (below), the oracle and `v1layout`'s v1 output included: from here `process.Derived` and the UI samples are the oracles | `make check` |
| 12 | Deploy to the DigitalOcean cluster: `helm upgrade` (never uninstall), the bucket cleared per Rollout | Pages load on the cluster's new traffic; `/lag` settles |

## Deleted

- **API:** `buckets.py` (scopes, `resolve_person`, `_session_group`,
  `_session_rows` and title runs, `read_newest`); `v1.py`'s scope, file plan,
  merge and key-commit readers, `call_keys` and `request_keys`;
  `state_sql.py` but its person tables; `stream_sql.py`; `sql/prompts.sql`;
  `sql/session_rows.sql`; the `ru.rollups` and `dr.linked_accounts` queries
  `/people/{id}` ran; the legacy empty tables; the dead code
  (`cc.person_sessions`, `cc.linked_accounts`, `pp.retries`); their tests
  (`test_session_sql`, `test_stream_sql`, `test_events_sql`,
  `test_prompts_sql`, `test_buckets`, `test_stream_pipeline`). `person_sql.py`
  and the directory's SQL stay: the directory runs them at request time.
- **Go:** `rollup/reported.go`; `records/keymarks.go` and ingest's marker
  writes; the merge's reading of keys' tables (`ReadKeyTables`: it reads `kd`
  parts) and its index partition (`org/accounts` replaces it; `DayOf` moves to
  the key run); each key's `out/` tables and their compaction (state
  compaction stays); the SQL-fixture writers (`TestWriteRequestSQLFixtures`,
  `TestWriteEventsSQLFixtures`, `TestPromptsSQLFixtures`, `TestSessionRowsSQL`).
  The mirrors in `state/*_test.go` move into package code.
- **S3:** `calls/<id>.keys/`, `v1/reported/`, the merge's v5 day and index
  partitions (the day objects replace them), each key's `out/`.

## Decisions and quirks

- **Recompute, don't delta.** A key run renders its whole session each run and
  writes the objects whose hash changed. Every object is recomputed whole from
  absolute inputs. The person head is rebuilt by replacing a changed session's
  rows, and the merge's group object by replacing a key's entry: both
  idempotent, neither an increment.
- **One writer per object,** under its entity's lock (table in §S3 layout).
  Labels are the merge's, so `s/<sid>/x` has one writer.
- **Accepted limit (owner: "I don't care about 200 days. We're working on 1
  week right now"):** a person's head and group objects hold every session the
  person ever had, so each person run and merge touch rewrites history. Per
  session: about 1.1 KB in the head (its `session_rows` row, about 1 KB in the
  capture, plus presence and folded hashes) and 0.2 KB in the group object. A
  week of the capture's busiest person (about 40 sessions) is a 45 KB head; a
  year at that pace (about 1,500 sessions) is about 1.6 MB, plus 0.3 MB of group
  object per linked account. The merge's `merge_keys` holds every key, as in
  v5. Month partitions are in Not now.
- **Reads follow the range asked.** A session page reads one object per hour the
  session has rows in; a person page one per active day in its range, and All
  one per active day of the person's history (the owner's call).
- **Person ranges read narrow raw rows** (§3.6): exact, one SQL line changes.
- **The directory is day buckets** (owner): every key in the day object of its
  `DayOf`, as v5's merge does; `/people` and `/overview` read day(from) through
  today and aggregate with today's SQL. Any `from` and any `bin` work: no
  contract change but the in-flight drawer.
- **Drawer:** a call in flight 404s until it ends (owner); its sample changes.
- **A Codex thread's unthreaded calls** are read from the `c:` view at request
  time and filtered as today, never copied: no `c:` → session edge. `seq` only
  breaks ties within one call, so the union sorts as today.
- **Every cross-key flag goes through the merge,** so person pages lag a merge
  period, as in v5.
- **Reads aren't isolated across objects:** a read racing a rewrite can mix two
  consecutive versions of one entity for one poll, and the next poll (≤ 15 s) is
  whole. Tests compare at rest.
- **Calls that never end:** as built (#39), `merge_keys` included.

The inventory's quirks, each reproduced:
1. **Directory partitions from day(from):** by construction. The day objects
   are v5's partitions, cut to what the directory reads, and the API loads the
   same days.
2. **Codex `label_text` in time order over the account group:** the merge runs
   the same assignment over the same account-connected component;
   `label_text_alone` per session.
3. **Person and session request numbering differ:** person rows by the person
   run across sessions, session rows by the key run within the session.
4. **No final tiebreaker in the person requests sort:** `rq/` stores today's
   order (`ts_ns`, `request_id`), loaded in that order with one thread, so ties
   fall as today.
5. **Session pages derived the whole account group:** not reproduced as a cost;
   the group-dependent values (labels) arrive precomputed.
6. **`/people/{id}` facts are the URL account's:** the head holds every linked
   account's rows, and the endpoint picks the URL's, as today.
7. **The drawer's `lastDataAt` is global:** `org/status.json` or Redis, in its
   first round.
8. **`org_kind` reads the environment at request time:** unchanged.

## Not now: add when real traffic needs it

- Smaller directory buckets (hours, or per-session call counts instead of call
  marks) when a day object grows heavy: today's holds every call mark of the
  day, about 10k rows in the capture.

- Month partitions for a person's head and group objects (the accepted limit
  above).
- Minute cells with raw edges for person ranges, if the gate's 14 d p95 misses.
- Day chunks for sessions that run for days (one object per active hour).
- DO Spaces' 800 ops/s per bucket (about 250 active people at the rates above).
  Production is AWS.
- Fewer, larger S3 files to cut cost.
- Hot keys: a shared API key used by thousands of people (dispatch fans out to
  each), or many people behind one NAT IP.
- Redis high availability. Losing Redis costs freshness, never correctness.
- Changing response contracts.
