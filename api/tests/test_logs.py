import json
import logging

from aintercept_api.logs import JSONFormatter


def records(caplog):
    fmt = JSONFormatter()
    return [json.loads(fmt.format(r)) for r in caplog.records if r.name == "aintercept_api"]


def test_each_request_logs_one_structured_line(dataset, client_for, caplog):
    client = client_for(dataset)
    with caplog.at_level(logging.INFO, logger="aintercept_api"):
        r = client.get("/api/v1/status")
    assert r.headers["X-Query-Id"] == "q_sample"
    req = [x for x in records(caplog) if x["msg"] == "request"]
    assert len(req) == 1
    line = req[0]
    assert {"time", "level", "msg"} <= set(line)
    assert line["level"] == "INFO"
    assert line["queryId"] == "q_sample"
    assert line["route"] == "/api/v1/status" and line["status"] == 200 and line["method"] == "GET"
    assert isinstance(line["ms"], float)


def test_failure_log_carries_the_same_query_id_as_the_error_body(dataset, client_for, caplog, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    for name in ("query", "status_stats"):  # /status's reads: a query, or the v1 store's merge
        monkeypatch.setattr(client.app.state.store, name, lambda *a, **k: 1 / 0, raising=False)
    with caplog.at_level(logging.INFO, logger="aintercept_api"):
        r = client.get("/api/v1/status")
    qid = r.json()["error"]["queryId"]
    failed = [x for x in records(caplog) if x["msg"] == "query failed"]
    assert len(failed) == 1 and failed[0]["queryId"] == qid
    assert "ZeroDivisionError" in failed[0]["error"]
    assert r.headers["X-Query-Id"] == qid


def test_failed_request_still_gets_its_request_line(dataset, client_for, caplog, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    for name in ("query", "status_stats"):  # /status's reads: a query, or the v1 store's merge
        monkeypatch.setattr(client.app.state.store, name, lambda *a, **k: 1 / 0, raising=False)
    with caplog.at_level(logging.INFO):
        client.get("/api/v1/status")
    lines = records(caplog)
    assert [x["msg"] for x in lines if x["msg"] in ("query failed", "request")] == ["query failed", "request"]
    req = next(x for x in lines if x["msg"] == "request")
    assert req["level"] == "ERROR" and req["status"] == 500 and req["route"] == "/api/v1/status"
    # Nothing escapes to the server's own (untagged) error log.
    assert not [
        r
        for r in caplog.records
        if r.name.startswith("uvicorn") or r.levelname == "ERROR" and r.name != "aintercept_api"
    ]


def test_unknown_route_returns_error_shape(dataset, client_for, caplog):
    client = client_for(dataset)
    with caplog.at_level(logging.INFO, logger="aintercept_api"):
        r = client.get("/api/v1/nope")
    assert r.status_code == 404
    assert r.json() == {"error": {"code": "http_404", "message": "Not Found", "queryId": "q_sample"}}
    assert next(x for x in records(caplog) if x["msg"] == "request")["route"] is None


def test_fields_never_overwrite_fixed_keys():
    rec = logging.makeLogRecord({"name": "aintercept_api", "msg": "hello", "levelname": "INFO"})
    rec.fields = {"msg": "spoof", "level": "DEBUG", "n": 1}
    out = json.loads(JSONFormatter().format(rec))
    assert out["msg"] == "hello" and out["level"] == "INFO"
    assert out["msg_"] == "spoof" and out["level_"] == "DEBUG" and out["n"] == 1


def test_route_template_is_never_mangled(dataset, client_for, caplog):
    """Path values that also occur elsewhere in the path (review repro)."""
    client = client_for(dataset)

    @client.app.get("/api/v1/people/{personId}/sessions/{sessionId}", response_model=None, include_in_schema=True)
    def probe(personId: str, sessionId: str):  # noqa: N803 (mirrors the contract's camelCase params)
        return {}

    with caplog.at_level(logging.INFO, logger="aintercept_api"):
        for path in ("/api/v1/people/a/sessions/b", "/api/v1/people/v1/sessions/x", "/api/v1/people/z/sessions/z"):
            client.get(path)
    routes = [x["route"] for x in records(caplog) if x["msg"] == "request"]
    assert routes == ["/api/v1/people/{personId}/sessions/{sessionId}"] * 3
