"""/people/{personId}, /summary, /requests and /coverage."""

from tests.builders import ACCOUNT, ClaudeCode, account_call, limits_on_last_call

S1 = "11111111-1111-4111-8111-111111111111"
S2 = "22222222-2222-4222-8222-222222222222"
BASE = f"/api/v1/people/{ACCOUNT}"
ENV = {"platform": "darwin", "terminal": "iTerm.app", "shell": "zsh"}


def working_session(ds, sid=S1, hour=9, bypassed=0):
    """A realistic slice: prompts, calls with reports, tools, commands, git, retries."""
    cc = ClaudeCode(ds, sid)
    h = f"2026-11-04T{hour:02d}"
    cc.event(f"{h}:00:00", "tengu_input_prompt", env=ENV, subscription_type="max", prompt_index=1, cc_prompt_id="p1")
    for i in range(3):
        rid = cc.call(f"{h}:0{i + 1}:00", prompt="p1", output_tokens=200)
        cc.reported(f"{h}:0{i + 1}:05", rid)
        cc.event(
            f"{h}:0{i + 1}:10",
            "tengu_api_success",
            requestId=rid,
            costUSD="0.25",
            cache_creation_1h_input_tokens="1000",
        )
    cc.call(f"{h}:05:00", request_class="subagent")
    limits_on_last_call(ds)
    cc.event(f"{h}:06:00", "tengu_tool_use_success", toolName="Bash", isMcp=False, preToolHookDurationMs="40")
    cc.event(
        f"{h}:06:01",
        "tengu_tool_use_success",
        toolName="mcp__github__create_pr",
        isMcp=True,
        preToolHookDurationMs="60",
    )
    cc.event(
        f"{h}:06:02",
        "tengu_bash_tool_command_executed",
        bash_argv0="git",
        destructive_category="none",
        sandbox_enabled="false",
    )
    cc.event(
        f"{h}:06:03",
        "tengu_bash_tool_command_executed",
        bash_argv0="rm",
        destructive_category="rm_recursive_force",
        destructive_target_scope="outside_cwd",
        sandbox_enabled="false",
    )
    cc.event(f"{h}:06:04", "tengu_git_operation", operation="commit")
    cc.event(f"{h}:06:05", "tengu_git_operation", operation="push")
    cc.event(f"{h}:06:06", "tengu_session_linked_to_pr", prNumber=2)
    cc.event(f"{h}:06:07", "tengu_api_retry", attempt=1)
    cc.event(
        f"{h}:06:08",
        "tengu_api_success",
        requestId="req_x",
        gzipSkipReason="custom_ca",
        permissionMode="bypassPermissions",
    )
    for m in range(bypassed):
        cc.reported(f"{h}:{20 + m}:00", f"req_missed_{m}", ttftMs=2400, cache_creation_1h_input_tokens=512)
    account_call(cc, f"{h}:00:30")
    return cc


# --- /people/{id} -------------------------------------------------------------------------------


def test_person_identified(dataset, client_for, write_sample):
    working_session(dataset)
    client = client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "identified")
    p = r.json()["data"]
    assert p["plan"] == "max" and p["orgIdShort"] == "0000…00a9"
    assert p["devices"] == [{"deviceId": "d" * 64, "platform": "darwin", "terminal": "iTerm.app", "shell": "zsh"}]
    assert p["limits"] == {"status": "allowed", "utilization7d": 0.05, "overage": "rejected"}
    assert p["posture"]["sandbox"] == "off" and p["posture"]["proxyDetected"] == "custom_ca"
    assert p["name"] is None and p["flags"]["total"] is None  # need body extraction (M7): unknown, never 0


def test_person_unidentified(dataset, client_for, write_sample):
    """Calls with identity but no analytics: almost everything is unknown."""
    ClaudeCode(dataset, S1).call("2026-11-04T09:01:00")
    client = client_for(dataset)
    r = client.get(BASE)
    write_sample(client, r, "unidentified")
    p = r.json()["data"]
    assert p["plan"] is None and p["devices"] == [] and p["posture"]["bypassPct"] is None and p["lastSeenAt"]


def test_person_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    write_sample(client, client.get(BASE), "error", status=500)


# --- /summary -----------------------------------------------------------------------------------


def test_summary_one_session(dataset, client_for, write_sample):
    working_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/summary")
    write_sample(client, r, "one_session")
    d = r.json()["data"]
    rb = d["ribbon"]
    assert (rb["modelCalls"], rb["sessions"], rb["accountCalls"]) == (4, 1, 1)
    assert rb["tokens"]["output"] == 700 and rb["tokens"]["cacheWrite1h"] == 3000 and rb["cost"]["usd"] == 0.75
    assert rb["cost"]["estimatedCalls"] == 1  # the subagent call has no tengu_api_success: its cost isn't in usd
    fp = d["footprint"]
    assert fp["toolsByName"] == {"Bash": 1, "mcp__github__create_pr": 1}
    assert fp["destructive"] == {"total": 1, "rmF": 0, "rmRf": 1, "outsideCwd": 1}
    assert fp["codeShipped"] == {"headChanges": None, "commits": 1, "pushes": 1, "prs": [2]}
    assert fp["failures"]["apiRetries"] == 1 and fp["subagentCalls"] == 1
    assert d["capabilities"]["mcpServers"] == [{"name": "github", "tools": None, "toolsUsed": 1, "riskyVerbs": []}]
    assert d["capabilities"]["hookOverheadMs"] == 50.0


def test_summary_many_follows_the_range(dataset, client_for, write_sample):
    working_session(dataset, S1, 9)
    working_session(dataset, S2, 14)
    client = client_for(dataset)
    r = client.get(f"{BASE}/summary")
    write_sample(client, r, "many")
    assert r.json()["data"]["ribbon"]["sessions"] == 2 and r.json()["data"]["ribbon"]["modelCalls"] == 8
    morning = client.get(f"{BASE}/summary", params={"from": "2026-11-04T08:00:00Z", "to": "2026-11-04T12:00:00Z"})
    assert morning.json()["data"]["ribbon"]["modelCalls"] == 4


def test_summary_coverage_gap(dataset, client_for, write_sample):
    working_session(dataset, bypassed=3)
    client = client_for(dataset)
    r = client.get(f"{BASE}/summary")
    write_sample(client, r, "coverage_gap")
    cov = r.json()["data"]["coverage"]
    assert cov["notCaptured"] == 4 and [w["count"] for w in cov["windows"]] == [4]  # req_x + 3 missed, one window


def test_summary_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    write_sample(client, client.get(f"{BASE}/summary"), "error", status=500)


# --- /requests ----------------------------------------------------------------------------------


def test_requests_empty(dataset, client_for, write_sample):
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests")
    write_sample(client, r, "empty")
    assert r.json()["data"] == []


def test_requests_one(dataset, client_for, write_sample):
    cc = ClaudeCode(dataset, S1)
    rid = cc.call("2026-11-04T09:01:00", stop_reason="end_turn", cache_read=40_000, content_encoding="gzip")
    cc.event("2026-11-04T09:01:05", "tengu_api_success", requestId=rid, ttftMs=1900, cache_creation_5m_input_tokens=0)
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"kind": "model"})
    write_sample(client, r, "one")
    (row,) = r.json()["data"]
    assert row["kind"] == "model" and row["detail"] == "main · opus · end_turn" and row["captured"] is True
    assert row["status"] == 200 and row["background"] is False and row["durationMs"] == 2.0
    # Cell tooltips (handoff §6B.10).
    assert (row["statusText"], row["stopReason"], row["ttftMs"], row["contentEncoding"]) == (
        "OK",
        "end_turn",
        1900.0,
        "gzip",
    )
    assert row["tokens"] == {
        "input": 3,
        "cacheRead": 40_000,
        "cacheWrite": None,
        "cacheWrite5m": 0,
        "cacheWrite1h": None,
        "output": 100,
        "accuracy": "exact",
    }
    assert row["error"] is None


def test_requests_failed(dataset, client_for, write_sample):
    """A connection reset mid-stream, a 500, and Anthropic's unregistered 529."""
    cc = ClaudeCode(dataset, S1)
    cc.call("2026-11-04T09:01:00", status=None, error="connection reset", bytes_down=2_048)
    cc.call("2026-11-04T09:02:00", status=500)
    cc.call("2026-11-04T09:03:00", status=529)
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"order": "asc"})
    write_sample(client, r, "failed")
    reset, internal, overloaded = r.json()["data"]
    assert reset["status"] == "ERR" and reset["statusText"] == "connection reset"
    assert reset["error"] == {
        "message": "connection reset",
        "afterMs": 2.0,
        "bytesReceived": 2_048,
        "retries": None,  # until tengu_api_retry is joined to requests (M4)
        "retrySucceededAt": None,
    }
    assert internal["statusText"] == "Internal Server Error" and internal["error"]["message"] == "Internal Server Error"
    assert internal["error"]["bytesReceived"] is None
    assert overloaded["statusText"] is None and overloaded["error"]["message"] == "HTTP 529"


def test_requests_many_newest_first_filtered_and_paged(dataset, client_for, write_sample):
    working_session(dataset)
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"pageSize": 50})
    write_sample(client, r, "many")
    rows = r.json()["data"]
    assert [x["t"] for x in rows] == sorted((x["t"] for x in rows), reverse=True)
    kinds = {x["kind"] for x in rows}
    assert kinds == {"model", "analytics", "account"}
    batch = next(x for x in rows if x["kind"] == "analytics")
    assert batch["background"] is True and batch["detail"].endswith(" events") and batch["sessionId"] == S1
    assert batch["tokens"] is None and batch["stopReason"] is None  # model calls only
    assert batch["statusText"] is None  # the batch's response wasn't captured: no status, no phrase
    model = [x for x in rows if x["kind"] == "model" and x["captured"]]
    assert [x["tokens"]["output"] for x in model] == [100, 200, 200, 200]  # newest first: the subagent call
    assert all(x["statusText"] == "OK" and x["tokens"]["output"] is not None for x in model)
    only_model = client.get(f"{BASE}/requests", params={"kind": "model"}).json()["data"]
    assert {x["kind"] for x in only_model} == {"model"}
    assert client.get(f"{BASE}/requests", params={"q": "kind=account"}).json()["meta"]["total"] == 1


def test_requests_not_captured(dataset, client_for, write_sample):
    working_session(dataset, bypassed=2)
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"captured": "false"})
    write_sample(client, r, "not_captured")
    rows = r.json()["data"]
    assert len(rows) == 3 and all(x["captured"] is False and x["method"] is None for x in rows)
    # Only what the tengu_api_success carries: no response was seen.
    assert all(x["statusText"] is None and x["stopReason"] is None and x["error"] is None for x in rows)
    missed = next(x for x in rows if x["requestId"] == "reported:req_missed_0")
    assert missed["ttftMs"] == 2400.0 and missed["tokens"]["cacheWrite1h"] == 512
    assert missed["tokens"]["output"] is None and missed["tokens"]["accuracy"] == "missing"
    assert {x["requestId"] for x in rows} >= {"reported:req_missed_0", "reported:req_missed_1"}


def test_requests_account_calls(dataset, client_for, write_sample):
    cc = ClaudeCode(dataset, S1)
    cc.call("2026-11-04T09:01:00")
    account_call(cc, "2026-11-04T09:00:30", "/api/oauth/organizations/x/skills/list-skills")
    account_call(cc, "2026-11-04T09:00:31", "/mcp-registry/v0/servers")
    client = client_for(dataset)
    r = client.get(f"{BASE}/requests", params={"kind": "account"})
    write_sample(client, r, "account_calls")
    rows = r.json()["data"]
    assert [x["detail"] for x in rows] == ["servers", "list-skills"] and all(x["sessionId"] is None for x in rows)


def test_requests_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    write_sample(client, client.get(f"{BASE}/requests"), "error", status=500)


# --- /coverage ----------------------------------------------------------------------------------


def test_coverage_complete(dataset, client_for, write_sample):
    cc = ClaudeCode(dataset, S1)
    rid = cc.call("2026-11-04T09:01:00")
    cc.reported("2026-11-04T09:01:05", rid)
    client = client_for(dataset)
    r = client.get("/api/v1/coverage", params={"personId": ACCOUNT})
    write_sample(client, r, "complete")
    assert r.json()["data"] == {"reported": 1, "captured": 1, "notCaptured": 0, "windows": [], "calls": []}


def test_coverage_gap_lists_the_calls(dataset, client_for, write_sample):
    working_session(dataset, bypassed=2)
    working_session(dataset, S2, 14)
    client = client_for(dataset)
    r = client.get("/api/v1/coverage", params={"personId": ACCOUNT, "sessionId": S1})
    write_sample(client, r, "gap")
    d = r.json()["data"]
    assert d["notCaptured"] == 3 and len(d["calls"]) == 3 and {c["sessionId"] for c in d["calls"]} == {S1}
    assert [c["t"] for c in d["calls"]] == sorted(c["t"] for c in d["calls"])


def test_coverage_requires_a_person(dataset, client_for):
    r = client_for(dataset).get("/api/v1/coverage")
    assert r.status_code == 422 and r.json()["error"]["code"] == "invalid_request"


def test_coverage_error(dataset, client_for, write_sample, monkeypatch):
    client = client_for(dataset, raise_server_exceptions=False)
    monkeypatch.setattr(client.app.state.store, "query", lambda *a, **k: 1 / 0)
    write_sample(client, client.get("/api/v1/coverage", params={"personId": ACCOUNT}), "error", status=500)
