"""The stateless read path (docs/STREAMING_PIPELINE.md §5.1), case by case: bucket routing,
finding each bucket's newest commit, the newest-wins fold, the object cache, resolving a
person's keys, and commits landing while requests are served."""

import pathlib

import duckdb
import pytest

from aintercept_api.buckets import (
    BUCKET_BITS,
    BucketStore,
    CommitLog,
    LocalSource,
    ObjectCache,
    Scope,
    bucket_of,
)
from tests.bucket_fixture import commit_key, table_key, write_commit, write_rows

# --- bucket routing: the same function as the Go processor ------------------------------------


@pytest.mark.parametrize(
    ("key", "bucket"),
    [("", 50), ("s:s1", 37), ("c:k:2026-11-12", 60), ("a:a1", 5)],  # records/route_test.go TestBucketVectors
)
def test_bucket_vectors_match_the_processor(key, bucket):
    assert BUCKET_BITS == 6
    assert bucket_of(key) == bucket


def test_one_more_bit_splits_each_bucket_in_two():
    for i in range(2000):
        k = f"s:{i}x"
        assert bucket_of(k, 7) >> 1 == bucket_of(k, 6)


# --- commit discovery ------------------------------------------------------------------------


class Clock:
    def __init__(self) -> None:
        self.t = 1000.0

    def __call__(self) -> float:
        return self.t


class CountingSource(LocalSource):
    def __init__(self, root: pathlib.Path) -> None:
        super().__init__(root)
        self.lists = 0
        self.gets = 0
        self.stale: list[str] | None = None  # a listing to return instead (an eventually consistent store)

    def list_after(self, prefix: str, start_after: str) -> list[str]:
        self.lists += 1
        if self.stale is not None:
            return [k for k in self.stale if k.startswith(prefix) and k > start_after]
        return super().list_after(prefix, start_after)

    def get(self, key: str) -> bytes:
        self.gets += 1
        return super().get(key)


def _commit(root, seq, bucket=7):
    return write_commit(root, "", bucket, seq, {}, {})


def test_no_commit_yet(tmp_path):
    log = CommitLog(LocalSource(tmp_path), "", 7, clock=Clock())
    assert log.latest() is None


def test_one_commit(tmp_path):
    _commit(tmp_path, 1)
    assert CommitLog(LocalSource(tmp_path), "", 7, clock=Clock()).latest().seq == 1


def test_many_commits_newest_wins(tmp_path):
    for s in (1, 2, 3, 10, 11):
        _commit(tmp_path, s)
    assert CommitLog(LocalSource(tmp_path), "", 7, clock=Clock()).latest().seq == 11


def test_a_gap_in_sequence_is_fine(tmp_path):
    for s in (1, 2, 5):
        _commit(tmp_path, s)
    assert CommitLog(LocalSource(tmp_path), "", 7, clock=Clock()).latest().seq == 5


def test_a_stale_listing_never_goes_backwards(tmp_path):
    for s in (1, 2):
        _commit(tmp_path, s)
    src, clock = CountingSource(tmp_path), Clock()
    log = CommitLog(src, "", 7, clock=clock)
    assert log.latest().seq == 2
    src.stale = [commit_key("", 7, 1)]  # a listing that hasn't caught up
    clock.t += 5
    assert log.latest().seq == 2


def test_lists_at_most_once_a_second_per_bucket(tmp_path):
    _commit(tmp_path, 1)
    src, clock = CountingSource(tmp_path), Clock()
    log = CommitLog(src, "", 7, clock=clock)
    log.latest()
    log.latest()
    assert src.lists == 1
    _commit(tmp_path, 2)
    clock.t += 0.5
    assert log.latest().seq == 1  # not looked for yet
    clock.t += 0.6
    assert log.latest().seq == 2 and src.lists == 2


def test_lists_only_after_the_newest_known(tmp_path):
    for s in range(1, 6):
        _commit(tmp_path, s)
    src, clock = CountingSource(tmp_path), Clock()
    log = CommitLog(src, "", 7, clock=clock)
    log.latest()
    seen: list[str] = []
    orig = src.list_after

    def spy(prefix: str, start_after: str) -> list[str]:
        seen.append(start_after)
        return orig(prefix, start_after)

    src.list_after = spy  # type: ignore[method-assign]
    clock.t += 2
    log.latest()
    assert seen == [commit_key("", 7, 5)]


# --- the object cache ------------------------------------------------------------------------


def test_cache_miss_then_hit(tmp_path):
    (tmp_path / "src").mkdir()
    (tmp_path / "src" / "a").write_bytes(b"x" * 10)
    src = CountingSource(tmp_path / "src")
    cache = ObjectCache(src, tmp_path / "cache", max_bytes=100)
    p1 = cache.path("a")
    p2 = cache.path("a")
    assert p1 == p2 and p1.read_bytes() == b"x" * 10
    assert src.gets == 1 and (cache.hits, cache.misses) == (1, 1)


def test_cache_evicts_least_recently_used_over_its_cap(tmp_path):
    (tmp_path / "src").mkdir()
    for n in "abc":
        (tmp_path / "src" / n).write_bytes(b"x" * 40)
    src = CountingSource(tmp_path / "src")
    cache = ObjectCache(src, tmp_path / "cache", max_bytes=100)
    cache.path("a")
    cache.path("b")
    cache.path("a")  # a is now newer than b
    cache.path("c")  # 120 bytes: b goes
    assert cache.size <= 100
    cache.path("a")
    assert src.gets == 3
    cache.path("b")
    assert src.gets == 4


# --- the newest-wins fold --------------------------------------------------------------------


def _layout(root, bucket, files):
    """files: [(name, [(key, id, tomb, v)])] in commit order: the first is the base."""
    con = duckdb.connect()
    keys = []
    for name, rows in files:
        k = table_key("", "t", bucket, name)
        con.execute("DROP TABLE IF EXISTS r")
        con.execute("CREATE TABLE r (key VARCHAR, id VARCHAR, tomb BOOLEAN, v INTEGER)")
        if rows:
            con.executemany("INSERT INTO r VALUES (?, ?, ?, ?)", rows)
        write_rows(root, k, con, "SELECT * FROM r", [])
        keys.append(k)
    write_commit(root, "", bucket, 1, {"t": keys[:1]}, {"t": keys[1:]})


def _fold(root, bucket=3):
    store = BucketStore(LocalSource(root), root / ".cache", tables={"t": ("s:", "key")}, clock=Clock())
    with store.scope(Scope.everything()):
        return store.query("SELECT v FROM t ORDER BY v")


def test_a_run_replaces_the_base_row(tmp_path):
    _layout(tmp_path, 3, [("base-1", [("s:a", "1", False, 1)]), ("run-2", [("s:a", "1", False, 2)])])
    assert _fold(tmp_path) == [(2,)]


def test_a_tombstone_removes_the_row(tmp_path):
    _layout(tmp_path, 3, [("base-1", [("s:a", "1", False, 1), ("s:a", "2", False, 5)]), ("run-2", [("s:a", "1", True, None)])])
    assert _fold(tmp_path) == [(5,)]


def test_a_row_put_back_after_its_tombstone(tmp_path):
    _layout(
        tmp_path,
        3,
        [("base-1", [("s:a", "1", False, 1)]), ("run-2", [("s:a", "1", True, None)]), ("run-3", [("s:a", "1", False, 3)])],
    )
    assert _fold(tmp_path) == [(3,)]


def test_file_order_is_the_commits_not_the_names(tmp_path):
    # "run-9" sorts after "run-10" by name, but the commit lists it first.
    _layout(tmp_path, 3, [("base-1", []), ("run-9", [("s:a", "1", False, 9)]), ("run-10", [("s:a", "1", False, 10)])])
    assert _fold(tmp_path) == [(10,)]


def test_the_same_id_under_two_keys_is_two_rows(tmp_path):
    _layout(tmp_path, 3, [("base-1", [("s:a", "1", False, 1), ("s:b", "1", False, 2)])])
    assert _fold(tmp_path) == [(1,), (2,)]


# --- scopes: a request reads only its keys' buckets ------------------------------------------


def _two_sessions(root):
    con = duckdb.connect()
    for sid, v in (("one", 1), ("two", 2)):
        b = bucket_of(f"s:{sid}")
        k = table_key("", "t", b, "base-1")
        write_rows(root, k, con, "SELECT ? AS key, '1' AS id, false AS tomb, ? AS session_id, ? AS v", [f"s:{sid}", sid, v])
        write_commit(root, "", b, 1, {"t": [k]}, {})
    assert bucket_of("s:one") != bucket_of("s:two")


def test_a_session_scope_reads_only_its_bucket(tmp_path):
    _two_sessions(tmp_path)
    src = CountingSource(tmp_path)
    store = BucketStore(src, tmp_path / ".cache", tables={"t": ("s:", "session_id")}, clock=Clock())
    fetched: list[str] = []
    orig = src.get
    src.get = lambda key: (fetched.append(key), orig(key))[1]  # type: ignore[method-assign]
    with store.scope(Scope(sessions={"one"})):
        assert store.query("SELECT v FROM t") == [(1,)]
    # its bucket's commit and table file, nothing of the other session's bucket
    assert fetched == [commit_key("", bucket_of("s:one"), 1), table_key("", "t", bucket_of("s:one"), "base-1")]


def test_everything_reads_every_bucket(tmp_path):
    _two_sessions(tmp_path)
    store = BucketStore(LocalSource(tmp_path), tmp_path / ".cache", tables={"t": ("s:", "session_id")}, clock=Clock())
    with store.scope(Scope.everything()):
        assert sorted(store.query("SELECT v FROM t")) == [(1,), (2,)]


def test_a_table_with_no_files_in_scope_is_empty_with_its_columns(tmp_path):
    _two_sessions(tmp_path)
    store = BucketStore(
        LocalSource(tmp_path),
        tmp_path / ".cache",
        tables={"t": ("s:", "session_id")},
        empty={"t": "CREATE TABLE t (session_id VARCHAR, v INTEGER)"},
        clock=Clock(),
    )
    with store.scope(Scope(sessions={"nobody"})):
        assert store.query("SELECT v FROM t") == []


# --- resolving a person's keys through the dimensions ----------------------------------------


def _people(root):
    """person_sessions (person → session) and person_map (person → accounts, by email group)."""
    con = duckdb.connect()
    rows = {
        "person_sessions": [("p", "s1"), ("p", "s2"), ("q", "s3"), ("unattributed", "s4")],
        "person_map": [("p", "acct-1"), ("p", "acct-2"), ("q", "acct-3")],
    }
    commits: dict[int, dict[str, list[str]]] = {}
    for table, pairs in rows.items():
        by_bucket: dict[int, list[tuple[str, str]]] = {}
        for person, other in pairs:
            by_bucket.setdefault(bucket_of(f"p:{person}"), []).append((person, other))
        for b, ps in by_bucket.items():
            con.execute("DROP TABLE IF EXISTS r")
            col = "session_id" if table == "person_sessions" else "account_id"
            con.execute(f"CREATE TABLE r (key VARCHAR, id VARCHAR, tomb BOOLEAN, person_id VARCHAR, {col} VARCHAR)")
            con.executemany("INSERT INTO r VALUES (?, ?, false, ?, ?)", [[f"p:{p}", o, p, o] for p, o in ps])
            k = table_key("", table, b, "base-1")
            write_rows(root, k, con, "SELECT * FROM r", [])
            commits.setdefault(b, {})[table] = [k]
    for b, base in commits.items():
        write_commit(root, "", b, 1, base, {})


def _people_store(root):
    return BucketStore(
        LocalSource(root),
        root / ".cache",
        tables={"person_sessions": ("p:", "person_id"), "person_map": ("p:", "person_id")},
        clock=Clock(),
    )


def test_a_person_resolves_to_their_sessions_and_accounts(tmp_path):
    _people(tmp_path)
    scope = _people_store(tmp_path).resolve_person("p")
    assert scope.people == {"p"} and scope.sessions == {"s1", "s2"} and scope.accounts == {"acct-1", "acct-2"}


def test_unattributed_resolves_like_a_person(tmp_path):
    _people(tmp_path)
    assert _people_store(tmp_path).resolve_person("unattributed").sessions == {"s4"}


def test_an_unknown_person_resolves_to_nothing(tmp_path):
    _people(tmp_path)
    scope = _people_store(tmp_path).resolve_person("nobody")
    assert scope.sessions == set() and scope.accounts == set()


# --- sequences: commits landing while requests are served ------------------------------------


def test_a_request_sees_one_commit_throughout(tmp_path):
    _layout(tmp_path, 3, [("base-1", [("s:a", "1", False, 1)])])
    clock = Clock()
    store = BucketStore(LocalSource(tmp_path), tmp_path / ".cache", tables={"t": ("s:", "key")}, clock=clock)
    with store.scope(Scope.everything()):
        assert store.query("SELECT v FROM t") == [(1,)]
        # a new commit lands mid-request
        con = duckdb.connect()
        k = table_key("", "t", 3, "run-2")
        write_rows(tmp_path, k, con, "SELECT 's:a' AS key, '1' AS id, false AS tomb, 2 AS v", [])
        write_commit(tmp_path, "", 3, 2, {"t": [table_key("", "t", 3, "base-1")]}, {"t": [k]})
        clock.t += 5
        assert store.query("SELECT v FROM t") == [(1,)]
    with store.scope(Scope.everything()):
        assert store.query("SELECT v FROM t") == [(2,)]


def test_requests_between_commits_reuse_the_loaded_tables(tmp_path):
    _layout(tmp_path, 3, [("base-1", [("s:a", "1", False, 1)])])
    src, clock = CountingSource(tmp_path), Clock()
    store = BucketStore(src, tmp_path / ".cache", tables={"t": ("s:", "key")}, clock=clock)
    for _ in range(5):
        clock.t += 2
        with store.scope(Scope.everything()):
            assert store.query("SELECT v FROM t") == [(1,)]
    assert src.gets == 2  # the commit and the one table file, fetched once
    assert store.loads == 1  # the tables were built once for this commit


# --- the app picks the store from its environment --------------------------------------------


def test_the_app_reads_bucketed_tables_when_told_to(tmp_path, monkeypatch):
    from aintercept_api.app import create_app
    from aintercept_api.buckets import S3Source

    monkeypatch.setenv("AINTERCEPT_STORE", "buckets")
    monkeypatch.setenv("AINTERCEPT_BUCKETS_DIR", str(tmp_path))
    monkeypatch.setenv("AINTERCEPT_CACHE_DIR", str(tmp_path / "cache"))
    store = create_app().state.store
    assert isinstance(store, BucketStore) and isinstance(store.source, LocalSource)
    monkeypatch.delenv("AINTERCEPT_BUCKETS_DIR")
    monkeypatch.setenv("AINTERCEPT_S3_BUCKET", "b")
    monkeypatch.setenv("AINTERCEPT_S3_REGION", "us-east-1")
    monkeypatch.setenv("AINTERCEPT_S3_PREFIX", "ailogger")
    store = create_app().state.store
    assert isinstance(store.source, S3Source) and store.prefix == "ailogger/"


def test_s3_requests_are_signed(monkeypatch):
    from aintercept_api.buckets import S3Source

    sent = []

    class Resp:
        def __init__(self, body):
            self.body = body

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def read(self):
            return self.body

    def urlopen(req, timeout):
        sent.append(req)
        if "list-type" in req.full_url:
            return Resp(
                b'<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated>'
                b"<Contents><Key>p/a</Key></Contents><Contents><Key>p/b</Key></Contents></ListBucketResult>"
            )
        return Resp(b"body")

    monkeypatch.setattr("urllib.request.urlopen", urlopen)
    s = S3Source("bkt", "us-east-1", endpoint="https://example.test", access_key="AK", secret_key="SK")
    assert s.list_after("p/", "p/") == ["p/a", "p/b"]
    assert s.get("p/a") == b"body"
    for r in sent:
        auth = r.get_header("Authorization")
        assert auth.startswith("AWS4-HMAC-SHA256 Credential=AK/") and "Signature=" in auth
    assert "start-after=p%2F" in sent[0].full_url and sent[1].full_url == "https://example.test/bkt/p/a"
