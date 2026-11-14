"""/people/{personId}/sessions and /timeline, from Claude Code-shaped traffic."""

from aintercept_api.timeline import BYPASS_CAUSE
from tests.builders import ACCOUNT, ClaudeCode

S1 = "11111111-1111-4111-8111-111111111111"
S2 = "22222222-2222-4222-8222-222222222222"
S3 = "33333333-3333-4333-8333-333333333333"
SESSIONS = f"/api/v1/people/{ACCOUNT}/sessions"
TIMELINE = f"/api/v1/people/{ACCOUNT}/timeline"


def a_session(ds, sid=S1, start_hour=9, calls=3):
    cc = ClaudeCode(ds, sid)
    cc.event(
        f"2026-11-04T{start_hour:02d}:00:00", "tengu_input_prompt", prompt_index=1, is_negative=False, cc_prompt_id="p1"
    )
    for i in range(calls):
        rid = cc.call(f"2026-11-04T{start_hour:02d}:{i + 1:02d}:00", prompt="p1")
        cc.reported(f"2026-11-04T{start_hour:02d}:{i + 1:02d}:05", rid)
    return cc


# --- sessions ---------------------------------------------------------------------------------


def test_sessions_empty(dataset, client_for, write_sample):
    client = client_for(dataset)
    r = client.get(SESSIONS)
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0


def test_sessions_one(dataset, client_for, write_sample):
    a_session(dataset)
    client = client_for(dataset)
    r = client.get(SESSIONS)
    write_sample(client, r, "one")
    (s,) = r.json()["data"]
    assert s["sessionId"] == S1 and s["modelCalls"] == 3 and s["prompts"] == 1 and s["events"] == 7
    assert s["coverage"] == {"reported": 3, "captured": 3, "notCaptured": 0, "windows": []}
    assert s["bypassPct"] == 100.0 and s["live"] is False and s["end"] == "2026-11-04T09:03:05Z"
    assert s["capturedFromPrompt"] is None and s["fieldsMissing"] is False


def test_sessions_many_newest_first_and_paged(dataset, client_for, write_sample):
    for sid, hour in ((S1, 9), (S2, 11), (S3, 13)):
        a_session(dataset, sid, hour, calls=1)
    client = client_for(dataset)
    r = client.get(SESSIONS)
    write_sample(client, r, "many")
    assert [s["sessionId"] for s in r.json()["data"]] == [S3, S2, S1]
    page2 = client.get(SESSIONS, params={"pageSize": 2, "page": 2}).json()
    assert [s["sessionId"] for s in page2["data"]] == [S1] and page2["meta"]["total"] == 3
    ranged = client.get(SESSIONS, params={"from": "2026-11-04T10:30:00Z", "to": "2026-11-04T12:00:00Z"}).json()
    assert [s["sessionId"] for s in ranged["data"]] == [S2]


def test_sessions_pre_parser(dataset, client_for, write_sample):
    """Model calls without identity; the session is attributed through its analytics."""
    cc = ClaudeCode(dataset, S1)
    cc.call("2026-11-04T09:01:00", prompt=None, parsed=False)
    cc.event("2026-11-04T09:05:00", "tengu_turn_end")
    client = client_for(dataset)
    r = client.get(SESSIONS)
    write_sample(client, r, "pre_parser")
    (s,) = r.json()["data"]
    assert s["fieldsMissing"] is True and s["prompts"] is None and s["modelCalls"] == 1


def test_sessions_coverage_gap(dataset, client_for, write_sample):
    """Claude Code reports calls the proxy never saw (the IPv6 bypass): one window."""
    cc = a_session(dataset, calls=2)
    for minute in (10, 12, 14):
        cc.reported(f"2026-11-04T09:{minute}:00", f"req_bypassed_{minute}")
    client = client_for(dataset)
    r = client.get(SESSIONS)
    write_sample(client, r, "coverage_gap")
    cov = r.json()["data"][0]["coverage"]
    assert (cov["reported"], cov["captured"], cov["notCaptured"]) == (5, 2, 3)
    assert cov["windows"] == [{"from": "2026-11-04T09:10:00Z", "to": "2026-11-04T09:14:00Z", "count": 3, "cause": None}]


def test_sessions_live_with_friction(dataset, client_for, write_sample):
    """Activity within 5 minutes of now (18:05): live, no end. Friction and a mid-session capture start."""
    cc = ClaudeCode(dataset, S1)
    cc.event("2026-11-04T17:40:00", "tengu_input_prompt", prompt_index=12, is_negative=True)
    cc.call("2026-11-04T17:41:00")
    cc.event("2026-11-04T17:50:00", "tengu_cancel", source="escape")
    cc.event("2026-11-04T17:55:00", "tengu_bash_tool_command_executed", destructive_category="rm_recursive_force")
    cc.event("2026-11-04T17:56:00", "tengu_bash_tool_command_executed", destructive_category="none")
    cc.call("2026-11-04T18:03:00")
    client = client_for(dataset)
    r = client.get(SESSIONS)
    write_sample(client, r, "live")
    (s,) = r.json()["data"]
    assert s["live"] is True and s["end"] is None
    assert s["friction"] == {"frustrated": 1, "interrupted": 1, "destructive": 1}
    assert s["capturedFromPrompt"] == 12


def test_sessions_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    r = client.get(SESSIONS)
    write_sample(client, r, "error", status=500)


def test_sessions_of_another_person_are_not_included(dataset, client_for):
    a_session(dataset, S1)
    ClaudeCode(dataset, S2, account="00000000-0000-4000-8000-0000000000b2").call("2026-11-04T10:00:00")
    assert [s["sessionId"] for s in client_for(dataset).get(SESSIONS).json()["data"]] == [S1]


# --- timeline ---------------------------------------------------------------------------------


def test_timeline_empty(dataset, client_for, write_sample):
    client = client_for(dataset)
    r = client.get(TIMELINE)
    write_sample(client, r, "empty")
    d = r.json()["data"]
    # The default range is the last 14 days: hourly bins. Snapped, it's 14 days and a minute less
    # 1 ns, but the auto bin comes from the span asked for (docs/PIPELINE.md v8 §5.3).
    assert d["sessions"] == [] and d["modelCalls"] == [] and d["binSeconds"] == 3600


def test_timeline_one_session(dataset, client_for, write_sample):
    a_session(dataset)
    client = client_for(dataset)
    r = client.get(TIMELINE, params={"from": "2026-11-04T08:00:00Z", "to": "2026-11-04T12:00:00Z"})
    write_sample(client, r, "one_session")
    d = r.json()["data"]
    assert d["binSeconds"] == 300 and d["now"] is None
    assert [b["count"] for b in d["modelCalls"]] == [3] and d["modelCalls"][0]["tokens"] == 300
    assert d["sessions"][0]["row"] == 0 and len(d["prompts"]) == 1
    assert d["presence"] == [
        {
            "from": "2026-11-04T09:00:00Z",
            "to": "2026-11-04T09:03:05Z",
            "state": "focused",
            "label": None,
            "source": "focus",
        }
    ]
    # Tooltip fields (handoff §6B.10): everything the hover shows ships with the payload.
    band = d["sessions"][0]
    assert (band["prompts"], band["modelCalls"], band["repo"], band["branch"]) == (1, 3, None, None)
    (p,) = d["prompts"]
    assert (p["promptId"], p["index"], p["steps"], p["tokensIn"], p["tokensOut"]) == ("p1", 1, 3, 9, 300)
    assert p["durationMs"] is None and p["text"]["retained"] is False  # no tengu_turn_end; content is off
    (b,) = d["modelCalls"]
    assert b["byClass"] == {"main": 3, "subagent": 0, "auxiliary": 0}
    assert (b["tokensIn"], b["tokensOut"], b["notCaptured"]) == (9, 300, 0)
    (a,) = d["analytics"]
    assert a["count"] == 7 and a["top"] == [
        {"name": "tengu_api_query", "count": 3},
        {"name": "tengu_api_success", "count": 3},
        {"name": "tengu_input_prompt", "count": 1},
    ]


def test_timeline_many_overlapping_sessions_pack_into_rows(dataset, client_for, write_sample):
    a_session(dataset, S1, 9, calls=3)
    b = ClaudeCode(dataset, S2)  # overlaps S1
    b.call("2026-11-04T09:02:00")
    a_session(dataset, S3, 11, calls=1)  # after both: reuses row 0
    b.event("2026-11-04T09:02:30", "tengu_return_to_session", blurDurationMs=60_000)
    # Friction on S2: a negative prompt, an interruption in it, and two destructive commands in one bin.
    b.event("2026-11-04T09:01:30", "tengu_input_prompt", prompt_index=4, is_negative=True, cc_prompt_id="q4")
    b.event("2026-11-04T09:02:10", "tengu_cancel", cc_prompt_id="q4")
    b.event(
        "2026-11-04T09:02:40",
        "tengu_bash_tool_command_executed",
        destructive_category="rm_recursive_force",
        destructive_target_scope="outside_cwd",
    )
    b.event("2026-11-04T09:02:41", "tengu_bash_tool_command_executed", destructive_category="rm_force")
    b.call("2026-11-04T09:02:50", prompt="q4", request_class="subagent")
    b.call("2026-11-04T09:02:55", prompt=None, request_class="auxiliary")
    client = client_for(dataset)
    r = client.get(TIMELINE, params={"from": "2026-11-04T08:00:00Z", "to": "2026-11-04T12:00:00Z"})
    write_sample(client, r, "many")
    d = r.json()["data"]
    rows = {s["sessionId"]: s["row"] for s in d["sessions"]}
    assert rows == {S1: 0, S2: 1, S3: 0}
    marks = {m["kind"]: m for m in d["friction"]}
    assert {k: (m["count"], m["promptIndex"], m["tool"]) for k, m in marks.items()} == {
        "frustrated": (1, 4, None),
        "interrupted": (1, 4, None),
        "destructive": (2, None, "Bash"),  # the commands carry no cc_prompt_id here
    }
    assert marks["destructive"]["detail"] == "rm_recursive_force · outside_cwd, rm_force"
    assert marks["frustrated"]["detail"] == "prompt flagged negative"
    first_bin = d["modelCalls"][0]
    assert first_bin["count"] == 6 and first_bin["byClass"] == {"main": 4, "subagent": 1, "auxiliary": 1}


def test_timeline_coverage_gap_and_presence(dataset, client_for, write_sample):
    cc = a_session(dataset, calls=1)
    cc.reported("2026-11-04T09:20:00", "req_bypassed")
    cc.event("2026-11-04T09:30:00", "tengu_return_to_session", blurDurationMs=300_000)  # away 09:25-09:30
    cc.event(
        "2026-11-04T09:40:00", "tengu_event_loop_stall", likely_sleep=True, clock_jump_ms=120_000
    )  # asleep 09:38-09:40
    cc.call("2026-11-04T09:45:00")
    client = client_for(dataset)
    r = client.get(TIMELINE, params={"from": "2026-11-04T09:00:00Z", "to": "2026-11-04T10:00:00Z"})
    write_sample(client, r, "coverage_gap")
    d = r.json()["data"]
    assert d["notCaptured"] == [
        {
            "from": "2026-11-04T09:20:00Z",
            "to": "2026-11-04T09:20:00Z",
            "count": 1,
            "reported": 1,
            "captured": 0,
            "cause": BYPASS_CAUSE,  # no model call captured in the window while analytics were
        }
    ]
    gap_bin = next(b for b in d["modelCalls"] if b["t"] == "2026-11-04T09:20:00Z")
    assert (gap_bin["count"], gap_bin["notCaptured"], gap_bin["tokensIn"]) == (0, 1, None)
    assert {(p["state"], p["source"]) for p in d["presence"]} == {
        ("focused", "focus"),
        ("away", "focus"),
        ("asleep", "sleep"),
    }
    states = [(p["from"][11:16], p["to"][11:16], p["state"]) for p in d["presence"]]
    assert states == [
        ("09:00", "09:25", "focused"),
        ("09:25", "09:30", "away"),
        ("09:30", "09:38", "focused"),
        ("09:38", "09:40", "asleep"),
        ("09:40", "09:45", "focused"),
    ]


def test_timeline_presence_combines_the_sessions_shown(dataset, client_for):
    one = ClaudeCode(dataset, S1)
    one.call("2026-11-04T09:00:00")
    one.call("2026-11-04T09:10:00")
    two = ClaudeCode(dataset, S2)
    two.call("2026-11-04T11:00:00")
    # The device slept from 09:05 (two hours before the stall was noticed, in the later session).
    two.event("2026-11-04T11:05:00", "tengu_event_loop_stall", likely_sleep=True, clock_jump_ms=7_200_000)
    client = client_for(dataset)

    def states(**window):
        d = client.get(TIMELINE, params=window).json()["data"]
        return [(p["from"][11:16], p["to"][11:16], p["state"]) for p in d["presence"]]

    # Asleep wherever a shown session is open, whichever session reported the sleep.
    assert states() == [("09:00", "09:05", "focused"), ("09:05", "09:10", "asleep"), ("11:00", "11:05", "asleep")]
    # A range that ends before the later session starts doesn't show it, nor its sleep.
    assert states(to="2026-11-04T10:00:00Z") == [("09:00", "09:10", "focused")]
    assert states(**{"from": "2026-11-04T09:07:00Z"}) == [("09:07", "09:10", "asleep"), ("11:00", "11:05", "asleep")]


def test_timeline_presence_of_a_live_session_runs_to_now(dataset, client_for):
    cc = ClaudeCode(dataset, S1)
    cc.call("2026-11-04T18:00:00")
    cc.event("2026-11-04T18:03:00", "tengu_return_to_session", blurDurationMs=60_000)  # away 18:02-18:03
    d = client_for(dataset).get(TIMELINE).json()["data"]
    assert d["now"] == "2026-11-04T18:05:00Z"
    assert [(p["from"][11:16], p["to"][11:16], p["state"]) for p in d["presence"]] == [
        ("18:00", "18:02", "focused"),
        ("18:02", "18:03", "away"),
        ("18:03", "18:05", "focused"),
    ]


def test_timeline_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    r = client.get(TIMELINE)
    write_sample(client, r, "error", status=500)
