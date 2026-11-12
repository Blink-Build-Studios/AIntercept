# Streaming pipeline: the data path at any scale

Status: **proposed, for review.** It replaces the single batch processor
(`aintercept process`) and the API's whole-history load. The query API's
contract does not change: the UI, its samples and every constraint in
`docs/ui/BUILD_PLAN.md` stay exactly as they are.

## What it has to do

AIntercept sits on the path of every AI call a company makes, so its cost has to
follow the traffic. The target is a company of 21,000 people behind, say,
30 proxies. That rules out three things:

- **No single process.** No component holds, reads or rewrites all of history,
  and nothing exists at a fixed count of one that everything else waits on.
- **No batch passes.** Records are processed as they're formed. A run's cost is
  set by what arrived, never by how much is stored.
- **No new infrastructure.** S3 (or any S3-compatible store) is the only backing
  store. There's no database, no queue service, no table engine. Everything is
  Go and Python code in this repo.

It also has to end up exactly right: the tables must equal what one pass over
all the records would produce. That holds whatever the arrival order, however
late the data, however many duplicates arrive, however many pods there are, and
whatever restarts, scale-ups, scale-downs or crashes happen along the way.

**Scale, for sizing.** Today's capture is 12 people producing about 1.5 million
records a day, and they're heavy users. At that rate, 21,000 people would be
about 2.7 billion records a day: around 31,000 a second on average, or about
1,000 a second for each of 30 pods. The design must hold at peaks several times
that.

## The shape

```
              round-robin L4 load balancer (connections)
                 │             │             │
   ┌─────────────▼──┐  ┌───────▼────────┐  ┌─▼──────────────┐
   │ pod            │  │ pod            │  │ pod            │   … N pods, scaled
   │ Envoy          │  │ Envoy          │  │ Envoy          │     with traffic
   │  └ext_proc→    │  │  …             │  │  …             │
   │   observer ─┬─────────────► S3: raw log (durable)      │
   │             └→ processor ──► S3: session state + rows  │
   │ API (stateless) ◄────────── S3: tables                 │
   └────────────────┘  └────────────────┘  └────────────────┘
```

**Each pod is the whole stack:** Envoy, observer, processor and API. Scaling up
adds pods; scaling down removes them. Pods never call each other. Everything
they share is in S3, and every shared write is a conditional write (below), so
they never need to coordinate any other way.

**Routing doesn't affect correctness.** The load balancer spreads connections
round-robin. Because clients keep their connections open, a session usually stays
on one pod, and the design takes advantage of that as an optimization (a warm
in-memory cache). It never relies on it. A session whose requests land on three
pods ends with the same rows as one that stayed on one.

## The data path

### 1. Observer → raw log and processor

Envoy's ext_proc streams each HTTP transaction's lifecycle events (headers, body
chunks, completion, disconnect) to the observer in its own pod. One transaction
is one ext_proc stream, and a stream never leaves its pod. As each record is
formed, the observer does two things:

1. **It appends the record to the raw log in S3.** This is the durable source of
   truth, the same ndjson.gz files as today, partitioned by
   `dt/hour/instance`.
2. **It sends the record to the pod's processor over a Go channel.** The
   processor never polls S3 for new work.

**Flushing: a timer and a size cap, uploads off the hot path.** The sink is one
goroutine running a `select` over three cases:
- an incoming record, which is appended to the buffer;
- a **10-second ticker**;
- the buffer reaching its **byte cap**.

Whichever comes first, the buffer is handed to a **separate upload goroutine**
and a new buffer starts. The loop never waits on S3.

The uploader retries each file until S3 accepts it. A file waiting for a retry
stays queued; it's never dropped. So the worst case is 10 seconds of records in
memory, and under load the cap flushes much sooner. Lots of small files are fine:
data completeness is the priority, not the object count. (Today's sink waits up
to 60 seconds.)

**Backpressure.** If the processor falls behind, the channel fills and the
observer keeps writing the raw log. It never blocks Envoy (Decision 1). The
processor then catches up from the raw log, through the same replay path as in
§4. A full channel costs freshness, never data.

### 2. Processor stage A: assemble the call (in memory, pod-local)

A transaction's events all arrive at one pod, so assembling them into a call
needs no shared state. Memory is bounded by the calls in flight in that pod, not
by history. This is today's shaping code, `internal/process/shape.go`, fed from
the channel instead of from files. A finished call becomes one **call record**.
Analytics uploads (Claude Code, Codex and claude.ai client events) become
**event records**. Each record carries a globally unique id.

If a pod dies, its unfinished calls are finished by another pod; see "Crashes"
below.

### 3. Processor stage B: update the session (S3, conditional writes)

Each call or event record belongs to a **key**:
- a session id;
- for records that carry no session, a fallback key: the credential and client
  for unattributed calls, or the batch for analytics.

**The update is incremental, even within a session.** A record changes only the
rows it affects, and nothing ever rewrites a whole session. Every derived value
is one of three kinds:

1. **Append-only rows.** Event rows, a call's request row and step, and a
   session's marks. A new record adds its own rows and touches nothing else.
   Constant cost.
2. **Values that depend on a few neighbors.** These are updated through indexes
   kept in the session's state:
   - **Captured vs. reported:** a map from request id to row. Whichever of the
     report and the call arrives second updates one row.
   - **Retries and prompt membership:** a sorted index by time per prompt finds
     the nearest neighbor, so one or two rows change.
   - **Prompt totals** (steps, tokens, tools, friction, duration): running
     totals per prompt. A step adds to them, and a turn's end sets the duration.
     One prompt row changes.
   - **Presence:** an interval index of blur and sleep windows. A new window
     merges with at most its neighbors and changes only the segments it
     overlaps.
   - **Session totals** (counts, first and last, coverage inputs): running
     totals.
3. **Positions that a late arrival would shift:** step numbers, prompt numbers
   and row order. The tables store a sort key (time, then id), not the number,
   and the query numbers the rows it returns with `row_number()` over the prompt
   or session shown. A late call shifts nothing in storage. Decision 4 allows
   this kind of per-request ordering.

**Cost per record** is an index lookup plus the handful of rows the record
affects, independent of the session's size. A 50,000-event session costs the
same per record as a 50-event one.

**The session in S3 is a checkpoint plus a log of deltas**, so no object is
rewritten on the hot path:

- `sessions/<key>/delta-<seq>`: one apply's changes. It holds the ids of the
  records applied, the state changes (index and total updates), and the row
  upserts, keyed by row id, for every derived table. One object per apply makes
  each apply atomic.
- `sessions/<key>/checkpoint-<seq>`: the full state and rows as of `seq`.
  Checkpoints are written in the background, every 1,000 deltas. A checkpoint is
  deterministic from the deltas before it, so any pod may write it, and two pods
  writing the same one write the same bytes.

**Applying a batch of records to a key:**

1. Bring the state up to date. That's the pod's cached copy plus any deltas
   written since; on a cold start, the latest checkpoint plus the deltas after
   it.
2. Drop records already applied, using the id set in the state. Duplicates and
   replays then have no effect.
3. Apply each new record incrementally, as above. This collects the state
   changes and row upserts.
4. Write them as `delta-<seq+1>` with `If-None-Match: *`, so the PUT creates the
   object only if it doesn't exist yet. AWS S3 supports this, and DigitalOcean
   Spaces, R2 and MinIO are to be confirmed (§8).
5. If the object already exists, another pod applied to this session first.
   Read its delta, go back to step 2, and retry at the next number.

**Why this is exactly right:** each incremental update has to give the same
state and rows as a single pass over the session's records in sort order,
whatever order the records arrived in. The tables then depend only on the *set*
of records: arrival order, batching, duplicates and which pod does the work
can't change them. Every update kind gets a randomized test of this (shuffled
input, against a single pass), and the replay test in §6 checks the whole
pipeline.

**Batching per key.** A pod groups the records that arrived for a key over one
interval (1 s by default) into a single delta. A busy session then writes one
small object a second, not one per record.

**Pod memory is bounded** by the calls in flight plus the cache of recently
touched sessions' state (an LRU with a byte cap). A cold session is rebuilt from
its latest checkpoint and the few deltas after it.

**Freshness.** A record reaches the tables within about the apply interval plus
one S3 round trip, a couple of seconds. That's well inside today's ~30-second
commits, and the UI's polling is unchanged.

### 4. Late data, replay and batch: one code path

**Batch is replay.** `aintercept replay` feeds records from the raw log, or any
saved sequence, into the same processor through the same channel. Backfills,
reprocessing after a code change, recovery and every parity test work this way.
There's no separate batch mode to keep in step with the streaming one.

**Late records need nothing special.** A late analytics upload, or a call's
ending that arrives hours after its start, is just another record for its key.
Applying it updates the rows it affects, the same as any other record.

### 5. Cross-session values: dimensions and aggregates

Some values span sessions.

**Dimensions** are small tables, keyed and updated with the same conditional
writes:
- account → person, from email linking;
- credential → account;
- device → person;
- later, enrichment lookups such as a credential's owner from the provider's API.

Queries join them; they're never copied into session rows (Decision 4). A new
link changes one dimension row, and every view that joins it is right at the
next query.

**A person's views** are query-time unions of their sessions' rows, scoped by
the dimensions. The cost is set by that person's data, not everyone's.

**Org-wide views** (`/people`, `/overview`, `/coverage`) read **per-day summary
rows**:
- each session's apply also writes its contribution as a row keyed by day,
  person and session;
- the API sums them with a small `GROUP BY`.

That's thousands of rows per day at 21,000 people, which DuckDB aggregates in
milliseconds once they're compacted.

### 6. Storage layout and compaction

There are two tiers: per-session objects for sessions that are changing, and
compacted files for sessions that have settled.

- **Hot:** the per-session checkpoint and deltas above. A session is hot while
  it's receiving records.
- **Compacted:**
  - Once a session has had no new records for 2 hours, the compactor merges its
    rows into `tables/<table>/dt=<day>/part-<n>.parquet`, sorted by person,
    session and time.
  - Each table-day has a versioned manifest,
    `tables/<table>/dt=<day>/manifest-<n>.json`, that lists the day's files.
    A new version is written create-only, the same way session deltas are.
  - The compacted files are immutable, so readers can cache them for as long as
    they like.
- **Who compacts:** any pod. A pod claims a table-day by taking a short lease
  (an object written with `If-None-Match: *`, renewed while it works). No pod is
  special.
- **A late record for a compacted session** brings it back to hot. The session's
  key goes on the table-day's reopen list, queries prefer its hot rows, and the
  next compaction rewrites the affected file without it.

Hot sessions produce many small objects, and that's fine. Compaction folds
settled sessions into a few files per table and day, so reads stay cheap.

### 7. The query API

**API replicas are stateless.** For each request a replica:
1. reads the manifests and dimension rows it needs;
2. picks the compacted files and row groups by person, session and time range,
   using the sort order and the files' min/max statistics;
3. adds the hot sessions involved: each one's latest checkpoint plus the deltas
   after it, with row upserts applied by row id;
4. runs the endpoint's SQL over that set in an in-process DuckDB.

Each replica keeps a byte-capped LRU cache. Every object it caches (compacted
files, checkpoints, deltas) is immutable, so a cached copy never goes stale.
Freshness comes from listing a hot session's new deltas.
Any replica can serve any request, nothing holds all the data, and the Python
stays thin: SQL and field copies, as Decision 4 requires.

Endpoints, responses and samples don't change. The parity test (§9) proves
every endpoint's output identical to today's.

## Pods coming and going

**Scale-up.** A new pod starts empty and takes connections as the load balancer
sends them. It needs no hand-off: any state it needs, it reads from S3 on first
touch.

**Scale-down (graceful).** Kubernetes sends `SIGTERM`. Then, in order:
1. Envoy stops accepting connections and drains the streams in flight. Streaming
   calls can run for minutes, so `terminationGracePeriodSeconds` is long (15
   minutes).
2. The observer flushes the raw log.
3. The processor applies everything still queued.
4. The pod exits.

Clients reconnect, and their connections land on another pod. Nothing is lost.

**Crashes** (OOM, node loss). Three things happen:
1. **Connections drop.** The client sees an error and retries, and the retry
   lands on another pod. The tables show what they show today: a failed attempt,
   then the retry.
2. **At most the unflushed buffer is lost:** up to 10 seconds of records, less
   under load. That includes any files still waiting for an upload retry.
   Records already in the raw log are safe. A graceful shutdown flushes
   everything, so this applies only to hard crashes.
3. **Calls that were in flight in the dead pod are finished elsewhere.**
   - Each pod keeps a lease object, renewed every 10 seconds.
   - The lease lists the ids of the calls in flight in that pod, and where their
     events are in the raw log.
   - When a pod's lease has gone 60 seconds without renewal, any pod may take it
     over (conditional write).
   - That pod reads the listed calls' events from the raw log and finishes each
     one as cut off, exactly as a disconnect is recorded today.

   Because applies are idempotent, two pods racing to finish the same calls do
   no harm.

## Verification: proving it's right at scale

The parity method from the precompute work carries over, and it's extended to
cover distribution:

1. **Answer key.** One pass of today's code over a real capture, kept local and
   never committed.
2. **Distributed replay.** The same capture is fed through N in-process pods
   (N = 1, 3 and 8) sharing a local S3 emulator. While it runs:
   - connections are routed at random;
   - records are duplicated and reordered;
   - late data is injected;
   - pods are killed at random (crash and graceful), and their leases are taken
     over;
   - pods are added and removed mid-stream.
3. **Exact match.**
   - Every table must be identical to the answer key at the end.
   - At checkpoints along the way, every session that has settled must match.
   - Every API endpoint's response must be byte-identical to today's at pinned
     times. That's the existing endpoint parity tools, pointed at the new read
     path.
4. **Flat cost.** The same capture is replayed with 2×, 4× and 8× as much history
   before it. Peak memory per pod, and time per record, must not grow.
5. **Load test.** Synthetic traffic at 21,000 people's volume, built from the
   capture's shapes with new ids, is run through 30 pods against real object
   storage. It reports throughput, apply latency, conflict retries, S3 requests
   per second and cost, and API latency for person pages and org pages.

`make check` gets a small, fast version of step 2 (synthetic data, 3 pods,
random routing and kills) so the guarantee stays enforced.

## What carries over from today

- **The observer and its raw log format,** with the 10-second timer, the byte
  cap, the upload goroutine, and a channel to the processor.
- **Call assembly** (`shape.go`), fed from the channel.
- **The derived tables' value logic** from the precompute work. It's
  restructured from per-session builders into per-record incremental updates,
  and the builders stay as the single-pass reference those updates are tested
  against. That includes the bugs the replays found and fixed: batch numbering,
  session and account picks, and same-time ordering.
- **The table shapes and the endpoints' SQL**, pointed at the new read path.
- **The replay and parity tooling**, extended to many pods.

Replaced:
- the single processor process;
- the global manifest and whole-table snapshots;
- the derive step that reads all of history;
- the API's load of everything into memory.

## Open questions and risks

- **Create-only writes on the target store.** The design needs one primitive:
  `If-None-Match: *` on PUT, which creates an object only if it doesn't exist.
  AWS S3 supports it. DigitalOcean Spaces (the demo cluster), R2 and MinIO must
  be confirmed before building on them. If a store lacks it, a per-key lease
  object gives the same guarantee.
- **How the API reads S3.** Either DuckDB reads the objects directly (its httpfs
  extension), or the Python fetches the objects and DuckDB reads them locally.
  The second adds nothing to the dependency list. To be settled when the read
  path is built.
- **Records with no session.** Unattributed calls and analytics batches that
  span sessions use fallback keys. Their rules come from the current code and
  will be listed table by table in the implementation plan.

## Build order

1. **Session state and incremental apply.** Records go in; state changes and row
   upserts come out. In memory, it's proven against the single-pass builders and
   the answer key, with random order and duplicates. A test also checks that an
   apply's work doesn't grow with the session's size.
2. **Conditional writes, leases and compaction,** against the S3 emulator, with
   the distributed replay.
3. **Observer → channel → processor,** the streaming flush interval, and the
   drain on `SIGTERM`.
4. **The API read path,** with endpoint parity against today.
5. **The load test** at 21,000 people's volume, then deploy.

Each step goes through the same check before the next one starts: an exact match
with the answer key, plus flat cost.
