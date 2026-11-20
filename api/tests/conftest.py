"""Test fixtures, including the sample writer.

`pytest --write-samples` writes each `write_sample(...)` response to
`ui/src/api/samples/<route>/<scenario>.ts`, typed against the generated
`spec.ts` (docs/ui/BUILD_PLAN.md §4). Without the flag, write_sample still
checks the response but writes nothing. Run it through `make regen`, which
regenerates spec.ts first.
"""

import concurrent.futures
import contextlib
import fcntl
import functools
import gzip
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import time
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


def _build(out: pathlib.Path) -> None:
    out.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["go", "build", "-o", str(out), "./cmd/aintercept"], cwd=REPO / "aintercept", check=True)  # noqa: S603, S607


def pytest_configure(config):
    """The run's one `aintercept` build: in the controlling process, before xdist starts its workers,
    which use it (AINTERCEPT_TEST_BIN) rather than each building their own."""
    if hasattr(config, "workerinput") or os.environ.get("AINTERCEPT_TEST_BIN"):
        return
    out = REPO / ".cache" / "test-bin" / "aintercept"
    _build(out)
    os.environ["AINTERCEPT_TEST_BIN"] = str(out)


def aintercept_binary() -> pathlib.Path:
    """The `aintercept` binary: AINTERCEPT_TEST_BIN (the run's build, pytest_configure), else built
    here once."""
    if not _binary:
        given = os.environ.get("AINTERCEPT_TEST_BIN")
        out = pathlib.Path(given) if given else REPO / ".cache" / "test-bin" / "aintercept"
        if not given or not out.exists():
            _build(out)
        _binary.append(out)
    return _binary[0]


def load_v9(raw: pathlib.Path, dsn: str, out: pathlib.Path) -> None:
    """v9's path over raw (docs/PIPELINE.md §2, §8 "In CI"): `aintercept canon -raw` writes the rows as v9
    raw files into out through the observer's writer, and `canon -load -refresh 4` loads them into dsn as
    the workers do (each call's member range on its row) with the rollups refreshed and checked against
    the facts from scratch. A call's raw_key is relative to out."""
    binary = str(aintercept_binary())
    load = ["-src", str(out), "-load", dsn, "-refresh", "4", "-timings=false"]
    for args in (["-src", str(raw), "-raw", str(out)], load):
        r = subprocess.run([binary, "canon", *args], capture_output=True, text=True)  # noqa: S603
        if r.returncode != 0:
            raise RuntimeError(f"aintercept canon {' '.join(args)} failed:\n{r.stdout[-4000:]}{r.stderr[-4000:]}")


# Each distinct dataset is loaded once, into a template database, and every test that loads it gets a
# copy of that (CREATE DATABASE … TEMPLATE, a file copy). A template is named by what made it: the
# `aintercept` build and the schema (its prefix), and the dataset's rows. xdist's workers share them
# through a lock file per template; the test server keeps them between runs, so a run whose code and
# data didn't change loads nothing, and the controller drops an older build's (pytest_sessionstart).
TEMPLATES = REPO / ".cache" / "test-templates"


@functools.cache
def build_key() -> str:
    """What every load depends on but the data: the `aintercept` build and the schema."""
    h = hashlib.sha256(aintercept_binary().read_bytes())
    h.update(pgdb.SCHEMA.read_bytes())
    for m in sorted(pgdb.MIGRATIONS.iterdir()):
        h.update(m.name.encode() + m.read_bytes())
    return h.hexdigest()[:10]


def dataset_key(root: pathlib.Path) -> str:
    """The dataset's rows: each file's path and decompressed content (gzip stamps the time it wrote)."""
    h = hashlib.sha256()
    for f in sorted(p for p in root.rglob("*") if p.is_file()):
        h.update(str(f.relative_to(root)).encode() + b"\0")
        data = f.read_bytes()
        h.update(gzip.decompress(data) if f.suffix == ".gz" else data)
    return h.hexdigest()[:20]


def template_for(admin: str, root: pathlib.Path) -> tuple[str, pathlib.Path]:
    """The template database holding root's dataset, and its raw files' root, made the first time."""
    name = f"tpl_{build_key()}_{dataset_key(root)}"
    out = TEMPLATES / name
    TEMPLATES.mkdir(parents=True, exist_ok=True)
    with open(TEMPLATES / f"{name}.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        done = out / ".done"
        if not (done.exists() and pgdb.database_exists(admin, name)):
            shutil.rmtree(out, ignore_errors=True)
            out.mkdir(parents=True)
            load_v9(root, pgdb.create_template(admin, name), out)
            pgdb.seal_template(admin, name)
            done.touch()
        os.utime(TEMPLATES / f"{name}.lock")  # in use: not dropped as stale
    return name, out


def pytest_sessionstart(session):
    """The controller drops the templates an older build made and nothing used for an hour (each is
    the test server's memory; one a concurrent run still uses is kept)."""
    admin = pgdb.admin_dsn()
    if not admin or hasattr(session.config, "workerinput"):
        return
    prefix = f"tpl_{build_key()}_"

    def keep(name: str) -> bool:
        lock = TEMPLATES / f"{name}.lock"
        return name.startswith(prefix) or lock.exists() and time.time() - lock.stat().st_mtime < 3600

    with contextlib.suppress(Exception):
        pgdb.drop_templates(admin, keep)


class PgDataset:
    """A dataset in a fresh Postgres of its own: a copy of its template (its facts and rollups,
    load_v9), and the raw files the drawer's ranged GETs read."""

    def __init__(self, admin: str, root: pathlib.Path) -> None:
        template, self.raw = template_for(admin, pathlib.Path(root))
        self._db = pgdb.clone_database(admin, template)
        self.dsn = self._db.__enter__()

    def store(self, **kwargs: Any) -> Any:
        from aintercept_api.pgstore import PgStore

        return PgStore(self.dsn, source=LocalSource(self.raw), **kwargs)

    def close(self) -> None:
        self._db.__exit__(None, None, None)


def pg_datasets(admin: str, roots: dict[str, pathlib.Path]) -> dict[str, PgDataset]:
    """Each dataset root in a fresh Postgres of its own (PgDataset), loaded at once: each load is its own
    database and its own `aintercept` processes."""
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(roots) or 1) as pool:
        futures = {name: pool.submit(PgDataset, admin, root) for name, root in roots.items()}
        out: dict[str, PgDataset] = {}
        failure: BaseException | None = None
        for name, f in futures.items():
            try:
                out[name] = f.result()
            except BaseException as e:
                failure = failure or e
        if failure is not None:
            for pg in out.values():
                pg.close()
            raise failure
    return out


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
        # Created, never overwritten: `make regen` empties the folder first and runs the tests in
        # parallel, so a sample two tests write (in any two workers) fails here.
        try:
            f = out.open("x")
        except FileExistsError:
            pytest.fail(f"{out.name} already written by another test")
        with f:
            f.write(
                "// GENERATED by `make regen` from api/tests. Do not edit.\n"
                'import type { components } from "../../spec";\n\n'
                f'const sample: components["schemas"]["{schema}"] = {body};\n\n'
                "export default sample;\n"
            )

    return write
