"""`demo` scenarios: one person's real Claude Code traffic (sanitized; see
tools/make_demo_fixture.py), so stories show real shapes and numbers."""

import pytest

from tests.builders import Dataset, load_fixture
from tests.conftest import bounded_store, make_client

DEMO_ACCOUNT = "7d2e0000-0000-4000-8000-00000000a41c"


@pytest.fixture(scope="module")
def demo(tmp_path_factory):
    """One client over the demo fixture, shared: these tests only read it."""
    ds = Dataset(tmp_path_factory.mktemp("demo") / "data")
    load_fixture(ds, "demo")
    store = bounded_store(ds.write())
    yield make_client(store)
    store.close()


def test_person_sessions_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/sessions")
    write_sample(client, r, "demo")
    (s,) = r.json()["data"]
    # The main session as measured on the real capture (docs/ui/BUILD_PLAN.md §9, 2026-11-05).
    # Every typed prompt, as the Prompts tab lists them: 38 have a captured call, 5 went around the proxy.
    assert s["modelCalls"] == 640 and s["prompts"] == 43 and s["capturedFromPrompt"] == 7
    assert s["live"] is True and s["bypassPct"] == 99.7
    cov = s["coverage"]
    assert (cov["reported"], cov["notCaptured"]) == (602, 95)
    assert [w["count"] for w in cov["windows"]] == [37, 4, 3, 50, 1]


def test_person_timeline_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/timeline")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["binSeconds"] == 3600 and len(d["sessions"]) == 1 and d["sessions"][0]["live"] is True
    assert sum(b["count"] for b in d["modelCalls"]) == 640
    assert sum(w["count"] for w in d["notCaptured"]) == 95
    assert {p["state"] for p in d["presence"]} == {"focused", "away", "asleep"}


def test_person_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}")
    write_sample(client, r, "demo")
    p = r.json()["data"]
    assert p["live"] is True and p["posture"]["bypassPct"] == 99.7


def test_person_summary_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/summary")
    write_sample(client, r, "demo")
    rb = r.json()["data"]["ribbon"]
    assert rb["modelCalls"] == 640 and rb["sessions"] == 1


def test_person_requests_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/requests", params={"pageSize": 50})
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] > 640


def test_coverage_demo(demo, write_sample):
    client = demo
    r = client.get("/api/v1/coverage", params={"personId": DEMO_ACCOUNT})
    write_sample(client, r, "demo")
    assert r.json()["data"]["notCaptured"] == 95


# --- the session page (M3): the main session, live at the frozen clock ------------------------------

DEMO_SESSION = "d7ee5e66-a84c-4d8e-8455-45c27d00ec4a"
SESSION = f"/api/v1/sessions/{DEMO_SESSION}"
# The brief-era capture window (handoff §9): includes the 15:00-17:59 IPv6 bypass.
BRIEF_WINDOW = {"from": "2026-11-03T14:20:00Z", "to": "2026-11-03T22:40:00Z"}


def test_session_demo(demo, write_sample):
    client = demo
    r = client.get(SESSION)
    write_sample(client, r, "demo")
    h = r.json()["data"]
    assert (h["modelCalls"], h["notCaptured"], h["events"]) == (640, 95, 21305)  # DATA_ANSWERS §10: 640 calls
    assert h["live"] is True and h["personId"] == DEMO_ACCOUNT and h["device"]["platform"] == "darwin"
    assert h["networks"] == 3  # client IPs, faked one-for-one by the fixture


def test_session_timeline_demo(demo, write_sample):
    client = demo
    r = client.get(f"{SESSION}/timeline")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert sum(b["count"] for b in d["modelCalls"]) == 640 and sum(w["count"] for w in d["notCaptured"]) == 95
    # DATA_ANSWERS §6: bypass → default → bypass, twice within 20 s on Nov 3.
    assert [p["mode"] for p in d["permission"]] == ["bypassPermissions", "default"] * 2 + ["bypassPermissions"]
    toggles = d["permission"][1:4]
    assert all(p["from"].startswith("2026-11-03T16:51") for p in toggles)
    # Handoff §9: compaction from ~905k with a 71 s wait (the context after is the next call's, not the summary's).
    first = d["compactions"][0]
    assert len(d["compactions"]) == 3 and 900_000 < first["from"] < 910_000 and round(first["waitMs"] / 1000) == 71
    assert max(c["tokens"] for c in d["context"]) == first["from"]


def test_session_state_demo(demo, write_sample):
    client = demo
    r = client.get(f"{SESSION}/state")
    write_sample(client, r, "demo")
    c = r.json()["data"]["counts"]
    assert (c["modelCalls"], c["events"], c["compactions"]) == (640, 21305, 3)
    assert c["requests"] == client.get(f"{SESSION}/requests").json()["meta"]["total"]


def test_session_prompts_demo(demo, write_sample):
    client = demo
    r = client.get(f"{SESSION}/prompts", params={**BRIEF_WINDOW, "pageSize": 10})
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] == 30  # prompts and between rows, recaps included
    everything = client.get(f"{SESSION}/prompts", params={"pageSize": 200}).json()["data"]
    steps = sum(len(i["steps"]) for i in everything if i["type"] == "prompt")
    # Between rows that are calls (compactions, auxiliary calls); recaps aren't calls.
    outside = [i["between"] for i in everything if i["type"] == "between" and i["between"]["requestId"]]
    assert steps + len(outside) == 640  # every captured call is a step or a between row, exactly once


def test_session_requests_demo(demo, write_sample):
    client = demo
    r = client.get(f"{SESSION}/requests")
    write_sample(client, r, "demo")
    assert r.json()["meta"]["total"] > 640 + 95
    missed = client.get(f"{SESSION}/requests", params={"captured": "false"}).json()["meta"]["total"]
    assert missed == 95


def test_session_events_demo(demo, write_sample):
    client = demo
    r = client.get(f"{SESSION}/events")
    write_sample(client, r, "demo")
    meta = r.json()["meta"]
    assert meta["total"] == 21305 and meta["shown"] < meta["total"]


# --- the request drawer (M4): a main-thread call of the demo session ----------------------------------


def _demo_call(client) -> str:
    """The first main-thread step of the brief window (DATA_ANSWERS: the 15:00 bypass is later)."""
    items = client.get(f"{SESSION}/prompts", params={**BRIEF_WINDOW, "pageSize": 10}).json()["data"]
    main = [s["requestId"] for i in items if i["type"] == "prompt" for s in i["steps"] if s["requestClass"] == "main"]
    return str(main[0])


def test_request_demo(demo, write_sample):
    client = demo
    cid = _demo_call(client)
    r = client.get(f"/api/v1/requests/{cid}")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["kind"] == "normal" and d["sessionId"] == DEMO_SESSION and d["crossCheck"]["matched"] is True
    assert d["promptIndex"] is not None and d["stepIndex"] == 1
    # The demo's not-captured calls resolve too (built from analytics only).
    missed = client.get(f"{SESSION}/requests", params={"captured": "false", "pageSize": 1}).json()["data"][0]
    assert client.get(f"/api/v1/requests/{missed['requestId']}").json()["data"]["kind"] == "notCaptured"


def test_request_raw_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/requests/{_demo_call(client)}/raw")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["unavailable"] is None and d["contentRetained"] is False and d["responseLine"].startswith("200")


def test_request_details_demo(demo, write_sample):
    client = demo
    r = client.get(f"/api/v1/requests/{_demo_call(client)}/details")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert len(d["sections"]) == 8 and any(e["name"] == "tengu_api_success" for e in d["events"])


def test_event_demo(demo, write_sample):
    client = demo
    first = client.get(f"{SESSION}/events", params={"pageSize": 1}).json()["data"][0]
    r = client.get(f"/api/v1/events/{first['eventId']}")
    write_sample(client, r, "demo")
    d = r.json()["data"]
    assert d["name"] == first["name"] and d["sessionId"] == DEMO_SESSION and d["raw"]["name"] == first["name"]


def test_prompt_counts_agree(demo):
    """The session's prompt count (its row, its header) is the Prompts tab's: every typed prompt."""
    client = demo
    (row,) = client.get(f"/api/v1/people/{DEMO_ACCOUNT}/sessions").json()["data"]
    header = client.get(SESSION).json()["data"]
    tab, page = 0, 1
    while True:  # every page of the Prompts tab
        body = client.get(f"{SESSION}/prompts", params={"pageSize": 200, "page": page}).json()
        tab += sum(1 for i in body["data"] if i["type"] == "prompt")
        if page * 200 >= body["meta"]["total"]:
            break
        page += 1
    assert row["prompts"] == header["prompts"] == tab == 43


def test_kind_counts_follow_the_other_filters(demo):
    """The kind chips' counts: every filter but kind applies (handoff §11E.2: MODEL 94, the others 0)."""
    client = demo
    base = f"/api/v1/people/{DEMO_ACCOUNT}/requests"
    everything = client.get(base).json()
    assert sum(everything["kindCounts"].values()) == everything["meta"]["total"]
    model_only = client.get(base, params={"kind": "model"}).json()
    assert model_only["kindCounts"] == everything["kindCounts"]  # choosing a kind doesn't change the chips
    assert model_only["meta"]["total"] == everything["kindCounts"]["model"]
    missed = client.get(base, params={"captured": "false"}).json()
    assert missed["kindCounts"] == {"model": 95}
    s = client.get(f"{SESSION}/requests", params={"captured": "false"}).json()
    assert set(s["kindCounts"]) == {"model"} and s["kindCounts"]["model"] == s["meta"]["total"]
