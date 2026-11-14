"""The directory's three statements over every call mark, frozen as they were before v7 build step 7
(docs/PIPELINE.md §5, "Endpoint code that changes"): rollups._ACTIVITY, directory.overview()'s
sessions per product and directory.unattributed(). Since step 7 they read session_call_days, the
worker's per-session, per-day fold of the call marks.

The directory gates run "old" with these swapped in (`todays_sql`), so each change is proven side by
side: the oracle's call marks through today's SQL against the Postgres folds through the new SQL."""

import contextlib
from collections.abc import Iterator

from aintercept_api import directory, rollups

# A call's UTC day, from its time in (float) seconds as Python's datetime.fromtimestamp read it.
ACTIVITY = """SELECT k.row_id, strftime(DATE '1970-01-01' + CAST(floor(m.t_ns / 1e9 / 86400) AS INTEGER), '%Y-%m-%d')
         AS day, count(*) AS n
FROM kept k JOIN session_marks m USING (session_id) WHERE m.kind = 'call' GROUP BY 1, 2"""

SESSIONS_BY = """SELECT s.product, count(*) FROM (
  SELECT session_id, min(t_ns) AS first, max(t_ns) AS last FROM session_marks
  WHERE kind = 'call' GROUP BY session_id) c JOIN session_rows s USING (session_id)
WHERE (?::BIGINT IS NULL OR last >= ?) AND (?::BIGINT IS NULL OR first <= ?)
GROUP BY 1"""

UNATTRIBUTED = """SELECT count(DISTINCT session_id), count(*), min(t_ns) FROM session_marks
WHERE kind = 'call' AND session_id IN (SELECT session_id FROM person_sessions WHERE person_id = ?)"""


@contextlib.contextmanager
def todays_sql() -> Iterator[None]:
    """The endpoint code with the three statements as they were (requests in this block)."""
    saved = rollups._ACTIVITY, directory._SESSIONS_BY, directory._UNATTRIBUTED
    rollups._ACTIVITY, directory._SESSIONS_BY, directory._UNATTRIBUTED = ACTIVITY, SESSIONS_BY, UNATTRIBUTED
    try:
        yield
    finally:
        rollups._ACTIVITY, directory._SESSIONS_BY, directory._UNATTRIBUTED = saved
