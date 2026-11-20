# Data pipeline (v9)

**Status:** proposed, for the owner's review. Nothing is built on it until the
owner approves it. It replaces v8 (in git history at `8f1c6b8`).

## The design, in the owner's words

1. Ingest the data.
2. Do a minor amount of transformation.
3. Insert the data into the database.
4. Keep the raw data in S3.
5. Point back to S3 from the database.
6. Put the right indexes on the database.
7. Precompute rollups by running queries on the database and saving the
   results.

Everything below is one of those seven points. Anything that isn't one of them
doesn't get built without the owner's yes, asked as its own question.

## Rules

- **Postgres and S3 hold the data; processes don't.** The only things held in
  memory are what the observer needs to assemble traffic it's still seeing (a
  call's rows until it ends, an analytics batch until it's released), its
  write buffer, and the one file a worker is loading. No state engine, journal, snapshot,
  warm cache, byte cap or per-key ownership.
- **One row per fact.** No copies of a fact in a second table. Rollups are
  results computed from facts, stored in their own tables.
- **Rollups never write fact rows.** Facts are written only by ingest. That
  keeps ingest and rollups from ever waiting on each other's locks.
- **A batch's cost follows the batch.** Inserting a batch and refreshing what
  it touched reads the batch's rows plus bounded neighbours (one minute's rows,
  one prompt's calls, one session's hour rows), never a session's or person's
  raw facts.
- **A page load does reasonable work:** indexed reads over what's on screen,
  tens to a few hundred rows. Anything that would aggregate thousands of rows
  reads a rollup. Each page's bound is in §6.
- **Don't break what's shipped.** The UI, the API's response models and the data
  on every page stay the same, except the differences the owner approves by
  name (§8).

## 1. What runs

| Workload | What it does | Scales on |
|---|---|---|
| `envoy` pod (Envoy + observer), unchanged deployment | Envoy hands each transaction to the Go observer over ext_proc. The observer redacts it and streams it to S3 in buffered files. No database connection | CPU (as today) |
| `certsvc`, unchanged | Leaf certificates for interception | — |
| `worker` Deployment (same binary; replaces `rollup`) | Lists new raw files, loads each into Postgres in one transaction, refreshes dirty rollup rows; one of them also lists files, sweeps open calls and runs retention | Age of the oldest unloaded file or dirty row (KEDA) |
| `api` Deployment (Python), unchanged deployment | Pages: indexed reads plus today's response shaping | CPU (as today) |
| `postgres` StatefulSet, unchanged | The data | — |

No new services and no new binaries. The `tail` singleton, the per-key claim
and the warm worker are gone.

## 2. Ingest: stream to S3, then load into Postgres

Two stages, and only the second touches the database:
1. **Ingest writes to S3.** The observer streams everything it captures into
   S3, buffered.
2. **Workers load S3 into Postgres.** They read new files, batch-upsert the
   rows, and refresh the rollups.

### 2.1 Stage 1: the observer writes raw files

The observer already assembles traffic into records (`records/assemble.go`):
`call_start` when a request has been sent, `call_end` with the call's rows when
it completes, `client_event` (one per analytics event), `account`,
`credential` (who a credential belongs to, `internal/identity`), `verdict` and
`ws_connection`. v9 keeps that and writes them to one stream of files per pod:

- **Path:** `<prefix>/raw/<YYYY-MM-DD>/<HH>/<pod>/<ns>-<seq>.ndjson.gz`, so
  names sort by time.
- **Buffered, as today:** a file is written when the buffer reaches 8 MiB or
  every 30 seconds, whichever comes first (the existing writer, spool and retry
  in `events/writer.go`). One PUT per pod per file instead of one per call.
- **Each completed call is its own gzip member** inside the file (gzip allows
  members back to back; any gzip reader reads the whole file). So one call's
  data is addressable as (file, byte offset, length) and the drawer fetches it
  with one ranged GET.
- **Lines:** a record is `{"record":…}`; any other line is a row as the raw
  log has it. A completed call's member holds its call records, then its rows;
  the other records (starts, events, sightings) go in members of their own.
  The path's date and hour are when the file was uploaded (the writer names it
  at each upload attempt), so a file that waited out a store outage lands
  where the lister still looks.
- **Full mode** writes the bodies; **content-free mode** doesn't. One stream
  either way.

**Before anything is written, the observer does only what has to happen in
the pod:**
- **Redaction:** credentials, keys and cookies are removed or hashed
  (`events/events.go:457-545`), so secrets never reach S3.
- **Facts inside bodies:** model, token counts and Claude Code's analytics
  events are pulled out of bodies (`events.ExtractDeferred`), because
  content-free mode drops the bodies. This code exists today.

**One copy in S3.** These files are the only raw data. They replace today's
`capture/`, `dt=…`, `calls/<id>` objects and the per-key Parquet files under
`v1/keys/`. They are also what a rebuild reloads.

**Goes from the observer:** the Postgres writes it never gets, per-key routing
(`records/route.go`), the keyed spool (`records/keyed.go`), keymarks, the
`keyflag` index, the per-call `calls/` objects, Parquet record files, and
`zcodec`.

### 2.2 Stage 2: workers load files into Postgres

- **Listing:** one worker at a time (`pg_try_advisory_lock`) lists `raw/` after
  a cursor every few seconds, going back 5 minutes for late uploads, and
  inserts new keys into `ingest_files(key, listed_at, loaded_at)` with
  `ON CONFLICT DO NOTHING`.
- **Loading:** any worker claims an unloaded file (`FOR UPDATE SKIP LOCKED`),
  reads it, and writes all its records in one transaction (§4), setting
  `loaded_at` in the same transaction. Each `calls` row gets its
  `(raw_key, raw_offset, raw_len)`.
- **Safe to repeat:** every write is a keyed upsert, so a file loaded twice
  (after a crash, or a rebuild) changes nothing.
- **Rebuild:** clear `ingest_files` and the tables; the workers reload every
  file. A migration that says `-- reload:` does it for the tables it names, and
  `aintercept pg reset` for all of them (§3.4). `aintercept canon -load` loads
  one directory of files, for tests and fixtures.

## 3. Facts

The schema is hand-written SQL migrations (§3.4), which replaced the schema
generated from Go state types. `aintercept/internal/pg/schema.sql` is the schema
they build, to read.

### 3.1 `calls`: one row per HTTP call

| Column | Notes |
|---|---|
| `call_id` text PK | `corr`, or `corr#turn` for a WebSocket turn. A start inserts the row; the end fills it in. A start never overwrites a row that already has an end |
| `session_id` | The call's own session |
| `scope_session_id` | The session whose Requests tab lists it: its own. An analytics post is a call like any other: nothing inside it is read (§3.2) |
| `prompt_id`, `account_id`, `device_id`, `org_id`, `agent_id`, `key_hash`, `key_hint`, `source` | From the record |
| `kind` | The request kind (model, analytics, auth, …) derived from host and path at insert, as the Requests tab's filter uses it |
| `host`, `method`, `path`, `status`, `ip`, `transport`, `ws_turn` | From the record |
| `start_ns`, `end_ns`, `swept` | `end_ns` NULL while open; `swept` when the open-call sweep closed it (§5.5). A real end replaces a swept one |
| `model`, `effort`, `stop_reason`, token columns, `ttft_proxy_ms` | From the record |
| `tool_uses`, `detail`, `client`, `provider`, `ext` (jsonb) | As `canon.Request` has them |
| `hidden_kind`, `synthetic` | From the record |
| `request_id`, `client_request_id`, `message_id`, `parent_message_id` | For prev/next |
| `raw_key`, `raw_offset`, `raw_len` | The raw file and the byte range of this call's gzip member (§2.1) |

**Indexes:** `(scope_session_id, start_ns)`, `(session_id, start_ns)`,
`(prompt_id, start_ns)`, `(key_hash, start_ns)`, `(request_id)`,
`(client_request_id)`, `(message_id)`, `(parent_message_id)`, partial
`(start_ns) WHERE end_ns IS NULL`, and partial `(start_ns) WHERE NOT synthetic`.
For the session refresh (§5.2): `(session_id, coalesce(start_ns, end_ns))` (a
session's calls by their time, for its newest values) and partial
`(session_id, start_ns) WHERE prompt_id IS NULL OR hidden_kind IS NOT NULL`
(its calls with no prompt, and its hidden calls). For a step's exit codes (§5.2
`steps`): GIN on `detail -> 'results'` and on `detail -> 'calls'` (the results a
call carries back and the tool calls it made, by tool call id). For the label
(§6.1): partial `(account_id, start_ns) WHERE hidden_kind = 'title'`. For
`/status` (§6.4): partial `(end_ns) WHERE start_ns IS NULL` (a WebSocket turn has
only an end), `(start_ns) WHERE synthetic` (the first probe) and
`(start_ns) WHERE detail IS NOT NULL AND kind = 'model'` (whether any call kept its content; its
predicate names the kind so a step's result lookup can't take it for a scan of every call with
content, which on the cluster made a long turn's refresh never finish: that lookup is the GIN index's).

### 3.2 Analytics events: kept in the raw files, not loaded (owner decision, 2026-11-16)

Claude Code's and Codex's analytics posts (`tengu_*`, `codex_*` events) stay in
the raw files exactly as the observer writes them, but **nothing from inside
them goes into Postgres or is referenced there**. The post itself is a `calls`
row (kind `analytics`, its own session, metadata only) like any other HTTP
call. There is no `events` table.

**Why:** the client sends analytics on its own schedule, in batches that name
other sessions and prompts, so every rollup that read them had to be marked
and recomputed from events across a prompt or session as they trickled in:
the prompt refresh of a long Claude Code turn (24,504 events) went over the
worker's 60 s statement timeout and never finished, and its dirty row held
`/lag` and the worker count at their maximum. What the pages need is on the
wire, in the model calls themselves.

**What goes with them**, each now null, empty or 0 in the API (its shape
unchanged until the pages are redrawn):
- **Proxy-bypass detection:** `missed_reports`, coverage (reported, captured,
  not captured, windows) and a call's `unreported`.
- **Event rows and keys:** the Events tab, `/events/{id}`, the drawer's
  analytics, event names, hooks, heads, PRs, command words, destructive
  categories, git operations, the session's tools as events name them.
- **Friction and posture from events:** frustrated, destructive, outside-cwd,
  queries and bypassed (bypass %), sandbox, the custom CA, the turn end and
  cancels (`tengu_turn_end`, `tengu_cancel`), Codex's turn reports and
  approval policy, pastes and images, compactions and recaps, skills, hook ms.
- **Reported values:** a step's TTFT, cache-write split and cost, and every
  cost (no price table: costs are dropped).
- **Identity only events gave:** the plan, a device's platform, terminal and
  shell, and accounts and devices named only by events.
- **Turns created from `tengu_input_prompt`:** a turn is now only what its
  requests carry (Claude Code's prompt-id header, Codex's `turn_id`).

**Rebuilt from the wire instead:**
- **A step's work** from its own call's tool calls: their names and counts
  always (`tool_uses`), and with recorded content their commands, files,
  patches and the exit codes or errors their results carry back in a later
  call, matched by tool call id (§5.6).
- **A turn's text** from the new messages of its first main-class call
  (`detail.said`): the person's words, and each harness piece (system
  messages, reminders, notes) as its own typed part, named by the same list of
  markers as the Interceptor's parser (`convo.HarnessNotes`). Its size is the
  person's words' whole length.
- **Interrupted:** a step that stopped on an interrupt, or the harness's note
  in the next turn's request (Claude Code's `[Request interrupted by user…`,
  Codex's `<turn_aborted>`), which marks the turn before it.
- **Side requests:** Claude Code's title (`<session>`) and next-prompt
  suggestion (`[SUGGESTION MODE:`) requests are hidden calls (`hidden_kind`
  `title`, `suggestion`) by their notes.

### 3.3 Identity

| Table | Row | Written |
|---|---|---|
| `accounts` | one per account: email, name, org (its sightings'), codex plan, `seen_ns`. Index `(lower(email))` | Upsert; the newest sighting wins |
| `session_accounts` | (session, account), first/last ns. Index `(account_id, start_ns)` | Upsert; `least`/`greatest` on the times |
| `credentials` | one per credential (`key_hash`, never the credential): its account, how that was learned (`lookup` or `observed`), its hint, `first_ns` and `verified_ns`. Many to one account. Index `(account_id)`. Retention deletes a row last verified before the cutoff | Upsert; the newest sighting's account, source and hint; `least`/`greatest` on the times |
| `session_credentials` | (session, key_hash), first/last ns: its visible calls' credentials | Same |
| `account_devices` | (account, device): agent, version, first/last ns, from its visible calls | Same |
| `session_edges` | (child session, parent session) | Insert if absent |
| `ingest_pods` | one per observer pod: `last_ns` (its newest record) | Upsert per file |
| `ingest_files` | one per raw file: key, listed, loaded | §2.2 |
| `goose_db_version` | one per migration applied (goose's) | Migrations (§3.4) |
| `schema_version` | v0.6.2's schema hash, from before migrations: v0.6.2's API reads it, so its pods stay ready through the upgrade. A later migration drops it | Nothing |

A person is an email group: the accounts that share an email, keyed by the
group's lowest account id (as `process/personmap.go` does). It's resolved at
read from `accounts` (a handful of rows per person).

### 3.4 Changing the schema: migrations

The schema changes by migrations: SQL files in
`aintercept/internal/pg/migrations/`, `NNNN_<name>.sql`, numbered from 0001,
forward only and embedded in the binary. Every worker runs `pg.Migrate` at
start, before it loads anything:

1. **Our part, in one transaction:** it reads the database's version (goose's
   `goose_db_version`), refuses a database newer than the build, and does what
   the pending migrations' directives ask (below).
2. **goose** ([pressly/goose](https://github.com/pressly/goose)) applies the
   pending migrations, each in its own transaction, and records each in its
   table.

Both hold the same Postgres session lock (goose's locker, id `schemaLock`), so
workers starting together migrate once. The migration's connections have no
statement timeout (a migration takes as long as it takes) and wait a minute for
a lock. The worker serves `/healthz` meanwhile, so a long migration isn't
killed by its liveness probe.

**Before migrations:** 0001 is v0.6.2's `schema.sql` byte for byte under its
`-- +goose Up` line, so a database v0.6.2 made (that file's hash in
`schema_version`) is recorded as version 1 as it stands: no reset, no reload.
Any other hash is a database the chain can't reach: with `worker.schemaReset`
(the default) the first worker resets it once, and every fact reloads; without
it the workers refuse to start. A database with tables but neither
`goose_db_version` nor `schema_version` isn't ours, and is never touched.

**What a migration can be:**

| Kind | How | What it costs |
|---|---|---|
| In place | Plain SQL: a new table, column or index, or data rewritten in SQL | Nothing: every row stays, the pages show no gap |
| Reload | A `-- reload: <table>, <table>` line in its leading comments | Those tables and `ingest_files` are emptied before it runs, so it can change them as an empty table allows (a table it creates starts empty). The workers then list and load every raw file again: the named tables refill as the files load, and the others' upserts change nothing. Every rollup is marked dirty and recomputed as it would be from scratch |
| Reset | A `-- reset` line | The database is emptied and goose applies the whole chain; everything reloads from the raw files |

A reload or reset keeps only what the raw files still hold: rows older than the
bucket's lifecycle on `raw/` are gone. Ingest decides what to load from
`ingest_files` alone (the lister lists every raw file when it's empty), so a
reload can't load into some tables only; it reloads everything and only the
named tables start empty.

**A rollback** (an older build on a newer database) is refused: the workers
exit with "the database is at schema version N and this build's newest is M",
and nothing changes. The API is different: it reads any version from the
oldest it needs (`OLDEST` in `api/src/aintercept_api/pgstore.py`) on, so in an
upgrade the new workers can migrate while the old API pods still serve.

**Adding a migration:**
1. Add `migrations/NNNN_<name>.sql`, the next number, starting with
   `-- +goose Up` (goose's annotations otherwise as goose documents them; no
   `-- +goose Down`: migrations go forward only). Add `-- reload: …` or
   `-- reset` to its leading comments if it needs data rebuilt; prefer an in-place
   change.
2. Make the same change to `schema.sql`, a new column last in its table (as
   `ALTER TABLE` adds it). The pg tests apply the chain to an empty database and
   compare its catalog with `schema.sql`'s, and upgrade a v0.6.2 database full of
   rows through the chain, checking every row survives but in the tables the
   migrations reload.
3. Never take away what the previous release's API reads: drop or rename a
   table or column a release after the API stopped reading it. When the API
   starts reading what a migration adds, raise `OLDEST` to its number.
4. Never edit a migration once it's released: write another.

`aintercept pg reset` empties the database by hand and applies this build's
chain (the workers' DSN, `WORKER_POSTGRES_URL`). The workers, running or not,
then reload every raw file.

## 4. Writing: one flush

1. **Collapse duplicates in Go first.** Postgres rejects an upsert that touches
   the same row twice in one statement, and a flush often has a call's start and
   end. Analytics events are skipped (§3.2). So records are merged by key before
   sending: an end wins over a start, times take the earliest and latest.
2. **One transaction, one statement per table, rows sorted by key** (sorting
   keeps concurrent flushes from deadlocking):

```
BEGIN;
  INSERT INTO calls    … ON CONFLICT (call_id) DO UPDATE …   -- the end fills in the start
  INSERT INTO accounts, credentials, session_accounts, session_credentials, account_devices,
              session_edges, ingest_pods … ON CONFLICT …
  INSERT INTO dirty (kind, key, seq) … ON CONFLICT DO UPDATE SET seq = excluded.seq
COMMIT;
```

3. **What it marks dirty** (§5.1): the minutes and credentials its calls fall
   in, and the step of each call with a prompt. A call carrying back tool
   calls' results also marks the steps of the calls that made them (one lookup
   per tool call id on the GIN index of `detail -> 'calls'`).

Re-running a flush changes nothing, so a retry is always safe.

**Order doesn't matter.** An end before its start, a call after a later call of
its prompt, a tool call's result before or after it: the facts are keyed upserts, and
everything that depends on order is a rollup computed after the flush commits.

## 5. Rollups

### 5.1 How refresh works

`dirty(kind, key, seq, claimed_at)` lists rollup rows that need recomputing.

A worker loops:
1. **Claim** a few dirty rows in a short transaction: set `claimed_at` on rows
   that are unclaimed (or whose claim is stale), `FOR UPDATE SKIP LOCKED`, and
   commit. No lock is held while refreshing, so flushes never wait on workers.
2. **Refresh** each claimed row in its own transaction: recompute it from the
   facts (or from lower rollups) with one `INSERT … SELECT … ON CONFLICT DO
   UPDATE`, and mark the next level dirty (minute → hour → session, and so on)
   in the same transaction.
3. **Release:** delete the dirty row if its `seq` hasn't changed since the
   claim; otherwise clear `claimed_at` so it's refreshed again.

**Coalescing:** a `step`, `prompt`, `session` or `account_day` row is claimed
only once it has been dirty for 5 seconds, so a busy one is recomputed at most
every 5 seconds however many flushes touch it. (That's one `WHERE` on the
claim.) Step 4 measures the refresh of the 1,244-step prompt and the
53,188-event session; if either costs more than 50 ms, it comes back to the
owner before anything is added.

Every refresh recomputes from the source, so it's idempotent and correct
whatever order data arrived in. **The step is the unit of a prompt's refresh:**
a step is recomputed from its one call (and its tool calls' results, one index
lookup each), its keys (`step_keys`) replace what it added to its prompt's
sums (`prompt_keys`: old taken away, new added, in the same transaction), and
the prompt's row is recomputed from those sums and a few indexed `LIMIT 1`
lookups on its steps. A new call in a 25,000-step prompt reads one call, never
the prompt's other steps; two steps of one prompt refresh one after the other
(a transaction-scoped advisory lock on the prompt). A step's number isn't
stored: the API counts it at read (`steps (prompt_id, session_id, t_ns, ord)`).
The full prompt refresh (every call at once) is only retention's, and the
from-scratch check holds the two equal. Two workers refreshing two minutes of the same
hour each mark the hour dirty, and the hour is recomputed after both commit.

### 5.2 The rollup tables

| Rollup | Grain | Computed from | Reads |
|---|---|---|---|
| `session_minutes` | (session, minute) | That minute's visible model calls (count, main and subagent, tokens, context sum, outputs known, tool calls) and hidden calls; its Requests tab rows counted by kind and credential, hidden apart (a read keeps the session's credentials, as today's tab does) | One minute's rows |
| `session_hours` | (session, hour) | Sum of that hour's `session_minutes` | 60 rows |
| `session_minute_keys` | (session, minute, kind, key) with count | Distinct things per minute: IPs, prompts (calls'), the tools its calls' tool_use blocks name, and the minute's work (kinds `w:…`, §5.6, from its steps' `step_keys`): reads and edits by file type, files by path, lines, commands by program and their failures, searches, MCP servers, subagents, commits, pushes (the Work lane's bins) | One minute's rows |
| `session_keys` | (session, hour, kind, key) with count | Sum of that hour's `session_minute_keys`. Distinct counts over a range are `count(DISTINCT key)` over these rows (minute rows at a range's partial hours) | 60 minutes' key rows |
| `steps` | call_id | One per visible model call with a prompt, from that call alone: its tokens, status, tools (`tool_uses` names and counts; with recorded content its commands, files, patches and the exit codes their results carry back, by tool call id), its work (§5.6), whether it stopped on an interrupt or its request says the turn before was, and what its new messages say. Its number is counted at read by `(t_ns, ord)` | One call, and one index lookup per tool call for its result |
| `step_keys` | (call_id, kind, key) | What a step adds to its prompt: `s:steps`, tokens, interrupts, tool counts by name, its work pieces (with a command's last run) | One step's row |
| `prompt_keys` | (prompt, session, kind, key) | Its steps' `step_keys` summed, by delta: a step refresh takes away what it added and adds what it adds now | The step's keys |
| `prompts` | (prompt_id, session) | One per prompt id on visible model calls (Claude Code's prompt-id header, Codex's `turn_id`): its time (first step), steps, tokens, tool counts, interrupted, its end (last step's), its work (its keys, each command's last exit by one `LIMIT 1` on `step_keys`), its text and typed parts (its first main-class step's new messages) | Its `prompt_keys` (its distinct tools, files and commands: tens to a few hundred rows) and four indexed `LIMIT 1` reads of its steps |
| `prompt_items` | (session, seq) | The Prompts page's list: prompts in order (with what the session's other prompts decide: its number (its place), the open tail, hidden calls), with the session's calls with no prompt interleaved | The session's prompts (hundreds) and prompt-less calls |
| `sessions` | session_id | Today's `session_rows` fields: sums from `session_hours`; distinct counts from `session_keys`; newest-wins values (effort, versions, limits, sandbox) by index lookup; `unattributed` (a visible call and no account). The turn ledger's header (§5.6): its turns, those interrupted and their times (its `prompts` rows) and its work (its `w:…` keys). A page computes the rest from these: the agent, durations, live, the label (§6.1), the children (§5.4) and Codex's limits (`agents.codex_limits` of the stored headers) | The session's hour rows and keys, its prompts rows, and an index lookup per newest value |
| `cred_minutes`, `cred_hours` | (key_hash, minute/hour, kind, hidden, agent) | Coding agents' calls, and how many are the request log's transactions: counts for the person request log and the overview (key_hash '' for calls with none). Indexes on the minute and the hour (the overview's window) | One minute's calls / 60 rows |

Friction marks and Claude Code's skills count, read at the page from events before, are gone with them (§3.2).
Friction is the wire's now (handoff §11S): a turn's interrupt (its `prompts` row) and failed calls (an error
status, or none once ended: `failed` key rows per minute, and `steps.failed` for a turn's).

**What the wire carries (handoff §11S).** Each call names its request's tool list by hash (`calls.tools_list`);
the list itself is a `tool_lists` row, written once by ingest (a session sends a few lists). `session_minute_keys`
adds, per minute and from that minute's calls alone: `offer` and `offer_main` (the lists its visible calls and its
main thread's carried, by hash), `failed`, `compaction`, `subagent` (by agent id) and `recorded` (a call kept its
content). A range's tools offered are its distinct lists (a few key rows per hour) and those few `tool_lists` rows;
its tools used are its `call_builtin`/`call_mcp` keys. The `sessions` row adds `failed_calls`, `recorded` and
`tools` (calls by tool name) from its own key rows; `account_days` adds `tool_calls` and `failed_calls` from its
sessions' hour rows and keys in the day. Nothing reads a session's or a prompt's calls for them.
| `account_days` | (account, UTC day, agent) | The directory's numbers for that day: sessions active (their ids, and the ids of those with a model call), prompts, model calls, tokens, first and last seen, the sessions' repos (Working on, §5.6). Devices are read from `account_devices` (§6.4) | The account's sessions' hour rows and keys in that day |

A turn (handoff §11R) is a `prompts` row and a step a `steps` row; the turn ledger's values are in §5.6.

### 5.3 Dirty kinds

| Kind | Marked by |
|---|---|
| `minute` (session, minute) | a flush with a call in that minute; a `step` refresh of a step in it (its work keys) |
| `hour` (session, hour) | a `minute` refresh |
| `step` (call id) | a flush with that call (with a prompt); a flush with a call that carries back the result of one of its tool calls (by tool call id); the sweep closing it |
| `prompt` | retention (§5.5), for a prompt that held calls before the cut: recomputed from all its calls at once |
| `session` | an `hour`, `step` or `prompt` refresh |
| `cred_minute`, `cred_hour` | a flush with a call of that credential; a `cred_minute` refresh |
| `account_day` | a `session` refresh, for each account of the session and each day it has hours (so an account seen on a session late still gets its earlier days) |

### 5.4 Computed at read instead

Values a late record can change but that are cheap to join by index aren't
stored: an event's `linkedTo` (the newest visible call with its request id), a
call's TTFT and cache-write values from its report, a session's children (by
`session_edges`), and the session label (§6.1).

### 5.5 Housekeeping

Run by whichever worker holds a `pg_try_advisory_lock`, so only one
runs it at a time.

- **Open-call sweep** (every 30 s): a call with no end 6 hours after its last
  data (7 days for a WebSocket), in data time as `stream/state/expire.go` does
  today, is closed: `end_ns` set and `swept` true, its rows marked dirty.
- **Retention** (hourly): delete facts older than the window in batches by
  time, and their rollup rows; the prompts, sessions and account-days that
  began before the cut are marked dirty and recomputed from what's left. Raw
  files expire by an S3 lifecycle rule on `raw/`.

`/lag` is the age of the oldest unloaded file or dirty row, whichever is
older. KEDA scales `worker` on it.

### 5.6 Work (handoff §11R)

What a turn, a step, a minute and a session did, from each step's own tool calls (`refresh/prompts.go`
`stepItems`): their names and counts always (`tool_uses`), and with recorded content (`detail.calls`) their
commands, files, patches and the exit codes their results carry back (`detail.results` of a later call, by tool
call id). One rule classes a step's tool calls as pieces of work, each keyed `(kind, key, n)`:

| Kind | From | Key |
|---|---|---|
| `w:read`, `w:edit`, `w:web`, `w:mcp`, `w:spawn`, `w:tool` | each tool call by its tool's name (a shell call counts by its commands) | file type, search or fetch, MCP server, subagent type |
| `w:read_file`, `w:edit_file` | a read tool's file; each file a patch changes | its path (counted distinct) |
| `w:lines_added`, `w:lines_removed` | a patch's lines | n is the lines |
| `w:run`, `w:run_failed` | each command a shell call (or a Codex exec script) runs, with the exit code its result carries back at its place (≠ 0 failed) | program (`npm test`; '' when not recorded) |
| `w:commit`, `w:push` | a `git commit` or `git push` command | — |

- **A turn's work** (`prompts.work`): its steps' pieces summed (`prompt_keys`), each command with its last run's
  exit code (by the steps' order). Its end is its last step's.
- **A step's work** (`steps.work_pieces`, `work_kind`, `program`, `server`, `agent_id`): its pieces in order; its
  kind is its first tool call's (no tool: thinking); the API groups consecutive steps of a turn where the kind,
  program or server changes, and calls a finished turn's last group with no tool its reply.
- **A minute's and an hour's** are `session_minute_keys` and `session_keys` rows (kinds `w:…`), from the
  minute's steps' `step_keys`: the Work lane's bins and a range's work read them (`api/…/work.py from_keys`).
- **A session's** (`sessions.work`) is its `w:…` keys added up (no last exits), with its turns, interrupted turns
  and active time (its `prompts` rows: the sum of each turn's time, prompt to last step).
- **A repo's, a group's, a subagent's**: the API adds the stored works of its sessions or steps.

PRs, the head range and the last test came from analytics events and are gone (§3.2).

## 6. Pages

The API stays Python. Its routers, response models and shaping code stay. What
changes is where each page gets its data: today most pages load a whole session
or person into DuckDB and run SQL over it (`pgstore.py`, `pgstore_person.py`,
`pgstore_directory.py`). In v9 each page runs indexed reads against §3 and §5.
Ranges are snapped to whole minutes, as today (`ranges.py`).

### 6.1 Session pages

| Page | Reads | Bound |
|---|---|---|
| `/sessions/{id}` | its `sessions` row. Label: its own title, else the earliest title call from its accounts 0–5 min after its start (calls by account and time). Children by `session_edges` (named `session_parents` until step 8) | One row + a few |
| `/sessions/{id}/timeline` | lanes from `session_minutes` (bins under an hour) or `session_hours`; event names from `session_minute_keys` (bins under an hour) or `session_keys`; not-captured windows from `missed_reports`; friction from friction events (few); typed prompts from `prompts`; compactions from events by name; context points: one per main-thread call, from `calls` by `(session_id, start_ns)`; the Turns lane (one block per turn) from `prompts`; the Work lane from the `w:…` key rows | Cells and prompts of the session (hundreds). Context points are one per main-thread call, ~1,700 on the heaviest session: what the chart draws |
| `/sessions/{id}/state` | counts from `session_minutes` in the brush; distinct heads and PRs from `session_keys` in the brush (minute grain at the edges); capabilities (tools used, MCP servers and tools, skills, hooks) for the whole session from the `sessions` row; recaps by name | Cells and keys in the brush + one row |
| `/sessions/{id}/events` | a page of `events` by `(session_id, ts_ns)`, one row per event; prompt numbers by join to `prompts`; `linkedTo` by join on request id; totals from `session_minutes`' event counts | One page + cells |
| `/sessions/{id}/requests` | a page of `calls` by `(scope_session_id, start_ns)` with `kind`, hidden and captured filters, unioned with the session's `missed_reports`; for a Codex thread also its client's sessionless Codex calls in its window by `(key_hash, start_ns)`; prompt and step numbers by join to `steps`; TTFT and cache by join to the report; counts from `session_minutes` | One page + cells |
| `/sessions/{id}/prompts` | a page of `prompt_items` by `(session, seq)`, with each prompt's `steps` | One page of prompts and their steps |
| `/sessions/{id}/turns` | a page of `prompt_items`' prompts in range by `(session, seq)` with their `prompts` rows (work, end, cost) and `steps` (grouped where the kind, program or server changes; subagent steps by subagent); for a Codex thread its subagent threads (`session_edges`, their `sessions` rows), each under the parent's turn running when it started | One page of turns and their steps; a session's children |

**Text search (`q`)** on events and requests scans the session's rows in range.
That's bounded by one session's range; if it's ever too slow it gets a trigram
index, not now.

### 6.2 The drawer: `/requests/{id}`, `/details`, `/raw`, `/events/{id}`

Rendered when opened, by today's `request_detail.py`, with its inputs read from
Postgres and S3 instead of a loaded session:

- **The call:** its `calls` row.
- **The raw data:** one ranged GET of the call's member in its raw file, decoded as today (unzip,
  reassemble the stream, redact, pick out the new messages).
- **Step N of M:** the call's `steps` row and its prompt's step count.
- **Previous and next:** by `message_id`/`parent_message_id`, else by step.
- **"Delivered in step N":** the next call's step within the prompt (§8, difference 7).
- **Analytics around the call:** `events` by request id, client request id and
  tool-use id.
- **Retry and notice:** the nearby retry event and the next call, by index.
- **A `reported:` row** (a report with no captured call): its `missed_reports`
  row and the event.
- **`/events/{id}`:** the `events` row and its batch's raw object.

Bound: one call, one S3 object, a few indexed lookups. The precomputed drawer
tables and the Go drawer go.

### 6.3 Person pages

A person's scope is their accounts (from `accounts` by email group), their
sessions (`session_accounts` by account; `unattributed`: the sessions with a
visible call and no account, through a partial index on `sessions`), and their
credentials (`session_credentials` of those sessions). Every person page reads
the scope first: the group's account rows and the person's `sessions` rows, one
per session within retention.

| Page | Reads | Bound |
|---|---|---|
| `/people/{id}` | `accounts` of the group; devices from `account_devices`; lifetime sums (missed, bypass %, agent info) and the newest values (limits, posture, live) from the person's `sessions` rows | A handful of accounts and devices; one row per session |
| `/people/{id}/sessions` | a page of the person's `sessions` rows in range, children nested (`session_parents`); labels as §6.1 | One page |
| `/people/{id}/timeline` | lanes from `session_hours`/`session_minutes` over the person's sessions in range; bands from `sessions`; typed prompts from `prompts` in range; the Work lane from the `w:…` key rows | Cells in range: up to a few thousand for a 24-hour, 5-minute view; 14 days reads hour cells |
| `/people/{id}/summary` | sums from `session_hours`; distinct counts from `session_keys`; latest values ≤ hi by index; account calls from `cred_hours`; the header row's work from the `w:…` key rows, its turns and active time from `prompts` by `(session_id, t_ns)`, its last test from the `sessions` rows | Hour cells and keys in range; one row per turn in range |
| `/people/{id}/work` | the person's `sessions` rows in range, grouped by repo (work, turns, active time are on the rows); labels as §6.1 | One row per session |
| `/people/{id}/requests` | a page of `calls` by `(key_hash, start_ns)` over the scope's credentials (a merge of a few index scans), with the scope's `missed_reports`; counts from `cred_minutes`/`cred_hours` | One page + cells |

### 6.4 Directory, overview, status

| Page | Reads | Bound |
|---|---|---|
| `/people` | each person's row sums their accounts' `account_days` on the UTC days the window touches; the 14-day activity strip is the last 14 days' rows; live is the newest day row's last activity; sort, filter (`agent`, `q` on name, email, id, device) and page in SQL. The page's rows then read their members (`accounts`), their devices seen in the window (`account_devices`), Codex limits (the newest Codex session's, through `session_accounts`), when they missed calls their not-captured windows (`missed_reports`), and for a live person their newest session and its newest turn (Working on; otherwise the day rows' repos) | ≤ 14 rows per account: about 21,000 small rows at 1,500 people, summed in one `GROUP BY`; then one page |
| `/overview` | totals over the same rows; requests from `cred_hours` (whole hours inside the window, `cred_minutes` at its edges); unattributed from `sessions`; an agent card's sessions are those with a model call in the window (the day rows' `call_session_ids`, and the unattributed sessions active in it) | Same |
| `/status` | first captured call by index (its start, else its end), the first probe's while waiting, whether any call kept its content; `lastDataAt` from `ingest_pods` | Index endpoints + pods |

**The directory sums activity inside the window** (owner decision 2), not the
lifetime totals of the sessions active in it. A day row's numbers are its
sessions' activity that day, and it keeps their ids, so a person's sessions are
the distinct ids of the window's day rows: a session active on several days
counts once. **`/coverage` is deleted** (owner decision 3).

### 6.5 Features removed (owner decision 6)

Each of these cost a rollup table or a fold over a whole session, far more than
the rest of its page. Claude Design removed them in handoff §11Q (2026-11-15).
The UI components and API fields go in step 1b, right after step 1.

| Feature | Where | What goes | What stays |
|---|---|---|---|
| Presence | Person timeline's Presence lane; session timeline's Focus and Device lanes | `presence` on both timeline responses; `process/presence.go` | The live indicator |
| Permission, approval and network lanes | Session timeline | `permission`, `permissionChanges` and `network` on the session timeline response | The header's "Approvals bypassed" share and network count; not-captured gaps on the Model calls lane |
| Collapse ticks | Session Events tab | The `collapseTicks` parameter and `collapsedCount` | One row per event. Tool-progress tick events are classed internal at insert, so they hide behind the existing "hide client internals" toggle (handoff §11Q) |
| Capabilities "at" the brush end | Session State tab | The "up to the brush end" computation | The same values for the whole session; every "in range" count |

## 7. What goes

Measured on `8f1c6b8`. Lines are code / tests.

### Go

| Removed | Lines | Why |
|---|---|---|
| `stream/state` (the in-memory state engine) | 6,091 / 7,325 | No in-memory state |
| `stream` (the v5/v6 bucketed processor, its simulation matrix and golden fixtures); a ~150-line S3 store moves out | 3,383 / 2,946 | Leftover; only `replay` reaches it |
| `drawer` (the Go drawer, `pyjson.go`, templates) | 5,493 / 345 | The drawer renders on open, in Python |
| `render` | 2,173 / 501 | Rollups are SQL |
| `process` derivations (`promptrows`, `requestrows`, `sessionrows`, `sessionmarks`, `presence`, `personmap`, `shape.go`); `store`, `products`, `requestfacts` and the per-event part of `eventrows` stay | ~5,900 / ~1,500 | Rewritten as rollup SQL |
| `snap` | 1,014 / 238 | No snapshots |
| `rollup` (v5 layout and merge) | 1,195 / 877 | v5 |
| `worker`; replaced by a ~600-line refresher | 2,517 / 3,100 | No per-key work |
| `tail`, `keyflag` | 827 / 912 | No key queue |
| `records`: keyed spool, keymarks, per-key sink | ~650 / ~900 | No per-key routing |
| `zcodec` | 82 / 94 | No Parquet record files |
| `pg` schema generation by reflection (pool, logins and `pgtest` stay) | ~700 | Hand-written migrations |
| `cmd`: `replay`, `v1layout`, `storecheck` | 309 / 71 | Test the v5/v6 paths |

### Python

| Removed | Lines | Why |
|---|---|---|
| `V1Store` (`v1.py`), `buckets.py`, `raw.py`, `bucket_fixture.py`, `s3_fake.py` | 1,531 / ~1,760 | v5/v6 read paths |
| The v7 loader: most of `pgstore.py`, `pgstore_person.py`, `pgstore_directory.py`, `state_sql.py`, `stream_sql.py`, `person_sql.py`, `sql/*.sql` | ~2,500 / ~2,260 | Pages read Postgres directly |
| `SqlPages` and the second reader in `pages.py` | ~580 | One reader |
| `rollups.py`, `directory.py`'s Python fold, `claude_code.py`'s `load_session` | ~600 | Rollups are SQL |
| The test oracle, `side_by_side.py`, `test_v8_*`, `test_oracle.py` | ~2,900 | One parity test (§8) |
| DuckDB and ADBC, the API's 4 Gi scratch volume and DuckDB memory settings | — | No DuckDB |

### Tables: 43 → 22

**Facts (9):** `calls` (replaces `requests`, `request_txs`, `request_reports`,
`request_missed`, `session_last`, `prompt_index`, `codex_turns`,
`session_request_rows` and `prompt_steps`), `events` (replaces `event_rows`),
`accounts`, `session_accounts`, `session_credentials`, `account_devices`,
`session_edges`, `ingest_pods`, `schema_version`.

**Rollups (12) and the dirty list:** `session_minutes`, `session_hours`, `session_minute_keys`, `session_keys`,
`missed_reports`, `prompts`, `steps`, `prompt_items`, `sessions`,
`cred_minutes`, `cred_hours`,
`account_days`, plus `dirty`.

**Removed:** `keys`, `dirty_keys`, `workers`, `tail`, `dirty_inputs`,
`batch_parts`, `session_marks`, `presence_segments`, `session_tools`,
`call_tool_uses`, `session_call_days`, `agent_calls`, `agent_call_hours`, the
`dim_*` tables, `session_call_accounts`, `session_title_calls`, and the four
`drawer_*` tables.

### Docs, chart, CI

- `docs/STREAMING_PIPELINE.md` (1,683 lines) is deleted; this file replaces v8's
  2,722 lines.
- Chart values that go: `rollup.warmBytes`, `rollup.parallel`,
  `observer.keyBuffers.*`, `api.memoryLimit`, `api.threads`,
  `api.stateless.cache*`, `api.store`, and Postgres's
  `max_locks_per_transaction=256`.
- CI: the simulation matrix jobs, the no-race second pass, the oracle job and
  the v7/v8 side-by-side suites go with their code.

| | Now | After (estimate) |
|---|---|---|
| PR checks (wall) | 10–12 min | ~5 min |
| Nightly (wall) | ~35 min | ~5 min |

## 8. Not breaking what's shipped

**The bar:** the same data on every page. API responses are compared as parsed
JSON values, so formatting never counts and only data differences fail.

**The baseline:** before anything changes, record every response of the 4,248
parity URLs from today's main on the real capture, kept privately outside the
repo.

| Group | URLs |
|---|---|
| Drawer (detail, raw, details for ~600 calls) | 1,804 |
| Session pages | 1,440 |
| Person pages | 748 |
| Single events | 244 |
| Status, overview, directory | 12 |

**Approved differences, and nothing else:**
1. **Minute snapping** (#63).
2. **The label rule** everywhere: a session's own title, else the earliest
   title call from its accounts within 5 minutes (v8 decision 2).
3. **A resent analytics batch counts once** (decision 4).
4. **The directory and overview sum activity inside the window** (decision 2).
   Their URLs are checked against a from-scratch SQL computation of the new
   numbers.
5. **`/coverage` is gone** (decision 3); its URLs leave the set.
6. **Four features are removed** (decision 6, §6.5). Their response fields are
   left out of the comparison.
7. **"Delivered in step N"** in the drawer is numbered within the prompt, like
   "Step N of M". Today it counts every model call since the session began
   (`request_detail.py:612,643,1148,1169`), which would mean counting a whole
   session's calls each time a drawer opens.

**In CI:** the API tests and generated samples run against Postgres loaded by
`canon -load` from the repo's synthetic datasets (today they run against the v5
store). One parity test compares every URL of the CI datasets as data. The real
capture's 4,248 URLs run locally before each merge, never in CI.

## 9. Build order: one step at a time

Each step is one PR by one agent, reviewed and merged before the next starts.
Every step has its tests written first, `make check` green, its pages equal to
the baseline as data, and its write or refresh cost measured flat at 1,000 vs
50,000 events in a session.

**Main stays deployable throughout:** ingest writes the new tables alongside
the old path from step 3; each page switches to v9 behind the existing reader
seam (`pages.py`) in its own step; the old path is deleted only in step 8.

| Step | What | Done when |
|---|---|---|
| 1 | **Baseline and parity test.** Record the 4,248 responses from today's main. A test that compares parsed JSON | The baseline compares equal to a fresh run of today's main |
| 1b | **Remove the four features** (§6.5) per handoff §11Q: import the handoff, delete the UI components and the API fields, class tick events internal, rerun UI parity | UI parity green against §11Q's 572 specimens; the baseline comparison passes with the removed fields skipped |
| 2 | **Facts and the loader.** Migrations for §3, the flush (§4) as a Go package, `canon -load` | The real capture loads; reloading it, or loading it in shuffled order, gives the same rows; flush cost flat; database bytes per person-day reported |
| 3 | **Ingest streams raw files to S3; workers load them** (§2), alongside the old path until step 8 | `make up && make verify`: a request through the proxy appears in `calls` and `events`; the observer has no Postgres connection; S3 PUTs per pod are one per 8 MiB or 30 s |
| 4 | **Rollups** (§5) | Every rollup equals a from-scratch computation over the facts, after loading the capture in shuffled order; refresh cost flat |
| 5 | **The drawer** reads v9 (§6.2); its tests and samples move to the Postgres fixture | 1,804 drawer URLs equal |
| 6 | **Session pages** read v9 (§6.1), with their tests and samples | 1,440 + 244 URLs equal |
| 7 | **Person pages, directory, overview, status** read v9 (§6.3, §6.4), with their tests and samples | 748 + 12 URLs equal, or equal to the new computation for the directory |
| 8 | **Delete** everything in §7: the old ingest path, worker, loader, DuckDB, chart values, CI jobs | `make check` green; line counts and CI times reported |
| 9 | **Deploy** to the DigitalOcean cluster with an empty Postgres, `helm upgrade` (never uninstall) | Pages load on new traffic; `/lag` under 30 s |
| 10 | **Remove the parity check.** Once the deploy is confirmed: delete `scripts/parity_compare.py` and its test, and the private baseline recordings. The ordinary API tests on the synthetic datasets stay | `make check` green |

**Estimate.** The real work is steps 4, 6 and 7: re-expressing about 6,400
lines of today's derivations (`requestrows`, `promptrows`, `sessionrows`,
`eventrows`, `presence`, `sessionmarks`, `personmap`) as rollup SQL with the
same data. The reviewer's estimate is 70–110 hours of agent work in total.
Part of the logic already exists as SQL in the API (`sql/*.sql`,
`state_sql.py`, `stream_sql.py`), which should help, but the estimate is
re-checked against real progress after step 4.

## 10. Owner decisions (2026-11-14)

1. **Ingest writes only to S3; workers load S3 into Postgres** (§2). The
   observer streams raw files, buffered by size and time, and never touches
   the database. (This replaces the earlier decision that the observer insert
   into Postgres directly.)
2. **The directory sums activity inside the window,** from a per-(account,
   day) rollup.
3. **`/coverage` is deleted.**
4. **Analytics events are upserted on their own unique id,** so a resent batch
   counts once.
5. **One raw copy in S3:** the ingest files. Each call is a gzip member
   inside its file, so Postgres points to it by file, offset and length.
6. **Four features are removed from the UI** (§6.5): presence, the permission,
   approval and network lanes, collapse ticks, and capabilities "at" the brush
   end. Claude Design updates the designs.
