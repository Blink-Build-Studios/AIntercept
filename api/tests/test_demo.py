"""`demo` scenarios: one person's real Claude Code traffic (sanitized: tests/fixtures/demo.ndjson.gz),
so stories show real shapes and numbers. Its analytics stay in the raw files: what only they fed (events,
coverage and the calls that went around the proxy, bypass %, compactions) is null, empty or 0."""

import pytest

from tests.builders import Dataset, load_fixture
from tests.conftest import PgDataset, make_client, pg_admin

DEMO_ACCOUNT = "7d2e0000-0000-4000-8000-00000000a41c"


@pytest.fixture(scope="module")
def pg_demo(tmp_path_factory):
    """The demo fixture in Postgres (conftest.PgDataset): every page's samples are v9's (§6)."""
    ds = Dataset(tmp_path_factory.mktemp("pg-demo") / "data")
    load_fixture(ds, "demo")
    pg = PgDataset(pg_admin(), ds.write())
    store = pg.store()
    yield make_client(store)
    store.close()
    pg.close()


def test_person_sessions_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/sessions")
    write_sample(client, r, "demo")
    (s,) = r.json()["data"]
    # The main session as measured on the real capture (docs/ui/BUILD_PLAN.md §9, 2026-11-05): its prompts
    # with a captured call (5 more went around the proxy, which only its analytics said).
    assert s["modelCalls"] == 640 and s["prompts"] == 38 and s["capturedFromPrompt"] is None
    assert s["live"] is True and s["bypassPct"] is None and s["coverage"] is None


def test_person_timeline_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/timeline")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["binSeconds"] == 3600 and len(d["sessions"]) == 1 and d["sessions"][0]["live"] is True
    assert sum(b["count"] for b in d["modelCalls"]) == 640 and d["notCaptured"] == []


def test_person_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}")
    write_sample(client, r, "demo")
    p = r.json()["data"]
    assert p["live"] is True and p["posture"]["bypassPct"] is None and p["devices"]


def test_person_summary_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/summary")
    write_sample(client, r, "demo")
    rb = r.json()["data"]["ribbon"]
    assert rb["modelCalls"] == 640 and rb["sessions"] == 1


def test_person_work_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/work")
    write_sample(client, r, "demo")
    (g,) = r.json()["data"]
    assert g["sessions"] == 1 and g["rows"][0]["sessionId"] == DEMO_SESSION


def test_person_requests_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/requests", params={"pageSize": 50})
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] > 640


# --- the session page (M3): the main session, live at the frozen clock ------------------------------

DEMO_SESSION = "d7ee5e66-a84c-4d8e-8455-45c27d00ec4a"
SESSION = f"/api/v1/sessions/{DEMO_SESSION}"
# The brief-era capture window (handoff §9): includes the 15:00-17:59 IPv6 bypass.
BRIEF_WINDOW = {"from": "2026-11-03T14:20:00Z", "to": "2026-11-03T22:40:00Z"}


def test_session_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(SESSION)
    write_sample(client, r, "demo")
    h = r.json()["data"]
    assert (h["modelCalls"], h["notCaptured"], h["events"]) == (640, None, None)  # DATA_ANSWERS §10: 640 calls
    assert h["live"] is True and h["personId"] == DEMO_ACCOUNT and h["device"]["platform"] is None
    assert (h["turns"], h["prompts"]) == (38, 38)
    assert h["networks"] == 3  # client IPs, faked one-for-one by the fixture


def test_session_timeline_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"{SESSION}/timeline")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert sum(b["count"] for b in d["modelCalls"]) == 640 and d["notCaptured"] == []
    # Handoff §9's compaction from ~905k was Claude Code's analytics (compactions are empty); the context the
    # calls sent still peaks there.
    assert d["compactions"] == [] and 900_000 < max(c["tokens"] for c in d["context"]) < 910_000


def test_session_state_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"{SESSION}/state")
    write_sample(client, r, "demo")
    c = r.json()["data"]["counts"]
    assert (c["modelCalls"], c["events"], c["compactions"]) == (640, 0, 0)
    assert c["requests"] == client.get(f"{SESSION}/requests").json()["meta"]["total"]


def test_session_prompts_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"{SESSION}/prompts", params={**BRIEF_WINDOW, "pageSize": 10})
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] == 21  # prompts and between rows (the calls outside a prompt)
    everything = client.get(f"{SESSION}/prompts", params={"pageSize": 200}).json()["data"]
    steps = sum(len(i["steps"]) for i in everything if i["type"] == "prompt")
    outside = [i["between"] for i in everything if i["type"] == "between" and i["between"]["requestId"]]
    assert steps + len(outside) == 640  # every captured call is a step or a between row, exactly once


def test_session_turns_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"{SESSION}/turns", params={**BRIEF_WINDOW, "pageSize": 10})
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] > 0


def test_session_requests_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"{SESSION}/requests")
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] == 640 + 9 and r.json()["kindCounts"] == {"count": 9, "model": 640}
    missed = client.get(f"{SESSION}/requests", params={"captured": "false"}).json()["meta"]["total"]
    assert missed == 0  # only its analytics said which calls went around the proxy


def test_session_events_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"{SESSION}/events")
    write_sample(client, r, "demo")
    meta = r.json()["meta"]
    assert (meta["total"], meta["shown"], r.json()["data"]) == (0, 0, [])  # analytics aren't stored


# --- the request drawer (M4): a main-thread call of the demo session ----------------------------------


def _demo_call(client) -> str:
    """The first main-thread step of the brief window (DATA_ANSWERS: the 15:00 bypass is later)."""
    items = client.get(f"{SESSION}/prompts", params={**BRIEF_WINDOW, "pageSize": 10}).json()["data"]
    main = [s["requestId"] for i in items if i["type"] == "prompt" for s in i["steps"] if s["requestClass"] == "main"]
    return str(main[0])


def test_request_demo(pg_demo, write_sample):
    client = pg_demo
    cid = _demo_call(client)
    r = client.get(f"/api/v1/requests/{cid}")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["kind"] == "normal" and d["sessionId"] == DEMO_SESSION and d["crossCheck"]["matched"] is False
    assert d["promptIndex"] is not None and d["stepIndex"] == 1


def test_request_raw_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/requests/{_demo_call(client)}/raw")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["unavailable"] is None and d["contentRetained"] is False and d["responseLine"].startswith("200")


def test_request_details_demo(pg_demo, write_sample):
    client = pg_demo
    r = client.get(f"/api/v1/requests/{_demo_call(client)}/details")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert len(d["sections"]) == 8 and d["events"] == []


def test_prompt_counts_agree(pg_demo):
    """The session's prompt count (its row, its header) is the Prompts tab's: every prompt with a call."""
    client = pg_demo
    (row,) = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/sessions").json()["data"]
    header = client.get(SESSION).json()["data"]
    tab, page = 0, 1
    while True:  # every page of the Prompts tab
        body = client.get(f"{SESSION}/prompts", params={"pageSize": 200, "page": page}).json()
        tab += sum(1 for i in body["data"] if i["type"] == "prompt")
        if page * 200 >= body["meta"]["total"]:
            break
        page += 1
    assert row["prompts"] == header["prompts"] == tab == 38


def test_kind_counts_follow_the_other_filters(pg_demo):
    """The kind chips' counts: every filter but kind applies (handoff §11E.2: MODEL 94, the others 0)."""
    client = pg_demo
    base = f"/api/v1/people/{DEMO_ACCOUNT}/requests"
    everything = client.get(base).json()
    assert sum(everything["kindCounts"].values()) == everything["meta"]["total"]
    model_only = client.get(base, params={"kind": "model"}).json()
    assert model_only["kindCounts"] == everything["kindCounts"]  # choosing a kind doesn't change the chips
    assert model_only["meta"]["total"] == everything["kindCounts"]["model"]
    missed = client.get(base, params={"captured": "false"}).json()
    assert missed["kindCounts"] == {} and missed["meta"]["total"] == 0  # every row is a captured call
    s = client.get(f"{SESSION}/requests", params={"kind": "count"}).json()
    assert s["kindCounts"] == {"count": 9, "model": 640} and s["meta"]["total"] == 9
