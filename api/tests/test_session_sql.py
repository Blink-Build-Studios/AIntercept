"""session_rows from the stream processor's per-session tables (sql/session_rows.sql), in the store:
equal to the processor's reference on every generated seed, with the Codex titles' recursive query
run only over a scope's sessions and the accounts behind them, never over all sessions."""

import pathlib

import duckdb
import pytest

from aintercept_api.buckets import ROUTING, SESSION_INPUTS, BucketStore, LocalSource, Scope, bucket_of
from tests.bucket_fixture import table_key, write_commit, write_rows
from tests.test_buckets import Clock

SEEDS = sorted((pathlib.Path(__file__).parent / "fixtures" / "session_rows").glob("seed-*"))
_KEY = {"session_edges": "child_session_id"}


def _layout(seed: pathlib.Path, root: pathlib.Path) -> None:
    con = duckdb.connect()
    commits: dict[int, dict[str, list[str]]] = {}
    for table in SESSION_INPUTS:
        col = _KEY.get(table, "session_id")
        f = str(seed / f"{table}.parquet")
        con.execute("DROP TABLE IF EXISTS src")
        sql = (
            "CREATE TABLE src AS SELECT 's:' || CAST(t.c AS VARCHAR) AS key, "  # noqa: S608 (fixed column names)
            "CAST(row_number() OVER () AS VARCHAR) AS id, false AS tomb, t.* EXCLUDE (c) "
            "FROM (SELECT *, " + col + " AS c FROM read_parquet(?)) t"
        )
        con.execute(
            sql,
            [f],
        )
        keys = [k for (k,) in con.execute("SELECT DISTINCT key FROM src").fetchall()]
        for b in sorted({bucket_of(k) for k in keys}):
            ks = [k for k in keys if bucket_of(k) == b]
            k = table_key("", table, b, "base-1")
            write_rows(root, k, con, "SELECT * FROM src WHERE key IN (SELECT unnest(?::VARCHAR[]))", [ks])
            commits.setdefault(b, {})[table] = [k]
    for b, base in commits.items():
        write_commit(root, "", b, 1, base, {})


def _store(root):
    return BucketStore(
        LocalSource(root), root / ".cache", tables={t: ROUTING[t] for t in SESSION_INPUTS}, clock=Clock()
    )


def _same(store, want_file, sessions=None):
    con = store._tables().con
    cols = [d[0] for d in con.execute("SELECT * FROM session_rows LIMIT 0").description]
    sel = ", ".join(f'"{c}"' for c in cols)
    con.execute("CREATE OR REPLACE TEMP TABLE want AS SELECT * FROM read_parquet(?)", [str(want_file)])
    con.execute("CREATE OR REPLACE TEMP TABLE got AS SELECT * FROM session_rows")
    if sessions is not None:
        for t in ("want", "got"):
            con.execute(f"DELETE FROM {t} WHERE session_id NOT IN (SELECT unnest(?::VARCHAR[]))", [sorted(sessions)])  # noqa: S608,E501
    extra = con.execute(f"SELECT * FROM got EXCEPT SELECT {sel} FROM want").fetchall()  # noqa: S608
    missing = con.execute(f"SELECT {sel} FROM want EXCEPT SELECT * FROM got").fetchall()  # noqa: S608
    assert not extra and not missing, (extra[:1], missing[:1])
    return con.execute("SELECT count(*) FROM want").fetchall()[0][0]


@pytest.mark.parametrize("seed", SEEDS, ids=lambda p: p.name)
def test_session_rows_equal_the_reference(seed, tmp_path):
    _layout(seed, tmp_path)
    store = _store(tmp_path)
    with store.scope(Scope.everything()):
        assert _same(store, seed / "want.parquet") > 0
    # The titles ran once per group of sessions linked by their calls' accounts, never across groups.
    groups = _groups(seed)
    for run in store.title_runs:
        if run.titles_accounts:
            assert run.sessions in groups


def _groups(*seeds, suffix=""):
    """The sessions linked through shared call accounts, per connected group."""
    pairs = []
    for seed in seeds:
        pairs += (
            duckdb.connect()
            .execute(
                "SELECT session_id, account_id FROM read_parquet(?)", [str(seed / "session_call_accounts.parquet")]
            )
            .fetchall()
        )
    out: list[frozenset[str]] = []
    left = {s for s, _ in pairs}
    while left:
        group, accts = {left.pop()}, set[str]()
        while True:
            more = {a for s, a in pairs if s in group} - accts
            if not more:
                break
            accts |= more
            group |= {s for s, a in pairs if a in more}
        left -= group
        out.append(frozenset(group))
    return out


def test_two_account_groups_run_apart(tmp_path):
    """Two copies of a seed under different session and account ids: two groups, two runs."""
    seed = SEEDS[0]
    twin = tmp_path / "twin"
    twin.mkdir()
    con = duckdb.connect()
    for f in seed.glob("*.parquet"):
        cols = [d[0] for d in con.execute("SELECT * FROM read_parquet(?) LIMIT 0", [str(f)]).description]
        ids = {"session_id", "account_id", "parent_session_id", "child_session_id", "origin_session_id", "person_id"}
        sel = ", ".join(f"{c} || '-b' AS {c}" if c in ids else c for c in cols)
        union = f"SELECT * FROM read_parquet(?) UNION ALL SELECT {sel} FROM read_parquet(?)"  # noqa: S608
        both = con.sql(union, params=[str(f), str(f)])
        both.write_parquet(str(twin / f.name))
    root = tmp_path / "layout"
    _layout(twin, root)
    store = _store(root)
    originals = {s for g in _groups(seed) for s in g}
    with store.scope(Scope.everything()):
        assert _same(store, seed / "want.parquet", sessions=originals) > 0
        assert store.query("SELECT count(*) FROM session_rows") == [(2 * len(originals),)]
    titled = [r for r in store.title_runs if r.titles_accounts]
    assert len(titled) == 2 and titled[0].sessions.isdisjoint(titled[1].sessions)


@pytest.mark.parametrize("seed", SEEDS[:3], ids=lambda p: p.name)
def test_a_sessions_titles_run_over_its_account_group_only(seed, tmp_path):
    _layout(seed, tmp_path)
    store = _store(tmp_path)
    con = duckdb.connect()
    calls = con.execute(
        "SELECT session_id, account_id FROM read_parquet(?)", [str(seed / "session_call_accounts.parquet")]
    ).fetchall()
    titled = {
        a
        for (a,) in con.execute(
            "SELECT DISTINCT account_id FROM read_parquet(?)", [str(seed / "session_title_calls.parquet")]
        ).fetchall()
    }
    sid = next(s for s, a in calls if a in titled)
    # the session's group: sessions linked through shared call accounts
    group, accts = {sid}, set[str]()
    while True:
        a2 = {a for s, a in calls if s in group}
        g2 = group | {s for s, a in calls if a in a2}
        if (g2, a2) == (group, accts):
            break
        group, accts = g2, a2
    with store.scope(Scope(sessions={sid})):
        _same(store, seed / "want.parquet", sessions=group)
        rows = {r[0] for r in store.query("SELECT session_id FROM session_rows")}
    assert sid in rows and rows <= group
    runs = store.title_runs
    assert runs and all(r.sessions <= group and r.titles_accounts <= accts for r in runs)
