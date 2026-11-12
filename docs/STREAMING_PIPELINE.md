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
- **Data completeness comes first.** The pipeline must never lose a captured
  record. If it can't store records, it stops AI traffic rather than let traffic
  go through unrecorded.
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
- **Fail closed:**
  - If the spool fills, the observer stops accepting new streams. Envoy's
    ext_proc runs with `failure_mode_allow: false`, so AI traffic is rejected
    instead of passing through unrecorded.
  - If the observer is down, the same rule applies.
  - Spool depth and fail-closed events are metrics with alerts.

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
- **Who measures it.** Each replica publishes its buckets' lag as a metric,
  alongside the watermark.
- **Who scales.** A scaler loop inside the processor, run by whichever replica
  holds the scaler lease, reads every bucket's watermark from S3.
  - It sets the Deployment's replica count through the Kubernetes scale API,
    with RBAC limited to that one Deployment's `scale` subresource.
  - It scales out when lag stays above a target (for example 30 seconds) and
    scales in when lag stays near zero.
  - Its limits are a minimum of 1 and a maximum of B.

  This adds nothing to the dependency list. Kubernetes' own HPA can't read a
  custom metric without a metrics adapter, which would be a new component.
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

## Build order

The current deployment is broken at three people, so the build starts with what's
broken now:

1. **Stop losing data.** The single writer, the disk spool, failing closed, and
   the `preStop` ordering. Data is being lost today.
2. **Canonical records,** by bucket and minute, plus the per-call detail objects.
3. **The bucketed processor:** leases, watermarks, per-record updates, run files
   and compaction. It's proven against the answer key with the distributed
   replay.
4. **The API read path,** with endpoint parity against today.
5. **The lag scaler,** then deploy.

Each step has to pass the same gate before the next one starts: an exact match
with the answer key, and flat cost.
