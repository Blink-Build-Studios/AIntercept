"""The store's guarantees over the processor's tables: polling cost, incremental
loads, rebuilt output, and raw rows read from a transaction's lineage."""

import gzip
import itertools
import json
import pathlib

from aintercept_api.raw import HttpRaw, LocalRaw
from aintercept_api.store import Store
from tests.builders import Dataset, ns
from tests.conftest import make_client, run_processor


def shaped(ds: Dataset, *, fresh: bool = True) -> pathlib.Path:
    root = ds.write()
    run_processor(root, root.parent / "shaped", fresh=fresh)
    return root


def more_of(ds: Dataset) -> Dataset:
    """More rows for the same data dir, with correlation ids the first set didn't use."""
    more = Dataset(ds.root)
    more._ids = itertools.count(1000)
    return more


def manifest(root: pathlib.Path) -> dict:
    m: dict = json.loads((root.parent / "shaped" / "manifest.json").read_text())
    return m


def test_polling_cost_is_only_new_parts(dataset):
    """The deterministic cost check: a poll with no new commit reads nothing, and
    a new commit loads only its own parts."""
    dataset.transaction("2026-11-04T17:00:00")
    dataset.transaction("2026-11-04T18:00:00")
    root = shaped(dataset)
    store = Store(root)
    client = make_client(store)
    client.get("/api/v1/status")
    first = store.parts_read
    assert first > 0
    for _ in range(5):
        client.get("/api/v1/status")
    assert store.parts_read == first  # no new commit: nothing read

    before = manifest(root)
    more = more_of(dataset)
    more.transaction("2026-11-04T19:00:00")
    more.write()
    run_processor(root, root.parent / "shaped", fresh=False)  # the processor's next run
    after = manifest(root)
    new_facts = sum(len(after["facts"][t]) - len(before["facts"].get(t, [])) for t in after["facts"])
    new_snaps = sum(after["snapshots"][t] != before["snapshots"].get(t) for t in after["snapshots"])
    r = client.get("/api/v1/status")
    assert store.parts_read == first + new_facts + new_snaps  # exactly the new commit's files
    assert r.json()["data"]["lastIngestAt"] == "2026-11-04T19:00:00.002000Z"
    assert store.query("SELECT count(*) FROM calls")[0][0] == 3


def test_ingest_interval_limits_how_often_the_manifest_is_read(dataset):
    dataset.transaction("2026-11-04T18:00:00")
    root = shaped(dataset)
    store = Store(root)
    assert store.ingest() > 0
    more = more_of(dataset)
    more.transaction("2026-11-04T19:00:00")
    more.write()
    run_processor(root, root.parent / "shaped", fresh=False)
    store.maybe_ingest(min_interval_s=3600)  # too soon: not read
    assert store.query("SELECT count(*) FROM calls")[0][0] == 1
    store.maybe_ingest(min_interval_s=0)
    assert store.query("SELECT count(*) FROM calls")[0][0] == 2


def test_a_failed_load_keeps_what_was_loaded(dataset):
    """A part that can't be read (gone mid-read) rolls the load back: the tables
    stay as they were, the request is still served, and the next load retries."""
    dataset.transaction("2026-11-04T18:00:00")
    root = shaped(dataset)
    store = Store(root)
    store.ingest()
    before = set(manifest(root)["facts"]["requests"])
    more = more_of(dataset)
    more.transaction("2026-11-04T19:00:00")
    more.write()
    run_processor(root, root.parent / "shaped", fresh=False)
    new = [k for k in manifest(root)["facts"]["requests"] if k not in before]
    gone = root.parent / "shaped" / new[0].split("shaped/", 1)[-1]
    saved = gone.read_bytes()
    gone.unlink()
    client = make_client(store)
    assert client.get("/api/v1/people").status_code == 200
    assert store.query("SELECT count(*) FROM calls")[0][0] == 1
    gone.write_bytes(saved)
    store.ingest()
    assert store.query("SELECT count(*) FROM calls")[0][0] == 2


def test_a_rebuilt_output_is_reloaded(dataset):
    """The processor's output rebuilt from scratch (its run numbers start over)
    replaces what was loaded, rather than being taken for an append."""
    dataset.transaction("2026-11-04T18:00:00")
    root = shaped(dataset)
    store = Store(root)
    store.ingest()
    more = more_of(dataset)
    more.transaction("2026-11-04T18:30:00")
    more.write()
    run_processor(root, root.parent / "shaped")  # fresh: same run number, different content
    store.ingest()
    assert store.query("SELECT count(*) FROM calls")[0][0] == 2


def test_body_chunks_become_byte_totals(dataset):
    cid = dataset.transaction("2026-11-04T18:00:00")
    for i, (phase, size) in enumerate(
        [("request_body_chunk", 1000), ("request_body_chunk", 24), ("response_body_chunk", 512)]
    ):
        dataset.add(
            {
                "correlation_id": cid,
                "seq": 10 + i,
                "timestamp_ns": ns("2026-11-04T18:00:00") + 500_000 + i,
                "phase": phase,
                "bytes": size,
            }
        )
    store = Store(shaped(dataset))
    store.ingest()
    assert store.query("SELECT request_bytes, response_bytes FROM calls WHERE correlation_id = ?", [cid]) == [
        (1024, 512)
    ]


def test_malformed_lines_are_dropped_and_counted(dataset):
    dataset.transaction("2026-11-04T18:00:00")
    root = dataset.write()
    part = next(root.glob("dt=*/hour=*/instance=*"))
    with gzip.open(part / "2-junk.ndjson.gz", "wt") as f:
        f.write('not json\n{"phase": "complete"}\n[1, 2]\n')
    run_processor(root, root.parent / "shaped")
    store = Store(root)
    store.ingest()
    assert store.query("SELECT count(*) FROM calls")[0][0] == 1
    assert store.query("SELECT sum(bad) FROM ledger")[0][0] == 3


def test_raw_rows_come_from_the_transactions_files(dataset):
    """The drawer's raw view reads only the transaction's lineage, including a
    transaction that crossed into the next hour's file."""
    other = dataset.transaction("2026-11-04T17:30:00")
    cid = dataset.transaction("2026-11-04T17:59:59.9995")  # its complete row lands at 18:00:00.0015
    store = Store(shaped(dataset))
    store.ingest()
    first, last = store.query("SELECT first_file, source_file FROM calls WHERE correlation_id = ?", [cid])[0]
    assert "hour=17" in first and "hour=18" in last
    phases = [r["phase"] for r in store.raw_rows(cid)]
    assert sorted(phases) == ["complete", "request_headers", "response_headers"]
    assert all(r["correlation_id"] == other for r in store.raw_rows(other))
    assert store.raw_rows("nope") == []


def test_raw_rows_read_every_hour_a_long_transaction_spans(dataset):
    """A stream from 17:59 to 19:01 has rows in hour 18 too: neither its first
    file's hour nor its last's."""
    cid = dataset.transaction("2026-11-04T17:59:00")
    for r in dataset.rows:
        if r["correlation_id"] == cid and r["phase"] == "response_headers":
            r["timestamp_ns"] = ns("2026-11-04T18:30:00")
        if r["correlation_id"] == cid and r["phase"] == "complete":
            r["timestamp_ns"] = ns("2026-11-04T19:01:00")
    store = Store(shaped(dataset))
    store.ingest()
    first, last = store.query("SELECT first_file, source_file FROM calls WHERE correlation_id = ?", [cid])[0]
    assert "hour=17" in first and "hour=19" in last
    assert sorted(r["phase"] for r in store.raw_rows(cid)) == ["complete", "request_headers", "response_headers"]


def test_the_drawer_works_from_the_tables_when_raw_files_cant_be_read(dataset, tmp_path):
    """Raw files unreachable (not synced, the object store down): the request
    still opens, from its request row, and Raw says why it's partial."""
    cid = dataset.transaction(
        "2026-11-04T18:00:00",
        path="/v1/messages",
        headers={"x-claude-code-session-id": "s1", "user-agent": "claude-cli/2"},
    )
    store = Store(shaped(dataset), raw=LocalRaw(tmp_path / "nothing-here"))
    client = make_client(store)
    r = client.get(f"/api/v1/requests/{cid}")
    assert r.status_code == 200, r.text
    assert r.json()["data"]["path"] == "/v1/messages"
    assert client.get(f"/api/v1/requests/{cid}/details").status_code == 200
    raw = client.get(f"/api/v1/requests/{cid}/raw").json()["data"]
    assert raw["unavailable"].startswith("The raw files for this request can't be read")
    assert {h["name"] for h in raw["requestHeaders"]} == {
        "user-agent",
        "x-claude-code-session-id",
    }  # what the tables keep


def test_raw_files_can_come_from_the_mirror_over_http(dataset):
    """In the cluster the raw files come from the `aintercept mirror` sidecar."""
    import http.server
    import threading
    import urllib.parse

    cid = dataset.transaction("2026-11-04T18:00:00")
    root = shaped(dataset)
    local = LocalRaw(root)

    class Mirror(http.server.BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            u = urllib.parse.urlparse(self.path)
            q = urllib.parse.parse_qs(u.query)
            if u.path == "/raw/list":
                folder = q["prefix"][0]
                body = "\n".join(folder + n for n in local.folder(folder)).encode()
            else:
                body = local.read(q["key"][0]) or b""
                if not body:
                    self.send_response(404)
                    self.end_headers()
                    return
            self.send_response(200)
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a: object) -> None:
            pass

    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Mirror)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        store = Store(root, raw=HttpRaw(f"http://127.0.0.1:{srv.server_address[1]}"))
        store.ingest()
        assert sorted(r["phase"] for r in store.raw_rows(cid)) == ["complete", "request_headers", "response_headers"]
    finally:
        srv.shutdown()


def test_a_compaction_swaps_only_the_parts_it_replaced(dataset):
    """When the processor merges run parts into day files, the store deletes the
    replaced parts' rows and loads the new files, not the whole table."""
    root = dataset.write()
    store = Store(root)
    sets = [dataset] + [more_of(dataset) for _ in range(5)]
    for i, ds in enumerate(sets):
        ds._ids = itertools.count(100 * (i + 1))
        ds.transaction(f"2026-11-04T{10 + i:02d}:00:00")
        ds.write()
        run_processor(root, root.parent / "shaped", fresh=False, compact_at=2)
        store.ingest()
        assert store.query("SELECT count(*), count(DISTINCT call_id) FROM calls")[0] == (i + 1, i + 1)
    days = [k for k in manifest(root)["facts"]["requests"] if "/day-" in k or k.startswith("requests/day-")]
    assert days, "the runs should have been compacted into a day file"
    fresh = Store(root)
    fresh.ingest()
    assert store.query("SELECT * EXCLUDE (filename) FROM requests ORDER BY call_id") == fresh.query(
        "SELECT * EXCLUDE (filename) FROM requests ORDER BY call_id"
    )
