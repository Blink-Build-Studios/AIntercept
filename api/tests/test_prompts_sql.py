"""The prompt list's and marks' SQL (sql/prompts.sql) over the streaming
processor's stored rows equals the reference's rows.

The fixtures come from the Go side (aintercept/internal/stream/state,
TestPromptsSQLFixtures with PROMPTS_SQL_OUT): each case's stored tables and the
reference's prompt_rows, prompt_steps and session_marks, as Parquet. Set
PROMPTS_SQL_FIXTURES to that directory to run this; real data stays local.
"""

import math
import os
from pathlib import Path
from typing import Any

import duckdb
import pytest

FIXTURES = os.environ.get("PROMPTS_SQL_FIXTURES", "")
SQL = Path(__file__).resolve().parents[1] / "src" / "aintercept_api" / "sql" / "prompts.sql"
STORED = ("prompt_items", "prompt_steps", "prompt_session", "session_marks", "mark_prompt_index")
VIEWS = {
    "v_prompt_rows": ("ref_prompt_rows", "session_id, seq"),
    "v_prompt_steps": ("ref_prompt_steps", "session_id, seq, step"),
    "v_session_marks": ("ref_session_marks", "session_id, seq"),
}


def cases() -> list[Path]:
    if not FIXTURES:
        return []
    return sorted(p for p in Path(FIXTURES).iterdir() if p.is_dir())


def rows(con: duckdb.DuckDBPyConnection, table: str, order: str) -> list[dict[str, Any]]:
    rel = con.table(table).order(order)
    return [dict(zip(rel.columns, r, strict=True)) for r in rel.fetchall()]


def same(a: Any, b: Any) -> bool:
    if isinstance(a, float) and isinstance(b, float):
        return a == b or (math.isnan(a) and math.isnan(b))
    return bool(a == b)


@pytest.mark.skipif(not FIXTURES, reason="PROMPTS_SQL_FIXTURES isn't set")
@pytest.mark.parametrize("case", cases(), ids=lambda p: p.name)
def test_prompts_sql_equals_the_reference(case: Path) -> None:
    con = duckdb.connect()
    for t in STORED:
        con.read_parquet(str(case / f"{t}.parquet")).create(t)
    con.execute(SQL.read_text())
    for view, (ref, order) in VIEWS.items():
        con.read_parquet(str(case / f"{ref}.parquet")).create(ref)
        want = rows(con, ref, order)
        got = rows(con, view, order)
        assert len(got) == len(want), f"{view}: {len(got)} rows, want {len(want)}"
        for i, (g, w) in enumerate(zip(got, want, strict=True)):
            diff = {k: (g.get(k), w[k]) for k in w if not same(g.get(k), w[k])}
            assert not diff, f"{view} row {i}: {diff}"
