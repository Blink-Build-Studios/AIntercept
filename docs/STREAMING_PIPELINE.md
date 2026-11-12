# Data pipeline: canonicalize fast, process only what changed

Status: **proposed, for review.** It replaces two things:
- the single batch processor (`aintercept process`), which reads everything new
  each run, rebuilds and commits one global manifest;
- the API's load of all history into memory.

The query API's contract doesn't change. The UI, its samples and every
constraint in `docs/ui/BUILD_PLAN.md` stay exactly as they are, values included.

## Requirements

- **Every tier scales horizontally with traffic on Kubernetes.** Nothing that
  everything waits on exists at a fixed count of one.
- **Delta in, correct state out.** Each stage reads only what's new since it last
  ran, and its cost follows what arrived, never how much is stored. The tables
  must equal one pass over all the records, whatever the arrival order,
  lateness, routing, restarts, scale-ups, scale-downs or crashes.
- **Data completeness comes first.** The pipeline does everything it can not to
  lose a captured record: a disk spool, retries forever, and pushing back
  instead of dropping. In enforce mode it fails closed, rejecting a call it
  can't record. In observe mode, a drop is a counted, alerted last resort, and
  traffic is never broken.
- **S3 is the only backing store.** No database, queue or table engine, and no
  new services. Everything is Go and Python in this repo, plus Kubernetes.
- **The UI and the API contract don't change.**

## The shape

```
                 round-robin load balancer (by connection)
                          │                 │
      ┌───────────────────▼──┐   ┌──────────▼───────────┐
      │ ingest pod           │   │ ingest pod           │  … scales with traffic
      │ Envoy → ext_proc →   │   │                      │
      │  assemble → canonical│   │                      │
      │  records → writer ───┼───┼──► S3 records/, by bucket and minute
      └──────────────────────┘   └──────────────────────┘
                                        │ new files only
      ┌─────────────────────┐  ┌────────▼────────────┐
      │ processor replica   │  │ processor replica   │  … scales with lag
      │ owns buckets 0–31   │  │ owns buckets 32–63  │
      │ (leases in S3)      │  │                     │
      └─────────┬───────────┘  └─────────┬───────────┘
                └──────► S3 tables/, by bucket ◄┘
                                │
                      API replicas (stateless) … scale with queries
```

There are three tiers, and each one scales on its own signal:

1. **Ingest** scales with traffic. Each pod captures, canonicalizes and writes,
   and derives nothing.
2. **The processor** scales with lag. Each replica owns a set of key buckets and
   processes only those buckets' new records.
3. **The API** scales with query load. It reads tables from S3 and holds no
   state of its own.

## 1. Ingest: capture, canonicalize, write

Each ingest pod runs Envoy and the observer. Inside the observer, data moves
through a pipeline of long-lived workers joined by channels. Each worker is a
`select` loop over `ctx.Done()`, a ticker and its input channel, and one
supervisor drains every stage on shutdown.

```
ext_proc streams → assembler workers (by stream) → canonicalizer → writer → S3
```

**Assembler.** One ext_proc stream is one HTTP transaction and never leaves its
pod, so assembly needs no shared state.
- It emits a **call-start** record as soon as the request is known, and a
  **call-end** record when the call finishes or disconnects.
- Today's UI shows calls while they're still streaming, and the start record is
  what lets it keep doing that.
- Memory is bounded by the calls in flight.

**Canonicalizer.** It identifies the provider and maps each record to the common
schema (`docs/harness-parity.md`): calls, analytics events, and WebSocket frames
and turns.
- Analytics uploads are split into one record per event.
- A record carries everything later stages need, so no stage goes back to raw
  bytes:
  - a **record id**, `<pod incarnation>-<sequence>`, which is unique and
    deterministic;
  - a **routing key** (§2);
  - the bodies and headers when the deployment records bodies.

**Writer: the only code that writes to S3.** Every stage sends
`(file, content)` to one queue, and one goroutine owns it. It flushes when either
of these comes first:
- a **10-second ticker**;
- a **byte cap**.

Each flush hands sealed files to an uploader goroutine, so the loop never waits
on S3.

What a flush writes:
- `records/bucket=NN/minute=YYYY-MM-DDTHH:MM/<incarnation>-<seq>.parquet`: the
  canonical records, one file per bucket the flush touched. The `minute` is the
  **upload** time, not the data time, so records uploaded late (after an outage)
  still land after the processor's watermark (§2).
- `calls/<call id>`: each finished call's detail (headers, bodies, and its
  events in order), written once when the call completes. The request drawer
  reads it directly instead of scanning the pod's raw log.

**Nothing is dropped.**
- **Every drop path in today's observer goes:**
  - ext_proc buffer overflow (`events/processor.go`);
  - a full upload queue (`events/s3sink.go`);
  - a failed upload (`events/s3sink.go`).
- **Spool:** uploads are retried until S3 accepts them. Sealed files waiting to
  upload sit in an on-disk spool, which survives container restarts and is sized
  for hours of peak traffic.
- **What happens when data can't be stored depends on the mode:**
  - **Observe mode** (the async tee, today's default) does its utmost not to
    drop: it spools, retries forever, and pushes back instead of dropping. But
    it never breaks the user's traffic. If the spool fills or the observer is
    down, traffic keeps flowing (`failure_mode_allow: true`, as today). Any
    record dropped as a last resort is counted, and the count alerts.
  - **Enforce mode** (the Interceptor's synchronous, in-path filter) fails
    closed, as it does today (`failure_mode_allow: false`). If the observer
    can't take the call, the call is rejected.
  - Spool depth, last-resort drops and rejections are all metrics with alerts.

**Shutdown.** Kubernetes signals every container at once, so the order is
enforced with `preStop` hooks:
1. Envoy stops taking connections and drains.
2. WebSockets, which never end on their own, are closed 60 seconds before the
   deadline. Clients reconnect to another pod.
3. The observer flushes the writer and the spool, and exits last.

If a hard kill (spot reclaim or OOM) loses buffered records, the loss is limited
to what hadn't reached the on-disk spool. Everything already on disk is uploaded
when the pod restarts.

**A call cut off by a dead pod** has a start record and no end record. One pass
over the records sees exactly the same thing, so no takeover protocol is needed.
The tables show it the way they show any unfinished call today: streaming, then
an error after 10 minutes.

## 2. Routing keys and buckets

Every record is routed by a key that's known at ingest:
- the **session id**, for calls and events that carry one;
- otherwise the **credential**, a hash of the auth header, plus the day. This
  covers calls with no session: Codex usage and catalog polls, flag fetches and
  similar.

The key hashes to one of **B buckets**: `bucket = top bits of hash(key)`. Start
with B = 64. Taking one more bit of the hash later splits every bucket cleanly
in two, without reshuffling keys.

**When one record matters to two keys, ingest writes it to both.** The writer
has the record in hand, so this is cheap and deterministic. One example: a Codex
call has no thread id until it completes.
- Its start is keyed by credential.
- Its end is written to the credential key, which closes the in-flight row, and
  to the thread's key.

The same mechanism covers any other record that needs to be in two places.

## 3. The processor: only what changed, sharded by bucket

The processor is a Deployment of R replicas. Each bucket has exactly one owner
at a time.

**Leases.**
- A replica claims a bucket by creating `leases/bucket=NN/epoch-<n>` with
  `If-None-Match: *` (create only).
- It renews by writing the next epoch.
- A lease not renewed within 30 seconds, judged by S3's own timestamps rather
  than pod clocks, may be taken over at the next epoch.
- Every output carries its epoch, so a paused old owner can't overwrite a newer
  owner's work. That's the fencing.
- Replicas balance the buckets among themselves by counting live leases. There's
  no coordinator.

**Each cycle, for each owned bucket:**
1. **List only what's new.** List the minute prefixes since the bucket's
   watermark, minus a grace window for skew between pod clocks. Skip files
   already processed, because the watermark records them by name. A file is the
   unit of work and its name is unique, so a re-uploaded file is never applied
   twice.
2. **Apply each record to its key's state,** incrementally. A record touches
   only the rows it affects (below).
3. **Write the touched keys' new rows** as one file per table per cycle:
   `tables/<table>/bucket=NN/run=<epoch>-<seq>.parquet`. These are upserts by row
   id, and the newest run wins.
4. **Write a checkpoint** of the touched keys' state, and the new watermark
   (create-only, by sequence).

**State in memory:** keys touched recently stay in an LRU with a byte cap. A key
that hasn't been touched is read back from its last checkpoint when a late
record arrives. Memory follows activity, not history.

**Compaction:** per bucket, run by the bucket's owner.
- It merges run files into `tables/<table>/bucket=NN/base-<gen>.parquet`, sorted
  by key and then time, and drops superseded versions.
- Old files are deleted only after a grace window. Readers follow the newest
  generation marker, so a reader in the middle of a read never loses a file.

**Freshness:** up to 10 seconds of ingest flush, plus the processor's cycle
(about 5 seconds), plus one S3 round trip. That's about 15–20 seconds, within
today's roughly 30-second commits, so the UI doesn't change.

### Incremental per record, even within a key

A record never makes the processor rebuild a whole session.

1. **Append-only rows.** Events, a call's request row, a step and a mark: the
   record adds its own row.
2. **Values that depend on a few neighbors**, through indexes in the key's
   state:
   - captured-vs-reported matching by request id;
   - a time-sorted index for retries and prompt membership;
   - running totals per prompt and per session;
   - an interval index for presence.

   One record changes a handful of rows.
3. **Values derived from the session as a whole aren't copied into rows.** For
   example: the session's last activity, a prompt's number, a call's label, and
   whether events are pending. Rows store keys (`prompt_id`, `request_id`,
   `session_id`). The query joins small per-session lookup tables: prompt to
   number, call to label, session to last activity. A late prompt or call
   updates one lookup row, not thousands.
4. **Positions** (step numbers, Codex turn numbers, row order) are stored as
   sort keys and numbered by the query, with `row_number()` or `dense_rank()`.
   Each table's sort key reproduces today's order exactly.

**Ties are broken by (time, record id)** everywhere: picking an account's email,
reports, retries, the device, version and limits. Today's code breaks some ties
by table order or arrival order. This change fixes it here, and the parity
answer key uses the same rule.

### Values that span keys

These are small tables joined at query time, never copied into rows:
- **Dimensions:**
  - session → credential and account;
  - account → email;
  - credential → session.

  Each bucket writes its own rows, and email grouping (account → person) is
  resolved over that small table by the query.
- **Codex threads and calls without a session:** a thread's interval per
  credential is a dimension. The query attaches the credential's sessionless
  calls to the threads whose interval covers them.
- **Session lineage:** parent and child sessions are recorded as edges.
- **Codex titles:** a title names the latest thread of the same account started
  in the 5 minutes before it, and the assignment is a greedy chain. Title calls
  and thread starts are both kept per account, and the chain is resolved per
  account at query time. This is the one value that needs a check that it's
  bounded; the replay checks it.
- **"Unattributed":** sessions with no account in the dimensions. That's a query
  over the dimensions, not a stored list.

## 4. Autoscaling on lag

- **What lag means.** A bucket's lag is how far its watermark trails the newest
  file written to its prefix: now minus the time of its oldest unprocessed file,
  or 0 if it has none. **Pipeline lag** is the maximum over all buckets.
- **Who measures it.** Every processor replica serves the pipeline lag at
  `GET /lag`. It computes this from each bucket's watermark and the files after
  it in S3, so every replica gives the same answer.
- **Who scales.** KEDA, which already runs in the cluster. A `ScaledObject` with
  the `metrics-api` trigger polls `/lag`, scales out when lag stays above a
  target (for example 30 seconds) and scales in when it stays near zero. The
  limits are a minimum of 1 and a maximum of B. This needs no Prometheus and
  adds no new component.
- **Rebalancing.** When replicas are added or removed, buckets rebalance through
  the leases. A bucket's state and watermark are in S3, so its next owner picks
  up exactly where the last one stopped.

## 5. The query API

API replicas are stateless. Each request goes like this:
1. Resolve the person to their accounts, sessions and credentials through the
   dimensions.
2. Find the buckets those keys hash to.
3. For each bucket, read the newest base file (narrowed to the keys by sort order
   and min/max statistics), plus the run files written since. Apply upserts so the
   newest version wins.
4. Run the endpoint's SQL in an in-process DuckDB. That's today's SQL plus the
   lookup joins, and nothing else.

Every object the API reads is immutable, so a byte-capped LRU cache never goes
stale. Freshness comes from listing a bucket's new run files.

Before the cutover, every API query that still touches raw fact tables is
inventoried and moved onto these tables. That covers:
- the person header and summary;
- coverage;
- `/people` and `/overview`;
- device owners and attribution.

The UI's semantics don't change.

## 6. Reprocessing and versions

- **Versioned output roots.** Outputs live under a code-version root
  (`v<N>/tables/…`, `v<N>/state/…`). A change to the derivation logic gets a new
  root.
- **Rebuilding.** The processor replays `records/` into the new root, with the
  buckets spread across replicas the same way as live work.
- **Switching over.** The API switches when a pointer object is written,
  create-only.
- **Rolling deploys.** A bucket is never processed by two code versions at once,
  because each version owns its own root.

## 7. Verification

The precompute parity method carries over, and it's extended to the distributed
pipeline:

1. **Answer key:** one pass of today's code over a real capture. It's kept local
   and never committed, and it uses the tie rule above.
2. **Distributed replay:** the capture is fed through N ingest pods and R
   processor replicas, in process, against a local S3 emulator. During the run:
   - connections are routed at random;
   - pods are killed hard and gracefully, and added and removed;
   - leases move;
   - uploads fail and are retried;
   - the spool is replayed.
3. **Exact match:**
   - every table is identical to the answer key at the end;
   - at each checkpoint, every settled key matches;
   - every endpoint's response is identical to today's at the fixed times.
4. **Flat cost:** the same capture is replayed with 2×, 4× and 8× as much history
   before it. Per-record time and memory, per replica and per pod, must not grow.
5. **Lag scaling:** a burst of traffic must drive lag up, the scaler must add
   replicas, and lag must come back under target. No record may be lost or
   applied twice along the way.

`make check` keeps a small, fast version of step 2 so the guarantee stays
enforced.

## What carries over

- **Assembly** (`shape.go`), with versioned call-start and call-end records.
- **Provider mapping** (`internal/events`, `docs/harness-parity.md`).
- **The derived tables' value logic** from the precompute work, restructured into
  per-record updates. The single-pass builders stay as the reference those
  updates are tested against. The real bugs the replays found stay fixed.
- **The endpoints' SQL,** pointed at the bucketed tables.
- **The replay and parity tooling,** extended to many pods and replicas.


# Implementation plan

This part says how to get from today's code to the design above, in order. Each
step names the code it changes, what's built, how it's tested, and the gate that
must pass before the next step starts.

File references are to `main` at `dd2797b` unless marked **PORT**. PORT is the
precompute branch stack, `precompute-request-rows` … `precompute-event-rows`,
plus the uncommitted table-7 work.

## Where things stand

### Ingest (`aintercept/internal/observer`, `internal/events`, `internal/extproc`)

- **Envoy and the observer are two Deployments.** They talk over the network:
  Envoy reaches the headless `observer-pods` Service through ORCA-weighted round
  robin (`deploy/helm/aintercept/templates/observer.yaml:20-219`,
  `templates/envoy.yaml`, `files/envoy.multi.yaml:382-411`). The observer
  autoscales on CPU or memory budget, through KEDA or HPA
  (`observer.yaml:220-290`).
- **Everything fails open.** The observing ext_proc filter runs with
  `observability_mode: true` and `failure_mode_allow: true`
  (`envoy.multi.yaml:157,159`), and `scripts/smoke-test.sh:118` asserts that a
  request succeeds with the observer stopped.
- **Captured data is dropped in six places:**
  1. **`events/processor.go:113-128`.** The ext_proc buffer (`OBSERVER_BUFFER_SIZE`,
     16,384 in Helm) drops the **whole event** when full, `complete` rows
     included.
  2. **`events/processor.go:148-157`.** A panic in processing drops the event.
  3. **`events/processor.go:194-211`.** A sink error skips the row and that
     transaction's client events.
  4. **`events/s3sink.go:313-320`.** A full upload queue (depth 8) drops a sealed
     batch.
  5. **`events/s3sink.go:349-351`.** A failed PUT is never retried, so the batch
     is lost.
  6. **SIGKILL.** Every unsealed or in-flight batch is in memory only, with no
     disk spool.
- **Five independent `S3Sink`s each run their own upload goroutine**
  (`observer.go:101-221`): body-stripped, capture, two local sinks, and schema
  samples.
- **Raw keys.** Raw files go to
  `<prefix>/dt=/hour=/instance=<pod>/<batchStartNanos>-<seq>.ndjson.gz`
  (`s3sink.go:325-341`), with the batch **start** time in the key, not the upload
  time.
- **Assembly is split.** The observer keeps per-stream state and already builds
  call start and call end records, but only for the Interceptor
  (`interceptor/build.go:10-161`). It writes phase rows: headers, body chunks,
  complete, disconnect, ws_connection, client_event. Pairing them into calls
  happens in the processor (`process/shape.go`).
- **Session ids are known at request time** for Claude Code
  (`x-claude-code-session-id`) and Codex HTTP (`x-codex-turn-metadata`). Codex
  WebSocket ids are known per turn, at `response.create`. Analytics events
  carry their session id per event. claude.ai chat turns have none; they're
  identified by credential and conversation.

### Processor (`aintercept/internal/process`)

- **It's a single process by construction.**
  - Helm runs `replicas: 1` with `strategy: Recreate` (`templates/process.yaml:15-16`).
  - `processor.go:123-124` says so.
  - The manifest commit is a plain PUT with no compare-and-swap
    (`processor.go:573-583`, `store.go:30-42`).
  - Run numbers come from `man.Run + 1`.
  - `removeOrphans` deletes any output its manifest doesn't name
    (`processor.go:271-302`).
- **State is global:**
  - one map each for sessions, prompts, devices, pending, accounts and bodies
    (`shape.go:35-52`);
  - one data clock, `newestNS`, that drives every key's expiry
    (`shape.go:263-282`);
  - people derived from everything at each snapshot (`shape.go:412-456`);
  - every snapshot table rewritten whole each run (`processor.go:626-636`).
- **PORT adds a full rebuild of every derived table on every run**
  (`requestrows.go:478-556`), which is what made memory grow with history.

### API (`api/src/aintercept_api`)

- **One replica** (`templates/api.yaml:15`), with a `mirror` sidecar that copies
  the whole shaped output to local disk. `store.py:396-451` loads every fact and
  snapshot table into DuckDB on each commit.
- **On PORT**, the session endpoints and `/people/{id}/requests` and `/timeline`
  are SQL over precomputed tables. These still derive from raw fact views
  (`calls`, `cc_events`, `accounts`):
  - `/people` and `/overview` (`directory.py`);
  - `/people/{id}` (header facts);
  - `/people/{id}/summary`;
  - `/coverage`;
  - the person→sessions lookup (`claude_code.py:147-205`);
  - `/status`;
  - the request drawer (`request_detail.py`).
- **The uncommitted table-7 work** (`git stash@{0}`, 14 files, +585/−678, plus
  `personmap.go`, `personmap_test.go` and `rollups.py`) moves most of the person
  and directory endpoints onto five new tables.
- **The request drawer reads raw files** between a call's first and last file
  for its pod (`store.py:546-606`, `raw.py:88-108`).

## Sequence

There are seven phases, each delivered as one PR or a small stack. Phases 1 and 2
don't depend on each other and can run in parallel; every other phase depends on
the ones before it.

| Phase | What | Fixes | Ships to the cluster? |
|---|---|---|---|
| 1 | Stop losing data | data loss happening today | yes, on its own |
| 2 | Finish the table work and make it deterministic | the API contract the new processor must produce | no: merged as the reference (2.5) |
| 3 | Canonical records at ingest, by bucket | the processor's input | yes, written alongside today's raw log |
| 4 | The bucketed processor | the bottleneck | yes, alongside the old processor |
| 5 | The API reads the bucketed tables | the whole-history load | yes, after a live parity soak |
| 6 | Lag scaler, cutover, deletion | the single process | yes |
| 7 | The behavior fixes (the parity oddities list) | 50 known Python quirks | yes |

---

## Phase 0: housekeeping, before anything else

1. **Merge PR #13** (late endings of expired calls no longer written twice; the
   replay flushes quiet pods on time). It fixes the processor that's running
   today, and every replay below relies on its harness fix.
2. **Rebase the PORT stack onto `main`**, keeping it a stack. Re-run
   `make check` on its tip.
3. **Confirm create-only writes on every target store** with a small command,
   `aintercept storecheck -bucket … -prefix …`:
   - a `PutObject` with `If-None-Match: *` succeeds;
   - a second one fails with 412;
   - a listing returns the new object at once.

   Run it against AWS S3 (the production target) and against MinIO (CI). The
   demo cluster's DigitalOcean Spaces bucket is checked too and the result
   recorded. If Spaces fails, the demo cluster is moved to AWS for phase 4 or
   later; the design isn't weakened to fit it.
4. The four decisions are settled (see "Decisions" at the end).

---

## Phase 1: stop losing data (ingest completeness)

This ships to the cluster on its own. It changes no table or UI output.

### 1.1 Failure policy by mode (decided)

- **Observe mode** (`observability_mode: true`, async). Best effort, never
  breaks traffic.
  - `failure_mode_allow` stays `true`.
  - The observer does everything it can not to drop data: the spool, retrying
    forever, and pushing back instead of dropping (1.4).
  - Once the spool is full, it drops only as a last resort, with a counter and
    an alert.
- **Enforce mode** (the Interceptor's `FULL_DUPLEX_STREAMED` filter,
  `envoy.multi.yaml:176-203`). Fails closed.
  - That filter already runs with `failure_mode_allow: false`.
  - Add: when the spool is full, or the writer can't accept, enforce-mode streams
    are refused, so the call is rejected rather than allowed through
    unrecorded.
  - The e2e test covers it (1.7).
- No spike is needed for observe mode. Its behavior doesn't change.

### 1.2 Co-locate the observer with Envoy (decided)

Every ingest pod is the full stack, scaled as one unit. ext_proc goes over
localhost, and fail-closed and the shutdown order are local to the pod.

1. **The pod.** Add an `observer` container to the Envoy Deployment
   (`templates/envoy.yaml`). Point the `observer` cluster in `envoy.multi.yaml`
   at `127.0.0.1:9000`, as a `STATIC` single endpoint with no ORCA.
2. **Retire the observer's own Deployment.** Remove the separate observer
   Deployment, the `observer-pods` headless Service, ORCA weighting,
   `/stats/fleet` and the observer's KEDA/HPA. Autoscale the combined pod on
   CPU, as Envoy does today (or with KEDA if that's already configured).
3. **Keep the observer's code unchanged otherwise.** `OBSERVER_INSTANCE` stays
   the pod name.
4. **Resources and policy.** Size requests and limits for both containers, and
   keep the NetworkPolicy in line (`templates/networkpolicy.yaml:52-73`).
5. **Update** `docs/DEPLOY_DOKS.md` and `docs/PRODUCTION.md`.

### 1.3 The single writer (`aintercept/internal/events/writer.go`, new)

1. **The type.**
   `type Writer struct{ in chan item; spool *Spool; uploads chan sealed; … }`.
   The `item` is `{Key string; Body []byte; ContentType, ContentEncoding string}`.
   A `Run(ctx)` select loop runs over `ctx.Done()`, `in`, a 10 s ticker and the
   byte cap. It follows the hopper pattern: a worker struct, `Run(ctx)`, and
   channels in and out.
2. **Sealing.** A batch of items is sealed by writing each item to the spool
   (`spool.Put` writes a temp file, fsyncs, renames it into `spool/ready/`, then
   fsyncs the directory) and handing the key to the uploader.
3. **Uploaders.** A small pool, 4 goroutines by default. Each reads from
   `spool/ready/`, calls `PutObject` with a 30 s timeout, and deletes the spool
   file only after a successful upload. A failure retries with capped
   exponential backoff (1 s up to 60 s) and jitter, **forever**. A file is never
   dropped.
4. **Startup.** Every file left in `spool/ready/` is re-queued. That covers a
   crash, an OOM, or a restarted container.
5. **Spool limits.** The spool's byte limit is
   `OBSERVER_SPOOL_MAX_BYTES` (the default matches the volume's size limit).
   It reports `Full() bool` past 95% and clears below 80%.
6. **Moving the existing sinks onto the writer.** Each `S3Sink` keeps its
   batching (MaxBytes/MaxEvents/MaxAge), but its seal calls `writer.Put`
   instead of queuing to its own goroutine:
   - delete `uploadLoop`, `uploads`, the queue-full drop at `s3sink.go:313-320`
     and the PUT-failure drop at `349-351`;
   - `S3Sink.Flush` becomes "seal, then `writer.Flush`";
   - `writer.Flush` waits until the spool is empty.

   `localdir.go` becomes a different putter under the same writer.
7. **Defaults.** `OBSERVER_S3_MAX_AGE_SECONDS` defaults to 10 (it's 60 today).
   Update the Helm `objectStore.maxAgeSeconds` default and `docker-compose.yml`.
8. **Metrics** on `/stats` and in Prometheus:
   - spool bytes and files;
   - age of the oldest spooled file;
   - upload attempts, failures and retries;
   - full state;
   - fail-closed rejections.
9. **Readiness.** `/healthz/ready` fails when `spool.Full()`. `/healthz` (liveness)
   stays as it is. Split the Helm readiness probe off from liveness
   (`templates/observer.yaml:149-156`).
10. **The volume.** Mount an `emptyDir` with `sizeLimit` (value
    `observer.spool.sizeLimit`, default 10Gi) at `/spool`. The container's root
    filesystem stays read-only.

### 1.4 Remove the drop paths in `events/processor.go`

1. **`Offer` (113-128).**
   - Replace the non-blocking send with a blocking send that respects the
     stream's context.
   - When the buffer is full, the ext_proc stream goroutine waits, and gRPC flow
     control pushes back on Envoy. That's the fail-closed path from 1.1.
   - Count waits that last longer than 100 ms as a metric.
   - Remove `dropped`. Keep `OBSERVER_BUFFER_SIZE`.
2. **A panic (148-157)** still recovers, but the original event is emitted
   unprocessed (`Phase`, `CorrelationID`, the raw fields) so the raw log keeps
   it. Count it.
3. **A sink error (194-211)** can only mean the writer has closed during
   shutdown, so it becomes a fatal log line, not a silent skip.
4. **Schema sampling caps (172-187, 306-309)** stay. Samples are diagnostics,
   not data.

### 1.5 The last resort, by mode

- **Observe mode.**
  - When the spool passes its high watermark, `Offer` stops waiting. The event
    is dropped, `last_resort_drops` is incremented and an alarm is logged. The
    counter is exported, and alerts on any value above zero.
  - Traffic is never blocked: the observing filter keeps
    `failure_mode_allow: true`, and `scripts/smoke-test.sh:118` keeps its
    assertion.
- **Enforce mode.**
  - When the spool is full, the enforce path (`extproc/enforce.go:66`) returns
    an error for new streams. Envoy's enforcing filter has
    `failure_mode_allow: false`, so it rejects the call.
  - Add a smoke and e2e case that fills the spool in enforce mode and asserts
    the rejection.

### 1.6 Shutdown order (with co-location)

1. **Envoy's `preStop`** (`templates/envoy.yaml:83-108`) stays as it is: sleep,
   `drain_listeners?graceful`, then wait for active requests to reach 0 or the
   drain timeout.
2. **WebSockets.** Before `drainTimeSeconds`, Envoy's `preStop` calls
   `POST /drain_listeners` (non-graceful) 60 s before the deadline. That forces
   upgraded connections closed. Each WebSocket's ext_proc stream ends, so
   `wsClose` writes its `ws_connection` row (`extproc/ws.go:176-209`). Clients
   reconnect to another pod.
3. **The observer's `preStop`** waits until Envoy's admin port stops answering
   (Envoy has exited) or the deadline is near. Only then does the observer start
   its existing shutdown sequence (`observer.go:334-393`):
   `GracefulStop` → `proc.Close` → `writer.Flush` → spool empty → exit.
4. **`terminationGracePeriodSeconds`** = Envoy drain + 60 s for WebSocket close
   + writer flush time (bounded by spool size ÷ upload rate) + margin. Default
   300.
5. **Data on a SIGKILL.** Anything already in the spool survives a restart of
   the same pod. If the node is lost, `emptyDir` is lost with it; that's the
   accepted bound. What's at risk is the ≤ 10 s of unsealed buffer plus any
   unuploaded spool. Document it in `docs/PRODUCTION.md`.

### 1.7 Tests and the gate for phase 1

- **Unit tests** (`events/writer_test.go`):
  - nothing is ever dropped under random PUT failures;
  - a spool left by a crash is uploaded on restart;
  - `Full()` follows the high and low watermarks;
  - `Flush` waits for the spool to empty;
  - ordering within a key is kept.
- **Processor test:** replace `processor_test.go` `DropsWhenBufferFull` with
  `BlocksWhenBufferFull`.
- **End to end** (`internal/e2e`, run by `make test-interceptor`): send N
  requests (mixed HTTP, SSE, WebSocket, analytics) while:
  - MinIO is stopped for 2 minutes, then restarted;
  - the observer container is killed (SIGKILL) and restarted;
  - the spool is forced full: in observe mode, traffic keeps flowing and drops
    are counted; in enforce mode, requests are rejected and accepted again once
    the spool drains.
- **Gate:**
  - **No call goes missing short of a full spool.** Through S3 outages shorter
    than the spool's capacity, and through observer SIGKILLs, every request has
    exactly one `complete` (or `disconnect`) row in the bucket. Counted by
    correlation id.
  - **Every drop is counted.** Drops happen only past the spool's high
    watermark, and each one is counted. There are no silent drop paths left.
  - **Enforce mode never lets an unrecorded call through.**
  - `make check` and `make test-interceptor` pass.
- **Deploy** to the demo cluster with the spool volume. Watch spool depth and the
  last-resort counter for a day.

---

## Phase 2: finish the table work and make it deterministic

This is the existing precompute work, finished. Nothing here is deployed. Its
outputs, the seven tables and their API SQL, are the **contract** phases 4 and 5
must produce exactly. Its single-pass builders are the **reference** the
incremental processor is tested against.

### 2.1 Finish table 7

Apply `stash@{0}` on a branch `precompute-person-map` from `precompute-event-rows`.

1. **Go: `process/personmap.go`.** It builds:
   - `person_map` (person, account, directory);
   - `person_sessions`;
   - `account_facts` (org, plan, codex_plan);
   - `account_devices` (devices and the newest env per account and device);
   - `call_tool_uses`.

   It also adds columns to `session_rows` (`prompt_ids`, `devices`,
   `call_accounts`, `queries`, `bypassed`, `cli_version`, `rl_*`, `sandbox`,
   `custom_ca`, `limits_ns`), to `session_marks` (token columns, `prompt_id`,
   `unreported`) and to `event_rows` (tool, hook, argv0, category, PR, cost
   columns).
2. **API.** `store.py` loads the five tables. `routers/people.py`,
   `directory.py` and `rollups.py` (new) move these onto SQL:
   - `/people`;
   - `/overview`;
   - `/people/{id}`;
   - `/people/{id}/summary`;
   - `/coverage`;
   - the person→sessions lookup.

   Delete `email_groups`, `_attribution`, `_outside`, `header_facts`,
   `event_rollup`, `call_tokens`, `tool_uses` and `latest_md`.
3. **Parity, using the fast gate:**
   - the derive against the answer key on the two-day capture, at the four
     fixed now values;
   - four stress replay windows;
   - endpoint parity for every endpoint switched;
   - re-checks of tables 1–6 and their endpoints.

   All must show 0 differences.
4. **Fix `verify.go:131-153`** so its `Diff` covers `request_rows` and
   `person_last` too. Today it loads them but doesn't compare them.

### 2.2 Make the reference deterministic

Every value must depend only on the **set** of records, never on their order. The
review found these order dependences:

| Where | Today | Becomes |
|---|---|---|
| `shape.go:385-396` account email/name | the latest *arriving* sighting wins | latest by `(ts, record id)`, field by field |
| `shape.go:294-352` `minStr` person/org/device | lexically smallest | unchanged (already order-free) |
| `requestrows.go:781-788` reports | first in table order | earliest `(ts, event id)` |
| `requestrows.go:1025` `nearestRetry` ties | table order | `(ts, call id)` |
| `requestrows.go:847-852` prompt index merge across sessions | "later wins" | latest `(ts, event id)` |
| `sessionrows.go:84-94` env/device pick | strict `>`/`<` on ms client time | `(ts, event id)` |
| `sessionrows.go:637` Codex limits | `>=` | `(ts, call id)` |
| `prompt_rows`/`marks`/`event_rows` same-time ordering | `(ts, corr, n)`, with `n` per part | `(ts, corr, n)`, with `n` per batch (already fixed in `eventrows.go` `renumberSpanning`) |

- **Tests.** Each change gets a Go test that shuffles the input and checks the
  output is identical.
- **The answer key.** `oracle.py` and `det.py` (gitignored) pin the same rules,
  so main's Python and the reference agree.
- **The bug list.** Record each change in the parity oddities list as a
  deliberate behavior pin.

### 2.3 Inventory what's left (as of the precompute PR)

After 2.1, these API queries still read a fact table or a snapshot instead of a
precomputed table. The files are in `api/src/aintercept_api/`.

| Endpoint | Function | Reads | Where |
|---|---|---|---|
| `GET /status` | `get_status` | `calls`, `analytics`, `probes` (first and newest data, probe times) | `routers/status.py:11-16` |
| `GET /overview` | `directory.overview` | `calls` (requests in the window) | `directory.py:222-226` |
| `GET /people`, `/overview` | `_DIRECTORY` | `sessions` snapshot (drops sessions far outside the range) | `directory.py:122-125` |
| `GET /people`, `/overview` | `_MEMBERS` | `accounts` snapshot (names, emails) | `directory.py:128-133` |
| `GET /people/{id}` | `get_person` | `accounts` snapshot (name, email) | `routers/people.py:244-252` |
| `GET /people/{id}/*` | `rollups.linked` → `cc.linked_accounts` | `accounts` (email group) | `claude_code.py:149-158` |
| `GET /requests/{id}` (drawer) | `detail` → `load_wire` / `_wire_from_tables` | raw files; `calls`; `cc_events` (a batch's session) | `request_detail.py:200-337` |
| | `detail` → `cc.load_session` | `cc_calls`, `hidden_calls`, `sessions`, `cc_events` | `claude_code.py:198-320` |
| | `facts` → `ss.events(about=…)` | `cc_events` | `request_detail.py:498`, `session.py:157-212` |
| | `position` → `ss.steps`, the message-id chain | `cc_calls`, `calls` | `request_detail.py:603-638`, `session.py:121-135` |
| | `find_report`, `reported_detail` (`reported:` ids) | `cc_events` | `request_detail.py:881-909` |
| `GET /requests/{id}/raw` | `raw` → `load_wire` / `find_report` | as above | `request_detail.py:1015` |
| `GET /requests/{id}/details` | `details` → `_event_rows` → `pp.prompt_indexes` | `cc_events`, `cc_calls` | `request_detail.py:1084-1102,1174`, `person.py:239-256` |
| `GET /events/{id}` | `event` | `cc_events`, then the raw file | `request_detail.py:1386-1406` |

No endpoint calls `rows.request_rows`, `pp.transactions`, `pp.key_hashes`,
`pp.reports`, `pp.retries`, `cc.load_person`, `cc.person_sessions` or
`cc.unattributed_sessions` any more. They stay as the test suite's request-row
parity reference (`tests/conftest.py:132-155`).

Each remaining read gets a home in a later phase:
- **The `accounts` reads and the email group** become dimensions (4.6).
- **The drawer** reads per-call detail objects plus per-key outputs (3.2, 4.9).
- **`/status` and the overview's request count** become a count over the
  bucketed records.
- **The `sessions`-snapshot range filter** becomes `session_rows`.

### 2.4 The deliverable

**One PR: "Precompute: the seven tables, every endpoint SQL"** holds the whole
stack. It's merged so that main carries the reference builders, the table
schemas, the SQL endpoints and the parity tooling that phases 4 and 5 build on.

### 2.5 Merged, not deployed (decided)

The precompute PR is merged as the **reference implementation and the API
contract** for the streaming pipeline. It is **not deployed**, and the cluster
stays on today's processor.

**Why it isn't deployed:** memory.
- **Full history:** the derive step over the demo cluster's whole history (22
  days, 7.9 GB of raw capture) peaks at **1.9 GB RSS**, with about 930 MB of live
  heap.
- **Two days:** the same step over two days peaks at 705 MB RSS, with 337 MB of
  live heap.
- **Growth:** live heap grows about 145 MB per GB of raw. Recent days bring
  1.8–3.1 GB of raw each, so that's roughly **+250–450 MB per day**.
- **The limit:** the nodes have about 2.9 GiB allocatable each, so it would
  outgrow them within days.

The person page stays slow until phases 3–6 replace the full rebuild.

---

## Phase 3: canonical records at ingest

The observer starts writing the processor's new input **alongside** today's raw
log, through the single writer. The old processor keeps running unchanged.

### 3.1 The canonical record (`aintercept/internal/records`, new package)

- **One Parquet schema, versioned (`record_version`).** Its columns are the
  union of today's `Request` (`process/tables.go:9-91`) and `Event`
  (`tables.go:93-130`), plus:
  - `record_id`: `<incarnation>-<seq>`. The incarnation is the pod name plus the
    process start time in nanoseconds plus 4 random bytes. That's unique across
    container restarts, which fixes the hostname reuse at `observer.go:509-514`.
  - `kind`: `call_start`, `call_end`, `ws_turn`, `ws_connection`,
    `client_event`, `verdict`, `analytics_batch`.
  - `call_id`: `correlation_id`, or `correlation_id#turn` for a WebSocket turn,
    as today.
  - `routing_key` and `bucket` (3.3).
  - `batch_id` and `batch_n`: an analytics event's batch and its number in the
    **whole** batch, assigned at ingest where the whole batch is in hand. That
    retires the processor's per-part numbering and `renumberSpanning`.
  - `event_key`: the record id for client events. This replaces
    `corr/h<hash>-N`, which removes the duplicate-key problem (PR #12) for new
    data.
- **Codegen.** Go struct tags drive the Parquet schema, as `tables.go` does
  today. A test pins the column list, so the schema can't change silently.

### 3.2 Move assembly from the processor into the observer

Today `shape.go` pairs phase rows across files, because rows of one call could
arrive in different files. Inside the observer, all of a call's rows pass
through one ext_proc stream, so assembly becomes per stream.

1. **`call_start`**, emitted when the request is known:
   - for HTTP, at request end of stream (headers and body, so the model and
     metadata are parsed);
   - for WebSocket, at each turn's `response.create` (`extproc/ws.go:107-111`).

   It holds the fields `requestFrom` fills from the pending state today
   (`shape.go:508-557`), and the client headers (`pendingClientHeaders`,
   `shape.go:24-28`). This is what lets the UI show a call while it streams.
2. **`call_end`**, emitted at completion (`emitComplete`, `server.go:465-511`),
   on a disconnect (`server.go:213-220`), and at each WebSocket turn's end.
   - It holds everything `requestFrom` produces.
   - Request and response byte counts are summed in stream state (the
     `RequestBytes += …` at `shape.go:175-180` moves here).
3. **`ws_connection`**: unchanged in content (`ws.go:176-209`).
4. **Provider classification** (`products.go:20-61`, `applyCodex` `85-111`,
   `setClient` `shape.go:584-595`) moves into the canonicalizer, so every record
   carries `product`, `client`, `client_version`, `hidden_kind`, `session_id`,
   `prompt_id` and `ext_json`, exactly as the processor sets them today.
5. **Client events.** `eventFrom` (`shape.go:597-646`) moves into the
   canonicalizer: one `client_event` record per event, with session and prompt
   resolved exactly as today.
6. **The analytics request.** Its own call records carry
   `batch_session = the earliest event's session` (the rule fixed by the
   replays, `requestrows.go:291-303`).
7. **Per-call detail** (full interception mode):
   - At call end, `DetailFromBodies` (`events/detail.go:56-69`) runs in the
     observer on the stream's buffered bodies.
   - The writer writes `calls/<call_id>.json.gz` holding headers, the detail,
     and the bodies, capped as today (`maxCallBody`, `shape.go:61`).
   - That retires `pending_bodies` and the drawer's raw-file scans.
8. **What retires once this lands:** `pending`, `pending_bodies`, the done
   markers, `pendingTTL` expiry and the global `newestNS` clock
   (`shape.go:16-21,238-282`). A call whose pod died has a `call_start` and no
   `call_end`, and so does a one-pass run over the same records.
9. **Today's raw ndjson log** (headers, body chunks, phase rows) stays exactly
   as it is, written through the same writer. It's the raw source of truth and
   the replay input.

### 3.3 Routing keys and buckets (`records/route.go`)

1. **The routing key:**
   - `s:<session_id>` when the record has one. For a Codex record that's the
     thread id. For an analytics event it's the event's own session. For the
     analytics request's call records it's `batch_session`.
   - Otherwise `c:<key_hash>:<YYYY-MM-DD of ts>`. That covers flag and catalog
     calls, Codex usage polls, claude.ai chat turns and similar.
2. **The bucket:** `bucket = fnv64a(routing_key) >> (64 - BUCKET_BITS)`, with
   `BUCKET_BITS = 6` (64 buckets). The value is stored in the record and in the
   object key.
3. **Records with two keys get both.** A record that two keys need is written
   once per key, each copy with its own routing key and the same `record_id`.
   Expected uses:
   - a Codex HTTP call before its thread id is known, if the spike in 3.4 finds
     any;
   - claude.ai `/api/account` bootstrap rows, which feed account identity.
4. **Object key:** `records/v1/bucket=NN/minute=<upload UTC minute>/<incarnation>-<seq>.parquet`.
   The minute is the writer's **upload-time** minute, taken when the batch is
   sealed, so a spool replayed after an outage lands at the current minute.

### 3.4 Spike: when the session is known

Assert in tests where each client's session id first appears:
- Claude Code: request header;
- Codex HTTP: `x-codex-turn-metadata`;
- Codex WebSocket: per turn;
- analytics: per event.

Any client whose session appears only at completion gets the two-key write from
3.3. The capture in `data/parity` is used to check this (locally, never
committed).

### 3.5 A tool to canonicalize the old raw log

`aintercept canon -src <raw ndjson dir|s3://…> -out <records dir>` runs the new
assembler and canonicalizer over the old raw log.

- It's how existing data is backfilled (phase 6).
- It's how the replay harness gets records (phase 4).
- It's how 3.6 is proven.

### 3.6 The gate for phase 3

- **`canon` over the two-day capture matches today's processor.** The
  `call_end` records must equal today's `requests` table row for row, field by
  field. The `client_event` records must equal `events`, except `event_key`,
  which is compared through a mapping table.
- **The live observer matches `canon`.** Replay the capture's raw rows through
  the observer's code path (`aintercept replay` with a canon sink), and the
  records must equal what `canon` produces from the same raw rows.
- **Tests and checks:** `make check`; the phase 1 e2e test with records
  counted; deploy to the demo cluster and run `canon` over the same hours
  daily. The records it produces must equal what the observer wrote.

---

## Phase 4: the bucketed processor (`aintercept/internal/stream`, new)

It's built next to `internal/process`, which keeps running until phase 6. It
reads `records/v1`, writes `v1/` tables, and is driven by `aintercept stream`.

### 4.1 Store primitives (`process/store.go`, extended)

Add `PutIfAbsent(ctx, key, body) (created bool, err error)`, which is S3
`If-None-Match: *`. The local-directory store does it with `O_CREAT|O_EXCL` and
a rename, and the test emulator does it in memory.

Add `ListAfter(prefix, startAfter)` paging, and `Stat` with `LastModified` for
lease ages measured on S3's clock.

### 4.2 The per-bucket commit log

There's no global manifest.

- **Commits.** Each bucket has `buckets/NN/commit-%012d.json`, created only with
  `PutIfAbsent`. A commit names:
  - the run files written for each table;
  - the state checkpoint files;
  - the watermark: the last fully processed minute, plus the names of files
    processed in the grace window;
  - the lease epoch that wrote it;
  - the current base file per table, and the generation.
- **Readers** (the API and the next owner) find the newest commit by listing
  from the last commit they know.
- **The commit is the compare-and-swap point.** A paused old owner can't create
  `commit-(n+1)` once the new owner has. Its run files stay unreferenced and are
  deleted after the grace period. That's the fencing.

### 4.3 Leases (`stream/lease.go`)

1. **Claim.** `leases/bucket=NN/epoch-%010d` is created with `PutIfAbsent`. The
   body holds the replica id and the time it was claimed.
2. **Renew.** Every 10 s, the owner creates the next epoch. The age of the
   newest epoch is judged by S3's `LastModified` against the S3 `Date` header,
   never by pod clocks.
3. **Takeover.** A bucket whose newest epoch is older than 30 s is free. Any
   replica may create the next epoch.
4. **Balancing.** Each replica lists the live leases. It claims free buckets up
   to `ceil(B / live replicas)`, and releases any above that by letting them
   lapse. A replica that's shutting down stops renewing after its final commit.

### 4.4 The cycle (`stream/cycle.go`)

The cycle is a worker in the hopper pattern: a lister, then a reader pool, then
per-bucket appliers, then a writer.

1. Per owned bucket, every `STREAM_CYCLE_SECONDS` (5 by default):
   1. List `records/v1/bucket=NN/minute=` prefixes from the watermark minute
      minus the grace window (5 min) up to now. Skip file names already in the
      watermark set.
   2. Read the new files: Parquet, only the needed columns.
   3. Group the records by routing key and order them within a key by
      `(ts, record_id)`.
   4. Apply each record to its key's state (4.5).
   5. Write one run file per table that changed:
      `v1/tables/<table>/bucket=NN/run-%012d.parquet`. Rows are upserts keyed by
      row id; a deletion is a tombstone row.
   6. Write the touched keys' state: `v1/state/bucket=NN/run-%012d.parquet`
      (key → state blob).
   7. Create `commit-(n+1)`.
2. **Memory.** An LRU keeps touched keys' state, capped by bytes. A key that
   isn't in memory is loaded from the newest state file that holds it. A
   per-bucket key→file index lives in each commit, and compaction (4.8) keeps it
   short.
3. **Concurrency.** Buckets are processed in parallel up to `GOMAXPROCS`. Each
   bucket is single-threaded, so no state needs locks.

### 4.5 Per-key incremental state: porting the reference builders

Each table's single-pass builder (PORT) is restructured into **per-record
updates** over per-key state. The builders stay as the reference.

The work goes table by table, in the same order as the precompute work, and each
table has its own gate (4.11).

| Table | Key | State per key | What a record touches | Values that move to query-time lookups |
|---|---|---|---|---|
| **request rows** (PORT `requestrows.go`) | `s:` session / `c:` credential-day | the key's transactions indexed by `request_id`/`client_request_id`; reports by request id; retry index by (prompt, class, time); step index per prompt | its own row; the matched report row; the previous/next attempt's retry fields | **Person view:** `request_rows` stops being stored per person. Rows are stored per key and carry `session_id`, `key_hash` and `account_id`. The person view is a SQL union of the person's session keys and credential-day keys (4.6). TTFT and cache from reports, and the person-wide prompt-index merge, become joins to small per-key tables (`report_facts`, `prompt_index`). `person_last` becomes `max(last_ns)` over the person's keys. |
| **prompt rows / steps** (`promptrows.go`) | session | prompts sorted by `(start, prompt id)`; running totals per prompt; open-tail flags; hidden-call assignment | its prompt's row; its step row; at most one neighbouring prompt (hidden calls) | `session_last_ns` → join to the `session_rows` key. Positional `seq` → a sort key, with `row_number()` in SQL. Codex turn numbers → `dense_rank()` over the first call per turn. |
| **session rows** (`sessionrows.go`) | session | min/max/sums; coverage windows (interval list); newest-by-`(ts,id)` picks for env, device, version, limits | the one session row | `children_json` → `session_edges` (parent, child, kind), joined. Codex titles → per-account `title_calls` and `thread_starts`, resolved with a DuckDB `ASOF JOIN` within 5 minutes. |
| **session marks** (`sessionmarks.go`) | session | the session's marks by `(ts, kind, id)` | appended mark rows; the network or permission segment the record extends | prompt index on marks → join to `prompt_index` |
| **presence** (`presence.go`) | session | blur and sleep intervals; open and last | the segments the new window overlaps | already combined per person in SQL (`timeline.py:132-162`), unchanged |
| **event rows / session tools** (`eventrows.go`) | session | event links by request id and tool id | its own row; `first_ns` per tool | `#n` labels and prompt index → joins to `prompt_index`. Call labels (class, model, stop reason) → join to the call's request row. |
| **session request rows** | session | — | — | stops being stored. It's the request-rows query scoped to one session. The Codex rule that attaches sessionless calls by time becomes a SQL range join over the credential-day keys of the thread's credentials, within `[start, last+60s]`. |
| **table 7** (`personmap.go`) | dimensions (4.6) | — | — | the person map, person sessions and account facts become the dimension tables of 4.6 |

**For every table:**
- **The order test.** A randomized Go test shuffles the key's records, applies
  them one at a time, and requires the output to equal the reference builder
  over the same set.
- **The flat-cost test.** It applies record N+1 to a key that already holds 10,
  1,000 and 100,000 records. Work and allocations must not grow.

### 4.6 Dimensions: values that span keys

These are small tables, written by each bucket's applier as upsert rows for the
facts that bucket sees:

| Table | Row | Replaces |
|---|---|---|
| `dim_session_account` | (session, account, first_ts, record_id) | `byAccount`, session `person_id` |
| `dim_session_credential` | (session, key_hash) | `callHashes`, `eventCorrs → corrHashes` |
| `dim_account` | (account, email, name, ts, record_id), latest by `(ts, id)` | the `accounts` snapshot |
| `dim_account_device` | (account, device, env, ts) | `person_devices`, `account_devices` |
| `dim_thread` | (credential, client, session, start, last) | Codex time attribution |
| `title_calls`, `thread_starts` | per account | Codex titles |
| `session_edges` | (parent, child, kind) | lineage, `children_json` |

**A person is resolved at query time:**
1. Email groups come from `dim_account`, which is tiny: one row per account.
2. Then accounts → sessions through `dim_session_account`.
3. Then sessions → credentials through `dim_session_credential`.
4. "Unattributed" means sessions with a call and no row in
   `dim_session_account`.

All of it is SQL. Nothing is stored per person, so a new link changes one row,
not history.

### 4.7 Time-dependent values

Today's `now()` CASE expressions (live, streaming, in flight, open tails) stay in
SQL, over stored inputs. Per-key "last" values are joined in from `session_rows`
instead of being copied into rows (4.5).

### 4.8 Compaction and garbage collection (`stream/compact.go`)

- **When.** It runs within the cycle once a bucket has more than 64 run files
  for a table, or every 10 minutes.
- **What it writes.** It merges `base-<gen>` + runs into
  `base-<gen+1>.parquet`. Rows are sorted by key and then row id, the newest
  version wins and tombstones drop out. It writes per-file min and max of the
  key into the commit, so readers can skip files.
- **The state files** are compacted the same way.
- **Garbage collection.** A file is deleted only when no commit from the last
  10 minutes names it.

### 4.9 The request drawer

**What it reads.** `detail`, `raw` and `/events/{id}` read
`calls/<call_id>.json.gz` (3.2), plus the event rows of the call's key.

**What's precomputed.** The drawer's derived parts become per-key outputs of the
request-rows state:
- `facts` (`request_detail.py:498-526`): events about the call.
- `position` (`603-638`): steps and the message-id chain.
- `pp.prompt_indexes`.

**What it no longer reads:** raw-file listings (`store.py:546-606`,
`raw.py:88-108`).

**Old calls.** Calls captured before phase 3 have no detail object. The drawer
falls back to today's raw read for them until the backfill (6.3) writes their
detail objects.

### 4.10 The lag scaler (`stream/scale.go`)

- **Lag, per bucket:** `now − mtime(oldest record file after the watermark)`, or
  0 if there's nothing to process. Each replica exports this as a Prometheus
  gauge, `stream_bucket_lag_seconds{bucket}`. **Pipeline lag** is the maximum
  over all buckets.
- **The lag endpoint.** Every replica serves `GET /lag` on its HTTP port as
  JSON: `{"lag_seconds": <pipeline lag>, "backlog_files": <n>}`.
  - It computes this from S3: each bucket's newest commit, plus the files listed
    after its watermark.
  - It's cached for 10 s.
  - Every replica returns the same answer, so it doesn't matter which one KEDA
    reaches.
- **Scaling with KEDA (decided; KEDA already runs in the cluster).** A
  `ScaledObject` (`templates/stream.yaml`) uses the **`metrics-api`** trigger
  against the processor Service's `/lag`, with `valueLocation: lag_seconds`.
  This needs no Prometheus, no scaler lease and no RBAC.
- **Policy:**
  - target value `STREAM_LAG_TARGET` (30 s), polled every 15 s;
  - scale out quickly (`scaleUp` stabilization 60 s) and in slowly (`scaleDown`
    stabilization 600 s);
  - `minReplicaCount: 1`, `maxReplicaCount: B`.
  - With the chart value `stream.autoscaling.enabled: false`, there's a fixed
    replica count instead.

### 4.11 Replay harness v2 and the gate

**`aintercept replay` is extended** to take `-ingest-pods N` and
`-processors R`. It:
1. canonicalizes the capture with the phase-3 code;
2. spreads connections across N ingest writers at random;
3. runs R stream replicas against an in-memory S3 emulator with real
   conditional-write semantics;
4. injects chaos throughout: kills (graceful and hard), lease steals, paused
   owners resuming late, PUT failures, delayed uploads, and replicas added and
   removed.

**At each checkpoint and at the end**, it compares every table against the
phase-2 reference builders run once over all records. The endpoint parity tools
compare the API (phase 5) the same way.

**Gate per table, then for the whole pipeline:**
- 0 differences on the full two-day capture, for N ∈ {1, 3, 8} and
  R ∈ {1, 3, 8};
- 0 differences at every checkpoint in all four stress windows;
- **flat cost:** the capture replayed with 2×, 4× and 8× synthetic history
  before it, with per-record time and per-replica memory flat to within 10%;
- **lag:** a 10× burst drives lag up, the scaler adds replicas, and lag returns
  under target with no loss or duplicates.

---

## Phase 5: the API reads the bucketed tables

### 5.1 A new store (`api/src/aintercept_api/store.py`, rewritten)

1. **Remove the whole-history load:** `ingest`, `maybe_ingest`, `_WIRES`, the
   `mirror` sidecar (`templates/api.yaml`, `process/mirror.go`), and the
   `shaped`/`db` emptyDirs.
2. **Resolve a request in five steps:**
   1. Resolve keys through the dimensions (4.6). The dimension tables are
      cached by commit and refreshed by listing each bucket's newest commit,
      at most once a second per bucket.
   2. Map keys to buckets with the same `fnv64a` rule. The function is shared
      by a test vector checked in both Go and Python.
   3. For each bucket, read the base file (skipping files by key min and max)
      plus the run files since, and build a view where the newest row wins.
   4. Run today's endpoint SQL, plus the joins from 4.5, on DuckDB.
   5. Cache every object fetched from S3 in a local, byte-capped LRU. They're
      all immutable, so the cache needs no invalidation.
3. **Fetching** uses boto3, which is already a dependency through the API's S3
   access, or the plain S3 HTTP API. DuckDB reads only local files, so there's
   no `httpfs` extension and no new dependency.

### 5.2 The endpoints, one at a time

Every endpoint in the phase 2.3 inventory is moved and checked for parity. The
order:
1. the session endpoints;
2. `/people/{id}/requests` and `/timeline`;
3. person header and summary, and `/coverage`;
4. `/people` and `/overview`;
5. `/status`;
6. the drawer.

The UI samples are regenerated (`make regen`), and the diff must be empty.

### 5.3 Replicas

The API Deployment drops `replicas: 1` and gets an HPA on CPU (built in, no
adapter). Stateless replicas can serve any request.

### 5.4 The gate for phase 5

- **Endpoint parity.** Every endpoint's response is byte-identical to the
  phase-2 API on the same data, at the fixed now values, over the two-day
  capture and the four windows.
- **Timings.** A person-page poll and a session-page poll are measured locally,
  and the p95 recorded.
- **`make check`** passes.

---

## Phase 6: lag scaling, cutover, deletion

### 6.1 Helm

- **New: `templates/stream.yaml`.** A Deployment with no fixed `replicas` (the
  scaler owns it), `RollingUpdate`, the scaler RBAC, `/healthz` and a metrics
  port. Values: `stream.{bucketBits, cycleSeconds, lagTarget, minReplicas,
  maxReplicas, resources}`.
- **The ingest pod** gets the phase-1 changes.
- **The API** loses its sidecar and gains its HPA.

### 6.2 Run old and new side by side

On the demo cluster (stitchwork `deploy/aintercept/values.yaml`):
1. Deploy with ingest writing raw and records, the old processor writing
   `shaped/`, and the stream processor writing `v1/`.
2. Run a second API Deployment, `api-v1` (internal only), that reads `v1/`.
3. A parity job (`aintercept parity-live`) compares every endpoint between
   `api` and `api-v1` every 15 minutes, at the same pinned now, for the people
   and sessions active in the last hour.
4. Soak for 3 days with **0 differences**.

### 6.3 Backfill history

1. `aintercept canon -src s3://…/ailogger -out s3://…/records/v1` re-canonicalizes
   all existing raw data. Its minutes come from the original upload times, so
   the watermarks see it as old data. The stream processor picks it up through a
   one-time watermark reset per bucket (`aintercept stream -rewind`).
2. The detail objects for historic calls are written by the same `canon` pass.
3. **The gate:** `v1/` tables over all history equal a one-pass reference over
   all history.

### 6.4 Cutover

1. The UI's `api` Service points at `api-v1`.
2. Watch for a day.
3. Stop the old processor.

**Rollback** is to point the Service back. The old processor's output stays in
the bucket for 7 days.

### 6.5 Delete the old path

Delete:
- `internal/process` processor, shape snapshots, derive and the mirror;
- `templates/process.yaml`;
- the old store code;
- `rows.request_rows` and the other Python derivation the parity tests no
  longer need.

The reference builders move to `internal/stream/reference` and stay as the test
oracle.

---

## Phase 7: the behavior fixes

One PR, stacked on phase 6. It fixes the 50 entries on the parity oddities list
in Go, regenerates the UI samples, and re-runs the replay gate against the
corrected reference. Each fix gets a test. Examples:
- a call reporting 0 output tokens counted as missing;
- `bypassPct` measured against the wrong base;
- a prompt number that rounds in one place and truncates in another;
- the 10-minute and 5-minute windows disagreeing.

---

## Decisions

Decided on 2026-11-12:

1. **Each ingest pod is the full stack.** Envoy and the observer run in one pod
   and scale together (1.2).
2. **Only enforce mode fails closed.** Observe mode does its utmost not to drop
   data, but never breaks traffic (1.1, 1.5).
3. **The phase 2 stack is merged but not deployed:** its memory grows with
   history, about 1.9 GB over 22 days and 250–450 MB more a day (2.5).
4. **Lag scaling uses KEDA's `metrics-api` trigger** on the processor's `/lag`
   endpoint. KEDA already runs in the cluster (4.10).
