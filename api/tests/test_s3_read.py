"""The read path over S3 (docs/STREAMING_PIPELINE.md §5): one DuckDB query per table, straight
over the object store through httpfs, against exactly the files each partition's newest commit
names, filtered on key so DuckDB fetches only the row groups that can hold the request's keys.
Counted against a fake S3 that logs every request and byte."""

import json
import pathlib

import duckdb
import pytest

from aintercept_api import buckets
from aintercept_api.buckets import BucketStore, LocalSource, S3Source
from aintercept_api.raw import LocalRaw
from tests.conftest import few_per_group, make_client
from tests.endpoints import endpoint_urls as _urls
from tests.s3_fake import BUCKET, FakeS3, fake_s3
from tests.test_stream_pipeline import group, layout  # noqa: F401 (layout: the pipeline's committed layout, a fixture)


@pytest.fixture(scope="module", autouse=True)
def httpfs_installed():
    """Tests install httpfs once (the image has it built in; the API never downloads it)."""
    duckdb.connect().execute("INSTALL httpfs")


def s3_store(fake: FakeS3, cache: pathlib.Path, raw: pathlib.Path) -> BucketStore:
    src = S3Source(BUCKET, "us-east-1", endpoint=fake.endpoint, access_key="test", secret_key="test")  # noqa: S106 (the fake S3 ignores credentials)
    return BucketStore(src, cache, prefix="sim/", raw=LocalRaw(raw))


# --- atomic: a key lookup reads only its row groups ---------------------------------------------


def test_a_key_lookup_reads_only_the_row_groups_that_hold_it(tmp_path):
    """A 10 MB table file in 1,000 row groups: one key's lookup fetches the footer and its row
    group, not the file. (DuckDB reads a file under ~1 MB whole: smaller ones aren't held to it.)"""
    root = tmp_path / "s3"
    (root / "t").mkdir(parents=True)
    path = root / "t" / "big.parquet"
    sql = (
        "COPY (SELECT printf('s:%07d', i // 5) AS key, printf('r%07d', i) AS id, false AS tomb, i AS v "
        "FROM range(1000000) t(i) ORDER BY key, id) TO '" + str(path) + "' (FORMAT parquet, ROW_GROUP_SIZE 1000)"
    )
    duckdb.execute(sql)
    size = path.stat().st_size
    with fake_s3(root) as fake:
        src = S3Source(BUCKET, "us-east-1", endpoint=fake.endpoint, access_key="test", secret_key="test")  # noqa: S106 (the fake S3 ignores credentials)
        con = buckets.connect(src)
        rows = buckets.read_newest(con, [src.url("t/big.parquet")], keys=["s:0004321"]).fetchall()
        assert sorted(r[-1] for r in rows) == list(range(4321 * 5, 4321 * 5 + 5))
        assert fake.bytes_read() < size // 10, (fake.bytes_read(), size)


# --- atomic: files whose key range can't hold the keys are never read ----------------------------


def test_a_commit_names_only_the_files_whose_key_range_holds_the_keys():
    body = json.dumps(
        {
            "bucket": 3,
            "seq": 7,
            "layout": "stream-state/1",
            "base": {"t": [{"key": "b1", "min_key": "s:a", "max_key": "s:m", "rows": 9}]},
            "runs": {
                "t": [
                    {"key": "r1", "min_key": "s:n", "max_key": "s:z", "rows": 2},
                    {"key": "r2", "min_key": "s:c", "max_key": "s:d", "rows": 1},
                ]
            },
        }
    ).encode()
    c = buckets._parse_commit("p/stream/v1/buckets/03/commit-000000000007.json", body)
    assert c.layout == "stream-state/1"
    assert c.files("t") == ["b1", "r1", "r2"]
    assert c.files("t", keys=["s:c5"]) == ["b1", "r2"]
    assert c.files("t", keys=["s:q"]) == ["r1"]
    assert c.files("t", keys=["s:zz"]) == []


# --- the pipeline's layout, served from a fake S3 -----------------------------------------------


@pytest.fixture(scope="module")
def s3(layout):  # noqa: F811
    tmp, root = layout
    with fake_s3(tmp / "layout") as fake:
        store = s3_store(fake, tmp / "s3cache", root)
        local = BucketStore(LocalSource(tmp / "layout"), tmp / "lcache", prefix="sim/", raw=LocalRaw(root))
        yield fake, make_client(store), make_client(local)
        store.close()
        local.close()


def test_the_layout_is_named_by_the_commits_not_read_from_a_table_file(s3):
    fake, client, _ = s3
    fake.reset()
    store = client.app.state.store
    store._state = None
    assert store.stream_state
    assert not [h.key for h in fake.gets() if "/tables/" in h.key]


def test_unrouted_paths_read_nothing(s3):
    fake, client, _ = s3
    fake.reset()
    for path in ("/assets/app.js", "/people/someone", "/api/v1/nonexistent", "/api/v1/coverage"):
        client.get(path)
    assert fake.hits == [], [(h.method, h.key) for h in fake.hits]


def test_freshness_gets_the_next_commit_instead_of_listing(s3):
    fake, client, local = s3
    url = next(u for u in _urls(local) if u.startswith("/api/v1/sessions/") and u.count("/") == 4)
    client.get(url)
    store = client.app.state.store
    for log in store._logs.values():  # each log's one-second window has passed (not its relist)
        if log._checked is not None:
            log._checked -= 2
    store._loaded.clear()
    fake.reset()
    client.get(url)
    assert fake.lists() == [], [h.query for h in fake.lists()]
    assert any(h.key.endswith(".json") for h in fake.hits)


@pytest.mark.full
def test_every_endpoint_through_s3_is_byte_identical_to_the_local_read(s3):
    _, client, local = s3
    urls = _urls(local)
    assert len(urls) > 100
    _identical(client, local, urls)


def test_each_endpoint_group_through_s3_is_byte_identical_to_the_local_read(s3):
    _, client, local = s3
    _identical(client, local, few_per_group(_urls(local), group))


def _identical(client, local, urls: list[str]) -> None:
    differing = []
    for url in urls:
        a, b = local.get(url), client.get(url)
        if a.status_code != b.status_code or json.dumps(a.json(), sort_keys=True) != json.dumps(
            b.json(), sort_keys=True
        ):
            differing.append(url)
    assert not differing, (len(differing), differing[:5])


@pytest.mark.parametrize("every_url", [pytest.param(True, marks=pytest.mark.full), False])
def test_files_no_commit_names_are_never_read(s3, layout, every_url):  # noqa: F811
    fake, client, local = s3
    tmp, _ = layout
    tables = tmp / "layout" / "sim" / "stream" / "v1" / "tables"
    planted = []
    for d in sorted(p for p in tables.iterdir() if p.is_dir())[:3]:
        for part in sorted(p for p in d.iterdir() if p.is_dir())[:2]:
            for name in (
                "run-9999999999-999999999999.parquet",
                "base-999999999999-9999999999-999999999999-0000.parquet",
            ):
                src = next(part.glob("*.parquet"), None)
                if src is not None:
                    (part / name).write_bytes(src.read_bytes())
                    planted.append(str((part / name).relative_to(tmp / "layout")))
    assert planted
    fake.reset()
    urls = _urls(local)
    for url in urls if every_url else few_per_group(urls, group):
        client.get(url)
    read = {h.key for h in fake.hits}
    assert not read & set(planted), sorted(read & set(planted))[:3]


def test_a_session_page_reads_its_session_tables_only_from_its_partitions(s3):
    """Every table keyed by session ("s:" routing) is read only from the session group's
    partitions. (The tables still read whole, the "*" ones, wait on the spec revision of person
    resolution and those tables' shapes.)"""
    fake, client, local = s3
    url = next(u for u in _urls(local) if u.startswith("/api/v1/sessions/") and u.count("/") == 4)
    sid = url.split("/")[4].split("?")[0]
    client.get(url)
    store = client.app.state.store
    store._loaded.clear()
    fake.reset()
    client.get(url)
    from aintercept_api import state_sql

    keyed = {t for t, (prefix, _) in state_sql.STATE_ROUTING.items() if prefix == "s:"}
    want = {buckets.bucket_of("s:" + s) for s in store._session_group({sid})}
    stray = sorted(
        h.key
        for h in fake.hits
        if "/tables/" in h.key
        and h.key.split("/tables/")[1].split("/")[0] in keyed
        and int(h.key.split("bucket=")[1][:2]) not in want
    )
    assert not stray, stray[:5]


def test_connections_never_download_an_extension_and_the_image_has_httpfs():
    con = buckets.connect(LocalSource(pathlib.Path(".")))
    assert con.execute("SELECT current_setting('autoinstall_known_extensions')").fetchone() == (False,)
    dockerfile = (pathlib.Path(__file__).parents[1] / "Dockerfile").read_text()
    assert "INSTALL httpfs" in dockerfile and "AINTERCEPT_DUCKDB_EXTENSIONS" in dockerfile
