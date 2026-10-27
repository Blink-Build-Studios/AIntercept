"""Stage 1 of the aintercept ETL: collapse raw phase-events into transactions.

Raw capture writes one NDJSON event per HTTP *phase* (request_headers,
request_body_chunk, response_headers, response_body_chunk, complete, …), all
sharing a correlation_id. This stage groups those phase-events by correlation_id
and emits ONE normalized transaction row — the grain every rollup and every
drill-down builds on.

Databricks-ready scaffold for the eventual Spark/Auto Loader pipeline.

⚠️  SOURCE OF TRUTH IS etl/rollup.sql (the DuckDB transform that actually runs
    today). This Spark stage is NOT yet at parity and must be reconciled against
    rollup.sql before use in production:
      - it does NOT yet carry tools_available / tool_calls, so it cannot feed the
        tool rollups (Screens A/B) — add those StructType fields + the first()
        aggregates below;
      - it has no model_prices join / cost_usd (see transactions_enriched in the
        SQL);
      - it uses F.first() for identity fields where the SQL uses max(); these
        agree for fields stamped on every event but must be made identical.
    Until reconciled, do not run this in place of the DuckDB path.
"""

from pyspark.sql import DataFrame, SparkSession, functions as F
from pyspark.sql import types as T

# Schema of the raw NDJSON events, declared explicitly so Spark never has to
# infer it (inference scans data twice and guesses nullability).
RAW_EVENT_SCHEMA = T.StructType(
    [
        T.StructField("correlation_id", T.StringType()),
        T.StructField("seq", T.LongType()),
        T.StructField("timestamp_ns", T.LongType()),
        T.StructField("phase", T.StringType()),
        T.StructField("direction", T.StringType()),
        T.StructField("host", T.StringType()),
        T.StructField("method", T.StringType()),
        T.StructField("path", T.StringType()),
        T.StructField("status", T.IntegerType()),
        T.StructField("key_hash", T.StringType()),
        T.StructField("key_hint", T.StringType()),
        T.StructField("ip", T.StringType()),
        T.StructField("model", T.StringType()),
        T.StructField("input_tokens", T.IntegerType()),
        T.StructField("output_tokens", T.IntegerType()),
        T.StructField("cache_read_tokens", T.IntegerType()),
        T.StructField("cache_write_tokens", T.IntegerType()),
        T.StructField("bytes", T.IntegerType()),
        T.StructField("headers", T.MapType(T.StringType(), T.StringType())),
        T.StructField("observer_instance", T.StringType()),
        T.StructField("end_of_stream", T.BooleanType()),
        T.StructField("error", T.StringType()),
    ]
)


def provider_of(host_col: "F.Column") -> "F.Column":
    """Map an upstream host to a provider slug."""
    return (
        F.when(host_col == "api.anthropic.com", F.lit("anthropic"))
        .when(host_col == "api.openai.com", F.lit("openai"))
        .when(host_col == "generativelanguage.googleapis.com", F.lit("google"))
        .otherwise(F.lit("unknown"))
    )


def read_raw_events(spark: SparkSession, input_path: str) -> DataFrame:
    """Read the gzipped NDJSON capture under input_path into raw events.

    input_path is any Spark-readable glob, e.g.
    s3a://your-telemetry-bucket/aintercept/dt=YYYY-MM-DD/*/*/*.ndjson.gz
    """
    return spark.read.schema(RAW_EVENT_SCHEMA).json(input_path)


def collapse_to_transactions(events: DataFrame) -> DataFrame:
    """Collapse phase-events into one row per transaction (correlation_id).

    Identity (key_hash/key_hint/ip/method/path/host) is carried on the inbound
    request_headers event; model + token counts land on the complete event;
    byte totals are summed across body chunks. We take the first non-null of
    each field across the transaction's events, which is robust to phase
    ordering and to a field appearing on more than one phase.
    """
    first = lambda c: F.first(F.col(c), ignorenulls=True)  # noqa: E731

    tx = events.groupBy("correlation_id").agg(
        F.min("timestamp_ns").alias("ts_start_ns"),
        F.max("timestamp_ns").alias("ts_end_ns"),
        first("host").alias("host"),
        first("method").alias("method"),
        first("path").alias("path"),
        F.max("status").alias("status"),  # the real status (0 on unset phases)
        first("key_hash").alias("key_hash"),
        first("key_hint").alias("key_hint"),
        first("ip").alias("ip"),
        first("model").alias("model"),
        F.max("input_tokens").alias("input_tokens"),
        F.max("output_tokens").alias("output_tokens"),
        F.max("cache_read_tokens").alias("cache_read_tokens"),
        F.max("cache_write_tokens").alias("cache_write_tokens"),
        F.sum(
            F.when(F.col("direction") == "inbound", F.col("bytes")).otherwise(0)
        ).alias("request_bytes"),
        F.sum(
            F.when(F.col("direction") == "outbound", F.col("bytes")).otherwise(0)
        ).alias("response_bytes"),
        # The caller's client app, derived from the request User-Agent.
        first(F.col("headers")["user-agent"]).alias("user_agent"),
        first("observer_instance").alias("observer_instance"),
        # Any error recorded on the stream (e.g. disconnect).
        first("error").alias("error"),
        # Whether a terminal complete event was seen (vs. a dropped/partial txn).
        F.max(F.when(F.col("phase") == "complete", F.lit(1)).otherwise(0)).alias(
            "_completed"
        ),
    )

    return (
        tx.withColumn("provider", provider_of(F.col("host")))
        .withColumn("client_app", client_app_of(F.col("user_agent")))
        .withColumn("endpoint", F.split(F.col("path"), "\\?").getItem(0))
        .withColumn(
            "ts", (F.col("ts_start_ns") / 1_000_000_000).cast(T.TimestampType())
        )
        .withColumn("completed", F.col("_completed") == 1)
        .withColumn(
            "latency_ms",
            ((F.col("ts_end_ns") - F.col("ts_start_ns")) / 1_000_000).cast(
                T.DoubleType()
            ),
        )
        .drop("_completed", "ts_start_ns", "ts_end_ns")
    )


def client_app_of(ua_col: "F.Column") -> "F.Column":
    """Derive a client-app family from the User-Agent (coarse, extensible)."""
    ua = F.lower(F.coalesce(ua_col, F.lit("")))
    return (
        F.when(ua.contains("claude-code") | ua.contains("claude code"), F.lit("claude-code"))
        .when(ua.contains("anthropic"), F.lit("anthropic-sdk"))
        .when(ua.contains("openai") | ua.contains("codex"), F.lit("openai-client"))
        .when(ua.contains("curl"), F.lit("curl"))
        .when(ua == "", F.lit("unknown"))
        .otherwise(F.split(ua, "/").getItem(0))
    )
