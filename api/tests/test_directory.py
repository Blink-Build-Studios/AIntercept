"""/overview and /people (handoff §5.2.1, §5.2.2): the People page."""

from tests.builders import ACCOUNT, ClaudeCode, load_fixture

S1 = "11111111-1111-4111-8111-111111111111"
S2 = "22222222-2222-4222-8222-222222222222"
S3 = "33333333-3333-4333-8333-333333333333"
OTHER = "00000000-0000-4000-8000-00000000acc2"
THIRD = "00000000-0000-4000-8000-00000000acc3"
DEMO_ACCOUNT = "7d2e0000-0000-4000-8000-00000000a41c"


def person(ds, sid, account=ACCOUNT, *, day="2026-11-04", hour=9, calls=2, device="d" * 64, mode="bypassPermissions"):
    """One session of `calls` reported model calls, with a prompt."""
    cc = ClaudeCode(ds, sid, account=account, device=device)
    cc.event(f"{day}T{hour:02d}:00:00", "tengu_input_prompt", prompt_index=1, cc_prompt_id="p1")
    for i in range(calls):
        rid = cc.call(f"{day}T{hour:02d}:{i + 1:02d}:00", prompt="p1")
        cc.reported(f"{day}T{hour:02d}:{i + 1:02d}:05", rid, mode=mode)
    return cc


def unattributed(ds, sid=S3, calls=3):
    """A session from a machine whose rows never carried an identity (pre-parser, no analytics)."""
    cc = ClaudeCode(ds, sid, account=None, device=None)
    for i in range(calls):
        cc.call(f"2026-11-02T10:{i + 1:02d}:00", prompt=None, parsed=False)


# --- /overview ---------------------------------------------------------------------------------


def test_overview_empty(dataset, client_for, write_sample):
    client = client_for(dataset)
    r = client.get("/api/v1/overview")
    write_sample(client, r, "empty")
    o = r.json()["data"]
    assert (o["people"], o["activeNow"], o["sessions"], o["requests"], o["notCaptured"]) == (0, 0, 0, 0, 0)
    assert [a["agent"] for a in o["agents"]] == ["claude-code", "codex", "chatgpt", "claude-ai"]
    assert all(a["people"] == 0 and a["tokenAccuracy"] == "missing" for a in o["agents"])
    assert o["unattributed"] == {
        "sessions": 0,
        "modelCalls": 0,
        "firstSeen": None,
        "reason": o["unattributed"]["reason"],
    }


def test_overview_one(dataset, client_for, write_sample):
    person(dataset, S1)
    client = client_for(dataset)
    r = client.get("/api/v1/overview")
    write_sample(client, r, "one")
    o = r.json()["data"]
    assert (o["people"], o["activeNow"], o["sessions"], o["notCaptured"]) == (1, 0, 1, 0)
    assert o["requests"] == 2 + 3  # two model calls and three analytics batches (one per event time)
    cc = o["agents"][0]
    assert cc == {
        "agent": "claude-code",
        "people": 1,
        "sessions": 1,
        "tokenAccuracy": "exact",
        "shape": "sessions→prompts→steps",
        "missedCalls": 0,
        "bypassPct": 100.0,
        "highestWeekPct": None,
    }


def test_overview_many(dataset, client_for, write_sample):
    person(dataset, S1)
    person(dataset, S2, OTHER, hour=18, calls=1)  # active at 18:01, now is 18:05
    client = client_for(dataset)
    r = client.get("/api/v1/overview")
    write_sample(client, r, "many")
    o = r.json()["data"]
    assert (o["people"], o["activeNow"], o["sessions"]) == (2, 1, 2)


def test_overview_unattributed(dataset, client_for, write_sample):
    person(dataset, S1)
    unattributed(dataset)
    client = client_for(dataset)
    r = client.get("/api/v1/overview")
    write_sample(client, r, "unattributed")
    u = r.json()["data"]["unattributed"]
    assert (u["sessions"], u["modelCalls"], u["firstSeen"]) == (1, 3, "2026-11-02T10:01:00Z")
    assert r.json()["data"]["people"] == 1


def test_overview_coverage_gap(dataset, client_for, write_sample):
    cc = person(dataset, S1)
    for minute in (10, 12, 14):
        cc.reported(f"2026-11-04T09:{minute}:00", f"req_bypassed_{minute}")
    client = client_for(dataset)
    r = client.get("/api/v1/overview")
    write_sample(client, r, "coverage_gap")
    assert r.json()["data"]["notCaptured"] == 3


def test_overview_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    write_sample(client, client.get("/api/v1/overview"), "error", status=500)


# --- /people -----------------------------------------------------------------------------------


def test_people_empty(dataset, client_for, write_sample):
    client = client_for(dataset)
    r = client.get("/api/v1/people")
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0


def test_people_one(dataset, client_for, write_sample):
    person(dataset, S1, calls=3)
    client = client_for(dataset)
    r = client.get("/api/v1/people")
    write_sample(client, r, "one")
    (p,) = r.json()["data"]
    assert p["personId"] == ACCOUNT and p["accountIdShort"] == "0000…acc1" and p["orgIdShort"] == "0000…00a9"
    assert (p["sessions"], p["prompts"], p["devices"], p["tokensOut"], p["tokensAccuracy"]) == (1, 1, 1, 300, "exact")
    assert p["agents"] == ["claude-code"] and p["live"] is False and p["lastSeenAt"] == "2026-11-04T09:03:05Z"
    assert p["bypassPct"] == 100.0 and p["linkedBy"] is None and p["name"] is None
    assert p["coverage"] == {"reported": 3, "captured": 3, "notCaptured": 0, "windows": []}
    assert len(p["activity"]) == 14 and p["activity"][-1] == {"day": "2026-11-04", "count": 3}
    assert sum(d["count"] for d in p["activity"]) == 3 and p["activity"][0]["day"] == "2026-10-22"
    assert p["agentInfo"] == [
        {"agent": "claude-code", "version": "2.1.308", "firstSeenAt": "2026-11-04T09:00:00Z", "devices": 1}
    ]


def test_people_many_sorted_and_searched(dataset, client_for, write_sample):
    person(dataset, S1, calls=3)
    person(dataset, S2, OTHER, hour=18, calls=1, device="e" * 64)
    person(dataset, S3, THIRD, day="2026-11-01", calls=2, device="f" * 64, mode="default")
    client = client_for(dataset)
    r = client.get("/api/v1/people")
    write_sample(client, r, "many")
    assert [p["personId"] for p in r.json()["data"]] == [OTHER, ACCOUNT, THIRD]  # newest first
    by_activity = client.get("/api/v1/people", params={"sort": "activity"}).json()["data"]
    assert [p["personId"] for p in by_activity] == [ACCOUNT, THIRD, OTHER]
    assert client.get("/api/v1/people", params={"q": "ACC3"}).json()["data"][0]["personId"] == THIRD
    by_device = client.get("/api/v1/people", params={"q": "eeee"}).json()["data"]
    assert [p["personId"] for p in by_device] == [OTHER]
    page2 = client.get("/api/v1/people", params={"pageSize": 2, "page": 2}).json()
    assert [p["personId"] for p in page2["data"]] == [THIRD] and page2["meta"]["total"] == 3
    assert client.get("/api/v1/people", params={"sort": "name"}).json()["meta"]["total"] == 3


def test_people_filtered_agent(dataset, client_for, write_sample):
    """Codex isn't parsed yet, so filtering to it finds nobody, even with people present."""
    person(dataset, S1)
    client = client_for(dataset)
    r = client.get("/api/v1/people", params={"agent": "codex"})
    write_sample(client, r, "filtered_agent")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0
    both = client.get("/api/v1/people", params={"agent": ["codex", "claude-code"]}).json()["data"]
    assert [p["personId"] for p in both] == [ACCOUNT]


def test_people_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    write_sample(client, client.get("/api/v1/people"), "error", status=500)


# --- demo --------------------------------------------------------------------------------------


def test_overview_demo(dataset, client_for, write_sample):
    load_fixture(dataset, "demo")
    client = client_for(dataset)
    r = client.get("/api/v1/overview")
    write_sample(client, r, "demo")
    o = r.json()["data"]
    assert (o["people"], o["activeNow"], o["sessions"], o["notCaptured"]) == (1, 1, 1, 95)


def test_people_demo(dataset, client_for, write_sample):
    load_fixture(dataset, "demo")
    client = client_for(dataset)
    r = client.get("/api/v1/people")
    write_sample(client, r, "demo")
    (p,) = r.json()["data"]
    assert p["personId"] == DEMO_ACCOUNT and p["live"] is True and p["bypassPct"] == 99.7
    assert p["coverage"]["notCaptured"] == 95 and p["agentInfo"][0]["agent"] == "claude-code"


def test_people_in_a_range(dataset, client_for):
    """A window keeps the people with a session overlapping it; its counts come from those sessions."""
    person(dataset, S1, day="2026-11-02")
    person(dataset, S2, OTHER, day="2026-11-04")
    client = client_for(dataset)
    window = {"from": "2026-11-03T00:00:00Z", "to": "2026-11-04T23:59:59Z"}
    rows = client.get("/api/v1/people", params=window).json()
    assert [r["personId"] for r in rows["data"]] == [OTHER] and rows["meta"]["total"] == 1
    assert rows["meta"]["from"] == "2026-11-03T00:00:00Z"
    o = client.get("/api/v1/overview", params=window).json()["data"]
    assert (o["people"], o["sessions"]) == (1, 1)
    assert len(client.get("/api/v1/people").json()["data"]) == 2  # no range: all time


# --- /people/unattributed (§11G.C3) -----------------------------------------------------------


def test_unattributed_traffic_is_a_person_like_page(dataset, client_for, write_sample):
    """The "view traffic" link opens /people/unattributed: the sessions no account appears on, as a person."""
    person(dataset, S1)
    unattributed(dataset)
    client = client_for(dataset)
    r = client.get("/api/v1/people/unattributed")
    write_sample(client, r, "unattributed")
    h = r.json()["data"]
    assert (h["personId"], h["accountIdShort"], h["name"]) == ("unattributed", "—", None)
    sessions = client.get("/api/v1/people/unattributed/sessions").json()["data"]
    assert [s["sessionId"] for s in sessions] == [S3]
    assert client.get("/api/v1/people/unattributed/timeline").status_code == 200
    reqs = client.get("/api/v1/people/unattributed/requests").json()["data"]
    assert reqs and all(r["sessionId"] in (S3, None) for r in reqs)


# --- names (Claude Code's account endpoints) ----------------------------------------------------


def test_people_are_named_from_their_account_profile(dataset, client_for):
    """/api/oauth/profile names the signed-in account: the People row and the person
    header show it, and search finds the person by it."""
    person(dataset, S1)
    cid = dataset.transaction("2026-11-04T09:00:30", method="GET", path="/api/oauth/profile")
    complete = next(r for r in dataset.rows if r["correlation_id"] == cid and r["phase"] == "complete")
    complete["auth"] = {"account_id": ACCOUNT, "email": "ada@example.com", "name": "Ada Lovelace", "org": "Example Co"}
    client = client_for(dataset)
    row = client.get("/api/v1/people").json()["data"][0]
    assert (row["name"], row["email"]) == ("Ada Lovelace", "ada@example.com")
    header = client.get(f"/api/v1/people/{ACCOUNT}").json()["data"]
    assert (header["name"], header["email"]) == ("Ada Lovelace", "ada@example.com")
    assert [r["personId"] for r in client.get("/api/v1/people?q=ada@example").json()["data"]] == [ACCOUNT]


def test_overview_counts_only_the_window(dataset, client_for):
    """Every number on the overview is over the same window: the agent card's sessions
    and the request total agree with the people in range."""
    person(dataset, S1, day="2026-11-02")
    person(dataset, S2, day="2026-11-04")
    o = client_for(dataset).get("/api/v1/overview?from=2026-11-04T00:00:00Z&to=2026-11-04T18:00:00Z").json()["data"]
    assert (o["people"], o["sessions"], o["agents"][0]["sessions"]) == (1, 1, 1)
    assert o["requests"] == 2 + 3


def test_a_range_without_an_offset_is_rejected(dataset, client_for):
    """A naive time can't be compared with the data's: a 422 that says so, not a 500."""
    person(dataset, S1)
    client = client_for(dataset)
    for path in (f"/api/v1/people/{ACCOUNT}/requests", "/api/v1/overview", f"/api/v1/sessions/{S1}/requests"):
        r = client.get(f"{path}?from=2026-11-04T08:00:00")
        assert r.status_code == 422, (path, r.status_code)
