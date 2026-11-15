import threading

from tests.test_session import error_on_query


def test_waiting_for_traffic(dataset, pg_client_for, write_sample):
    client = pg_client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "waiting")
    d = r.json()["data"]
    assert d["firstRequestAt"] is None and d["lastIngestAt"] is None and d["waitingSince"] is None
    assert r.json()["meta"]["lastDataAt"] is None


def test_probes_only_is_still_waiting(dataset, pg_client_for, write_sample):
    dataset.transaction("2026-11-04T17:40:00", path="/", method="GET", synthetic=True)
    dataset.transaction("2026-11-04T18:00:00", path="/", method="GET", synthetic=True)
    client = pg_client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "probes_only")
    d = r.json()["data"]
    assert d["firstRequestAt"] is None
    assert d["waitingSince"] == "2026-11-04T17:40:00Z"
    # Probes count as data for /status: the badge reports whether the pipeline flows.
    assert d["lastIngestAt"] == r.json()["meta"]["lastDataAt"] == "2026-11-04T18:00:00.002000Z"


def test_receiving_traffic(dataset, pg_client_for, write_sample):
    dataset.transaction("2026-11-02T05:31:00")
    dataset.transaction("2026-11-04T18:04:30")
    client = pg_client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "receiving")
    d = r.json()["data"]
    assert d["firstRequestAt"] == "2026-11-02T05:31:00Z"
    assert d["waitingSince"] is None
    assert d["lastIngestAt"] == "2026-11-04T18:04:30.002000Z"


def test_delayed(dataset, pg_client_for, write_sample):
    """Data stopped 20 minutes before "now": the UI shows DELAYED."""
    dataset.transaction("2026-11-04T17:45:00")
    client = pg_client_for(dataset)
    r = client.get("/api/v1/status")
    write_sample(client, r, "delayed")
    assert r.json()["meta"]["lastDataAt"] == "2026-11-04T17:45:00.002000Z"


def test_query_failure_returns_typed_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)

    error_on_query(client, monkeypatch)
    r = client.get("/api/v1/status")
    write_sample(client, r, "error", status=500)
    assert r.json() == {"error": {"code": "internal", "message": "Query failed", "queryId": "q_sample"}}


def test_concurrent_requests(dataset, pg_client_for):
    for h in range(24):
        dataset.transaction(f"2026-11-04T{h:02d}:00:00")
    client = pg_client_for(dataset)
    results = []

    def hit():
        results.append(client.get("/api/v1/status").json()["data"]["firstRequestAt"])

    threads = [threading.Thread(target=hit) for _ in range(16)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert results == ["2026-11-04T00:00:00Z"] * 16


def test_retention_mode(tmp_path, pg_client_for):
    """`contentRetained` is the deployment's mode, as the calls show it (§6.4): body-stripped rows (the
    default) say false; once a call's recorded bodies gave its detail, true."""
    from tests.builders import Dataset
    from tests.test_requests import retained

    stripped, kept = Dataset(tmp_path / "stripped"), Dataset(tmp_path / "kept")
    stripped.transaction("2026-11-04T18:00:00")
    assert pg_client_for(stripped).get("/api/v1/status").json()["data"]["contentRetained"] is False
    retained(kept)
    assert pg_client_for(kept).get("/api/v1/status").json()["data"]["contentRetained"] is True
