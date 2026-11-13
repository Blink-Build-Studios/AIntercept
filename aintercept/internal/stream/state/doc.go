// Package state is the streaming processor's per-key state (docs/STREAMING_PIPELINE.md
// §4.5): pure functions, no I/O. A key is a routing key (records.RoutingKey): a
// session, a credential and day, or an account. Its state folds in the key's
// records one at a time, and each record yields a Delta: the derived rows it
// adds, changes or removes, per table.
//
//	s := state.New(key)
//	d := s.Apply(rec)        // upserts and tombstones, by table and row id
//	b, _ := state.Encode(s)            // checkpoint: the records applied since the last one
//	s2, _ := state.Decode(b1, b2, b)   // resume from every checkpoint, oldest first
//	m, _ := state.Merge(b1, b2)        // compaction: one checkpoint for several
//
// The rules every table keeps:
//
//   - Order free. Applying a key's records in any order, with any duplicates
//     (by record id), ends in the same state and rows: a pure function of the set.
//     Ties are broken by (time, record id).
//   - Flat cost. A record touches only the rows it affects, found through the
//     state's ordered indexes; nothing walks the whole key. Its work doesn't grow
//     with how many records the key already holds.
//   - Flat checkpoints. A checkpoint is what changed since the last one (its
//     records), never the key's history; loading costs the same per record held.
//   - Exact. Folding every Delta gives the rows the single-pass reference builds
//     (process.DeriveRows) from the same records, in each table's stored shape:
//     positions are sort keys (the query numbers them), and values that span a
//     session are lookups the query joins, not copies.
//
// The query side (query.go) rebuilds today's API tables from stored rows, as
// the API's SQL does; RenderSession (render.go) is a session's finished
// tables from its own key's rows through it.
//
// Delta.Cases names the branches a record took (see cases.go): the tests require
// an atomic test for every case, and the real capture's records for a histogram
// of the ones that occur.
package state
