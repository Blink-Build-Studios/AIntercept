"""Single source of truth for per-model pricing (USD per 1M tokens).

The ETL runner (etl/local_rollup.py) and any query layer import MODEL_PRICES
from here, so cost attribution can never drift between paths. Keys are the BARE
model family — the rollup strips a trailing -YYYYMMDD
date suffix before joining, so pinned dated ids (claude-opus-4-8-20260115) price
correctly against the bare key.

Keep current. A model absent from this table yields NULL cost downstream, which
is surfaced honestly as "unpriced" (never silently $0).
"""

# (model, input, output, cache_read, cache_write) — USD per 1,000,000 tokens.
MODEL_PRICES = [
    ("claude-opus-4-8", 15.0, 75.0, 1.5, 18.75),
    ("claude-sonnet-4-6", 3.0, 15.0, 0.3, 3.75),
    ("claude-haiku-4-5", 1.0, 5.0, 0.1, 1.25),
    ("gpt-4", 30.0, 60.0, 0.0, 0.0),
]
