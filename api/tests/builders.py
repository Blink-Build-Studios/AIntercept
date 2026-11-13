"""Build small telemetry datasets shaped exactly like the observer's rows, in the
same partition layout, so endpoint tests run the real queries."""

import base64
import gzip
import itertools
import json
import pathlib
from datetime import UTC, datetime

INSTANCE = "observer-test-0"


def ns(when: str) -> int:
    """'2026-11-03T14:20:00' (UTC) -> epoch nanoseconds."""
    return int(datetime.fromisoformat(when).replace(tzinfo=UTC).timestamp() * 1_000_000_000)


class Dataset:
    def __init__(self, root: pathlib.Path):
        self.root = root
        self.rows: list[dict] = []
        self._ids = itertools.count(1)

    def add(self, row: dict) -> dict:
        self.rows.append(row)
        return row

    def transaction(
        self,
        when: str,
        *,
        host="api.anthropic.com",
        method="POST",
        path="/v1/messages",
        status=200,
        synthetic=False,
        headers: dict | None = None,
        key_hash: str | None = None,
    ) -> str:
        """The request_headers / response_headers / complete rows of one transaction."""
        cid = f"{INSTANCE}-{next(self._ids)}"
        t = ns(when)
        base = {"correlation_id": cid, "host": host, "method": method, "path": path, "observer_instance": INSTANCE}
        self.add(
            {
                **base,
                "seq": 0,
                "timestamp_ns": t,
                "phase": "request_headers",
                "direction": "inbound",
                "synthetic": synthetic,
                "headers": headers or {},
                **({"key_hash": key_hash} if key_hash else {}),
            }
        )
        self.add(
            {
                **base,
                "seq": 1,
                "timestamp_ns": t + 1_000_000,
                "phase": "response_headers",
                "direction": "outbound",
                "status": status,
                "synthetic": synthetic,
            }
        )
        self.add(
            {
                **base,
                "seq": 2,
                "timestamp_ns": t + 2_000_000,
                "phase": "complete",
                "status": status,
                "synthetic": synthetic,
                "end_of_stream": True,
            }
        )
        return cid

    def write(self) -> pathlib.Path:
        """Write rows into one file per hour partition."""
        by_hour: dict[tuple[str, str], list[dict]] = {}
        for r in self.rows:
            d = datetime.fromtimestamp(r["timestamp_ns"] / 1e9, UTC)
            by_hour.setdefault((d.strftime("%Y-%m-%d"), d.strftime("%H")), []).append(r)
        for (day, hour), rows in by_hour.items():
            name = f"{rows[0]['timestamp_ns']}-000001.ndjson.gz"  # stable as rows are added
            rows = sorted(rows, key=lambda r: r["timestamp_ns"])  # the order the observer emits them
            part = self.root / f"dt={day}" / f"hour={hour}" / f"instance={INSTANCE}"
            part.mkdir(parents=True, exist_ok=True)
            with gzip.open(part / name, "wt") as f:
                for r in rows:
                    f.write(json.dumps(r) + "\n")
        return self.root


ACCOUNT = "00000000-0000-4000-8000-00000000acc1"
ORG = "00000000-0000-4000-8000-0000000000a9"
DEVICE = "d" * 64


class ClaudeCode:
    """Claude Code traffic in the observer's row shapes: model calls (request /
    response headers + complete) and analytics events (client_event rows)."""

    def __init__(self, ds: Dataset, session: str, *, account: str | None = ACCOUNT, device: str | None = DEVICE):
        self.ds, self.session, self.account, self.device = ds, session, account, device
        self.key_hash = f"kh-{(account or 'anon')[-4:]}"
        self._n = itertools.count(1)
        self._seq = itertools.count(1)  # events keep their order within a batch

    def call(
        self,
        when: str,
        *,
        prompt: str | None = "p1",
        request_class="main",
        parsed=True,
        output_tokens=100,
        cache_read: int | None = None,
        cache_write: int | None = None,
        stop_reason: str | None = None,
        agent_type: str | None = None,
        ip: str | None = None,
        complete=True,
        status: int | None = 200,
        error: str | None = None,
        content_encoding: str | None = None,
        bytes_down: int | None = None,
    ) -> str:
        """One model call; returns its request id. `parsed=False` mimics pre-parser rows (no identity);
        `complete=False` leaves it streaming (no complete row yet). `status=None` is a transport failure:
        no response headers, and the complete row carries only the proxy's `error`. `bytes_down` adds a
        response body chunk of that size."""
        n = next(self._n)
        rid = f"req_{self.session[:8]}_{n:04d}"
        headers = {
            "user-agent": "claude-cli/2.1.308 (external, cli)",
            "x-claude-code-session-id": self.session,
            "x-claude-code-request-class": request_class,
            "x-client-request-id": f"crid-{self.session[:8]}-{n:04d}",
        }
        if prompt:
            headers["x-claude-code-prompt-id"] = prompt
        if agent_type:
            headers["x-claude-code-agent-type"] = agent_type
        cid = self.ds.transaction(
            when, path="/v1/messages?beta=true", headers=headers, key_hash=self.key_hash, status=status
        )
        for r in self.ds.rows:
            if r["correlation_id"] != cid:
                continue
            if r["phase"] == "request_headers" and ip:
                r["ip"] = ip
            if r["phase"] == "response_headers":
                r["headers"] = {"request-id": rid}
                if content_encoding:
                    r["headers"]["content-encoding"] = content_encoding
            if r["phase"] == "complete":
                if status is None:
                    del r["status"]
                if error:
                    r["error"] = error
                r.update(
                    {
                        "model": "claude-opus-5-5",
                        "output_tokens": output_tokens,
                        "input_tokens": 3,
                        **({"cache_read_tokens": cache_read} if cache_read is not None else {}),
                        **({"cache_write_tokens": cache_write} if cache_write is not None else {}),
                        **({"stop_reason": stop_reason} if stop_reason else {}),
                    }
                )
                if parsed:
                    r.update({"account_uuid": self.account, "device_id": self.device, "session_id": self.session})
        if not complete:
            self.ds.rows[:] = [r for r in self.ds.rows if not (r["correlation_id"] == cid and r["phase"] == "complete")]
        if status is None:
            self.ds.rows[:] = [
                r for r in self.ds.rows if not (r["correlation_id"] == cid and r["phase"] == "response_headers")
            ]
        if bytes_down is not None:
            self.ds.add(
                {
                    "correlation_id": cid,
                    "seq": 3,
                    "timestamp_ns": ns(when) + 1_500_000,
                    "phase": "response_body_chunk",
                    "host": "api.anthropic.com",
                    "path": "/v1/messages?beta=true",
                    "observer_instance": INSTANCE,
                    "bytes": bytes_down,
                }
            )
        return rid

    def event(
        self,
        when: str,
        name: str,
        *,
        env: dict | None = None,
        head: str | None = None,
        batch: str | None = None,
        arrive: str | None = None,
        **md,
    ) -> None:
        """One analytics event, in a batch transaction (one batch per timestamp, or `batch`). A
        batch's rows arrive together (the observer emits them from one request): `arrive` is the
        batch's own time when its events carry different ones."""
        batch = batch or f"batch-{self.session[:8]}-{when}"
        seen = self.ds.__dict__.setdefault("_batches", set())  # one batch request, whichever session's builder
        if batch not in seen:
            seen.add(batch)
            self.ds.add(
                {
                    "correlation_id": batch,
                    "seq": 0,
                    "timestamp_ns": ns(arrive or when) + 29_000_000_000,
                    "phase": "request_headers",
                    "host": "api.anthropic.com",
                    "method": "POST",
                    "path": "/api/event_logging/v2/batch",
                    "observer_instance": INSTANCE,
                    "synthetic": False,
                    "headers": {"user-agent": "claude-code/2.1.308"},
                    "key_hash": self.key_hash,
                }
            )
        data: dict = {"additional_metadata": md}
        if env:
            data["env"] = env
        if head:
            data["head_sha"] = head
        self.ds.add(
            {
                "correlation_id": batch,
                "seq": next(self._seq),
                "timestamp_ns": ns(arrive or when) + 30_000_000_000,  # batches arrive after the client's own timestamp
                "phase": "client_event",
                "host": "api.anthropic.com",
                "method": "POST",
                "path": "/api/event_logging/v2/batch",
                "client_event": {
                    "source": "claude_code",
                    "type": "ClaudeCodeInternalEvent",
                    "name": name,
                    "timestamp": when + "Z",
                    "session_id": self.session,
                    "account_uuid": self.account,
                    "device_id": self.device,
                    "organization_uuid": ORG,
                    "data": data,
                },
            }
        )

    def reported(self, when: str, request_id: str, mode="bypassPermissions", **md) -> None:
        """Claude Code's own report of a model call (the coverage cross-check); `md` adds to its tengu_api_success."""
        self.event(when, "tengu_api_query", permissionMode=mode)
        self.event(
            when,
            "tengu_api_success",
            requestId=request_id,
            clientRequestId="crid-unmatched",
            permissionMode=mode,
            **md,
        )


CODEX_ACCOUNT = "c0de0000-0000-4000-8000-00000000c0de"
CODEX_INSTALL = "1a5e0000-0000-4000-8000-0000000001a5"


class Codex:
    """Codex CLI traffic in the observer's row shapes: response.create turns on a WebSocket
    (complete rows with client metadata) and Codex's own analytics events."""

    def __init__(
        self,
        ds: Dataset,
        thread: str,
        *,
        account: str = CODEX_ACCOUNT,
        email: str | None = None,
        originator: str = "codex_exec",
        lineage: dict | None = None,
    ):
        self.ds, self.thread, self.account, self.email, self.originator = ds, thread, account, email, originator
        self.lineage = lineage or {}
        self.key_hash = f"kh-{account[-4:]}"
        # One WebSocket connection per thread, as the observer names every ext_proc stream: the
        # root, its subagent, its review and its fork share the thread id's first 8 characters.
        self.conn = f"{INSTANCE}-ws-{thread[:8]}-{thread[-6:]}"
        self._turn = itertools.count(1)
        self._seq = itertools.count(1)

    def turn(
        self,
        when: str,
        turn_id: str | None,
        *,
        kind: str = "turn",
        source: str = "user",
        used_5h: str = "41",
        used_week: str = "83",
        output_tokens: int | None = 50,
        tool_uses: list[dict] | None = None,
    ) -> str:
        """One model turn on the thread's connection; returns its call id. `kind="prewarm"` and
        `source="thread_title"` are the calls Codex hides."""
        n = next(self._turn)
        md = {
            "thread_id": self.thread,
            "session_id": self.thread,
            "turn_id": turn_id or "",
            "request_kind": kind,
            "thread_source": source,
            "history_initialization": "new",
            "installation_id": CODEX_INSTALL,
            "agent_name": "/root",
            "sandbox_mode": "danger-full-access",
            "turn_trigger": "exec",
            "workspaces": {"/work/pomodoro": {"has_changes": False, "latest_git_commit_hash": "1aa2f42879503b17"}},
            **self.lineage,
        }
        version = "0.167.0" if self.originator == "codex-tui" else "0.164.0"
        self.ds.add(
            {
                "correlation_id": self.conn,
                "seq": 100 + n,
                "timestamp_ns": ns(when),
                "phase": "complete",
                "host": "chatgpt.com",
                "method": "GET",
                "path": "/backend-api/codex/responses",
                "status": 101,
                "observer_instance": INSTANCE,
                "key_hash": self.key_hash,
                "transport": "websocket",
                "ws_turn": n,
                "model": "gpt-6.1-sol" if source != "thread_title" else "gpt-5.6-luna",
                "effort": "medium",
                **(
                    {
                        "stop_reason": "completed",
                        "input_tokens": 2 if kind == "prewarm" else 1200,
                        "output_tokens": 0 if kind == "prewarm" else output_tokens,
                        "cache_read_tokens": 0 if kind == "prewarm" else 900,
                    }
                    if output_tokens is not None
                    else {}
                ),
                **({"tool_uses": tool_uses} if tool_uses else {}),
                **({"prewarm": True} if kind == "prewarm" else {}),
                "client": {
                    "originator": self.originator,
                    "user-agent": f"{self.originator}/{version} (Debian 12.0.0; aarch64) unknown",
                    "version": version,
                    "chatgpt-account-id": self.account,
                },
                "provider": {
                    "x-codex-plan-type": "plus",
                    "x-codex-primary-used-percent": used_5h,
                    "x-codex-primary-window-minutes": "300",
                    "x-codex-primary-reset-at": "1794199931",
                    "x-codex-secondary-used-percent": used_week,
                    "x-codex-secondary-window-minutes": "10080",
                    "x-codex-secondary-reset-at": "1794237951",
                },
                "account_uuid": self.account,
                "device_id": CODEX_INSTALL,
                "session_id": self.thread,
                "auth": {"account_id": self.account, "name": "Ada Lovelace", "plan": "plus"}
                | ({"email": self.email} if self.email else {}),
                "client_metadata": md,
            }
        )
        return f"{self.conn}#{n}"

    def post(self, when: str, turn_id: str, request: dict, response: list[dict], *, source: str = "user") -> str:
        """One model call over HTTP whose bodies were recorded (full interception): the request
        JSON and the response's SSE events, as base64 body chunks. Returns its correlation id."""
        cid = f"{INSTANCE}-cxp-{self.thread[:8]}-{next(self._seq)}"
        t = ns(when)
        base = {
            "correlation_id": cid,
            "host": "chatgpt.com",
            "method": "POST",
            "observer_instance": INSTANCE,
            "path": "/backend-api/codex/responses",
        }
        req = json.dumps(request).encode()
        resp = "".join(f"data: {json.dumps(e)}\n\n" for e in response).encode()
        version = "0.164.0"
        rows = [
            {"seq": 0, "timestamp_ns": t, "phase": "request_headers", "key_hash": self.key_hash, "headers": {}},
            {
                "seq": 1,
                "timestamp_ns": t + 1_000,
                "phase": "request_body_chunk",
                "bytes": len(req),
                "body_b64": base64.b64encode(req).decode(),
            },
            {"seq": 2, "timestamp_ns": t + 500_000_000, "phase": "response_headers", "status": 200, "headers": {}},
            {
                "seq": 3,
                "timestamp_ns": t + 900_000_000,
                "phase": "response_body_chunk",
                "bytes": len(resp),
                "body_b64": base64.b64encode(resp).decode(),
            },
            {
                "seq": 4,
                "timestamp_ns": t + 1_000_000_000,
                "phase": "complete",
                "status": 200,
                "key_hash": self.key_hash,
                "model": "gpt-6.1-sol",
                "stop_reason": "completed",
                "input_tokens": 1200,
                "output_tokens": 40,
                "client": {"originator": self.originator, "user-agent": f"{self.originator}/{version}"},
                "account_uuid": self.account,
                "device_id": CODEX_INSTALL,
                "session_id": self.thread,
                "client_metadata": {
                    "thread_id": self.thread,
                    "session_id": self.thread,
                    "turn_id": turn_id,
                    "request_kind": "turn",
                    "thread_source": source,
                    "installation_id": CODEX_INSTALL,
                },
            },
        ]
        for r in rows:
            self.ds.add({**base, **r})
        return cid

    def other(self, when: str, path: str, *, method: str = "GET", status: int = 200) -> str:
        """One of Codex's calls that no thread id names (usage, catalog, analytics…), with the
        thread's credentials and client headers; returns its correlation id."""
        cid = f"{INSTANCE}-cx-{self.thread[:8]}-{next(self._seq)}"
        version = "0.167.0" if self.originator == "codex-tui" else "0.164.0"
        client = {"originator": self.originator, "user-agent": f"{self.originator}/{version} (Debian 12.0.0; aarch64)"}
        base = {
            "correlation_id": cid,
            "host": "chatgpt.com",
            "method": method,
            "path": path,
            "observer_instance": INSTANCE,
        }
        t = ns(when)
        self.ds.add({**base, "seq": 0, "timestamp_ns": t, "phase": "request_headers", "key_hash": self.key_hash})
        self.ds.add(
            {
                **base,
                "seq": 1,
                "timestamp_ns": t + 80_000_000,
                "phase": "complete",
                "status": status,
                "key_hash": self.key_hash,
                "client": client,
                "account_uuid": self.account,
            }
        )
        return cid

    def turn_event(self, when: str, turn_id: str, **params) -> None:
        """Codex's codex_turn_event for a turn (its own report)."""
        batch = f"{INSTANCE}-cxa-{self.thread[:8]}-{when}"
        self.ds.add(
            {
                "correlation_id": batch,
                "seq": next(self._seq),
                "timestamp_ns": ns(when),
                "phase": "client_event",
                "host": "chatgpt.com",
                "method": "POST",
                "path": "/backend-api/codex/analytics-events/events",
                "client_event": {
                    "source": "codex",
                    "type": "codex_turn_event",
                    "name": "codex_turn_event",
                    "session_id": self.thread,
                    "data": {"thread_id": self.thread, "turn_id": turn_id, "session_id": self.thread, **params},
                },
            }
        )


def load_fixture(ds: Dataset, name: str) -> None:
    """Add a committed NDJSON fixture's rows (tests/fixtures/<name>.ndjson.gz) to the dataset."""
    path = pathlib.Path(__file__).parent / "fixtures" / f"{name}.ndjson.gz"
    with gzip.open(path, "rt") as f:
        for line in f:
            ds.add(json.loads(line))


def account_call(cc: ClaudeCode, when: str, path: str = "/api/oauth/profile") -> str:
    """A call with the person's credential but no session (skills list, profile, MCP registry, …)."""
    return cc.ds.transaction(when, method="GET", path=path, key_hash=cc.key_hash)


def limits_on_last_call(ds: Dataset, status="allowed", util7d="0.05", overage="rejected") -> None:
    """Put the unified rate-limit headers on the most recent model call's response."""
    rs = [r for r in ds.rows if r["phase"] == "response_headers" and r.get("path", "").startswith("/v1/messages")][-1]
    rs.setdefault("headers", {}).update(
        {
            "anthropic-ratelimit-unified-status": status,
            "anthropic-ratelimit-unified-7d-utilization": util7d,
            "anthropic-ratelimit-unified-overage-status": overage,
        }
    )


def cid_of(ds: Dataset, request_id: str) -> str:
    """The correlation id of the call whose response carried `request_id` (what ClaudeCode.call returns)."""
    for r in ds.rows:
        if r["phase"] == "response_headers" and (r.get("headers") or {}).get("request-id") == request_id:
            return str(r["correlation_id"])
    raise KeyError(request_id)


def rows_of(ds: Dataset, cid: str, phase: str) -> list[dict]:
    return [r for r in ds.rows if r["correlation_id"] == cid and r["phase"] == phase]


def complete_meta(ds: Dataset, cid: str, **fields) -> None:
    """Content-free request/response metadata the observer writes on the complete row (effort, message ids, …)."""
    for r in rows_of(ds, cid, "complete"):
        r.update(fields)


def headers_on(ds: Dataset, cid: str, phase: str, **headers: str) -> None:
    for r in rows_of(ds, cid, phase):
        r.setdefault("headers", {}).update(headers)


def body_chunks(ds: Dataset, cid: str, phase: str, chunks: list[bytes], *, start_ms: float, step_ms: float) -> None:
    """Body chunk rows; with `body_b64`, as the observer writes them under full-body capture."""
    t0 = rows_of(ds, cid, "request_headers")[0]["timestamp_ns"]
    for i, c in enumerate(chunks):
        ds.add(
            {
                "correlation_id": cid,
                "seq": 10 + i if phase == "request_body_chunk" else 100 + i,
                "timestamp_ns": t0 + int((start_ms + i * step_ms) * 1_000_000),
                "phase": phase,
                "host": "api.anthropic.com",
                "path": "/v1/messages?beta=true",
                "observer_instance": INSTANCE,
                "bytes": len(c),
                "body_b64": base64.b64encode(c).decode(),
            }
        )


def sse(*events: tuple[str, dict]) -> list[bytes]:
    """Server-sent events, one chunk each."""
    return [f"event: {name}\ndata: {json.dumps(data)}\n\n".encode() for name, data in events]


def timing(ds: Dataset, cid: str, *, response_ms: float, end_ms: float) -> None:
    """Move the response headers and the complete row to these offsets from the request headers."""
    t0 = rows_of(ds, cid, "request_headers")[0]["timestamp_ns"]
    for r in rows_of(ds, cid, "response_headers"):
        r["timestamp_ns"] = t0 + int(response_ms * 1_000_000)
    for r in rows_of(ds, cid, "complete"):
        r["timestamp_ns"] = t0 + int(end_ms * 1_000_000)
