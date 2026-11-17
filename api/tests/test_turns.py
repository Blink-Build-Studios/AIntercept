"""The turn ledger (handoff §11R): /sessions/{id}/turns, /people/{id}/work, and the turn and work values the
session and person pages and the People page add, on the Postgres store (docs/PIPELINE.md §6.1, §6.3).

The datasets send Claude Code's analytics as it does; they stay in the raw files. A turn and its work come
from its model calls: the tools each asked for and, with recorded content, their commands, files and exit
codes. Without content a command has no program, a file no type, and nothing counts commits or PRs."""

import json

from tests.builders import ACCOUNT, ClaudeCode, Codex, body_chunks, cid_of
from tests.test_session import error_on_query

S = "22222222-2222-4222-8222-222222222222"
TURNS = f"/api/v1/sessions/{S}/turns"
WORK = f"/api/v1/people/{ACCOUNT}/work"
H = "2026-11-04T10"  # ended by the frozen 18:05
LIVE = "2026-11-04T18"
CODEX = "01a1bd9a-0000-7000-8000-0000000c0dex"
THREAD = "01a1bd9a-0000-7000-8000-00000thread1"


def prompt(cc: ClaudeCode, when: str, pid: str, index: int, chars: int = 212, **md) -> None:
    cc.event(when, "tengu_input_prompt", cc_prompt_id=pid, prompt_index=index, prompt_length=chars, **md)


def step(
    cc: ClaudeCode,
    when: str,
    pid: str,
    tool: str | None = None,
    *,
    ext: str | None = None,
    argv0: str | None = None,
    sub: str | None = None,
    exit: int | None = None,
    server: str | None = None,
    **kw,
) -> str:
    """One model call of prompt pid with Claude Code's report, and the tool it asked for with that tool's
    analytics (its tool event; a Bash command's event with its program and exit code)."""
    stop = "tool_use" if tool else "end_turn"
    rid = cc.call(when, prompt=pid, tool_uses=[tool] if tool else None, stop_reason=stop, cache_read=1_000, **kw)
    cc.event(when, "tengu_api_success", cc_prompt_id=pid, requestId=rid, costUSD=0.25)
    if tool is None:
        return rid
    tid = f"toolu_{rid[-9:]}"
    md: dict = {"cc_prompt_id": pid, "requestId": rid, "toolName": tool, "toolUseID": tid, "durationMs": 1200}
    if ext:
        md["fileExtension"] = ext
    if server:
        md |= {"isMcp": True, "mcpServerName": server}
    if argv0:
        md |= {"bash_argv0": argv0, **({"bash_subcommand": sub} if sub else {})}
    cc.event(when, "tengu_tool_use_success", **md)
    if tool == "Bash":
        bash: dict = {"cc_prompt_id": pid, "tool_use_id": tid, "bash_argv0": argv0, "exit_code": exit}
        if sub:
            bash["bash_subcommand"] = sub
        cc.event(when, "tengu_bash_tool_command_executed", destructive_category="none", **bash)
    return rid


def work_session(ds, sid: str = S, at: str = H) -> ClaudeCode:
    """Two turns. #1 reads two files, edits one (+31 −9), runs `npm test` twice (fails, then passes),
    commits, links PR #2 and replies. #2 is flagged negative, uses an MCP server, searches the web,
    spawns a test-writer subagent that writes a test, and is interrupted."""
    cc = ClaudeCode(ds, sid)
    prompt(cc, f"{at}:00:00", "p1", 1)
    step(cc, f"{at}:00:05", "p1", "Read", ext="ts")
    cc.event(f"{at}:00:05", "tengu_file_operation", cc_prompt_id="p1", operation="read", filePathHash="h-a")
    step(cc, f"{at}:00:10", "p1", "Read", ext="ts")
    cc.event(f"{at}:00:10", "tengu_file_operation", cc_prompt_id="p1", operation="read", filePathHash="h-b")
    step(cc, f"{at}:00:20", "p1", "Edit", ext="ts")
    cc.event(f"{at}:00:20", "tengu_file_operation", cc_prompt_id="p1", operation="edit", filePathHash="h-a")
    cc.event(f"{at}:00:21", "tengu_file_changed", cc_prompt_id="p1", lines_added=31, lines_removed=9)
    step(cc, f"{at}:00:30", "p1", "Bash", argv0="npm", sub="test", exit=1)
    step(cc, f"{at}:00:40", "p1", "Bash", argv0="npm", sub="test", exit=0)
    cc.event(f"{at}:00:45", "tengu_git_operation", cc_prompt_id="p1", operation="commit", head="a41f0000")
    cc.event(f"{at}:00:46", "tengu_session_linked_to_pr", cc_prompt_id="p1", prNumber=2, head="9c020000")
    step(cc, f"{at}:00:50", "p1")
    cc.event(f"{at}:00:55", "tengu_turn_end", cc_prompt_id="p1")
    prompt(cc, f"{at}:05:00", "p2", 2, chars=40, is_negative=True)
    step(cc, f"{at}:05:10", "p2", "mcp__linear-server__get_issue", server="linear-server")
    step(cc, f"{at}:05:20", "p2", "WebSearch")
    step(cc, f"{at}:05:30", "p2", "Agent")
    step(
        cc,
        f"{at}:05:40",
        "p2",
        "Write",
        ext="ts",
        request_class="subagent",
        agent_type="test-writer",
        agent_id="agent-1",
    )
    cc.event(f"{at}:05:45", "tengu_cancel", cc_prompt_id="p2")
    cc.event(f"{at}:05:46", "tengu_turn_end", cc_prompt_id="p2")
    return cc


def codex_thread(ds) -> Codex:
    """A Codex thread: one turn that runs commands (one fails), patches a file and spawns a subagent thread."""
    root = Codex(ds, CODEX)
    root.turn(f"{H}:20:00", "turn-1", tool_uses=[{"name": "exec_command", "tool": "exec_command", "kind": "builtin"}])
    root.turn(f"{H}:20:10", "turn-1", tool_uses=[{"name": "apply_patch", "tool": "apply_patch", "kind": "builtin"}])
    for i, code in enumerate([1, 0]):
        root.event(f"{H}:20:0{i + 1}", "codex_command_execution_event", "turn-1", exit_code=code)
    root.event(f"{H}:20:11", "codex_file_change_event", "turn-1", tool_name="apply_patch")
    lines = {"accepted_added_lines": 12, "accepted_deleted_lines": 3}
    root.event(f"{H}:20:12", "codex_accepted_line_fingerprints", "turn-1", **lines)
    root.turn_event(f"{H}:20:30", "turn-1", status="completed", shell_command_count=2, file_change_count=1)
    sub = {"parent_thread_id": CODEX, "parent_turn_id": "turn-1", "subagent_kind": "thread_spawn"}
    Codex(ds, THREAD, lineage={**sub, "agent_name": "/root/edge_tests", "thread_source": "subagent"}).turn(
        f"{H}:20:20", "turn-c1"
    )
    return root


# --- /sessions/{id}/turns -------------------------------------------------------------------------------


def test_turns_empty(dataset, pg_client_for, write_sample):
    """A range with no turn in it."""
    work_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(TURNS, params={"from": "2026-11-04T10:30:00Z", "to": "2026-11-04T10:40:00Z"})
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0


def test_turns_one(dataset, pg_client_for, write_sample):
    work_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(TURNS, params={"to": "2026-11-04T10:01:00Z"})
    write_sample(client, r, "one")
    (t,) = r.json()["data"]
    assert (t["n"], t["steps"], t["asked"]["chars"]) == (1, 6, None)
    assert [g["kind"] for g in t["groups"]] == ["read", "edit", "run", "reply"]
    assert [len(g["steps"]) for g in t["groups"]] == [2, 1, 2, 1]
    assert t["groups"][2]["exits"] == [] and t["groups"][2]["program"] is None  # without content
    assert "costUsd" not in t and t["live"] is False
    # From its first step to its last step's end.
    assert t["startedAt"] == "2026-11-04T10:00:05Z" and t["endedAt"] == "2026-11-04T10:00:50.002000Z"
    assert t["durationMs"] == 45002.0


def test_turns_many(dataset, pg_client_for, write_sample):
    work_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(TURNS)
    write_sample(client, r, "many")
    one, two = r.json()["data"]
    assert two["friction"] == {"interrupted": False, "failedCalls": 0}
    assert [g["kind"] for g in two["groups"]] == ["mcp", "web", "spawn"]
    # paged by turn: the page that holds #2
    r = client.get(TURNS, params={"pageSize": 1, "turn": 2})
    assert r.json()["meta"]["page"] == 2 and r.json()["data"][0]["n"] == 2


def test_turns_live(dataset, pg_client_for, write_sample):
    """The last turn is still running at the frozen 18:05: its step is streaming."""
    cc = ClaudeCode(dataset, S)
    prompt(cc, f"{LIVE}:00:00", "p1", 1)
    step(cc, f"{LIVE}:00:10", "p1", "Read", ext="py")
    step(cc, f"{LIVE}:00:20", "p1")
    prompt(cc, f"{LIVE}:04:00", "p2", 2)
    cc.call(f"{LIVE}:04:30", prompt="p2", cache_read=1_000, complete=False)
    client = pg_client_for(dataset)
    r = client.get(TURNS)
    write_sample(client, r, "live")
    one, two = r.json()["data"]
    assert one["live"] is False and [g["kind"] for g in one["groups"]] == ["read", "reply"]
    assert two["live"] is True and two["endedAt"] is None and two["groups"][-1]["kind"] == "think"


def test_turns_subagents(dataset, pg_client_for, write_sample):
    work_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(TURNS, params={"from": "2026-11-04T10:05:00Z"})
    write_sample(client, r, "subagents")
    (t,) = r.json()["data"]
    (sub,) = t["subagents"]
    assert (sub["agentId"], sub["name"], sub["steps"], sub["sessionId"]) == ("agent-1", "test-writer", 1, None)
    assert [g["kind"] for g in sub["groups"]] == ["edit"]
    assert all(s["kind"] == "model" for g in t["groups"] for s in g["steps"])


def test_turns_codex(dataset, pg_client_for, write_sample):
    codex_thread(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/sessions/{CODEX}/turns")
    write_sample(client, r, "codex")
    (t,) = r.json()["data"]
    assert "costUsd" not in t and [g["kind"] for g in t["groups"]] == ["run", "edit"]
    (sub,) = t["subagents"]
    assert (sub["sessionId"], sub["name"], sub["groups"]) == (THREAD, "/root/edge_tests", [])


def test_turns_with_content(dataset, pg_client_for, write_sample):
    """Recorded bodies add the commands, the patched paths and the reply's text."""
    t = Codex(dataset, CODEX)
    script = (
        'await tools.exec_command({cmd: "npm test"});'
        ' await tools.apply_patch("*** Begin Patch\\n*** Update File: src/a.ts\\n+x\\n*** End Patch");'
    )
    t.post(
        f"{H}:30:00",
        "turn-x",
        {"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "fix the test"}]}]},
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "custom_tool_call", "call_id": "call_1", "name": "exec", "input": script},
            }
        ],
        tool_uses=["exec", "exec_command", "apply_patch"],
    )
    t.post(
        f"{H}:30:10",
        "turn-x",
        {
            "input": [
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
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/sessions/{CODEX}/turns")
    write_sample(client, r, "with_content")
    (turn,) = r.json()["data"]
    assert turn["asked"]["text"] == "fix the test"
    first, last = turn["groups"]
    assert first["kind"] == "run" and first["paths"] == ["src/a.ts"] and first["exits"] == [1]
    assert last["kind"] == "reply" and last["text"] == "Fixed the flaky test."


def test_turns_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(TURNS), "error", status=500)


# --- /people/{id}/work ----------------------------------------------------------------------------------


def test_work_empty(dataset, pg_client_for, write_sample):
    client = pg_client_for(dataset)
    r = client.get(WORK)
    write_sample(client, r, "empty")
    assert r.json()["data"] == [] and r.json()["meta"]["total"] == 0


def test_work_one(dataset, pg_client_for, write_sample):
    work_session(dataset)
    client = pg_client_for(dataset)
    r = client.get(WORK)
    write_sample(client, r, "one")
    (g,) = r.json()["data"]
    assert g["repo"] is None and g["sessions"] == 1 and g["agents"] == ["claude-code"]
    (row,) = g["rows"]
    assert row["sessionId"] == S and row["turns"] == 2


def test_work_many(dataset, pg_client_for, write_sample):
    work_session(dataset)
    work_session(dataset, "33333333-3333-4333-8333-333333333333", "2026-11-04T12")
    client = pg_client_for(dataset)
    r = client.get(WORK)
    write_sample(client, r, "many")
    (g,) = r.json()["data"]
    assert g["sessions"] == 2 and [x["sessionId"][0] for x in g["rows"]] == ["3", "2"]  # newest first


def test_work_codex(dataset, pg_client_for, write_sample):
    """Codex threads group under their workspace."""
    codex_thread(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/people/{Codex(dataset, CODEX).account}/work")
    write_sample(client, r, "codex")
    (g,) = r.json()["data"]
    assert g["repo"] == "pomodoro" and g["agents"] == ["codex"] and g["sessions"] == 2


def test_work_error(dataset, pg_client_for, write_sample, monkeypatch):
    client = pg_client_for(dataset, raise_server_exceptions=False)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(WORK), "error", status=500)


# --- the turn and work values the other pages add ----------------------------------------------------


def test_session_header_and_outcome(dataset, pg_client_for):
    work_session(dataset)
    h = pg_client_for(dataset).get(f"/api/v1/sessions/{S}").json()["data"]
    assert (h["turns"], h["turnsInterrupted"], h["activeMs"]) == (2, 0, 75004.0) and "costUsd" not in h
    w = h["work"]
    assert (w["reads"], w["readFiles"], w["readTypes"]) == (2, None, {})
    assert (w["edits"], w["editedFiles"], w["editTypes"], w["linesAdded"], w["linesRemoved"]) == (
        2,
        None,
        {},
        None,
        None,
    )
    assert w["commands"] == [{"program": None, "runs": 2, "failed": 0, "lastExit": None}]
    assert w["web"] == {"searches": 1, "fetches": 0} and w["otherTools"] == 0 and len(w["mcp"]) == 1
    assert w["subagents"] == [{"name": None, "count": 1}] and (w["commits"], w["prs"]) == (0, [])
    o = h["outcome"]
    assert (o["commits"], o["headFrom"], o["headTo"], o["editedFiles"], o["prs"], o["lastTest"]) == (
        0,
        None,
        None,
        None,
        [],
        None,
    )


def test_turn_work_is_its_steps_tool_calls(dataset, pg_client_for):
    """A turn's work line comes from its steps' tool calls (its subagent's included); its groups' from theirs."""
    work_session(dataset)
    one, two = pg_client_for(dataset).get(TURNS).json()["data"]
    w = one["work"]
    assert (w["reads"], w["readFiles"], w["edits"], w["editedFiles"], w["linesAdded"]) == (2, None, 1, None, None)
    assert w["commands"] == [{"program": None, "runs": 2, "failed": 0, "lastExit": None}]
    read, edit, run, reply = one["groups"]
    assert (read["work"]["reads"], read["work"]["readTypes"]) == (2, {})
    assert edit["work"]["edits"] == 1 and run["work"]["commands"][0]["runs"] == 2
    assert [s["step"] for g in one["groups"] for s in g["steps"]] == [1, 2, 3, 4, 5, 6]
    assert reply["text"] is None  # no content recorded
    assert two["work"]["edits"] == 1  # the subagent's Write


def test_a_turn_is_interrupted_when_it_stops_on_one_or_the_next_says_so(dataset, pg_client_for):
    """Interrupted: a step stopped on an interrupt (#1), or the next turn's request carries the harness's note
    that the turn before it was interrupted (#2, from #3's recorded request)."""
    cc = ClaudeCode(dataset, S)
    cc.call(f"{H}:00:05", prompt="p1", stop_reason="interrupted", cache_read=1_000)
    cc.call(f"{H}:01:00", prompt="p2", stop_reason="end_turn", cache_read=1_000)
    rid = cc.call(f"{H}:02:00", prompt="p3", stop_reason="end_turn", cache_read=1_000)
    body = {
        "messages": [
            {"role": "user", "content": "fix it"},
            {"role": "assistant", "content": [{"type": "text", "text": "On it."}]},
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "[Request interrupted by user]"},
                    {"type": "text", "text": "try the other test"},
                ],
            },
        ]
    }
    cid = cid_of(dataset, rid)
    body_chunks(dataset, cid, "request_body_chunk", [json.dumps(body).encode()], start_ms=1, step_ms=1)
    client = pg_client_for(dataset)
    turns = client.get(TURNS).json()["data"]
    assert [(t["n"], t["friction"]["interrupted"]) for t in turns] == [(1, True), (2, True), (3, False)]
    assert turns[2]["asked"]["text"] == "try the other test"
    assert client.get(f"/api/v1/sessions/{S}").json()["data"]["turnsInterrupted"] == 2
    prompts = [i["prompt"] for i in client.get(f"/api/v1/sessions/{S}/prompts").json()["data"]]
    assert [p["friction"]["interrupted"] for p in prompts] == [True, True, False]


def test_timeline_turns_and_work_lanes(dataset, pg_client_for):
    work_session(dataset)
    d = pg_client_for(dataset).get(f"/api/v1/sessions/{S}/timeline", params={"bin": 60}).json()["data"]
    assert [(b["n"], b["startedAt"], b["endedAt"]) for b in d["turns"]] == [
        (1, "2026-11-04T10:00:05Z", "2026-11-04T10:00:50.002000Z"),
        (2, "2026-11-04T10:05:10Z", "2026-11-04T10:05:40.002000Z"),
    ]
    assert sum(b["edits"] for b in d["work"]) == 2 and sum(b["other"] for b in d["work"]) == 7
    first = d["work"][0]
    assert (first["t"], first["steps"], first["edits"], first["other"]) == ("2026-11-04T10:00:00Z", 6, 1, 4)
    assert first["work"]["commands"] == [{"program": None, "runs": 2, "failed": 0, "lastExit": None}]


def test_person_summary_work(dataset, pg_client_for):
    work_session(dataset)
    w = pg_client_for(dataset).get(f"/api/v1/people/{ACCOUNT}/summary").json()["data"]["work"]
    assert (w["turns"], w["activeMs"], w["repos"]) == (2, 75004.0, 0)
    assert (w["work"]["edits"], w["work"]["commits"]) == (2, 0)
    assert w["outcome"]["lastTest"] is None and w["outcome"]["headFrom"] is None


def test_person_timeline_work_lane(dataset, pg_client_for):
    work_session(dataset)
    d = pg_client_for(dataset).get(f"/api/v1/people/{ACCOUNT}/timeline").json()["data"]
    assert sum(b["edits"] for b in d["work"]) == 2 and sum(b["other"] for b in d["work"]) == 7


def test_people_turns_and_working_on(dataset, pg_client_for):
    """The People page's Turns, and Working on: the live session's newest turn."""
    cc = ClaudeCode(dataset, S)
    prompt(cc, f"{LIVE}:00:00", "p1", 1)
    step(cc, f"{LIVE}:00:10", "p1", "Read", ext="py")
    prompt(cc, f"{LIVE}:04:00", "p2", 2)
    step(cc, f"{LIVE}:04:10", "p2")
    client = pg_client_for(dataset)
    (row,) = client.get("/api/v1/people").json()["data"]
    assert row["turns"] == 2
    assert row["workingOn"] == {"live": True, "sessionId": S, "repo": None, "turn": 2, "repos": []}
    assert client.get("/api/v1/overview").json()["data"]["turns"] == 2


def test_working_on_lists_the_repos_when_nothing_is_live(dataset, pg_client_for):
    codex_thread(dataset)
    (row,) = pg_client_for(dataset).get("/api/v1/people").json()["data"]
    assert row["workingOn"] == {"live": False, "sessionId": None, "repo": None, "turn": None, "repos": ["pomodoro"]}
