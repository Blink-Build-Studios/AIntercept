"""/sessions/{sessionId}, /timeline, /state, /prompts, /requests and /events, on the Postgres store: the
session pages read v9's facts and rollups (docs/PIPELINE.md §6.1).

The datasets send Claude Code's analytics as it does. They stay in the raw files: what only they fed
(events, coverage, friction, heads, hooks, recaps, compactions, TTFT, cost, reported-only calls) is null,
empty or 0, and the rest comes from the model calls themselves."""

import pytest

from tests.builders import ACCOUNT, DEVICE, ClaudeCode, account_call, end_batches

S1 = "11111111-1111-4111-8111-111111111111"
BASE = f"/api/v1/sessions/{S1}"
ENV = {"platform": "darwin", "terminal": "iTerm.app", "shell": "zsh"}
H = "2026-11-04T09"  # an ended session (the frozen clock is 18:05)
LIVE = "2026-11-04T18"


def prompt(cc, when, pid, index, **md):
    cc.event(when, "tengu_input_prompt", env=ENV, cc_prompt_id=pid, prompt_index=index, prompt_length=42, **md)


def step(cc, when, pid, *, tools=(), ctx=100_000, cost="0.25", **kw):
    """A main-thread call that asks for its tools, with Claude Code's tengu_api_query/_success and tool events
    (sent as Claude Code sends them; they stay in the raw files)."""
    mode = kw.pop("mode", "bypassPermissions")
    names = [name for name, _ in tools] or None
    rid = cc.call(when, prompt=pid, cache_read=ctx, cache_write=1_000, stop_reason="tool_use", tool_uses=names, **kw)
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
    """Every read the store can do fails: its Postgres snapshots and a call's raw member."""
    store = client.app.state.store
    monkeypatch.setattr(store, "read_range", lambda *a, **k: 1 / 0)
    monkeypatch.setattr(store.reader, "snapshot", lambda *a, **k: 1 / 0)


# --- /sessions/{id} -----------------------------------------------------------------------------


def test_session_ended(dataset, pg_client_for, write_sample):
    ended_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "ended")
    h = r.json()["data"]
    # Its span is its model calls' (the turn-end event after the last isn't kept).
    assert h["start"] == "2026-11-04T09:01:00Z" and h["end"] == "2026-11-04T09:10:30Z" and h["live"] is False
    assert h["personId"] == ACCOUNT and (h["modelCalls"], h["prompts"], h["notCaptured"], h["networks"]) == (
        4,
        2,
        None,
        1,
    )
    assert h["device"] == {"deviceId": DEVICE, "platform": None, "terminal": None, "shell": None}
    assert h["friction"] == {"interrupted": 0, "failedCalls": 0} and h["bypassPct"] is None
    assert (h["events"], h["coverage"], h["capturedFromPrompt"]) == (None, None, None) and "costUsd" not in h
    assert h["repo"] is None and h["branch"] is None  # M7 extraction: unknown, never ""


def test_session_live(dataset, pg_client_for, write_sample):
    live_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "live")
    h = r.json()["data"]
    assert h["live"] is True and h["end"] is None and h["networks"] is None


def test_session_pre_parser(dataset, pg_client_for, write_sample):
    """Calls from before the identity parser: almost everything is unknown."""
    cc = ClaudeCode(dataset, S1)
    cc.call(f"{H}:01:00", prompt=None, parsed=False)
    cc.call(f"{H}:02:00", prompt=None, parsed=False)
    client = pg_client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "pre_parser")
    h = r.json()["data"]
    assert h["fieldsMissing"] is True and h["personId"] is None and h["device"] is None
    assert h["events"] is None and h["notCaptured"] is None and h["coverage"] is None


def test_session_unknown_is_a_typed_404(dataset, pg_client_for):
    r = pg_client_for(dataset).get("/api/v1/sessions/nope")
    assert r.status_code == 404 and r.json()["error"]["code"] == "http_404"


def test_session_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(BASE), "error", status=500)


# --- /timeline ----------------------------------------------------------------------------------


def test_timeline_one_prompt(dataset, pg_client_for, write_sample):
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:00", "p1", 1)
    for minute, ctx in ((1, 50_000), (2, 60_000), (3, 70_000)):
        step(cc, f"{H}:0{minute}:00", "p1", ctx=ctx)
    cc.event(f"{H}:04:00", "tengu_turn_end", cc_prompt_id="p1")
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/timeline")
    write_sample(client, r, "one_prompt")
    d = r.json()["data"]
    assert d["binSeconds"] == 300 and d["now"] is None and len(d["sessions"]) == 1
    assert [p["index"] for p in d["prompts"]] == [1]
    assert [c["tokens"] for c in d["context"]] == [51_003, 61_003, 71_003]
    # Tooltip fields (handoff §6B.10).
    assert (d["sessions"][0]["prompts"], d["sessions"][0]["modelCalls"]) == (1, 3)
    (p,) = d["prompts"]
    # A prompt starts at its first step and lasts to its last step's end; its size needs its content.
    assert p["t"] == "2026-11-04T09:01:00Z" and (p["promptId"], p["steps"], p["durationMs"]) == ("p1", 3, 120_002.0)
    assert p["text"] == {"retained": False, "text": None, "chars": None, "pastedBytes": None, "images": None}
    assert (p["tokensIn"], p["tokensOut"]) == (51_003 + 61_003 + 71_003, 300)
    c = d["context"][0]
    assert (c["model"], c["thread"], c["promptIndex"], c["windowTokens"]) == ("claude-opus-5-5", "main", 1, None)
    assert c["requestId"].startswith("observer-test-0-")
    (b,) = d["modelCalls"]
    assert b["byClass"] == {"main": 3, "subagent": 0, "auxiliary": 0} and b["tokensIn"] == p["tokensIn"]
    assert d["analytics"] == [] and d["compactions"] == [] and d["notCaptured"] == [] and d["friction"] == []


def test_timeline_many(dataset, pg_client_for, write_sample):
    """Several prompts; Claude Code's friction events aren't kept."""
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
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/timeline", params={"bin": 600})
    write_sample(client, r, "many")
    d = r.json()["data"]
    assert d["binSeconds"] == 600 and [p["index"] for p in d["prompts"]] == [1, 2, 3]
    assert d["friction"] == [] and d["analytics"] == []


def test_timeline_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
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


def test_state_default(dataset, pg_client_for, write_sample):
    state_session(dataset)
    end_batches(dataset)  # an analytics post is its session's from its end (test_a_post_in_flight_…)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/state")
    write_sample(client, r, "default")
    d = r.json()["data"]
    cap = d["capabilitiesAt"]
    # The tools its calls asked for (Bash and Read, and the MCP tool by its server); Claude Code's hooks,
    # skills and pool changes aren't kept.
    assert cap["builtin"] == {"total": None, "used": 2} and cap["mcpTools"] == {"total": None, "used": 1}
    assert (cap["hooks"], cap["skills"], cap["poolChangesInRange"]) == (None, None, None)
    repo = d["repoInRange"]
    assert (repo["headFrom"], repo["headTo"], repo["headChanges"]) == (None, None, None)
    assert (repo["commits"], repo["pushed"], repo["prs"], repo["cwdTrail"]) == (0, False, [], None)
    assert d["recapsTotal"] is None and d["recapsInRange"] is None
    assert d["experimentsAt"] is None and d["flagFetchesInRange"] is None
    # Each count matches its tab over the same range: the analytics posts name no session, so the tab lists
    # its 4 model calls.
    assert d["counts"] == {
        "prompts": 2,
        "modelCalls": 4,
        "requests": 4,
        "events": 0,
        "compactions": 0,
        "turns": 2,
        "steps": 4,
    }
    assert client.get(f"{BASE}/requests").json()["meta"]["total"] == d["counts"]["requests"]


def test_state_follows_the_range(dataset, pg_client_for):
    state_session(dataset)
    client = pg_client_for(dataset)
    d = client.get(f"{BASE}/state", params={"from": "2026-11-04T09:09:00Z"}).json()["data"]
    assert d["counts"]["prompts"] == 1 and d["counts"]["modelCalls"] == 1 and d["recapsInRange"] is None
    assert d["capabilitiesAt"]["builtin"]["used"] == 2


def test_state_capabilities_are_the_whole_sessions(dataset, pg_client_for):
    """Capabilities don't follow the brush (handoff §11Q.4): a range that ends before any tool ran shows the
    whole session's."""
    state_session(dataset)
    client = pg_client_for(dataset)
    whole = client.get(f"{BASE}/state").json()["data"]["capabilitiesAt"]
    early = client.get(f"{BASE}/state", params={"to": "2026-11-04T08:00:00Z"}).json()["data"]["capabilitiesAt"]
    assert early == whole and whole["builtin"]["used"] == 2


def test_state_empty_range(dataset, pg_client_for, write_sample):
    state_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/state", params={"from": "2026-11-04T12:00:00Z", "to": "2026-11-04T13:00:00Z"})
    write_sample(client, r, "empty_range")
    d = r.json()["data"]
    assert set(d["counts"].values()) == {0} and d["repoInRange"]["headFrom"] is None


def test_state_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/state"), "error", status=500)


# --- /prompts -----------------------------------------------------------------------------------


def test_prompts_empty(dataset, pg_client_for, write_sample):
    ended_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/prompts", params={"from": "2026-11-04T12:00:00Z"})
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0


def test_prompts_one(dataset, pg_client_for, write_sample):
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
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/prompts")
    write_sample(client, r, "one")
    ((item,),) = [r.json()["data"]]
    p, (s,) = item["prompt"], item["steps"]
    # The prompt is its one step: from its start to its end.
    assert (p["index"], p["steps"], p["durationMs"], p["inFlight"]) == (1, 1, 2.0, False)
    assert p["text"] == {"retained": False, "text": None, "chars": None, "pastedBytes": None, "images": None}
    assert p["tokensIn"] == 101_003 and p["toolCounts"] == {"Bash": 1} and p["friction"]["destructive"] == 0
    assert (s["status"], s["costUsd"], s["ttftMs"], s["eventsPending"]) == ("done", None, None, False)
    assert s["tokens"]["cacheWrite"] == 1_000 and s["tokens"]["cacheWrite1h"] is None and s["stopReason"] == "tool_use"
    (tool,) = s["tools"]  # what the call asked for; its exit code and destructive category were analytics'
    assert (tool["name"], tool["status"], tool["exitCode"], tool["destructive"]) == ("Bash", "ok", None, None)


def test_prompts_many(dataset, pg_client_for, write_sample):
    ended_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/prompts")
    write_sample(client, r, "many")
    data = r.json()["data"]
    assert [i["type"] for i in data] == ["prompt", "between", "prompt"]
    first, between, second = data
    assert first["prompt"]["text"]["pastedBytes"] is None and first["prompt"]["steps"] == 2
    assert [s["kind"] for s in first["steps"]] == ["model", "subagent"] and first["steps"][1]["agentType"] == "Explore"
    tools = first["steps"][0]["tools"]
    assert [t["mcpServer"] for t in tools] == [None, "github"] and tools[0]["destructive"] is None
    assert between["between"]["kind"] == "call" and between["between"]["detail"] == "auxiliary · opus · end_turn"
    assert second["prompt"]["friction"] == {"frustrated": False, "interrupted": False, "destructive": 0}
    assert [t["name"] for t in second["steps"][0]["tools"]] == ["Read"]
    page = client.get(f"{BASE}/prompts", params={"pageSize": 2, "page": 2}).json()
    assert [i["type"] for i in page["data"]] == ["prompt"] and page["meta"]["total"] == 3
    # A jump from the timeline asks for prompt #2: the page that holds it, whatever `page` says.
    jump = client.get(f"{BASE}/prompts", params={"pageSize": 2, "page": 1, "prompt": 2}).json()
    assert jump["meta"]["page"] == 2 and jump["data"][0]["prompt"]["index"] == 2
    assert client.get(f"{BASE}/prompts", params={"pageSize": 2, "prompt": 9}).json()["meta"]["page"] == 1


def test_prompts_in_flight(dataset, pg_client_for, write_sample):
    live_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/prompts")
    write_sample(client, r, "in_flight")
    done, running = (i["prompt"] for i in r.json()["data"])
    assert done["inFlight"] is False and running["inFlight"] is True and running["durationMs"] is None
    (s,) = r.json()["data"][1]["steps"]
    assert s["status"] == "streaming" and s["durationMs"] is None and s["eventsPending"] is False


def test_prompts_between_rows(dataset, pg_client_for):
    """A visible call outside any prompt (a compaction's) sits between prompts as a call row that opens its
    drawer; Claude Code's compaction and recap events aren't kept."""
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:00", "p1", 1)
    step(cc, f"{H}:01:00", "p1", ctx=800_000)
    cc.event(f"{H}:20:00", "tengu_reactive_compact_attempt", cc_prompt_id="p1")
    cc.call(f"{H}:20:01", prompt=None, request_class="compaction")
    cc.event(f"{H}:21:00", "tengu_reactive_compact_succeeded", cc_prompt_id="p1")
    cc.event(f"{H}:25:00", "tengu_return_to_session", blurDurationMs=60_000, hadRecap=True)
    step(cc, f"{H}:30:00", "p2")
    data = pg_client_for(dataset).get(f"{BASE}/prompts").json()["data"]
    assert [i["type"] for i in data] == ["prompt", "between", "prompt"]
    between = data[1]["between"]
    assert between["kind"] == "call" and between["requestId"]
    assert (between["tokensFrom"], between["tokensTo"], between["waitMs"]) == (None, None, None)


def test_prompts_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/prompts"), "error", status=500)


# --- /requests ----------------------------------------------------------------------------------


def test_requests_empty(dataset, pg_client_for, write_sample):
    ended_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"from": "2026-11-04T12:00:00Z"})
    write_sample(client, r, "empty")
    assert r.json()["data"] == []


def test_requests_one(dataset, pg_client_for, write_sample):
    ClaudeCode(dataset, S1).call(f"{H}:01:00")
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/requests")
    write_sample(client, r, "one")
    (row,) = r.json()["data"]
    assert row["kind"] == "model" and row["sessionId"] == S1 and row["captured"] is True


def test_requests_many_oldest_first_and_session_only(dataset, pg_client_for, write_sample):
    cc = ended_session(dataset)
    account_call(cc, f"{H}:00:30")  # the person's, but no session: not on the session page
    ClaudeCode(dataset, "22222222-2222-4222-8222-222222222222").call(f"{H}:05:00")
    end_batches(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"pageSize": 50})
    write_sample(client, r, "many")
    rows = r.json()["data"]
    assert [x["t"] for x in rows] == sorted(x["t"] for x in rows)
    # Its analytics posts name no session (test_an_analytics_post_is_no_sessions): its model calls are listed.
    assert {x["sessionId"] for x in rows} == {S1} and {x["kind"] for x in rows} == {"model"}
    model = [x for x in rows if x["kind"] == "model"]
    assert [x["promptIndex"] for x in model] == [1, 1, None, 2] and [x["stepIndex"] for x in model] == [1, 2, None, 1]
    assert [x["ttftMs"] for x in model] == [None, None, None, None]  # Claude Code's reports aren't kept
    assert [x["stopReason"] for x in model] == ["tool_use", None, "end_turn", "tool_use"]
    assert model[0]["tokens"]["cacheRead"] == 100_000 and model[0]["tokens"]["cacheWrite1h"] is None
    newest = client.get(f"{BASE}/requests", params={"order": "desc", "kind": "model"}).json()["data"]
    assert newest[0]["t"] == "2026-11-04T09:10:30Z"


def test_requests_unknown_session(dataset, pg_client_for):
    assert pg_client_for(dataset).get("/api/v1/sessions/nope/requests").status_code == 404


def test_requests_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"{BASE}/requests"), "error", status=500)


@pytest.mark.parametrize("path", ["", "/timeline", "/state", "/prompts"])
def test_every_session_endpoint_404s_on_an_unknown_session(dataset, pg_client_for, path):
    r = pg_client_for(dataset).get(f"/api/v1/sessions/nope{path}")
    assert r.status_code == 404 and r.json()["error"]["queryId"]


def test_capabilities_name_mcp_tools_from_the_calls(dataset, pg_client_for):
    """The tools a session's calls asked for name its MCP servers and tools (the response's tool_use blocks);
    Claude Code's own counts (skills, the MCP catalog) aren't kept."""
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
    cc.event(f"{H}:00:20", "tengu_skills_sync_success", env=ENV, total=14)
    cc.event(f"{H}:00:30", "tengu_tool_search_mode_decision", env=ENV, mcpToolCount=358)
    cap = pg_client_for(dataset).get(f"{BASE}/state").json()["data"]["capabilitiesAt"]
    assert [(m["name"], m["toolsUsed"]) for m in cap["mcpServers"]] == [("playwright", 2)]
    assert cap["mcpTools"] == {"total": None, "used": 2}
    assert cap["builtin"]["used"] == 1 and cap["skills"] is None and cap["hooks"] is None


def test_an_analytics_post_is_no_sessions(dataset, pg_client_for):
    """An analytics post is a call like any other, but it names no session: it's on its person's request log
    (by its credential), not a session's, before and after it ends; its events are none of the session's."""
    cc = ClaudeCode(dataset, S1)
    prompt(cc, f"{H}:00:10", "p1", 1)
    cc.call(f"{H}:01:00", prompt="p1")
    client = pg_client_for(dataset)
    body = client.get(f"{BASE}/requests").json()
    assert body["kindCounts"] == {"model": 1}
    end_batches(dataset)
    client = pg_client_for(dataset)
    assert client.get(f"{BASE}/requests").json()["kindCounts"] == {"model": 1}
    person = client.get(f"/api/v1/people/{ACCOUNT}/requests").json()
    assert person["kindCounts"] == {"analytics": 1, "model": 1}
    (post,) = [x for x in person["data"] if x["kind"] == "analytics"]
    assert post["sessionId"] is None and post["background"] is True and post["detail"] == "batch"


# --- without the agents' own analytics (handoff §11S) --------------------------------------------------------


def wire_session(ds, sid=S1):
    """A session as the wire tells it: tool lists (the main thread's changing once, a subagent's), tool calls
    (built-in and MCP), an overload and a reset (failed calls), an interrupted turn, a compaction."""
    cc = ClaudeCode(ds, sid)
    main = ["Bash", "Read", "Edit", "mcp__linear__list_issues", "mcp__linear__get_issue", "mcp__sentry__find"]
    cc.call(f"{H}:00:10", prompt="p1", tools_offered=main, tool_uses=["Read", "Read", "mcp__linear__list_issues"])
    cc.call(f"{H}:00:20", prompt="p1", tools_offered=main, tool_uses=["Bash"])
    cc.call(
        f"{H}:00:30", prompt="p1", tools_offered=["Read"], request_class="subagent", agent_id="ag1", tool_uses=["Read"]
    )
    cc.call(f"{H}:00:40", prompt="p1", tools_offered=main, status=529)
    cc.call(
        f"{H}:01:10", prompt="p2", tools_offered=[*main, "mcp__github__get_pr"], status=None, error="connection reset"
    )
    cc.call(f"{H}:01:20", prompt="p2", tools_offered=[*main, "mcp__github__get_pr"], stop_reason="interrupted")
    cc.call(f"{H}:02:00", prompt="p3", request_class="compaction")
    return cc


def test_header_says_what_the_wire_carries(dataset, pg_client_for):
    wire_session(dataset)
    h = pg_client_for(dataset).get(BASE).json()["data"]
    assert h["recording"] == "metadata" and h["failedCalls"] == 2
    t = h["tools"]
    assert (t["offered"], t["used"], t["calls"], t["listChanges"]) == (7, 3, 5, 1)
    assert t["byName"] == {"Read": 3, "Bash": 1, "mcp__linear__list_issues": 1}
    assert [(s["source"], s["offered"], s["used"]) for s in t["sources"]] == [
        ("Built-in", 3, 2),
        ("mcp:linear", 2, 1),
        ("mcp:github", 1, 0),
        ("mcp:sentry", 1, 0),
    ]
    assert "costUsd" not in h


def test_state_tools_context_and_failures_follow_the_range(dataset, pg_client_for):
    wire_session(dataset)
    client = pg_client_for(dataset)
    whole = client.get(f"{BASE}/state").json()["data"]
    assert whole["failedCalls"] == 2 and whole["context"] == {"compactions": 1, "subagentThreads": 1}
    first = client.get(f"{BASE}/state", params={"from": f"{H}:00:00Z", "to": f"{H}:00:59Z"}).json()["data"]
    assert first["failedCalls"] == 1 and first["tools"]["listChanges"] == 0 and first["context"]["compactions"] == 0


def test_timeline_friction_is_interrupts_and_failed_calls(dataset, pg_client_for):
    wire_session(dataset)
    marks = pg_client_for(dataset).get(f"{BASE}/timeline").json()["data"]["friction"]
    assert {m["kind"] for m in marks} == {"failed", "interrupted"}
    assert sum(m["count"] for m in marks if m["kind"] == "failed") == 2  # per bin, counted
    assert [m["promptIndex"] is None for m in marks if m["kind"] == "interrupted"] == [False]


def test_turns_carry_their_tools_and_failed_calls(dataset, pg_client_for):
    wire_session(dataset)
    turns = {t["n"] or t["promptId"]: t for t in pg_client_for(dataset).get(f"{BASE}/turns").json()["data"]}
    p1 = next(t for t in turns.values() if t["promptId"] == "p1")
    assert p1["tools"] == {"Read": 3, "Bash": 1, "mcp__linear__list_issues": 1}
    assert p1["friction"] == {"interrupted": False, "failedCalls": 1} and "costUsd" not in p1
    p2 = next(t for t in turns.values() if t["promptId"] == "p2")
    assert p2["friction"]["failedCalls"] == 1
