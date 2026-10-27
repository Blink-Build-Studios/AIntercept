"""Interim ETL runner — no Databricks required.

Reads the gzipped NDJSON capture directly from object storage (S3 / DO Spaces)
with DuckDB, runs the shared collapse+rollup transform (etl/rollup.sql), and
writes transactions + rollup_1m + rollup_1h as Parquet (and/or a DuckDB file a
query layer can read). This is what makes the captured data queryable TODAY; the
Spark/Auto Loader job (etl/stream_rollup.py) is the same logic for continuous
Databricks ingestion later.

Usage:
    python etl/local_rollup.py \
        --endpoint https://nyc3.digitaloceanspaces.com \
        --bucket your-telemetry-bucket \
        --prefix aintercept \
        --since-hours 24 \
        --out ./out

Credentials come from the standard AWS env vars (AWS_ACCESS_KEY_ID /
AWS_SECRET_ACCESS_KEY) — the same Spaces key the observer uses.
"""

import argparse
import datetime as dt
import os
import pathlib

import duckdb

# Prices come from the single shared source so cost attribution never drifts.
from model_prices import MODEL_PRICES


def _s3_glob(prefix: str, since_hours: int) -> list[str]:
    """Build dt=-partitioned path globs covering the last `since_hours`."""
    now = dt.datetime.now(dt.timezone.utc)
    days = {(now - dt.timedelta(hours=h)).strftime("%Y-%m-%d") for h in range(since_hours + 1)}
    return [f"{prefix}/dt={d}/*/*/*.ndjson.gz" for d in sorted(days)]


def main() -> None:
    """Run the interim rollup over recent capture and write Parquet outputs."""
    ap = argparse.ArgumentParser()
    ap.add_argument("--endpoint", required=True, help="S3-compatible endpoint URL")
    ap.add_argument("--bucket", required=True)
    ap.add_argument("--prefix", default="aintercept")
    ap.add_argument("--since-hours", type=int, default=24)
    ap.add_argument("--out", default="./out")
    ap.add_argument("--db", default="", help="optional path to persist a DuckDB file")
    args = ap.parse_args()

    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    con = duckdb.connect(args.db or ":memory:")
    # Pin the session to UTC so date_trunc('hour'/'minute', ts) buckets in UTC —
    # matching the UTC dt=/hour= object partitions the glob slices on. Without
    # this, a non-UTC host would bucket in local time and straddle day edges.
    con.execute("SET TimeZone='UTC';")
    con.execute("INSTALL httpfs; LOAD httpfs;")
    # Point DuckDB's S3 client at the Spaces endpoint. Strip scheme for host.
    host = args.endpoint.replace("https://", "").replace("http://", "")
    con.execute(f"SET s3_endpoint='{host}';")
    con.execute("SET s3_url_style='path';")
    con.execute(f"SET s3_access_key_id='{os.environ['AWS_ACCESS_KEY_ID']}';")
    con.execute(f"SET s3_secret_access_key='{os.environ['AWS_SECRET_ACCESS_KEY']}';")
    con.execute("SET s3_region='us-east-1';")  # Spaces ignores region; any value

    # Seed the price table.
    con.execute(
        "CREATE TABLE model_prices(model VARCHAR, input_usd_per_mtok DOUBLE, "
        "output_usd_per_mtok DOUBLE, cache_read_usd_per_mtok DOUBLE, "
        "cache_write_usd_per_mtok DOUBLE);"
    )
    con.executemany("INSERT INTO model_prices VALUES (?, ?, ?, ?, ?)", MODEL_PRICES)

    # Load raw events from the partitioned globs into a view. We declare the full
    # column set explicitly so fields that haven't appeared in the data yet
    # (e.g. `ip` before the LB forwards x-forwarded-for) still exist as NULL,
    # rather than being absent and breaking downstream references.
    columns = (
        "{correlation_id:'VARCHAR', seq:'BIGINT', timestamp_ns:'BIGINT', "
        "phase:'VARCHAR', direction:'VARCHAR', host:'VARCHAR', method:'VARCHAR', "
        "path:'VARCHAR', status:'INTEGER', key_hash:'VARCHAR', key_hint:'VARCHAR', "
        "ip:'VARCHAR', model:'VARCHAR', input_tokens:'INTEGER', "
        "output_tokens:'INTEGER', cache_read_tokens:'INTEGER', "
        "cache_write_tokens:'INTEGER', bytes:'INTEGER', "
        "headers:'MAP(VARCHAR, VARCHAR)', observer_instance:'VARCHAR', "
        "end_of_stream:'BOOLEAN', error:'VARCHAR', "
        # Tool visibility arrays (content-free) on the complete event.
        "tools_available:'STRUCT(name VARCHAR, server VARCHAR, tool VARCHAR, kind VARCHAR)[]', "
        "tool_calls:'STRUCT(name VARCHAR, server VARCHAR, tool VARCHAR, kind VARCHAR, count INTEGER)[]'}"
    )
    globs = _s3_glob(args.prefix, args.since_hours)
    paths = ", ".join(f"'s3://{args.bucket}/{g}'" for g in globs)
    con.execute(
        f"CREATE VIEW raw_events AS SELECT * FROM read_json([{paths}], "
        f"columns={columns}, ignore_errors=true, format='newline_delimited');"
    )
    n_events = con.execute("SELECT count(*) FROM raw_events").fetchone()[0]
    print(f"raw events loaded: {n_events:,}")

    # Run the shared transform.
    sql = (pathlib.Path(__file__).parent / "rollup.sql").read_text()
    con.execute(sql)

    # Report + write outputs — transaction, model/cost, AND tool rollups.
    for tbl in (
        "transactions",
        "rollup_1m",
        "rollup_1h",
        "tool_facts",
        "tool_rollup_1m",
        "tool_rollup_1h",
    ):
        n = con.execute(f"SELECT count(*) FROM {tbl}").fetchone()[0]
        con.execute(f"COPY {tbl} TO '{out / f'{tbl}.parquet'}' (FORMAT parquet);")
        print(f"{tbl}: {n:,} rows -> {out / f'{tbl}.parquet'}")

    print("\n== sample: usage by model (from rollup_1h) ==")
    for row in con.execute(
        "SELECT model, sum(transactions) txns, sum(input_tokens) in_tok, "
        "sum(output_tokens) out_tok, round(sum(cost_usd), 4) cost "
        "FROM rollup_1h GROUP BY model ORDER BY txns DESC"
    ).fetchall():
        print(" ", row)

    print("\n== sample: MCP servers by tool calls (from tool_rollup_1h) ==")
    for row in con.execute(
        "SELECT mcp_server, sum(tool_calls) calls, count(DISTINCT tool) tools, "
        "max(distinct_users_lb) users FROM tool_rollup_1h WHERE mcp_server IS NOT NULL "
        "GROUP BY mcp_server ORDER BY calls DESC LIMIT 15"
    ).fetchall():
        print(" ", row)


if __name__ == "__main__":
    main()
