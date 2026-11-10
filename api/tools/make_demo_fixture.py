"""Build api/tests/fixtures/demo.ndjson.gz: one person's real Claude Code traffic,
reduced to the fields the API reads and sanitized, for the `demo` scenarios.

    uv run python tools/make_demo_fixture.py <data-dir-or-api.duckdb> <account_uuid>

Kept: model calls (the request headers the views read, the response request-id,
the complete row's identity, model and token fields) and analytics events (name,
client timestamp, session/account/device, and only the metadata keys the rollups
use; other events keep their name and time so counts stay true). Replaced with
stable fakes: account, org, device, session, request and client request ids.
Message ids (the thread chain) are faked too; tool lists keep built-in tools
only. Client IPs become documentation-range fakes (192.0.2.x / 2001:db8::x), one per
real IP, so the session page's network lane keeps its shape. Dropped: key
hashes, everything else. Real timestamps are kept.
Check the output with scripts/check-real-data.py before committing.
"""

import gzip
import hashlib
import json
import pathlib
import sys

from aintercept_api.person import key_hashes
from aintercept_api.store import Store

DEMO_ACCOUNT = "7d2e0000-0000-4000-8000-00000000a41c"
KEEP_MD = {
    # coverage, presence, friction, prompts
    "requestId",
    "clientRequestId",
    "permissionMode",
    "is_negative",
    "destructive_category",
    "prompt_index",
    "blurDurationMs",
    "likely_sleep",
    "clock_jump_ms",
    "cc_prompt_id",
    # header and summary (person.py)
    "subscription_type",
    "sandbox_enabled",
    "gzipSkipReason",
    "costUSD",
    "cache_creation_5m_input_tokens",
    "cache_creation_1h_input_tokens",
    "toolName",
    "isMcp",
    "preToolHookDurationMs",
    "bash_argv0",
    "destructive_target_scope",
    "operation",
    "prNumber",
    "hookName",
    # session page (session.py): step timing, tool joins, prompt shape, recaps
    "ttftMs",
    "durationMs",
    "toolUseID",
    "tool_use_id",
    "toolResultSizeBytes",
    "exit_code",
    "prompt_length",
    "pastedTextBytes",
    "hadRecap",
}
KEEP_ENV = {"platform", "terminal", "shell"}
KEEP_RESP_HEADERS = {
    "anthropic-ratelimit-unified-status",
    "anthropic-ratelimit-unified-7d-utilization",
    "anthropic-ratelimit-unified-overage-status",
}
KEEP_REQ_HEADERS = {
    "x-claude-code-session-id",
    "x-claude-code-prompt-id",
    "x-claude-code-request-class",
    "x-client-request-id",
    "x-claude-code-agent-id",
    "x-claude-code-agent-type",
    "user-agent",
}


def fake_uuid(kind: str, value: str) -> str:
    h = hashlib.sha256(f"aintercept-demo:{kind}:{value}".encode()).hexdigest()
    return f"{h[:8]}-{h[8:12]}-4{h[13:16]}-8{h[17:20]}-{h[20:32]}"


def main(src: str, account: str) -> None:
    p = pathlib.Path(src)
    store = Store(p.parent, db_path=p) if p.suffix == ".duckdb" else Store(p)
    if p.suffix != ".duckdb":
        store.ingest()
    ids: dict[tuple[str, str], str] = {}

    def fake(kind: str, value: str | None) -> str | None:
        if value is None:
            return None
        if kind == "account" and value == account:
            return DEMO_ACCOUNT
        key = (kind, value)
        if key not in ids:
            ids[key] = (
                fake_uuid(kind, value)
                if kind != "device"
                else hashlib.sha256(f"demo-device:{value}".encode()).hexdigest()
            )
            if kind in ("request",):
                ids[key] = "req_demo_" + hashlib.sha256(value.encode()).hexdigest()[:20]
            if kind == "tool":
                ids[key] = "toolu_demo_" + hashlib.sha256(value.encode()).hexdigest()[:20]
            if kind == "ip":  # keep the family (the IPv6 bypass is the story), not the address
                n = sum(1 for k in ids if k[0] == "ip")  # this one included
                v6 = ":" in value and not value.lower().startswith("::ffff:")
                ids[key] = f"2001:db8::{n:x}" if v6 else f"192.0.2.{n}"
        return ids[key]

    sids = [
        r[0]
        for r in store.query(
            "SELECT session_id FROM cc_calls WHERE account_uuid = ? "
            "UNION SELECT session_id FROM cc_events WHERE account_uuid = ?",
            [account, account],
        )
    ]
    # Every transaction made with the person's credentials: model calls, count_tokens,
    # analytics batches (as transactions), flag fetches and account calls.
    hashes = key_hashes(store, sids)
    tx = [
        r[0]
        for r in store.query(
            """SELECT DISTINCT correlation_id FROM events WHERE phase = 'request_headers'
               AND host = 'api.anthropic.com' AND NOT synthetic AND key_hash IN (SELECT unnest(?::VARCHAR[]))""",
            [hashes],
        )
    ]
    rows = store.query(
        """SELECT phase, raw FROM events WHERE correlation_id IN (SELECT unnest(?::VARCHAR[]))
             AND phase IN ('request_headers', 'response_headers', 'complete')
           ORDER BY ts, seq""",
        [tx],
    )
    bodies = store.query(
        """SELECT correlation_id, phase, bytes, first_ts FROM bodies
           WHERE correlation_id IN (SELECT unnest(?::VARCHAR[]))""",
        [tx],
    )
    orgs = {
        r[0]
        for r in store.query("SELECT DISTINCT organization_uuid FROM cc_events WHERE organization_uuid IS NOT NULL")
    }
    out = []
    cid_map: dict[str, str] = {}
    for phase, raw in rows:
        r = json.loads(raw)
        cid = cid_map.setdefault(r["correlation_id"], f"demo-{len(cid_map) + 1:06d}")
        base = {k: r.get(k) for k in ("seq", "timestamp_ns", "phase", "host", "method", "path", "status")}
        base["correlation_id"], base["observer_instance"] = cid, "observer-demo-0"
        for org in orgs:  # account-call paths carry the org id
            if base["path"] and org in base["path"]:
                base["path"] = base["path"].replace(org, fake("org", org) or "")
        if phase == "request_headers" and r.get("ip"):
            base["ip"] = fake("ip", r["ip"])
        if phase == "request_headers" and r.get("key_hash"):
            base["key_hash"] = "kh-demo-" + hashlib.sha256(r["key_hash"].encode()).hexdigest()[:8]
        if phase == "request_headers":
            h = {k: v for k, v in (r.get("headers") or {}).items() if k in KEEP_REQ_HEADERS}
            if "x-claude-code-session-id" in h:
                h["x-claude-code-session-id"] = fake("session", h["x-claude-code-session-id"])
            if "x-client-request-id" in h:
                h["x-client-request-id"] = fake("client_request", h["x-client-request-id"])
            if "x-claude-code-prompt-id" in h:
                h["x-claude-code-prompt-id"] = fake("prompt", h["x-claude-code-prompt-id"])
            if "x-claude-code-agent-id" in h:
                h["x-claude-code-agent-id"] = fake("agent", h["x-claude-code-agent-id"])
            base["headers"] = h
        elif phase == "response_headers":
            h = r.get("headers") or {}
            base["headers"] = {
                "request-id": fake("request", h.get("request-id")),
                **{k: v for k, v in h.items() if k in KEEP_RESP_HEADERS},
            }
        elif phase == "complete":
            for k in (
                "model",
                "input_tokens",
                "output_tokens",
                "cache_read_tokens",
                "cache_write_tokens",
                "stop_reason",
                # request settings and response service (the drawer's sections)
                "thread_type",
                "stream",
                "max_tokens",
                "effort",
                "thinking_type",
                "service_tier",
                "inference_geo",
                "speed",
            ):
                if r.get(k) is not None:
                    base[k] = r[k]
            for k in ("message_id", "previous_message_id"):  # the thread chain, faked like the other ids
                if r.get(k):
                    base[k] = fake("message", r[k])
            # Built-in tools only: MCP tool names say which services are connected.
            for k in ("tool_uses", "tools_available"):
                if isinstance(r.get(k), list):
                    base[k] = [t for t in r[k] if isinstance(t, dict) and t.get("kind") == "builtin"]
            base["account_uuid"] = fake("account", r.get("account_uuid"))
            base["device_id"] = fake("device", r.get("device_id"))
            base["session_id"] = fake("session", r.get("session_id"))
        out.append(base)
    for corr, phase, nbytes, first_ts in bodies:  # byte totals, as one chunk per direction
        out.append(
            {
                "correlation_id": cid_map.setdefault(corr, f"demo-{len(cid_map) + 1:06d}"),
                "seq": 90,
                "timestamp_ns": first_ts,
                "phase": phase,
                "bytes": nbytes,
                "observer_instance": "observer-demo-0",
            }
        )
    events = store.query(
        """SELECT ts, row_ts, name, session_id, account_uuid, device_id, md, correlation_id, env, head_sha,
                  organization_uuid
           FROM cc_events WHERE session_id IN (SELECT unnest(?::VARCHAR[])) ORDER BY ts""",
        [sids],
    )
    for i, (ts, row_ts, name, sid, acct, dev, md, corr, env, head, org) in enumerate(events):
        md = json.loads(md) if md else {}
        kept = {k: v for k, v in md.items() if k in KEEP_MD}
        if "requestId" in kept:
            kept["requestId"] = fake("request", kept["requestId"])
        if "clientRequestId" in kept:
            kept["clientRequestId"] = fake("client_request", kept["clientRequestId"])
        if "cc_prompt_id" in kept:
            kept["cc_prompt_id"] = fake("prompt", kept["cc_prompt_id"])
        for k in ("toolUseID", "tool_use_id"):
            if k in kept:
                kept[k] = fake("tool", kept[k])
        sec, rem = divmod(ts, 1_000_000_000)
        from datetime import UTC, datetime

        when = datetime.fromtimestamp(sec, UTC).strftime("%Y-%m-%dT%H:%M:%S") + f".{rem // 1_000_000:03d}Z"
        out.append(
            {
                "correlation_id": cid_map.setdefault(corr, f"demo-{len(cid_map) + 1:06d}"),
                "seq": 100 + i,
                "timestamp_ns": row_ts,
                "phase": "client_event",
                "host": "api.anthropic.com",
                "method": "POST",
                "path": "/api/event_logging/v2/batch",
                "observer_instance": "observer-demo-0",
                "client_event": {
                    "source": "claude_code",
                    "name": name,
                    "timestamp": when,
                    "session_id": fake("session", sid),
                    "account_uuid": fake("account", acct),
                    "device_id": fake("device", dev),
                    "organization_uuid": fake("org", org),
                    "data": {
                        "additional_metadata": kept,
                        "env": {k: v for k, v in (json.loads(env) if env else {}).items() if k in KEEP_ENV},
                        # commit SHAs could point at a real repository: distinct fakes keep "head changes" true
                        "head_sha": hashlib.sha256(f"demo-head:{head}".encode()).hexdigest()[:40] if head else None,
                    },
                },
            }
        )
    dst = pathlib.Path(__file__).resolve().parents[1] / "tests" / "fixtures" / "demo.ndjson.gz"
    with gzip.open(dst, "wt") as f:
        for r in sorted(out, key=lambda r: (r["timestamp_ns"] or 0, r["correlation_id"], r["seq"] or 0)):
            f.write(json.dumps(r, sort_keys=True, separators=(",", ":")) + "\n")
    print(f"wrote {dst} ({len(out)} rows, {dst.stat().st_size // 1024} KB) for {len(sids)} session(s)")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
