import logging
import os
import pathlib
import re
import time
import uuid
from collections.abc import Awaitable, Callable, Mapping
from datetime import UTC, datetime
from typing import Any

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import FileResponse, JSONResponse, Response
from starlette.concurrency import run_in_threadpool
from starlette.exceptions import HTTPException as StarletteHTTPException

from aintercept_api.auth import OPEN_PATHS, AuthError, KeysUnavailable, Verifier, identity, verifier_from_env
from aintercept_api.models.common import ErrorBody, ErrorResponse
from aintercept_api.raw import HttpRaw
from aintercept_api.routers import people, requests, sessions, status
from aintercept_api.store import Store

log = logging.getLogger("aintercept_api")

# api/src/aintercept_api/app.py -> the repository root.
REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_DATA_DIR = REPO_ROOT / "data" / "telemetry"
# `aintercept process` writes here from the raw partitions (docker compose runs it).
DEFAULT_SHAPED_DIR = REPO_ROOT / "data" / "shaped"
# Ingest new files at most this often; requests in between read the tables as they are.
DEFAULT_INGEST_INTERVAL_S = 5.0

# Every route can answer with this shape; it is in the OpenAPI spec, so the UI's
# error states are typed from the contract like everything else.
ERROR_RESPONSES: dict[int | str, dict[str, Any]] = {500: {"model": ErrorResponse, "description": "Query failed"}}


def error_response(status: int, code: str, message: str, query_id: str) -> JSONResponse:
    body = ErrorResponse(error=ErrorBody(code=code, message=message, queryId=query_id))
    return JSONResponse(status_code=status, content=body.model_dump(), headers={"X-Query-Id": query_id})


# DuckDB's share of the container's memory when no limit is set: the rest is Python,
# responses being built, and DuckDB's own overhead outside its buffer manager.
DUCKDB_MEMORY_SHARE = 0.6


def duckdb_memory_limit(env: Mapping[str, str]) -> str | None:
    """AINTERCEPT_MEMORY_LIMIT as given; else a share of AINTERCEPT_CONTAINER_MEMORY (the
    container's limit in bytes, which the chart passes in), so the two can't drift apart."""
    if env.get("AINTERCEPT_MEMORY_LIMIT"):
        return env["AINTERCEPT_MEMORY_LIMIT"]
    try:
        container = int(env.get("AINTERCEPT_CONTAINER_MEMORY") or 0)
    except ValueError:
        return None
    return f"{int(container * DUCKDB_MEMORY_SHARE) >> 20}MB" if container > 0 else None


def create_app(
    store: Store | None = None,
    clock: Callable[[], datetime] | None = None,
    query_id: Callable[[], str] | None = None,
    ingest_interval_s: float = DEFAULT_INGEST_INTERVAL_S,
    auth: Verifier | None = None,
    ui_dir: pathlib.Path | None = None,
) -> FastAPI:
    """Build the app. Tests pass their own store, a frozen clock and fixed query
    ids so generated samples are deterministic.

    auth (default: from AINTERCEPT_AUTH_*) verifies an identity-aware proxy's
    assertion on every request (auth.py). ui_dir (default: AINTERCEPT_UI_DIR)
    is a built UI to serve at /, beside /api/v1, so one Service serves both."""
    auth = auth or verifier_from_env()
    if ui_dir is None and os.environ.get("AINTERCEPT_UI_DIR"):
        ui_dir = pathlib.Path(os.environ["AINTERCEPT_UI_DIR"])
    # operationId = the route function's name (e.g. get_status): readable in the
    # generated TypeScript and used to name sample folders.
    app = FastAPI(title="aintercept API", version="0.1.0", generate_unique_id_function=lambda route: route.name)
    data_dir = pathlib.Path(os.environ.get("AINTERCEPT_DATA_DIR") or DEFAULT_DATA_DIR)
    if store is None:
        shaped = pathlib.Path(os.environ.get("AINTERCEPT_SHAPED_DIR") or DEFAULT_SHAPED_DIR)
        # In the cluster, raw files come from the mirror sidecar rather than a directory.
        raw_url = os.environ.get("AINTERCEPT_RAW_URL")
        store = Store(
            data_dir,
            shaped,
            raw=HttpRaw(raw_url) if raw_url else None,
            memory_limit=duckdb_memory_limit(os.environ),
            threads=int(os.environ["AINTERCEPT_THREADS"]) if os.environ.get("AINTERCEPT_THREADS") else None,
            db_dir=pathlib.Path(os.environ["AINTERCEPT_DB_DIR"]) if os.environ.get("AINTERCEPT_DB_DIR") else None,
        )
    app.state.store = store
    app.state.ingest_interval_s = ingest_interval_s
    app.state.clock = clock or (lambda: datetime.now(UTC))
    app.state.query_id = query_id or (lambda: f"q_{uuid.uuid4().hex[:12]}")
    log.info(
        "store ready",
        extra={
            "fields": {
                "raw": os.environ.get("AINTERCEPT_RAW_URL") or str(app.state.store.data_dir),
                "shapedDir": str(app.state.store.shaped_dir),
                "memoryLimit": app.state.store.memory_limit,
            }
        },
    )

    templates: list[tuple[re.Pattern[str], str]] = []

    def route_template(path: str) -> str | None:
        """The matched route as a template (/api/v1/people/{personId}), for
        grouping log lines; None when no route matches."""
        if not templates:
            for t in app.openapi()["paths"]:
                templates.append((re.compile(re.sub(r"\{[^}]+\}", "[^/]+", t) + "$"), t))
        return next((t for rx, t in templates if rx.match(path)), None)

    if auth is not None:
        # Inside request_log (registered after it, so it wraps this): refusals
        # are logged, and the verified identity goes on the request's line.
        @app.middleware("http")
        async def require_identity(request: Request, call_next: Callable[[Request], Awaitable[Response]]) -> Response:
            if request.url.path in OPEN_PATHS:
                return await call_next(request)
            qid = request.state.query_id
            try:
                claims = await run_in_threadpool(auth.verify, request.headers)
            except KeysUnavailable as e:
                log.warning("identity keys unavailable", extra={"fields": {"queryId": qid, "error": str(e)}})
                return error_response(503, "identity_unavailable", "The identity provider's keys can't be fetched", qid)
            except AuthError as e:
                request.state.auth_error = str(e)
                return error_response(401, "unauthenticated", "A valid identity assertion is required", qid)
            request.state.identity = identity(claims)
            return await call_next(request)

        fields = {"header": auth.config.header, "audience": auth.config.audience}
        log.info("identity verification on", extra={"fields": fields})

    @app.middleware("http")
    async def request_log(request: Request, call_next: Callable[[Request], Awaitable[Response]]) -> Response:
        """Exactly one structured line per request, failures included, keyed by
        the query id that the X-Query-Id header and error bodies also carry."""
        qid = request.state.query_id = request.app.state.query_id()
        start = time.perf_counter()
        try:
            # Loading runs in a thread (it reads Parquet; the event loop keeps serving).
            # A failed load is logged and the request served from what's loaded.
            await run_in_threadpool(request.app.state.store.maybe_ingest, request.app.state.ingest_interval_s)
        except Exception:
            log.exception("ingest failed", extra={"fields": {"queryId": qid}})
        try:
            response = await call_next(request)
        except Exception:
            log.exception("query failed", extra={"fields": {"queryId": qid, "path": request.url.path}})
            response = error_response(500, "internal", "Query failed", qid)
        fields = {
            "queryId": qid,
            "method": request.method,
            "path": request.url.path,
            "route": route_template(request.url.path),
            "status": response.status_code,
            "ms": round((time.perf_counter() - start) * 1000, 1),
        }
        skipped = len(request.app.state.store.skipped)
        if skipped:
            fields["skippedFiles"] = skipped
        # Who asked (the verified assertion), so access to per-person data is attributable.
        if who := getattr(request.state, "identity", None):
            fields["identity"] = who
        if why := getattr(request.state, "auth_error", None):
            fields["authError"] = why
        log.log(logging.ERROR if response.status_code >= 500 else logging.INFO, "request", extra={"fields": fields})
        response.headers["X-Query-Id"] = qid
        return response

    @app.exception_handler(RequestValidationError)
    async def invalid_request(request: Request, exc: RequestValidationError) -> JSONResponse:
        """Bad parameters use the same ErrorResponse shape as every other error."""
        problems = "; ".join(f"{'.'.join(str(x) for x in e['loc'])}: {e['msg']}" for e in exc.errors())
        return error_response(422, "invalid_request", problems, request.state.query_id)

    @app.exception_handler(StarletteHTTPException)
    async def http_error(request: Request, exc: StarletteHTTPException) -> JSONResponse:
        """404s and other HTTP errors use the same ErrorResponse shape."""
        qid = request.state.query_id
        return error_response(exc.status_code, f"http_{exc.status_code}", str(exc.detail), qid)

    app.include_router(status.router, prefix="/api/v1", responses=ERROR_RESPONSES)
    app.include_router(people.router, prefix="/api/v1", responses=ERROR_RESPONSES)
    app.include_router(sessions.router, prefix="/api/v1", responses=ERROR_RESPONSES)
    app.include_router(requests.router, prefix="/api/v1", responses=ERROR_RESPONSES)
    if ui_dir is not None:
        mount_ui(app, ui_dir)
    return app


def mount_ui(app: FastAPI, ui_dir: pathlib.Path) -> None:
    """Serve the built UI (`vite build`'s dist/) at /: its files as they are,
    and index.html for every other path (the UI routes on the client). /api/*
    is never the UI: an unknown API path is a JSON 404."""
    root = ui_dir.resolve()
    index = root / "index.html"
    if not index.is_file():
        raise ValueError(f"AINTERCEPT_UI_DIR {ui_dir} has no index.html")

    @app.get("/{path:path}", include_in_schema=False)
    def ui(path: str, request: Request) -> Response:
        if path == "api" or path.startswith("api/"):
            return error_response(404, "http_404", "Not Found", request.state.query_id)
        f = (root / path).resolve()
        if path and f.is_file() and f.is_relative_to(root):
            # Vite fingerprints assets/: their content never changes under a name.
            cache = "public, max-age=31536000, immutable" if path.startswith("assets/") else "no-cache"
            return FileResponse(f, headers={"Cache-Control": cache})
        return FileResponse(index, headers={"Cache-Control": "no-cache"})

    log.info("serving the UI", extra={"fields": {"dir": str(root)}})
