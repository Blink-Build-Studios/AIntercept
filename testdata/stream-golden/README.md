# The stream processor's golden layout

Written by `aintercept/internal/stream`'s `TestStreamGolden` (regenerate with
`go test ./internal/stream -run TestStreamGolden -update` from `aintercept/`):
two buckets through seven cycles, with tiered compaction (segments at more
than one level whose key ranges overlap), superseded runs and tombstones, under the prefix `golden/`. `expected.json` is the tables a reader
must produce. The format is documented in
`aintercept/internal/stream/CONTRACT.md`. Synthetic data only.
