"""Test fixtures, including the sample writer.

`pytest --write-samples` writes each `write_sample(...)` response to
`ui/src/api/samples/<route>/<scenario>.ts`, typed against the generated
`spec.ts` (docs/ui/BUILD_PLAN.md §4). Without the flag, write_sample still
checks the response but writes nothing. Run it through `make regen`, which
regenerates spec.ts first.
"""

import atexit
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile
from datetime import UTC, datetime
from typing import Any

import pytest
from fastapi.testclient import TestClient

from aintercept_api.app import create_app
from aintercept_api.objects import LocalSource
from tests import pgdb
from tests.builders import Dataset

SAMPLES_DIR = pathlib.Path(__file__).resolve().parents[2] / "ui" / "src" / "api" / "samples"

# Every test sees the same "now" and query id, so samples are byte-for-byte reproducible.
FROZEN_NOW = datetime(2026, 11, 4, 18, 5, tzinfo=UTC)
FROZEN_QUERY_ID = "q_sample"

_written: dict[pathlib.Path, str] = {}  # sample path -> test that wrote it, per session


def pytest_addoption(parser):
    parser.addoption("--write-samples", action="store_true", help="write endpoint responses as UI sample files")


def pytest_collection_modifyitems(config, items):
    """While samples are being (re)written, only the tests that write them run (the write_sample
    fixture): the rest run in the normal test pass, and the requirements check would see a
    half-written folder."""
    if config.getoption("--write-samples"):
        items[:] = [i for i in items if "write_sample" in getattr(i, "fixturenames", ())]


def make_client(store, **kwargs) -> TestClient:
    """A client with the frozen clock and query id."""
    app = create_app(store=store, clock=lambda: FROZEN_NOW, query_id=lambda: FROZEN_QUERY_ID)
    return TestClient(app, **kwargs)


@pytest.fixture(scope="session")
def pg_dsn():
    """A fresh Postgres database with the schema applied, for this test session (each xdist worker
    its own; tests/pgdb.py). Skipped without AINTERCEPT_TEST_PG (`make check` starts a server), failed
    when AINTERCEPT_TEST_PG_REQUIRED=1 (CI)."""
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    with pgdb.fresh_database(admin) as dsn:
        yield dsn


@pytest.fixture
def dataset(tmp_path) -> Dataset:
    return Dataset(tmp_path / "data")


REPO = pathlib.Path(__file__).resolve().parents[2]
_binary: list[pathlib.Path] = []


def aintercept_binary() -> pathlib.Path:
    """The `aintercept` binary, built once per test session."""
    if not _binary:
        # One output per xdist worker: workers build at once, and must not write the same file.
        worker = os.environ.get("PYTEST_XDIST_WORKER", "main")
        default = REPO / ".cache" / "test-bin" / worker / "aintercept"
        out = pathlib.Path(os.environ.get("AINTERCEPT_TEST_BIN") or default)
        out.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["go", "build", "-o", str(out), "./cmd/aintercept"], cwd=REPO / "aintercept", check=True)  # noqa: S603, S607
        _binary.append(out)
    return _binary[0]


_scratch: list[pathlib.Path] = []


def _scratch_root() -> pathlib.Path:
    """A directory for this session's raw files, removed at exit."""
    if not _scratch:
        _scratch.append(pathlib.Path(tempfile.mkdtemp(prefix="aintercept-v9-")))
        atexit.register(shutil.rmtree, _scratch[0], True)
    return _scratch[0]


def load_v9(raw: pathlib.Path, dsn: str) -> pathlib.Path:
    """v9's path over raw (docs/PIPELINE.md §2, §8 "In CI"): `aintercept canon -raw` writes the rows as v9
    raw files through the observer's writer, and `canon -load -refresh 1` loads them into dsn as the
    workers do (each call's member range on its row) with the rollups refreshed. Returns the raw files'
    root: a call's raw_key is relative to it."""
    out = pathlib.Path(tempfile.mkdtemp(dir=_scratch_root(), prefix="v9-"))
    binary = str(aintercept_binary())
    subprocess.run([binary, "canon", "-src", str(raw), "-raw", str(out)], check=True, capture_output=True)  # noqa: S603
    subprocess.run(  # noqa: S603
        [binary, "canon", "-src", str(out), "-load", dsn, "-refresh", "1"], check=True, capture_output=True
    )
    return out


class PgDataset:
    """A dataset in a fresh Postgres of its own: its facts and rollups (load_v9), and the raw files the
    drawer's ranged GETs read."""

    def __init__(self, admin: str, root: pathlib.Path) -> None:
        self._db = pgdb.fresh_database(admin)
        self.dsn = self._db.__enter__()
        try:
            self.raw = load_v9(root, self.dsn)
        except BaseException:
            self._db.__exit__(None, None, None)
            raise

    def store(self, **kwargs: Any) -> Any:
        from aintercept_api.pgstore import PgStore

        return PgStore(self.dsn, source=LocalSource(self.raw), **kwargs)

    def close(self) -> None:
        self._db.__exit__(None, None, None)


def pg_admin() -> str:
    """The test Postgres' admin DSN; the test skips without one (fails in CI)."""
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    return admin


@pytest.fixture
def pg_client_for():
    """pg_client_for(dataset) -> a TestClient with the frozen clock over that dataset in Postgres
    (PgDataset)."""
    opened: list[tuple[PgDataset, Any]] = []

    def make(ds: Dataset, **kwargs) -> TestClient:
        root = ds.write()
        root.mkdir(parents=True, exist_ok=True)
        pg = PgDataset(pg_admin(), root)
        opened.append((pg, pg.store()))
        return make_client(opened[-1][1], **kwargs)

    yield make
    for pg, store in opened:
        store.close()
        pg.close()


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
