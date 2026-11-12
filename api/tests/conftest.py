"""Test fixtures, including the sample writer.

`pytest --write-samples` writes each `write_sample(...)` response to
`ui/src/api/samples/<route>/<scenario>.ts`, typed against the generated
`spec.ts` (docs/ui/BUILD_PLAN.md §4). Without the flag, write_sample still
checks the response but writes nothing. Run it through `make regen`, which
regenerates spec.ts first.
"""

import json
import os
import pathlib
import re
import shutil
import subprocess
import threading
from datetime import UTC, datetime

import duckdb
import pytest
from fastapi.testclient import TestClient

from aintercept_api import claude_code as cc
from aintercept_api.app import create_app
from aintercept_api.rows import request_row_from_table, request_rows
from aintercept_api.store import Store
from tests.builders import Dataset

SAMPLES_DIR = pathlib.Path(__file__).resolve().parents[2] / "ui" / "src" / "api" / "samples"

# Every test sees the same "now" and query id, so samples are byte-for-byte reproducible.
FROZEN_NOW = datetime(2026, 11, 4, 18, 5, tzinfo=UTC)
FROZEN_QUERY_ID = "q_sample"

_written: dict[pathlib.Path, str] = {}  # sample path -> test that wrote it, per session


def pytest_addoption(parser):
    parser.addoption("--write-samples", action="store_true", help="write endpoint responses as UI sample files")


def pytest_collection_modifyitems(config, items):
    """While samples are being (re)written, the requirements check would see a
    half-written folder; it runs in the normal test pass instead."""
    if config.getoption("--write-samples"):
        items[:] = [i for i in items if "test_requirements" not in i.nodeid]


def make_client(store: Store, **kwargs) -> TestClient:
    """Ingests on every request (interval 0), so a test sees exactly the files it wrote."""
    app = create_app(store=store, clock=lambda: FROZEN_NOW, query_id=lambda: FROZEN_QUERY_ID, ingest_interval_s=0)
    return TestClient(app, **kwargs)


@pytest.fixture
def dataset(tmp_path) -> Dataset:
    return Dataset(tmp_path / "data")


REPO = pathlib.Path(__file__).resolve().parents[2]
_binary: list[pathlib.Path] = []


def aintercept_binary() -> pathlib.Path:
    """The `aintercept` binary, built once per test session."""
    if not _binary:
        out = pathlib.Path(os.environ.get("AINTERCEPT_TEST_BIN") or REPO / ".cache" / "test-bin" / "aintercept")
        out.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["go", "build", "-o", str(out), "./cmd/aintercept"], cwd=REPO / "aintercept", check=True)  # noqa: S603, S607
        _binary.append(out)
    return _binary[0]


def run_processor(raw: pathlib.Path, shaped: pathlib.Path, *, fresh: bool = True, compact_at: int = 0) -> None:
    """`aintercept process` over raw, into shaped, from scratch: the real pipeline. (Tests
    rewrite their hour files as rows are added; the observer never rewrites one.)"""
    if fresh:
        shutil.rmtree(shaped, ignore_errors=True)
    env = {
        "PATH": os.environ.get("PATH", ""),
        "PROCESS_LOCAL_DIR": str(raw),
        "PROCESS_OUT_DIR": str(shaped),
        "PROCESS_ONCE": "true",
        "PROCESS_HTTP_LISTEN": "127.0.0.1:0",
        **({"PROCESS_COMPACT_AT": str(compact_at)} if compact_at else {}),
    }
    subprocess.run([str(aintercept_binary()), "process"], env=env, check=True, capture_output=True)  # noqa: S603


class ProcessedStore(Store):
    """A store whose ingest first runs the processor when the raw files changed,
    so a test sees exactly the files it has written, shaped by the real pipeline."""

    def __init__(self, root: pathlib.Path, **kwargs):
        super().__init__(root, root.parent / "shaped", **kwargs)
        self._listing: list[tuple[str, int]] | None = None
        self._process_lock = threading.Lock()

    def ingest(self) -> int:
        with self._process_lock:  # one processor at a time over the folder
            listing = sorted(
                (str(p), p.stat().st_size) for p in self.data_dir.glob("dt=*/hour=*/instance=*/*.ndjson.gz")
            )
            if listing != self._listing:
                run_processor(self.data_dir, self.shaped_dir)
                self._listing = listing
            return super().ingest()


def bounded_store(root, **kwargs) -> Store:
    """A store bounded like a small machine, so a memory regression fails here, not in CI's runner."""
    return ProcessedStore(pathlib.Path(root), memory_limit="512MB", threads=2, **kwargs)


@pytest.fixture
def client_for():
    """client_for(dataset) -> a TestClient over that dataset with the frozen clock."""
    stores: list[Store] = []

    def make(ds: Dataset, **kwargs) -> TestClient:
        root = ds.write()
        root.mkdir(parents=True, exist_ok=True)
        stores.append(bounded_store(root))
        return make_client(stores[-1], **kwargs)

    yield make
    for s in stores:
        assert_request_rows_parity(s)
        s.close()


def assert_request_rows_parity(store: Store) -> None:
    """The processor's request_rows table holds exactly the rows rows.request_rows derives, for every
    person (docs/architecture.md, Decision 4): checked over every test's dataset at its end."""
    store.ingest()
    try:
        manifest = json.loads((store.shaped_dir / "manifest.json").read_text())
    except FileNotFoundError:
        return
    key = manifest.get("snapshots", {}).get("request_rows")
    if not key:
        return
    people = {r[0] for r in store.query("SELECT account_id FROM accounts UNION SELECT person_id FROM people")}
    want = sorted(
        json.dumps({"person": pid, **r.model_dump(mode="json")}, sort_keys=True)
        for pid in people | {cc.UNATTRIBUTED}
        for r in request_rows(store, cc.load_person(store, pid))
    )
    cur = duckdb.connect().execute("SELECT * FROM read_parquet(?)", [str(store.shaped_dir / key)])
    cols = [c[0] for c in cur.description]
    got = sorted(
        json.dumps({"person": r["person_id"], **request_row_from_table(r).model_dump(mode="json")}, sort_keys=True)
        for r in (dict(zip(cols, t, strict=True)) for t in cur.fetchall())
    )
    assert got == want


def operation_for(app, method: str, path: str, status: int) -> tuple[str, str]:
    """(operationId, response schema name) for a request and status, read from the
    app's OpenAPI document, so a sample is typed with exactly the name spec.ts uses."""
    method = method.lower()
    for template, ops in app.openapi()["paths"].items():
        if method in ops and re.fullmatch(re.sub(r"\{[^}]+\}", "[^/]+", template), path):
            op = ops[method]
            try:
                ref = op["responses"][str(status)]["content"]["application/json"]["schema"]["$ref"]
            except KeyError as e:
                raise LookupError(
                    f"{op['operationId']} has no named schema for status {status}; "
                    "responses must be concrete models (see test_contract.py)"
                ) from e
            return op["operationId"], ref.rsplit("/", 1)[-1]
    raise LookupError(f"no operation for {method.upper()} {path}")


@pytest.fixture
def write_sample(request):
    enabled = request.config.getoption("--write-samples")

    def write(client: TestClient, response, scenario: str, status: int = 200) -> None:
        assert response.status_code == status, response.text
        operation_id, schema = operation_for(client.app, response.request.method, response.request.url.path, status)
        if not enabled:
            return
        out = SAMPLES_DIR / operation_id.removeprefix("get_") / f"{scenario}.ts"
        prior = _written.get(out)
        assert prior is None or prior == request.node.nodeid, f"{out.name} already written by {prior}"
        _written[out] = request.node.nodeid
        out.parent.mkdir(parents=True, exist_ok=True)
        body = json.dumps(response.json(), indent=2, sort_keys=True)
        out.write_text(
            "// GENERATED by `make regen` from api/tests. Do not edit.\n"
            'import type { components } from "../../spec";\n\n'
            f'const sample: components["schemas"]["{schema}"] = {body};\n\n'
            "export default sample;\n"
        )

    return write
