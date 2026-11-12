Synthetic dimension tables (§4.6) and the person tables the processor's reference rebuild
(`personTables` in `aintercept/internal/stream/state/dims_test.go`) makes from them, one
file per generated seed: `test_person_sql.py` checks the API's SQL against them. No real
data: the records come from `generateDims`. Regenerate from that package by writing each
seed's `o.rows` dimension tables and `personTables(o)` as JSON (key, id and the rows'
parquet columns).
