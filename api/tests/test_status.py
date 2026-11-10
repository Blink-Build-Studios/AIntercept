import gzip
import json
import threading

from tests.conftest import bounded_store, make_client


def test_waiting_for_traffic(dataset, client_for, write_sample):
    client = client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "waiting")
    d = r.json()["data"]
    assert d["firstRequestAt"] is None and d["lastIngestAt"] is None and d["waitingSince"] is None
    assert r.json()["meta"]["lastDataAt"] is None


def test_probes_only_is_still_waiting(dataset, client_for, write_sample):
    dataset.transaction("2026-11-04T17:40:00", path="/", method="GET", synthetic=True)
    dataset.transaction("2026-11-04T18:00:00", path="/", method="GET", synthetic=True)
    client = client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "probes_only")
    d = r.json()["data"]
    assert d["firstRequestAt"] is None
    assert d["waitingSince"] == "2026-11-04T17:40:00Z"
    # Probes count as data for /status: the badge reports whether the pipeline flows.
    assert d["lastIngestAt"] == r.json()["meta"]["lastDataAt"] == "2026-11-04T18:00:00.002000Z"


def test_receiving_traffic(dataset, client_for, write_sample):
    dataset.transaction("2026-11-02T05:31:00")
    dataset.transaction("2026-11-04T18:04:30")
    client = client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "receiving")
    d = r.json()["data"]
    assert d["firstRequestAt"] == "2026-11-02T05:31:00Z"
    assert d["waitingSince"] is None
    assert d["lastIngestAt"] == "2026-11-04T18:04:30.002000Z"


def test_delayed(dataset, client_for, write_sample):
    """Data stopped 20 minutes before "now": the UI shows DELAYED."""
    dataset.transaction("2026-11-04T17:45:00")
    client = client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "delayed")
    assert r.json()["meta"]["lastDataAt"] == "2026-11-04T17:45:00.002000Z"


def test_query_failure_returns_typed_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)

    def boom(*a, **k):
        raise RuntimeError("disk on fire")

    monkeypatch.setattr(client.app.state.store, "query", boom)
    r = client.get("/api/v1/status")
    write_sample(client, r, "error", status=500)
    assert r.json() == {"error": {"code": "internal", "message": "Query failed", "queryId": "q_sample"}}


def test_unreadable_files_are_skipped_not_fatal(dataset, tmp_path):
    """An empty, truncated (mid-write) or non-gzip file must not fail the query."""
    dataset.transaction("2026-11-04T18:00:00")
    root = dataset.write()
    part = next(root.glob("dt=*/hour=*/instance=*"))
    good = next(part.glob("*.ndjson.gz")).read_bytes()
    (part / "1-empty.ndjson.gz").write_bytes(b"")
    (part / "2-truncated.ndjson.gz").write_bytes(good[: len(good) // 2])
    (part / "3-notgzip.ndjson.gz").write_bytes(b"hello")
    store = bounded_store(root)
    r = make_client(store).get("/api/v1/status")
    assert r.status_code == 200
    assert r.json()["data"]["firstRequestAt"] == "2026-11-04T18:00:00Z"
    assert sorted(p.rsplit("/", 1)[-1] for p in store.skipped) == ["2-truncated.ndjson.gz", "3-notgzip.ndjson.gz"]


def test_skipped_file_is_retried_once_it_changes(dataset):
    dataset.transaction("2026-11-04T18:00:00")
    root = dataset.write()
    part = next(root.glob("dt=*/hour=*/instance=*"))
    late = part / "9-late.ndjson.gz"
    late.write_bytes(b"partial")
    store = bounded_store(root)
    assert make_client(store).get("/api/v1/status").json()["data"]["lastIngestAt"] == "2026-11-04T18:00:00.002000Z"
    row = {"timestamp_ns": 1_793_815_380_000_000_000, "phase": "complete", "correlation_id": "late-1"}  # 18:03
    with gzip.open(late, "wt") as f:  # the write finishes
        f.write(json.dumps(row) + "\n")
    assert make_client(store).get("/api/v1/status").json()["data"]["lastIngestAt"] == "2026-11-04T18:03:00Z"
    assert store.skipped == []


def test_data_dir_name_is_never_parsed_as_sql_or_glob(tmp_path):
    from tests.builders import Dataset

    ds = Dataset(tmp_path / "o'brien [1]? evil') UNION SELECT 1 --")
    ds.transaction("2026-11-04T18:00:00")
    r = make_client(bounded_store(ds.write())).get("/api/v1/status")
    assert r.status_code == 200
    assert r.json()["data"]["firstRequestAt"] == "2026-11-04T18:00:00Z"


def test_concurrent_requests(dataset, client_for):
    for h in range(24):
        dataset.transaction(f"2026-11-04T{h:02d}:00:00")
    client = client_for(dataset)
    results = []

    def hit():
        results.append(client.get("/api/v1/status").json()["data"]["firstRequestAt"])

    threads = [threading.Thread(target=hit) for _ in range(16)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert results == ["2026-11-04T00:00:00Z"] * 16


def test_deleted_skipped_file_is_forgotten(dataset):
    dataset.transaction("2026-11-04T18:00:00")
    root = dataset.write()
    part = next(root.glob("dt=*/hour=*/instance=*"))
    bad = part / "9-bad.ndjson.gz"
    bad.write_bytes(b"not gzip")
    store = bounded_store(root)
    make_client(store).get("/api/v1/status")
    assert len(store.skipped) == 1
    bad.unlink()
    make_client(store).get("/api/v1/status")
    assert store.skipped == []


def test_retention_mode(dataset, client_for):
    """`contentRetained` is the deployment's mode, as the processor saw it: body-stripped
    rows (the default) say false; once a row carries a body's content, true."""
    from tests.builders import body_chunks

    cid = dataset.transaction("2026-11-04T18:00:00")
    client = client_for(dataset)
    assert client.get("/api/v1/status").json()["data"]["contentRetained"] is False
    body_chunks(dataset, cid, "request_body_chunk", [b'{"model":"x"}'], start_ms=0.5, step_ms=0.1)
    dataset.write()
    assert client.get("/api/v1/status").json()["data"]["contentRetained"] is True
