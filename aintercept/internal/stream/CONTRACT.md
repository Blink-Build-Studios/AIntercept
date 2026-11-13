# The stream processor's output: the reader's contract

This is what the bucketed processor (`internal/stream`) writes and how a reader
(the query API) must read it. `testdata/stream-golden/` at the repo root is a
small layout written by this code, with `expected.json` giving the tables a
reader must produce from it. `TestStreamGolden` fails if the code stops writing
exactly that fixture, so a format change is always a visible diff, and the
API's reader is tested against the same fixture.

All keys are under the deployment's object-store prefix (`<p>/` below; the
golden's is `golden/`).

## Objects

| Key | What |
|---|---|
| `<p>/stream/v1/buckets/NN/commit-SSSSSSSSSSSS.json` | bucket `NN`'s commit log; `S` = sequence, 12 digits, zero-padded, so names sort in order |
| `<p>/stream/v1/tables/<table>/bucket=NN/run-EEEEEEEEEE-SSSSSSSSSSSS.parquet` | the rows one commit changed in one table (`E` = writer's lease epoch, `S` = the commit's sequence) |
| `<p>/stream/v1/tables/<table>/bucket=NN/base-GGGGGGGGGGGG-EEEEEEEEEE-SSSSSSSSSSSS-PPPP.parquet` | a compacted base: generation `G`, written by the commit `S`, part `P` |
| `<p>/stream/v1/state/bucket=NN/...` | the processor's own per-key state checkpoints. **Not for readers** (opaque) |
| `<p>/stream/v1/leases/...`, `<p>/stream/v1/members/...` | ownership. Not for readers |
| `<p>/records/v1/bucket=NN/minute=YYYY-MM-DDTHH:MM/...` | the processor's input (canonical records). Not for readers of the tables |

`NN` is the bucket, two digits. The number of buckets is 2^bits (64 in
production); a reader finds the buckets by listing `<p>/stream/v1/buckets/`.
A key's bucket is the top `bits` bits of its 64-bit FNV-1a hash
(`records.BucketOf`; the API pins the same test vectors).

A records file's `minute` is the minute it was **uploaded** in, fixed at the
upload attempt that landed (the observer's writer, `events.PutAtUpload`), not
when it was sealed. The processor lists record files from its watermark minus a
grace window, so a file that waited out a store outage in the ingest spool must
land after the watermark, never behind it. The writer logs each attempted key
and checks them (HEAD) before naming a file by a new minute, so a file is never
written twice.

## A commit (`commit-*.json`)

Only the newest commit of a bucket (highest sequence) is current; older ones
are kept for a grace period and then deleted. Fields:

| Field | Type | Meaning for a reader |
|---|---|---|
| `bucket` | int | the bucket |
| `seq` | int | this commit's sequence |
| `epoch`, `replica`, `nonce`, `at` | int, string, string, RFC 3339 | who wrote it and when; ignore |
| `watermark`, `processed` | string, string[] | the processor's input position; ignore |
| `gen` | int | the newest base segment number; ignore |
| `base` | {table: FileRef[]} | each table's tiered base: its segments **oldest first**, each segment's parts in key order (may be absent: no rows) |
| `runs` | {table: FileRef[]} | each table's run files since its base, **oldest first** |
| `state_base`, `state` | FileRef[] | state checkpoints, in tiers (groups), oldest first; not for readers. A key's state is every checkpoint of it, oldest first |
| `last_compact` | RFC 3339 | ignore |
| `garbage` | [{key, after}] | files no longer current, deleted after `after`; **never read them** |

A `FileRef` is `{"key": <object key>, "min_key": <first key>, "max_key": <last key>, "rows": <n>}`,
plus `bytes` (its size), on state files only `group` (the tier it belongs to),
and on table base parts `level` and `seg` (below; absent means 0). Readers may
ignore `bytes`, `group`, `level` and `seg`. A file holds only keys in
`[min_key, max_key]`, so a reader looking for some keys opens only the files
whose range covers them.

A table's base is **tiered**. Run files fold into a level-0 segment; once a
level holds `fanout` segments they fold into one segment of the next level.
A segment is the parts sharing a `seg`: their key ranges never overlap. Across
segments they may. `base` lists segments oldest first (level descending, then
`seg` ascending), so in list order a later file's row is newer. A segment
other than the first (oldest) may hold tombstones that remove rows of older
segments; the first never does. Each row is rewritten O(log N) times over its
life, and a key is in at most one part per segment.

## A table file

Parquet (zstd). Columns: `key` (string: the routing key whose state produced
the row: `s:<session>`, `c:<credential>:<day>` or `a:<account>`), `id`
(string: the row's id within the table), `tomb` (bool), then the table's own
typed columns, flattened (an embedded struct's fields are columns of their
own). In a tombstone row (`tomb = true`) the typed columns are zero values and
mean nothing.

## Reading a table

For each bucket, take its newest commit, then:

1. Read the files in `base[table]`, then the files in `runs[table]`, **all
   in the listed order** (segments overlap, so order matters in the base too).
2. Fold the rows: for each `(key, id)`, the row from the latest file wins.
   (Within one file a `(key, id)` appears once.)
3. Drop rows whose winner is a tombstone.

The result is the table. Concatenate across buckets (a key lives in exactly
one bucket). `expected.json` in the golden is this result for its tables,
sorted by `key` then `id`.

A commit names only files that exist: a file a commit drops goes to
`garbage` and is deleted only after its grace period (10 minutes), so a reader
holding a commit it read moments ago can always finish reading it. A reader
that finds a named file missing has a commit older than the grace period:
reload the newest.
