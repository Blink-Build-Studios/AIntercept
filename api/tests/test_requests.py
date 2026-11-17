"""/requests/{requestId}, /raw, /details and /events/{eventId} (handoff §5.2.10, §5.2.9), on the Postgres
store: the drawer reads v9's facts and rollups, and the call's raw member (docs/PIPELINE.md §6.2).

The datasets send Claude Code's analytics as it does; they stay in the raw files, so what only they said
(TTFT, cost, content lengths, retries, the cross-check, the events themselves) is null or empty."""

import json

from tests.builders import (
    INSTANCE,
    ClaudeCode,
    body_chunks,
    cid_of,
    complete_meta,
    headers_on,
    limits_on_last_call,
    sse,
    timing,
)
from tests.test_session import S1, H, error_on_query, prompt

TOOL_ID = "toolu_01ABCDEF"
SECRET = "Bearer " + "fake" * 10  # a credential-shaped string the API must redact


def two_steps(ds):
    """Prompt #1: a main call that asks for an Edit, then the call that carries its result."""
    cc = ClaudeCode(ds, S1)
    prompt(cc, f"{H}:00:10", "p1", 1)
    rid = cc.call(
        f"{H}:01:00", prompt="p1", cache_read=42_611, cache_write=0, output_tokens=1_204, stop_reason="tool_use"
    )
    cid = cid_of(ds, rid)
    timing(ds, cid, response_ms=1_850, end_ms=9_300)
    complete_meta(
        ds,
        cid,
        effort="high",
        thinking_type="enabled",
        max_tokens=32_000,
        stream=True,
        message_id="msg_01b2",
        previous_message_id="msg_01a7",
        thread_type="main",
        service_tier="standard",
        inference_geo="us",
        tool_uses=[{"name": "Edit", "tool": "Edit", "kind": "builtin", "count": 1}],
        tools_available=[
            {"name": "Edit", "tool": "Edit", "kind": "builtin"},
            {"name": "mcp__github__create_pr", "server": "github", "tool": "create_pr", "kind": "mcp"},
        ],
    )
    headers_on(ds, cid, "request_headers", authorization="<redacted>", **{"content-type": "application/json"})
    headers_on(ds, cid, "response_headers", **{"content-type": "text/event-stream"})
    limits_on_last_call(ds, util7d="0.41")
    cc.event(f"{H}:00:59", "tengu_api_query", cc_prompt_id="p1", permissionMode="bypassPermissions")
    cc.event(
        f"{H}:01:09",
        "tengu_api_success",
        cc_prompt_id="p1",
        requestId=rid,
        costUSD="0.21",
        ttftMs=1_912,
        durationMs=9_301,
        requestPrepareMs=41,
        attempt=1,
        streamEventCount=47,
        thinkingContentLength=380,
        textContentLength=212,
        messageCount=24,
        toolsCount=23,
        cache_creation_5m_input_tokens=0,
        cache_creation_1h_input_tokens=0,
        permissionMode="bypassPermissions",
    )
    cc.event(
        f"{H}:01:10",
        "tengu_tool_use_success",
        cc_prompt_id="p1",
        requestId=rid,
        toolName="Edit",
        toolUseID=TOOL_ID,
        durationMs=62,
        toolResultSizeBytes=300,
        preToolHookDurationMs=48,
    )
    rid2 = cc.call(f"{H}:01:20", prompt="p1", cache_read=43_900, output_tokens=200, stop_reason="end_turn")
    complete_meta(ds, cid_of(ds, rid2), message_id="msg_01c3", previous_message_id="msg_01b2")
    return cc, cid, cid_of(ds, rid2)


def failed_then_retried(ds):
    """Attempt 1 is reset before any response; Claude Code retries 1.2 s later and attempt 2 succeeds."""
    cc = ClaudeCode(ds, S1)
    prompt(cc, f"{H}:03:00", "p1", 1)
    bad = cc.call(f"{H}:03:08", prompt="p1", status=None, error="connection reset by upstream")
    cid = [r["correlation_id"] for r in ds.rows if r["phase"] == "request_headers"][-1]
    timing(ds, cid, response_ms=0, end_ms=600)
    cc.event(f"{H}:03:08", "tengu_api_retry", cc_prompt_id="p1", attempt=1, delayMs=1_200, error="ECONNRESET")
    ok = cc.call(f"{H}:03:10", prompt="p1")
    return cc, cid, cid_of(ds, ok), bad


def test_request_normal(dataset, pg_client_for, write_sample):
    _, cid, nxt = two_steps(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}")
    write_sample(client, r, "normal")
    d = r.json()["data"]
    assert (d["kind"], d["status"], d["durationMs"], d["sessionId"]) == ("normal", 200, 9300.0, S1)
    assert (d["promptIndex"], d["stepIndex"], d["stepCount"], d["requestClass"]) == (1, 1, 2, "main")
    assert d["prevRequestId"] is None and d["nextRequestId"] == nxt  # the chain: msg_01c3 follows msg_01b2
    k = d["keyStrip"]
    assert (k["model"], k["effort"], k["thinking"], k["stopReason"]) == ("claude-opus-5-5", "high", True, "tool_use")
    assert (k["ttftMs"], k["proxyTtftMs"], k["ttftSource"], k["toolCalls"]) == (None, 1850.0, "proxy", ["Edit"])
    assert k["cachedPct"] == 100.0 and k["cost"] == {"usd": None, "basis": "api-equivalent", "estimatedCalls": 0}
    assert d["crossCheck"] == {"matched": False, "costAgrees": None}
    assert d["notice"] is None and d["retry"] is None and d["contentRetained"] is False
    assert [x["label"] for x in d["received"]] == ["tool_use · Edit"]
    assert d["received"][0]["tone"] == "tool" and all(x["body"] is None for x in d["sent"] + d["received"])


def test_request_failed(dataset, pg_client_for, write_sample):
    _, cid, _, _ = failed_then_retried(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}")
    write_sample(client, r, "failed")
    d = r.json()["data"]
    assert (d["kind"], d["status"], d["durationMs"]) == ("failed", "ERR", 600.0)
    assert d["notice"]["title"] == "✕ connection reset by upstream"
    assert d["notice"]["body"] == "The request was sent but no response arrived: the connection failed after 0.6 s."
    assert d["retry"] is None  # Claude Code's tengu_api_retry isn't stored
    assert d["received"] == [
        {
            "label": "no response",
            "detail": "connection reset by upstream",
            "size": None,
            "unit": None,
            "tone": None,
            "body": None,
        }
    ]
    assert d["keyStrip"]["proxyTtftMs"] is None and d["keyStrip"]["cost"]["estimatedCalls"] == 0


def test_a_failed_rows_error_has_no_retry(dataset, pg_client_for):
    _, cid, _, _ = failed_then_retried(dataset)
    client = pg_client_for(dataset)
    (row,) = [x for x in client.get(f"/api/v1/sessions/{S1}/requests").json()["data"] if x["requestId"] == cid]
    assert row["retries"] is None and row["error"]["message"] == "connection reset by upstream"
    assert row["error"]["retries"] is None and row["error"]["retrySucceededAt"] is None


def retained(ds):
    """A call captured with full bodies: the new tool_result (with a token in it) and an SSE response."""
    cc, cid, _ = two_steps(ds)
    req = {
        "model": "claude-opus-5-5",
        "messages": [
            {"role": "user", "content": "Fix the refresh race."},
            {
                "role": "assistant",
                "content": [{"type": "tool_use", "id": "toolu_read", "name": "Read", "input": {"file_path": "a.ts"}}],
            },
            {
                "role": "user",
                "content": [
                    {
                        "type": "tool_result",
                        "tool_use_id": "toolu_read",
                        "content": f"41  const token = await fetchToken(s.id)\n42  // {SECRET}",
                    }
                ],
            },
        ],
    }
    body = json.dumps(req).encode()
    body_chunks(ds, cid, "request_body_chunk", [body[:40], body[40:]], start_ms=1, step_ms=1)
    stream = sse(
        ("message_start", {"type": "message_start", "message": {"id": "msg_01b2", "model": "claude-opus-5-5"}}),
        ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "thinking"}}),
        (
            "content_block_delta",
            {"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "Hmm."}},
        ),
        ("content_block_start", {"type": "content_block_start", "index": 1, "content_block": {"type": "text"}}),
        (
            "content_block_delta",
            {"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "I'll await "}},
        ),
        (
            "content_block_delta",
            {"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "the check."}},
        ),
        (
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 2,
                "content_block": {"type": "tool_use", "id": TOOL_ID, "name": "Edit"},
            },
        ),
        (
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 2,
                "delta": {"type": "input_json_delta", "partial_json": '{"file_path": "src/auth/session.ts",'},
            },
        ),
        (
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 2,
                "delta": {"type": "input_json_delta", "partial_json": ' "replace_all": false}'},
            },
        ),
        (
            "message_delta",
            {"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"output_tokens": 1204}},
        ),
        ("message_stop", {"type": "message_stop"}),
    )
    body_chunks(ds, cid, "response_body_chunk", stream, start_ms=1_870, step_ms=600)
    return cid


def test_request_retained(dataset, pg_client_for, write_sample):
    cid = retained(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}")
    write_sample(client, r, "retained")
    d = r.json()["data"]
    assert d["contentRetained"] is True and d["kind"] == "normal"
    (sent,) = d["sent"]
    assert sent["label"] == "tool_result · Read" and sent["detail"] == "2 lines"
    assert SECRET not in sent["body"] and "[redacted]" in sent["body"]
    assert [x["label"] for x in d["received"]] == ["thinking", "text", "tool_use · Edit"]
    assert d["received"][1]["body"] == "I'll await the check."
    assert json.loads(d["received"][2]["body"]) == {"file_path": "src/auth/session.ts", "replace_all": False}


def test_request_unknown_is_a_typed_404(dataset, pg_client_for):
    client = pg_client_for(dataset)
    for path in ("/api/v1/requests/nope", "/api/v1/requests/reported:nope", "/api/v1/requests/nope/raw"):
        r = client.get(path)
        assert r.status_code == 404 and r.json()["error"]["code"] == "http_404"


def test_request_error(dataset, pg_client_for, write_sample, monkeypatch):
    _, cid, _ = two_steps(dataset)
    client = pg_client_for(dataset)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"/api/v1/requests/{cid}"), "error", status=500)


# --- raw ----------------------------------------------------------------------------------------


def test_raw_normal(dataset, pg_client_for, write_sample):
    _, cid, _ = two_steps(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}/raw")
    write_sample(client, r, "normal")
    d = r.json()["data"]
    assert d["unavailable"] is None and d["requestLine"] == "POST /v1/messages?beta=true"
    assert d["responseLine"] == "200 OK" and d["keyHash"] == "kh-acc1"
    auth = next(h for h in d["requestHeaders"] if h["name"] == "authorization")
    assert auth == {"name": "authorization", "value": None, "redacted": True}
    assert d["requestBody"] is None and d["stream"] is None and d["contentRetained"] is False


def test_raw_retained(dataset, pg_client_for, write_sample):
    cid = retained(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}/raw")
    write_sample(client, r, "retained")
    d = r.json()["data"]
    assert SECRET not in d["requestBody"] and json.loads(d["requestBody"])["model"] == "claude-opus-5-5"
    stream = [(e["type"], e["count"]) for e in d["stream"]]
    assert stream == [
        ("message_start", None),
        ("content_block_start", None),
        ("content_block_delta", None),
        ("content_block_start", None),
        ("content_block_delta", 2),
        ("content_block_start", None),
        ("content_block_delta", 2),
        ("message_delta", None),
        ("message_stop", None),
    ]
    assert (
        d["stream"][0]["tOffsetMs"] == 1870.0
        and d["stream"][-2]["detail"] == "stop_reason tool_use · output_tokens 1204"
    )


def test_raw_unavailable(dataset, pg_client_for, write_sample):
    """A call whose request headers aren't among its raw rows: the drawer shows its row, and says why."""
    cid = dataset.transaction(f"{H}:06:00")
    dataset.rows[:] = [r for r in dataset.rows if not (r["correlation_id"] == cid and r["phase"] == "request_headers")]
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}/raw")
    write_sample(client, r, "unavailable")
    d = r.json()["data"]
    assert d["unavailable"].startswith("The raw files for this request can't be read here")
    assert d["requestBody"] is None and d["stream"] is None


def test_raw_failed_has_the_request_only(dataset, pg_client_for, write_sample):
    _, cid, _, _ = failed_then_retried(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}/raw")
    write_sample(client, r, "failed")
    d = r.json()["data"]
    assert d["unavailable"] == "The request was sent but no response arrived: connection reset by upstream after 0.6 s."
    assert d["requestHeaders"] and d["responseLine"] is None and d["responseHeaders"] == []


def test_raw_error(dataset, pg_client_for, write_sample, monkeypatch):
    _, cid, _ = two_steps(dataset)
    client = pg_client_for(dataset)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"/api/v1/requests/{cid}/raw"), "error", status=500)


# --- details ------------------------------------------------------------------------------------


def test_details_normal(dataset, pg_client_for, write_sample):
    _, cid, _ = two_steps(dataset)
    client = pg_client_for(dataset)
    r = client.get(f"/api/v1/requests/{cid}/details")
    write_sample(client, r, "normal")
    d = r.json()["data"]
    titles = [s["title"] for s in d["sections"]]
    assert titles == [
        "Tokens & cost",
        "Request settings",
        "Thread position",
        "What the model could use",
        "Response service",
        "Account limits at response",
        "Network & proxy",
        "Cross-check & posture",
    ]
    v = {s["title"]: {row["k"]: row["v"] for row in s["rows"]} for s in d["sections"]}
    assert v["Request settings"]["max_tokens"] == 32_000 and v["Request settings"]["stream"] is True
    assert v["Thread position"]["previous step"] == "msg_01a7" and v["Thread position"]["next step"] == "msg_01c3"
    assert v["What the model could use"]["MCP servers"] == 1
    assert v["Account limits at response"]["7-day utilization"] == 41.0
    # What only Claude Code's analytics said is null: the cost, its TTFT, the permission mode.
    assert v["Tokens & cost"]["costUSD (Claude Code)"] is None and v["Cross-check & posture"]["permissionMode"] is None
    assert (
        v["Cross-check & posture"]["ttft (Claude Code)"] is None
        and v["Cross-check & posture"]["ttft (proxy)"] == 1850.0
    )
    (tool,) = d["tool"]  # the observer's tally: its name
    assert (tool["toolUseId"], tool["name"], tool["execution"], tool["result"]) == (None, "Edit", [], [])
    phases = d["timing"]["proxy"]
    assert [p["label"] for p in phases] == ["accept + TLS", "upstream connect", "first byte", "streaming", "total"]
    assert phases[0]["durMs"] is None and phases[1]["startMs"] is None  # not recorded by the proxy: rows without a bar
    assert d["timing"]["client"] == [] and d["events"] == []
    fields = {f["key"]: f["value"] for f in d["fields"]}
    assert fields["effort"] == "high" and fields["speed"] is None
    assert not [k for k in fields if k.startswith("analytics.")]


def test_details_error(dataset, pg_client_for, write_sample, monkeypatch):
    _, cid, _ = two_steps(dataset)
    client = pg_client_for(dataset)
    error_on_query(client, monkeypatch)
    write_sample(client, client.get(f"/api/v1/requests/{cid}/details"), "error", status=500)


# --- one event ----------------------------------------------------------------------------------


def test_request_that_ended_in_a_disconnect(dataset, pg_client_for):
    """A client gone before any response: its raw rows end in a disconnect, which the
    drawer shows as the failure the request log shows (not a call with no end)."""
    cid = dataset.transaction(f"{H}:05:00")
    dataset.rows[:] = [r for r in dataset.rows if not (r["correlation_id"] == cid and r["phase"] != "request_headers")]
    dataset.add(
        {
            "correlation_id": cid,
            "seq": 1,
            "timestamp_ns": [r for r in dataset.rows if r["correlation_id"] == cid][0]["timestamp_ns"] + 400_000_000,
            "phase": "disconnect",
            "error": "client disconnected",
            "observer_instance": INSTANCE,
        }
    )
    d = pg_client_for(dataset).get(f"/api/v1/requests/{cid}").json()["data"]
    assert (d["kind"], d["status"], d["durationMs"]) == ("failed", "ERR", 400.0)


def test_request_whose_headers_are_out_of_reach_comes_from_its_row(dataset, pg_client_for):
    """Raw rows found, but not its request headers (an earlier file the lineage doesn't
    reach): the drawer builds it from its request row rather than answering 404."""
    cid = dataset.transaction(f"{H}:06:00")
    dataset.rows[:] = [r for r in dataset.rows if not (r["correlation_id"] == cid and r["phase"] == "request_headers")]
    r = pg_client_for(dataset).get(f"/api/v1/requests/{cid}")
    assert r.status_code == 200, r.text
