"""/sessions/{sessionId}, /timeline, /state, /prompts, /requests and /events."""

import pytest

from aintercept_api.timeline import BYPASS_CAUSE
from tests.builders import ACCOUNT, DEVICE, ClaudeCode, account_call

S1 = "11111111-1111-4111-8111-111111111111"
BASE = f"/api/v1/sessions/{S1}"
ENV = {"platform": "darwin", "terminal": "iTerm.app", "shell": "zsh"}
H = "2026-11-04T09"  # an ended session (the frozen clock is 18:05)
LIVE = "2026-11-04T18"


def prompt(cc, when, pid, index, **md):
    cc.event(when, "tengu_input_prompt", env=ENV, cc_prompt_id=pid, prompt_index=index, prompt_length=42, **md)


def step(cc, when, pid, *, tools=(), ctx=100_000, cost="0.25", **kw):
    """A main-thread call with its tengu_api_query/_success, and the tools it ran."""
    mode = kw.pop("mode", "bypassPermissions")
    rid = cc.call(when, prompt=pid, cache_read=ctx, cache_write=1_000, stop_reason="tool_use", **kw)
    cc.event(when, "tengu_api_query", cc_prompt_id=pid, permissionMode=mode)
    cc.event(
        when,
        "tengu_api_success",
        cc_prompt_id=pid,
        requestId=rid,
        costUSD=cost,
        ttftMs=900,
        cache_creation_1h_input_tokens=1_000,
        permissionMode="bypassPermissions",
    )
    for i, (name, ok) in enumerate(tools):
        tid = f"toolu_{rid[-4:]}_{i}"
        md = {"cc_prompt_id": pid, "requestId": rid, "toolName": name, "toolUseID": tid, "durationMs": 120}
        cc.event(when, "tengu_tool_use_progress", **md)
        cc.event(when, "tengu_tool_use_success" if ok else "tengu_tool_use_error", toolResultSizeBytes=512, **md)
    return rid


def ended_session(ds, sid=S1):
    """Two prompts, tools, a destructive command, a paste, a subagent and an auxiliary call."""
    cc = ClaudeCode(ds, sid)
    cc.event(f"{H}:00:00", "tengu_paste_text", env=ENV, pastedTextBytes=2_048)
    prompt(cc, f"{H}:00:10", "p1", 1)
    rid = step(cc, f"{H}:01:00", "p1", tools=[("Bash", True), ("mcp__github__create_pr", True)], ip="192.0.2.10")
    cc.event(
        f"{H}:01:30",
        "tengu_bash_tool_command_executed",
        cc_prompt_id="p1",
        tool_use_id=f"toolu_{rid[-4:]}_0",
        bash_argv0="rm",
        destructive_category="rm_recursive_force",
        destructive_target_scope="outside_cwd",
        exit_code=0,
    )
    cc.call(f"{H}:02:00", prompt="p1", request_class="subagent", agent_type="Explore", ip="192.0.2.10")
    cc.event(f"{H}:03:00", "tengu_turn_end", cc_prompt_id="p1")
    cc.call(f"{H}:04:00", prompt=None, request_class="auxiliary", stop_reason="end_turn", ip="192.0.2.10")
    prompt(cc, f"{H}:10:00", "p2", 2, is_negative=True)
    step(cc, f"{H}:10:30", "p2", tools=[("Read", False)], ip="192.0.2.10")
    cc.event(f"{H}:11:00", "tengu_cancel", cc_prompt_id="p2")
    cc.event(f"{H}:11:05", "tengu_turn_end", cc_prompt_id="p2")
    return cc


def live_session(ds):
    """Live at the frozen 18:05: the last call is still streaming and a tool is running."""
    cc = ClaudeCode(ds, S1)
    prompt(cc, f"{LIVE}:00:00", "p1", 1)
    step(cc, f"{LIVE}:00:30", "p1", tools=[("Bash", True)])
    cc.event(f"{LIVE}:01:00", "tengu_turn_end", cc_prompt_id="p1")
    prompt(cc, f"{LIVE}:03:00", "p2", 2)
    rid = cc.call(f"{LIVE}:03:10", prompt="p2", cache_read=120_000, complete=False)
    cc.event(f"{LIVE}:03:00", "tengu_tool_use_progress", cc_prompt_id="p2", requestId=rid, toolName="Bash")
    return cc


def error_on_query(client, monkeypatch):
    """Every read the store can do fails: its tables and a call's raw rows."""
    for name in ("query", "query_dicts", "raw_rows", "raw_event"):
        monkeypatch.setattr(client.app.state.store, name, lambda *a, **k: 1 / 0)


# --- /sessions/{id} -----------------------------------------------------------------------------


def test_session_ended(dataset, client_for, write_sample):
    ended_session(dataset)
    client = client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "ended")
    h = r.json()["data"]
    assert h["live"] is False and h["end"] == "2026-11-04T09:11:05Z" and h["personId"] == ACCOUNT
    assert (h["modelCalls"], h["prompts"], h["notCaptured"], h["networks"]) == (4, 2, 0, 1)
    assert h["device"] == {"deviceId": DEVICE, "platform": "darwin", "terminal": "iTerm.app", "shell": "zsh"}
    assert h["friction"] == {"frustrated": 1, "interrupted": 1, "destructive": 1} and h["bypassPct"] == 100.0
    assert h["repo"] is None and h["branch"] is None  # M7 extraction: unknown, never ""


def test_session_live(dataset, client_for, write_sample):
    live_session(dataset)
    client = client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "live")
    h = r.json()["data"]
    assert h["live"] is True and h["end"] is None and h["networks"] is None


def test_session_pre_parser(dataset, client_for, write_sample):
    """Calls from before the identity parser, with no analytics: almost everything is unknown."""
    cc = ClaudeCode(dataset, S1)
    cc.call(f"{H}:01:00", prompt=None, parsed=False)
    cc.call(f"{H}:02:00", prompt=None, parsed=False)
    client = client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "pre_parser")
    h = r.json()["data"]
    assert h["fieldsMissing"] is True and h["personId"] is None and h["device"] is None
    assert h["events"] is None and h["notCaptured"] is None and h["coverage"]["reported"] is None


def test_session_unknown_is_a_typed_404(dataset, client_for):
    r = client_for(dataset).get("/api/v1/sessions/nope")
    assert r.status_code == 404 and r.json()["error"]["code"] == "http_404"


def test_session_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(BASE), "error", status=500)


# --- /timeline ----------------------------------------------------------------------------------


def test_timeline_one_prompt(dataset, client_for, write_sample):
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:00", "p1", 1)
    for minute, ctx in ((1, 50_000), (2, 60_000), (3, 70_000)):
        step(cc, f"{H}:0{minute}:00", "p1", ctx=ctx)
    cc.event(f"{H}:04:00", "tengu_turn_end", cc_prompt_id="p1")
    client = client_for(dataset)
    r = client.get(f"{BASE}/timeline")
    write_sample(client, r, "one_prompt")
    d = r.json()["data"]
    assert d["binSeconds"] == 300 and d["now"] is None and len(d["sessions"]) == 1
    assert [p["index"] for p in d["prompts"]] == [1]
    assert [c["tokens"] for c in d["context"]] == [51_003, 61_003, 71_003]
    # Tooltip fields (handoff §6B.10).
    assert (d["sessions"][0]["prompts"], d["sessions"][0]["modelCalls"]) == (1, 3)
    (p,) = d["prompts"]
    assert (p["promptId"], p["steps"], p["durationMs"], p["text"]["chars"]) == ("p1", 3, 240_000.0, 42)
    assert (p["tokensIn"], p["tokensOut"]) == (51_003 + 61_003 + 71_003, 300)
    c = d["context"][0]
    assert (c["model"], c["thread"], c["promptIndex"], c["windowTokens"]) == ("claude-opus-5-5", "main", 1, None)
    assert c["requestId"].startswith("observer-test-0-")
    (b,) = d["modelCalls"]
    assert b["byClass"] == {"main": 3, "subagent": 0, "auxiliary": 0} and b["tokensIn"] == p["tokensIn"]
    assert d["analytics"][0]["top"][0] == {"name": "tengu_api_query", "count": 3}
    assert d["permissionChanges"] == []
    assert d["permission"] == [
        {"from": "2026-11-04T09:01:00Z", "to": "2026-11-04T09:04:00Z", "mode": "bypassPermissions"}
    ]
    assert d["compactions"] == [] and d["network"] == [] and d["notCaptured"] == []


def test_timeline_many(dataset, client_for, write_sample):
    """Several prompts, a permission toggle, a network change to IPv6, focus and sleep."""
    cc = ClaudeCode(dataset, S1)
    v4, v6 = "192.0.2.10", "2001:db8::7"
    plan = ((0, "bypassPermissions", v4), (20, "default", v4), (40, "bypassPermissions", v6))
    for i, (minute, mode, ip) in enumerate(plan):
        prompt(cc, f"{H}:{minute:02d}:00", f"p{i}", i + 1, is_negative=i == 1)
        step(cc, f"{H}:{minute + 1:02d}:00", f"p{i}", ip=ip, mode=mode)
    cc.event(f"{H}:22:00", "tengu_cancel", cc_prompt_id="p1")
    cc.event(
        f"{H}:42:00",
        "tengu_bash_tool_command_executed",
        cc_prompt_id="p2",
        destructive_category="rm_recursive_force",
        destructive_target_scope="outside_cwd",
    )
    cc.event(f"{H}:30:00", "tengu_return_to_session", blurDurationMs=300_000)
    cc.event(f"{H}:50:00", "tengu_event_loop_stall", likely_sleep="true", clock_jump_ms=240_000)
    client = client_for(dataset)
    r = client.get(f"{BASE}/timeline", params={"bin": 600})
    write_sample(client, r, "many")
    d = r.json()["data"]
    assert d["binSeconds"] == 600 and len(d["prompts"]) == 3
    assert [p["mode"] for p in d["permission"]] == ["bypassPermissions", "default", "bypassPermissions"]
    assert [(n["label"], n["ipv6"], n["bypassed"]) for n in d["network"]] == [
        ("192.0.2.10", False, False),
        ("2001:db8::7", True, False),
    ]
    assert {p["state"] for p in d["presence"]} == {"focused", "away", "asleep"}
    assert {p["source"] for p in d["presence"] if p["state"] == "asleep"} == {"sleep"}
    assert [(c["t"][11:16], c["from"], c["to"]) for c in d["permissionChanges"]] == [
        ("09:21", "bypassPermissions", "default"),
        ("09:41", "default", "bypassPermissions"),
    ]
    assert [(f["kind"], f["promptIndex"], f["detail"], f["tool"]) for f in d["friction"]] == [
        ("frustrated", 2, "prompt flagged negative", None),
        ("interrupted", 2, "turn interrupted", None),
        ("destructive", 3, "rm_recursive_force · outside_cwd", "Bash"),
    ]
    assert [n["address"] for n in d["network"]] == [None, None]  # until the coverage job (§5.4)


def test_timeline_compaction(dataset, client_for, write_sample):
    """Idle compaction: fired → attempt → compaction call → succeeded; context 900k → 30k."""
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:00", "p1", 1)
    step(cc, f"{H}:01:00", "p1", ctx=900_000)
    cc.event(f"{H}:30:00", "tengu_idle_compact_fired", cc_prompt_id="p1")
    cc.event(f"{H}:30:00", "tengu_reactive_compact_attempt", cc_prompt_id="p1")
    cc.call(f"{H}:30:01", prompt=None, request_class="compaction")
    cc.event(f"{H}:31:11", "tengu_reactive_compact_succeeded", cc_prompt_id="p1")
    prompt(cc, f"{H}:40:00", "p2", 2)
    step(cc, f"{H}:40:10", "p2", ctx=30_000)
    cc.event(f"{H}:50:00", "tengu_reactive_compact_succeeded", cc_prompt_id="p2")  # no attempt seen: no wait
    client = client_for(dataset)
    r = client.get(f"{BASE}/timeline")
    write_sample(client, r, "compaction")
    first, second = r.json()["data"]["compactions"]
    assert first == {
        "t": "2026-11-04T09:31:11Z",
        "from": 901_003,
        "to": 31_003,
        "idle": True,
        "waitMs": 71_000.0,
        "messages": None,
    }
    assert second["idle"] is False and second["waitMs"] is None and second["to"] is None
    assert first["messages"] is None  # until the compaction events' metadata is extracted (M7)


def test_timeline_coverage_gap(dataset, client_for, write_sample):
    """Calls that bypassed the proxy: a hatched window, and the network segment it overlaps is marked."""
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:00", "p1", 1)
    step(cc, f"{H}:01:00", "p1", ip="192.0.2.10")
    for minute in (10, 12, 14):
        cc.reported(f"{H}:{minute}:00", f"req_bypassed_{minute}")
    step(cc, f"{H}:20:00", "p1", ip="192.0.2.10")
    client = client_for(dataset)
    r = client.get(f"{BASE}/timeline")
    write_sample(client, r, "coverage_gap")
    d = r.json()["data"]
    assert [w["count"] for w in d["notCaptured"]] == [3]
    assert [n["bypassed"] for n in d["network"]] == [True]
    (gap,) = d["notCaptured"]
    # Nothing captured in the window while analytics were: a route around the proxy.
    assert (gap["reported"], gap["captured"], gap["cause"]) == (3, 0, BYPASS_CAUSE)
    assert [n["notCaptured"] for n in d["network"]] == [3]
    gap_bin = next(b for b in d["modelCalls"] if b["t"] == "2026-11-04T09:10:00Z")
    assert (gap_bin["count"], gap_bin["notCaptured"]) == (0, 3)


def test_timeline_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/timeline"), "error", status=500)


# --- /state -------------------------------------------------------------------------------------


def state_session(ds):
    cc = ended_session(ds)
    cc.event(f"{H}:00:05", "tengu_run_hook", hookName="PreToolUse:Bash", head="a" * 40)
    cc.event(f"{H}:05:00", "tengu_deferred_tools_pool_change", addedCount=3)
    cc.event(f"{H}:06:00", "tengu_git_operation", operation="commit", head="b" * 40)
    cc.event(f"{H}:06:30", "tengu_git_operation", operation="push", head="b" * 40)
    cc.event(f"{H}:07:00", "tengu_session_linked_to_pr", prNumber=2)
    cc.event(f"{H}:08:00", "tengu_return_to_session", blurDurationMs=60_000, hadRecap=True)
    return cc


def test_state_default(dataset, client_for, write_sample):
    state_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/state")
    write_sample(client, r, "default")
    d = r.json()["data"]
    cap = d["capabilitiesAt"]
    assert cap["builtin"] == {"total": None, "used": 1} and cap["mcpTools"] == {"total": None, "used": 1}
    assert [m["name"] for m in cap["mcpServers"]] == ["github"] and cap["hooks"] == 1
    assert cap["poolChangesInRange"] == 1 and cap["skills"] is None
    repo = d["repoInRange"]
    assert (repo["headFrom"], repo["headTo"], repo["headChanges"]) == ("a" * 40, "b" * 40, 1)
    assert (repo["commits"], repo["pushed"], repo["prs"], repo["cwdTrail"]) == (1, True, [2], None)
    assert d["recapsTotal"] == 1 and d["recapsInRange"] == [{"t": "2026-11-04T09:08:00Z", "text": None}]
    assert d["experimentsAt"] is None and d["flagFetchesInRange"] is None
    # Each count matches its tab over the same range.
    # 4 model calls + 15 analytics batches (one per event timestamp in the builder), 23 events.
    assert d["counts"] == {"prompts": 2, "modelCalls": 4, "requests": 4 + 15, "events": 23, "compactions": 0}
    tabs = {t: client.get(f"{BASE}/{t}").json()["meta"]["total"] for t in ("requests", "events")}
    assert tabs == {"requests": d["counts"]["requests"], "events": d["counts"]["events"]}


def test_state_follows_the_range(dataset, client_for):
    state_session(dataset)
    client = client_for(dataset)
    d = client.get(f"{BASE}/state", params={"from": "2026-11-04T09:09:00Z"}).json()["data"]
    assert d["counts"]["prompts"] == 1 and d["repoInRange"]["commits"] == 0 and d["recapsInRange"] == []
    assert d["capabilitiesAt"]["builtin"]["used"] == 1  # capabilities are as of `to`, from the session start


def test_state_empty_range(dataset, client_for, write_sample):
    state_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/state", params={"from": "2026-11-04T12:00:00Z", "to": "2026-11-04T13:00:00Z"})
    write_sample(client, r, "empty_range")
    d = r.json()["data"]
    assert set(d["counts"].values()) == {0} and d["repoInRange"]["headFrom"] is None


def test_state_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/state"), "error", status=500)


# --- /prompts -----------------------------------------------------------------------------------


def test_prompts_empty(dataset, client_for, write_sample):
    ended_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/prompts", params={"from": "2026-11-04T12:00:00Z"})
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0


def test_prompts_one(dataset, client_for, write_sample):
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:10", "p1", 1)
    rid = step(cc, f"{H}:01:00", "p1", tools=[("Bash", True)])
    cc.event(
        f"{H}:01:30",
        "tengu_bash_tool_command_executed",
        cc_prompt_id="p1",
        tool_use_id=f"toolu_{rid[-4:]}_0",
        destructive_category="rm_force",
        exit_code=1,
    )
    cc.event(f"{H}:02:00", "tengu_turn_end", cc_prompt_id="p1")
    client = client_for(dataset)
    r = client.get(f"{BASE}/prompts")
    write_sample(client, r, "one")
    ((item,),) = [r.json()["data"]]
    p, (s,) = item["prompt"], item["steps"]
    assert (p["index"], p["steps"], p["durationMs"], p["inFlight"]) == (1, 1, 110_000.0, False)
    assert p["text"] == {"retained": False, "text": None, "chars": 42, "pastedBytes": None, "images": 0}
    assert p["tokensIn"] == 101_003 and p["toolCounts"] == {"Bash": 1} and p["friction"]["destructive"] == 1
    assert (s["status"], s["costUsd"], s["ttftMs"], s["eventsPending"]) == ("done", 0.25, 900.0, False)
    assert s["tokens"]["cacheWrite1h"] == 1_000 and s["stopReason"] == "tool_use"
    (tool,) = s["tools"]
    assert tool["toolUseId"] == f"toolu_{rid[-4:]}_0" and tool["status"] == "ok" and tool["exitCode"] == 1
    assert tool["destructive"] == {"category": "rm_force", "scope": None} and tool["target"] is None


def test_prompts_many(dataset, client_for, write_sample):
    ended_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/prompts")
    write_sample(client, r, "many")
    data = r.json()["data"]
    assert [i["type"] for i in data] == ["prompt", "between", "prompt"]
    first, between, second = data
    assert first["prompt"]["text"]["pastedBytes"] == 2_048 and first["prompt"]["steps"] == 2
    assert [s["kind"] for s in first["steps"]] == ["model", "subagent"] and first["steps"][1]["agentType"] == "Explore"
    tools = first["steps"][0]["tools"]
    assert [t["mcpServer"] for t in tools] == [None, "github"] and tools[0]["destructive"]["scope"] == "outside_cwd"
    assert between["between"]["kind"] == "call" and between["between"]["detail"] == "auxiliary · opus · end_turn"
    assert second["prompt"]["friction"] == {"frustrated": True, "interrupted": True, "destructive": 0}
    assert second["steps"][0]["tools"][0]["status"] == "error"
    page = client.get(f"{BASE}/prompts", params={"pageSize": 2, "page": 2}).json()
    assert [i["type"] for i in page["data"]] == ["prompt"] and page["meta"]["total"] == 3
    # A jump from the timeline asks for prompt #2: the page that holds it, whatever `page` says.
    jump = client.get(f"{BASE}/prompts", params={"pageSize": 2, "page": 1, "prompt": 2}).json()
    assert jump["meta"]["page"] == 2 and jump["data"][0]["prompt"]["index"] == 2
    assert client.get(f"{BASE}/prompts", params={"pageSize": 2, "prompt": 9}).json()["meta"]["page"] == 1


def test_prompts_in_flight(dataset, client_for, write_sample):
    live_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/prompts")
    write_sample(client, r, "in_flight")
    done, running = (i["prompt"] for i in r.json()["data"])
    assert done["inFlight"] is False and running["inFlight"] is True and running["durationMs"] is None
    (s,) = r.json()["data"][1]["steps"]
    assert s["status"] == "streaming" and s["durationMs"] is None and s["eventsPending"] is True
    assert [(t["name"], t["status"]) for t in s["tools"]] == [("Bash", "running")]


def test_prompts_between_rows(dataset, client_for):
    """Compactions and recaps sit between prompts; a compaction keeps its call (drawer) and sizes."""
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:00", "p1", 1)
    step(cc, f"{H}:01:00", "p1", ctx=800_000)
    cc.event(f"{H}:20:00", "tengu_reactive_compact_attempt", cc_prompt_id="p1")
    cc.call(f"{H}:20:01", prompt=None, request_class="compaction")
    cc.event(f"{H}:21:00", "tengu_reactive_compact_succeeded", cc_prompt_id="p1")
    cc.event(f"{H}:25:00", "tengu_return_to_session", blurDurationMs=60_000, hadRecap=True)
    step(cc, f"{H}:30:00", "p1", ctx=20_000)
    data = client_for(dataset).get(f"{BASE}/prompts").json()["data"]
    kinds = [i["between"]["kind"] for i in data if i["type"] == "between"]
    assert kinds == ["compaction", "recap"]
    comp = data[1]["between"]
    assert (comp["tokensFrom"], comp["tokensTo"], comp["waitMs"]) == (801_003, 21_003, 60_000.0)
    assert comp["requestId"] and comp["detail"] == "compaction · context 801k → 21k tokens"


def test_prompts_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/prompts"), "error", status=500)


# --- /requests ----------------------------------------------------------------------------------


def test_requests_empty(dataset, client_for, write_sample):
    ended_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"from": "2026-11-04T12:00:00Z"})
    write_sample(client, r, "empty")
    assert r.json()["data"] == []


def test_requests_one(dataset, client_for, write_sample):
    ClaudeCode(dataset, S1).call(f"{H}:01:00")
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests")
    write_sample(client, r, "one")
    (row,) = r.json()["data"]
    assert row["kind"] == "model" and row["sessionId"] == S1 and row["captured"] is True


def test_requests_many_oldest_first_and_session_only(dataset, client_for, write_sample):
    cc = ended_session(dataset)
    account_call(cc, f"{H}:00:30")  # the person's, but no session: not on the session page
    ClaudeCode(dataset, "22222222-2222-4222-8222-222222222222").call(f"{H}:05:00")
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"pageSize": 50})
    write_sample(client, r, "many")
    rows = r.json()["data"]
    assert [x["t"] for x in rows] == sorted(x["t"] for x in rows)
    assert {x["sessionId"] for x in rows} == {S1} and {x["kind"] for x in rows} == {"model", "analytics"}
    model = [x for x in rows if x["kind"] == "model"]
    assert [x["promptIndex"] for x in model] == [1, 1, None, 2]
    assert [x["ttftMs"] for x in model] == [900.0, None, None, 900.0]  # from each step's tengu_api_success
    assert [x["stopReason"] for x in model] == ["tool_use", None, "end_turn", "tool_use"]
    assert model[0]["tokens"]["cacheRead"] == 100_000 and model[0]["tokens"]["cacheWrite1h"] == 1_000
    assert all(x["tokens"] is None for x in rows if x["kind"] == "analytics")
    newest = client.get(f"{BASE}/requests", params={"order": "desc", "kind": "model"}).json()["data"]
    assert newest[0]["t"] == "2026-11-04T09:10:30Z"


def test_requests_not_captured(dataset, client_for, write_sample):
    cc = ended_session(dataset)
    cc.reported(f"{H}:20:00", "req_bypassed_1")
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"captured": "false"})
    write_sample(client, r, "not_captured")
    (row,) = r.json()["data"]
    assert row["requestId"] == "reported:req_bypassed_1" and row["method"] is None and row["status"] is None


def test_requests_unknown_session(dataset, client_for):
    assert client_for(dataset).get("/api/v1/sessions/nope/requests").status_code == 404


def test_requests_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/requests"), "error", status=500)


# --- /events ------------------------------------------------------------------------------------


def test_events_empty(dataset, client_for, write_sample):
    """A pre-parser session: model calls, but no analytics at all."""
    ClaudeCode(dataset, S1).call(f"{H}:01:00", prompt=None, parsed=False)
    client = client_for(dataset)
    r = client.get(f"{BASE}/events")
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0 and r.json()["meta"]["shown"] == 0


def test_events_one(dataset, client_for, write_sample):
    ClaudeCode(dataset, S1).event(f"{H}:00:00", "tengu_feature_ok", env=ENV, feature="statusline", enabled=True)
    client = client_for(dataset)
    r = client.get(f"{BASE}/events")
    write_sample(client, r, "one")
    (e,) = r.json()["data"]
    assert e["name"] == "tengu_feature_ok" and e["summary"] == "feature statusline · enabled true"
    assert e["linkedTo"] == {"kind": "session", "id": S1, "label": "session"} and e["internal"] is False


def test_events_many(dataset, client_for, write_sample):
    ended_session(dataset)
    cc = ClaudeCode(dataset, S1)
    cc.event(f"{H}:12:00", "tengu_event_loop_stall", likely_sleep="true", clock_jump_ms=60_000)
    cc.reported(f"{H}:13:00", "req_bypassed_1")
    client = client_for(dataset)
    r = client.get(f"{BASE}/events", params={"pageSize": 50})
    write_sample(client, r, "many")
    rows = r.json()["data"]
    meta = r.json()["meta"]
    assert meta["total"] == meta["shown"] == len(rows) == 20
    assert [x["t"] for x in rows] == sorted(x["t"] for x in rows)
    by_name: dict = {}
    for x in rows:
        by_name.setdefault(x["name"], x)  # the first of each
    assert by_name["tengu_input_prompt"]["linkedTo"] == {"kind": "prompt", "id": "p1", "label": "#1"}
    assert by_name["tengu_input_prompt"]["promptIndex"] == 1
    assert by_name["tengu_bash_tool_command_executed"]["marker"] == "destructive"
    assert by_name["tengu_bash_tool_command_executed"]["linkedTo"]["kind"] == "tool"
    assert by_name["tengu_api_query"]["linkedTo"]["kind"] == "prompt"
    assert by_name["tengu_api_success"]["linkedTo"]["label"] == "main · opus · tool_use"
    assert by_name["tengu_event_loop_stall"]["linkedTo"] == {"kind": "device", "id": DEVICE, "label": "device"}
    assert any(x["marker"] == "frustrated" for x in rows)
    missed = [x for x in rows if x["linkedTo"]["id"] == "reported:req_bypassed_1"]
    assert [x["linkedTo"]["label"] for x in missed] == ["not captured"]
    assert len({x["eventId"] for x in rows}) == len(rows)
    ticks = [x for x in rows if x["name"] == "tengu_tool_use_progress"]
    assert [x["collapsedCount"] for x in ticks] == [None, None, None]  # each tick sits between other events
    found = client.get(f"{BASE}/events", params={"q": "rm_recursive"}).json()
    assert [x["name"] for x in found["data"]] == ["tengu_bash_tool_command_executed"] and found["meta"]["shown"] == 1


def test_events_internals_hidden(dataset, client_for, write_sample):
    """Client internals are hidden by default; consecutive tool-progress ticks fold into one row."""
    cc = ClaudeCode(dataset, S1)
    cc.event(f"{H}:00:00", "tengu_input_prompt", cc_prompt_id="p1", prompt_index=1)
    cc.event(f"{H}:00:01", "tengu_tether_decision", decision="keep")
    cc.event(f"{H}:00:02", "tengu_spinner_stalled_ui", ms=900)
    for s in range(3, 7):
        cc.event(f"{H}:00:0{s}", "tengu_tool_use_progress", cc_prompt_id="p1", toolName="Bash")
    cc.event(f"{H}:00:08", "tengu_bash_ast_too_complex")
    client = client_for(dataset)
    r = client.get(f"{BASE}/events")
    write_sample(client, r, "internals_hidden")
    rows, meta = r.json()["data"], r.json()["meta"]
    assert (meta["total"], meta["shown"]) == (8, 2)
    assert [x["collapsedCount"] for x in rows] == [None, 4]
    everything = client.get(f"{BASE}/events", params={"internal": "true", "collapseTicks": "false"}).json()
    assert everything["meta"]["shown"] == 8 and sum(x["internal"] for x in everything["data"]) == 3


def test_events_fold_and_page_what_the_filters_leave(dataset, client_for):
    """Ticks fold when nothing shown sits between them (a hidden internal doesn't), search is
    case-insensitive over the name and summary, and pages count shown rows, not events."""
    cc = ClaudeCode(dataset, S1)
    cc.event(f"{H}:00:00", "tengu_input_prompt", cc_prompt_id="p1", prompt_index=1)
    cc.event(f"{H}:00:01", "tengu_tool_use_progress", toolName="Bash")
    cc.event(f"{H}:00:02", "tengu_tether_decision", decision="keep")
    cc.event(f"{H}:00:03", "tengu_tool_use_progress", toolName="Bash")
    cc.event(f"{H}:00:04", "tengu_feature_ok", feature="StatusLine")
    cc.event(f"{H}:00:05", "tengu_tool_use_progress", toolName="Read")
    client = client_for(dataset)
    rows = client.get(f"{BASE}/events").json()["data"]
    assert [(x["name"], x["collapsedCount"]) for x in rows] == [
        ("tengu_input_prompt", None),
        ("tengu_tool_use_progress", 2),
        ("tengu_feature_ok", None),
        ("tengu_tool_use_progress", None),
    ]
    shown = client.get(f"{BASE}/events", params={"internal": "true"}).json()["meta"]["shown"]
    assert shown == 6  # the internal now splits the ticks
    found = client.get(f"{BASE}/events", params={"q": "STATUSLINE"}).json()
    assert [x["name"] for x in found["data"]] == ["tengu_feature_ok"] and found["meta"]["total"] == 6
    page = client.get(f"{BASE}/events", params={"pageSize": 1, "page": 2}).json()
    assert [x["collapsedCount"] for x in page["data"]] == [2] and page["meta"]["shown"] == 4


def test_events_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/events"), "error", status=500)


@pytest.mark.parametrize("path", ["", "/timeline", "/state", "/prompts", "/events"])
def test_every_session_endpoint_404s_on_an_unknown_session(dataset, client_for, path):
    r = client_for(dataset).get(f"/api/v1/sessions/nope{path}")
    assert r.status_code == 404 and r.json()["error"]["queryId"]


def test_capabilities_name_mcp_tools_the_analytics_redact(dataset, client_for):
    """Claude Code reports most MCP tool uses as "mcp_tool" (isMcp: true); the response's
    tool_use blocks name them. Skills and the MCP catalog come from Claude Code's own counts."""
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:10", "p1", 1)
    cc.call(f"{H}:01:00", prompt="p1")
    complete = [r for r in dataset.rows if r["phase"] == "complete"][-1]
    complete["tool_uses"] = [
        {
            "name": "mcp__playwright__browser_click",
            "server": "playwright",
            "tool": "browser_click",
            "kind": "mcp",
            "count": 2,
        },
        {
            "name": "mcp__playwright__browser_navigate",
            "server": "playwright",
            "tool": "browser_navigate",
            "kind": "mcp",
            "count": 1,
        },
        {"name": "Bash", "tool": "Bash", "kind": "builtin", "count": 1},
    ]
    cc.event(f"{H}:01:30", "tengu_tool_use_success", env=ENV, toolName="mcp_tool", isMcp=True)
    cc.event(f"{H}:00:20", "tengu_skills_sync_success", env=ENV, total=14)
    cc.event(f"{H}:00:30", "tengu_tool_search_mode_decision", env=ENV, mcpToolCount=358)
    cap = client_for(dataset).get(f"{BASE}/state").json()["data"]["capabilitiesAt"]
    assert [(m["name"], m["toolsUsed"]) for m in cap["mcpServers"]] == [("playwright", 2)]
    assert cap["mcpTools"] == {"total": 358, "used": 2}
    assert cap["builtin"]["used"] == 1  # Bash; "mcp_tool" isn't a built-in
    assert cap["skills"] == 14


def test_steps_name_commands_from_claude_codes_analytics(dataset, client_for):
    """Without recorded bodies a step still shows what its call carries: Claude Code's Bash
    events name each command's program and exit code (handoff §11M part 3.1)."""
    ended_session(dataset)
    data = client_for(dataset).get(f"/api/v1/sessions/{S1}/prompts").json()["data"]
    first = next(x for x in data if x["type"] == "prompt")["steps"][0]
    assert (first["commands"], first["exitCodes"], first["patches"], first["text"]) == (["rm"], [0], [], None)
