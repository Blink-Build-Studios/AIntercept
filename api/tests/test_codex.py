"""Codex as the second agent (handoff §11J): its threads roll up like Claude Code's
sessions, people link across agents by email, and what only Codex has comes out
in its own fields."""

import json
from datetime import UTC, datetime

from aintercept_api import agents, rows
from tests.builders import ACCOUNT, CODEX_ACCOUNT, ClaudeCode, Codex

ROOT = "01a1bd9a-0000-7000-8000-00000000r00t"
CHILD = "01a1bd9a-0000-7000-8000-0000000child"
REVIEW = "01a1bd9a-0000-7000-8000-000000review"
FORK = "01a1bd9a-0000-7000-8000-00000000fork"
EMAIL = "ada@example.com"
S1 = "11111111-1111-4111-8111-111111111111"


def claude_code_account(ds, email=EMAIL):
    """A Claude Code person whose account endpoints name them (the accounts table)."""
    cc = ClaudeCode(ds, S1)
    cc.event("2026-11-04T08:00:00", "tengu_input_prompt", prompt_index=1, cc_prompt_id="p1")
    cc.call("2026-11-04T08:00:10", prompt="p1")
    cid = ds.transaction("2026-11-04T08:00:20", method="GET", path="/api/oauth/profile", key_hash=cc.key_hash)
    for r in ds.rows:
        if r["correlation_id"] == cid and r["phase"] == "complete":
            r["auth"] = {"account_id": ACCOUNT, "email": email, "name": "Ada Lovelace"}
    return cc


def codex_work(ds, email=EMAIL):
    """A Codex exec thread: a prewarm, two turns (the first with its report), a title call; a
    subagent and a review thread under it, and a fork of it."""
    root = Codex(ds, ROOT, email=email)
    root.turn("2026-11-04T15:53:00", None, kind="prewarm")
    root.turn("2026-11-04T15:53:02", "turn-1")
    root.turn("2026-11-04T15:53:20", "turn-1")
    root.turn("2026-11-04T15:53:30", None, source="thread_title")
    root.turn_event(
        "2026-11-04T15:54:00",
        "turn-1",
        status="completed",
        duration_ms=58000,
        sampling_ms=41000,
        sampling_request_count=2,
        sampling_retry_count=0,
        shell_command_count=4,
        file_change_count=1,
        steer_count=1,
        turn_trigger="exec",
        approval_policy="never",
        sandbox_policy="full_access",
    )
    root.turn("2026-11-04T15:56:00", "turn-2", used_5h="44")
    sub = {"parent_thread_id": ROOT, "parent_turn_id": "turn-2", "subagent_kind": "thread_spawn"}
    Codex(ds, CHILD, email=email, lineage={**sub, "agent_name": "/root/edge_tests", "thread_source": "subagent"}).turn(
        "2026-11-04T15:56:30", "turn-c1"
    )
    Codex(ds, REVIEW, email=email, lineage={**sub, "subagent_kind": "review"}).turn("2026-11-04T15:57:00", "turn-r1")
    Codex(
        ds,
        FORK,
        email=email,
        originator="codex-tui",
        lineage={
            "forked_from_thread_id": ROOT,
            "forked_from_ordinal_exclusive": 2,
            "history_initialization": "cold_fork",
        },
    ).turn("2026-11-04T16:10:00", "turn-f1")
    return root


def test_people_link_accounts_by_email(dataset, client_for, write_sample):
    claude_code_account(dataset)
    codex_work(dataset)
    client = client_for(dataset)
    r = client.get("/api/v1/people")
    assert r.status_code == 200, r.text
    rows = r.json()["data"]
    assert len(rows) == 1
    row = rows[0]
    assert row["agents"] == ["claude-code", "codex"]
    assert row["linkedBy"] == "email"
    assert row["accounts"] == 2
    assert row["sessions"] == 5
    write_sample(client, r, "linked")


def test_unlinked_accounts_stay_separate(dataset, client_for):
    claude_code_account(dataset)
    codex_work(dataset, email="ada@personal.example")
    rows = client_for(dataset).get("/api/v1/people").json()["data"]
    assert sorted(r["agents"][0] for r in rows) == ["claude-code", "codex"]
    assert all(r["linkedBy"] is None for r in rows)


def test_overview_lists_each_agents_fields_and_channels(dataset, client_for):
    """Round 14 (handoff §11L): each product lists only the provider-specific fields it reports,
    and its own request channels; the Codex card's headline is its highest weekly limit."""
    codex_work(dataset)
    data = client_for(dataset).get("/api/v1/overview").json()["data"]
    cards = {a["agent"]: a for a in data["agents"]}
    assert cards["codex"]["people"] == 1
    assert cards["codex"]["sessions"] == 4
    assert cards["codex"]["highestWeekPct"] == 83.0 and cards["codex"]["missedCalls"] is None
    assert "bypass" not in data["capabilities"]["codex"] and "limits" in data["capabilities"]["codex"]
    assert "bypass" in data["capabilities"]["claude-code"] and "limits" not in data["capabilities"]["claude-code"]
    assert data["channels"]["codex"] == ["model", "analytics", "metrics", "usage", "catalog"]
    assert data["devices"] == 1


def test_person_header_lists_accounts_and_limits(dataset, client_for, write_sample):
    claude_code_account(dataset)
    codex_work(dataset)
    client = client_for(dataset)
    r = client.get(f"/api/v1/people/{CODEX_ACCOUNT}")
    assert r.status_code == 200, r.text
    h = r.json()["data"]
    assert h["linkedBy"] == "email"
    assert {a["agent"] for a in h["accounts"]} == {"claude-code", "codex"}
    assert next(a for a in h["accounts"] if a["agent"] == "codex")["plan"] == "plus"
    (limits,) = [x for x in h["agentLimits"] if x["agent"] == "codex"]
    assert [(w["name"], w["usedPct"]) for w in limits["windows"]] == [("5h", 41.0), ("week", 83.0)]
    assert [a["agent"] for a in h["agentInfo"]] == ["claude-code", "codex"]
    assert next(a for a in h["agentInfo"] if a["agent"] == "codex")["version"] == "0.167.0"
    write_sample(client, r, "linked_codex")


def test_person_sessions_nest_subagent_threads(dataset, client_for, write_sample):
    codex_work(dataset)
    client = client_for(dataset)
    r = client.get(f"/api/v1/people/{CODEX_ACCOUNT}/sessions")
    assert r.status_code == 200, r.text
    body = r.json()
    rows = {x["sessionId"]: x for x in body["data"]}
    assert body["meta"]["total"] == 2  # the root and the fork; the children ride with the root
    order = [x["sessionId"] for x in body["data"]]
    assert order.index(ROOT) < order.index(CHILD) and order.index(ROOT) < order.index(REVIEW)
    root = rows[ROOT]
    assert (root["agent"], root["client"], root["clientVersion"]) == ("codex", "exec", "0.164.0")
    assert root["modelCalls"] == 3 and root["hiddenCalls"] == 2
    assert root["children"] == {"subagent": 1, "review": 1, "sessionIds": [CHILD, REVIEW]}
    assert (root["repo"], root["branch"], root["commit"]) == ("pomodoro", None, "1aa2f42")
    assert rows[CHILD]["origin"]["kind"] == "subagent"
    assert rows[CHILD]["origin"]["agentName"] == "/root/edge_tests"
    assert rows[REVIEW]["origin"]["kind"] == "review"
    fork = rows[FORK]
    assert fork["origin"] == {"kind": "fork", "sessionId": ROOT, "turn": 2, "init": "cold_fork", "agentName": None}
    assert fork["client"] == "tui"
    write_sample(client, r, "codex_threads")


def test_codex_session_header_facts_and_limits(dataset, client_for, write_sample):
    codex_work(dataset)
    client = client_for(dataset)
    r = client.get(f"/api/v1/sessions/{ROOT}")
    assert r.status_code == 200, r.text
    h = r.json()["data"]
    assert h["agent"] == "codex"
    assert h["facts"] == {
        "cwd": "/work/pomodoro",
        "shell": None,
        "tz": None,
        "approval": "never",
        "sandbox": "danger-full-access",
    }
    assert h["limitsAtEnd"]["windows"][0]["usedPct"] == 44.0
    assert (h["tokens"]["input"], h["tokens"]["output"], h["toolCalls"]) == (3600, 150, None)
    assert h["events"] is None and h["coverage"]["reported"] is None
    write_sample(client, r, "codex")


def test_codex_prompts_carry_turn_report_and_hidden_calls(dataset, client_for, write_sample):
    codex_work(dataset)
    client = client_for(dataset)
    r = client.get(f"/api/v1/sessions/{ROOT}/prompts")
    assert r.status_code == 200, r.text
    prompts = [x for x in r.json()["data"] if x["type"] == "prompt"]
    assert [p["prompt"]["index"] for p in prompts] == [1, 2]
    first = prompts[0]
    assert len(first["steps"]) == 2
    assert first["prompt"]["hidden"] == {"prewarm": 1, "title": 1}  # the title names the turn before it
    report = first["prompt"]["turnReport"]
    assert report["samplingMs"] == 41000 and report["requests"] == 2 and report["steers"] == 1
    assert report["toolCalls"] == {"shell": 4, "fileChange": 1}
    assert prompts[1]["prompt"]["hidden"] == {}
    assert prompts[1]["prompt"]["turnReport"] is None
    write_sample(client, r, "codex")


def test_codex_requests_mark_hidden_calls(dataset, client_for, write_sample):
    codex_work(dataset)
    client = client_for(dataset)
    assert client.get(f"/api/v1/sessions/{ROOT}/requests").json()["hiddenCount"] == 2  # out unless asked
    r = client.get(f"/api/v1/sessions/{ROOT}/requests?hidden=true")
    assert r.status_code == 200, r.text
    rows = r.json()["data"]
    assert sorted(x["hiddenKind"] or "" for x in rows) == ["", "", "", "prewarm", "title"]
    assert all(x["agent"] == "codex" and x["kind"] == "model" for x in rows)
    assert all("~" in x["requestId"] for x in rows)  # each WebSocket turn is its own request
    write_sample(client, r, "codex_hidden")


def test_codex_turn_opens_in_the_drawer(dataset, client_for):
    codex_work(dataset)
    client = client_for(dataset)
    rid = next(
        x["requestId"]
        for x in client.get(f"/api/v1/sessions/{ROOT}/requests").json()["data"]
        if x["hiddenKind"] is None
    )
    r = client.get(f"/api/v1/requests/{rid}/details")
    assert r.status_code == 200, r.text
    (codex,) = [s for s in r.json()["data"]["sections"] if s["title"] == "Codex · response"]
    rows = {x["k"]: x["v"] for x in codex["rows"]}
    assert rows["request kind"] == "turn" and rows["limits"] == "5h 41% · week 83%" and rows["plan"] == "plus"
    raw = client.get(f"/api/v1/requests/{rid}/raw")
    assert raw.status_code == 200, raw.text


def test_codex_session_requests_carry_its_other_calls(dataset, client_for, write_sample):
    """A Codex thread's Requests tab has every call on the wire (handoff §11L.6): its model
    calls by thread id, and its client's usage and catalog calls by time; model calls carry
    their step, tool calls and, without usage, why."""
    root = codex_work(dataset)
    root.turn(
        "2026-11-04T15:56:10", "turn-2", tool_uses=[{"name": "exec", "tool": "exec", "kind": "builtin", "count": 2}]
    )
    root.turn("2026-11-04T15:56:20", "turn-2", output_tokens=None)
    root.other("2026-11-04T15:55:00", "/backend-api/wham/usage")
    root.other("2026-11-04T15:55:01", "/backend-api/ps/plugins/list")
    root.other("2026-11-04T17:00:00", "/backend-api/wham/usage")  # after the thread: not its
    client = client_for(dataset)
    r = client.get(f"/api/v1/sessions/{ROOT}/requests")
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["kindCounts"] == {"catalog": 1, "model": 5, "usage": 1}  # hidden calls only with hidden=true
    assert body["hiddenCount"] == 2
    rows = body["data"]
    exec_row = next(x for x in rows if x["toolCalls"])
    assert exec_row["toolCalls"] == ["exec ×2"] and exec_row["stepIndex"] == 2 and exec_row["effort"] == "medium"
    cut = next(
        x
        for x in rows
        if x["kind"] == "model" and x["tokens"] and x["tokens"]["output"] is None and not x["hiddenKind"]
    )
    assert cut["usageMissing"] == "streamCut"
    assert sorted(x["detail"] for x in rows if x["kind"] in ("usage", "catalog")) == ["ps/plugins/list", "wham/usage"]
    write_sample(client, r, "codex_channels")


def test_codex_limits_skip_a_zero_minute_window_and_format_credits():
    h = {
        "x-codex-plan-type": "pro",
        "x-codex-primary-used-percent": "49",
        "x-codex-primary-window-minutes": "10080",
        "x-codex-secondary-used-percent": "0",
        "x-codex-secondary-window-minutes": "0",
        "x-codex-credits-balance": "50652.1999150000",
    }
    lim = agents.codex_limits(json.dumps(h), datetime(2026, 11, 4, tzinfo=UTC))
    assert lim is not None
    assert [(w.name, w.usedPct) for w in lim.windows] == [("week", 49.0)]
    assert lim.credits == "50,652"


def test_usage_reasons():
    # (What a step did, rows.step_kind, is the processor's now: promptrows_test.go TestStepKind.)
    assert rows.usage_missing(640, 200, "completed", None, 900_000, None) is None
    assert rows.usage_missing(None, 200, None, None, 900_000, None) == "parseBuffer"
    assert rows.usage_missing(None, 200, None, None, 300_000, None) == "streamCut"
    assert rows.usage_missing(None, 101, "incomplete:interrupted", 3, None, None) == "interrupted"
    assert rows.usage_missing(None, 401, None, None, 0, None) == "failed"
    assert rows.usage_missing(None, 101, None, 2, None, "prewarm") is None  # a prewarm reads in → 0


def test_org_kind_needs_the_customer_org_ids(monkeypatch):
    monkeypatch.delenv("CUSTOMER_ORG_IDS", raising=False)
    assert agents.org_kind("org-a") == "unset"
    monkeypatch.setenv("CUSTOMER_ORG_IDS", "ORG-A, org-b")
    assert agents.org_kind("org-a") == "customer" and agents.org_kind("org-z") == "outside"


def test_recorded_bodies_show_commands_text_and_exit_codes(dataset, client_for):
    """Full interception: the bodies are recorded, so a step shows its commands, patch and
    first words, and the exit codes the next request carried back (handoff §11L.7)."""
    t = Codex(dataset, CHILD)
    script = (
        'await tools.exec_command({cmd: "npm test"});'
        ' await tools.apply_patch("*** Begin Patch\\n*** Update File: src/a.ts\\n+x\\n*** End Patch");'
    )
    t.post(
        "2026-11-04T16:00:00",
        "turn-x",
        {"input": [{"type": "message"}]},
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "custom_tool_call", "call_id": "call_1", "name": "exec", "input": script},
            }
        ],
    )
    t.post(
        "2026-11-04T16:00:10",
        "turn-x",
        {
            "input": [
                {"type": "reasoning"},
                {"type": "custom_tool_call", "call_id": "call_1"},
                {"type": "custom_tool_call_output", "call_id": "call_1", "output": '{"exit_code":1}'},
            ]
        },
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "message", "content": [{"type": "output_text", "text": "Fixed the flaky test."}]},
            }
        ],
    )
    client = client_for(dataset)
    prompts = [x for x in client.get(f"/api/v1/sessions/{CHILD}/prompts").json()["data"] if x["type"] == "prompt"]
    first, second = prompts[0]["steps"]
    assert first["commands"] == ["npm test"] and first["exitCodes"] == [1]
    assert first["patches"] == [{"file": "src/a.ts", "added": 1, "removed": 0}]
    assert second["text"] == "Fixed the flaky test." and second["does"] == "text"
    rows = client.get(f"/api/v1/sessions/{CHILD}/requests").json()["data"]
    assert [r["text"] for r in rows if r["kind"] == "model"] == [None, "Fixed the flaky test."]


def test_recorded_bodies_give_the_prompt_and_the_thread_title(dataset, client_for):
    """With bodies recorded, a prompt shows what it said, and a Codex thread its generated
    title (the answer to its title call); without, the label is untitled (handoff §11M 3.4)."""
    t = Codex(dataset, CHILD)
    t.post(
        "2026-11-04T16:00:00",
        "turn-x",
        {
            "input": [
                {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "fix the flaky test"}]}
            ]
        },
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "message", "content": [{"type": "output_text", "text": "On it."}]},
            }
        ],
    )
    t.post(
        "2026-11-04T16:00:20",
        "",
        {"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "title this"}]}]},
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "message", "content": [{"type": "output_text", "text": "Fix flaky timer test"}]},
            }
        ],
        source="thread_title",
    )
    client = client_for(dataset)
    prompt = next(x for x in client.get(f"/api/v1/sessions/{CHILD}/prompts").json()["data"] if x["type"] == "prompt")
    assert prompt["prompt"]["text"] == {
        "retained": True,
        "text": "fix the flaky test",
        "chars": 18,
        "pastedBytes": None,
        "images": None,
    }
    assert client.get(f"/api/v1/sessions/{CHILD}").json()["data"]["label"] == {
        "text": "Fix flaky timer test",
        "source": "title",
    }


def test_person_requests_and_sessions_scope_to_an_agent(dataset, client_for):
    """A linked person's AgentScope (handoff §11M 3.3) scopes the sessions table and All requests."""
    claude_code_account(dataset)
    codex_work(dataset)
    client = client_for(dataset)
    pid = client.get("/api/v1/people").json()["data"][0]["personId"]
    agents_of = lambda body: {x["agent"] for x in body["data"]}  # noqa: E731
    assert agents_of(client.get(f"/api/v1/people/{pid}/sessions?agent=codex").json()) == {"codex"}
    assert agents_of(client.get(f"/api/v1/people/{pid}/sessions?agent=claude-code").json()) == {"claude-code"}
    codex = client.get(f"/api/v1/people/{pid}/requests?agent=codex&pageSize=200").json()
    assert agents_of(codex) == {"codex"} and codex["hiddenCount"] == 2
    both = client.get(f"/api/v1/people/{pid}/requests?hidden=true&pageSize=200").json()
    assert {"claude-code", "codex"} <= agents_of(both)


def test_a_title_thread_names_the_thread_it_follows(dataset, client_for):
    """Codex titles a thread from a thread of its own, just after the first turn: the title
    goes to the same account's thread that started last before it (handoff §11M 3.4)."""
    t = Codex(dataset, CHILD)
    t.post("2026-11-04T16:00:00", "turn-x", {"input": []}, [])
    Codex(dataset, "01a1c1ec-3e49-71b3-b32b-55a748502bab").post(
        "2026-11-04T16:00:01",
        "",
        {"input": []},
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "message", "content": [{"type": "output_text", "text": "Fix flaky timer test"}]},
            }
        ],
        source="thread_title",
    )
    client = client_for(dataset)
    assert client.get(f"/api/v1/sessions/{CHILD}").json()["data"]["label"] == {
        "text": "Fix flaky timer test",
        "source": "title",
    }
