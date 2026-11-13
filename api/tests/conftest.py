"""Test fixtures, including the sample writer.

`pytest --write-samples` writes each `write_sample(...)` response to
`ui/src/api/samples/<route>/<scenario>.ts`, typed against the generated
`spec.ts` (docs/ui/BUILD_PLAN.md §4). Without the flag, write_sample still
checks the response but writes nothing. Run it through `make regen`, which
regenerates spec.ts first.
"""

import atexit
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile
import threading
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any

import pytest
from fastapi.testclient import TestClient

from aintercept_api.app import create_app
from aintercept_api.buckets import LocalSource
from aintercept_api.raw import LocalRaw
from aintercept_api.v1 import V1Store
from tests.builders import Dataset
from tests.oracle.store import OracleStore

SAMPLES_DIR = pathlib.Path(__file__).resolve().parents[2] / "ui" / "src" / "api" / "samples"

# Every test sees the same "now" and query id, so samples are byte-for-byte reproducible.
FROZEN_NOW = datetime(2026, 11, 4, 18, 5, tzinfo=UTC)
FROZEN_QUERY_ID = "q_sample"

_written: dict[pathlib.Path, str] = {}  # sample path -> test that wrote it, per session


# The full run (`make test-api-full`, nightly in CI): the tests marked `full` (every URL of the
# parity gates, the memory churn) and the pipeline at full length. `make check` runs the rest,
# and the parity gates over a few URLs of each endpoint group.
FULL = os.environ.get("AINTERCEPT_TESTS_FULL") == "1"


def pytest_addoption(parser):
    parser.addoption("--write-samples", action="store_true", help="write endpoint responses as UI sample files")


def pytest_configure(config):
    config.addinivalue_line("markers", "full: only in the full run (make test-api-full, nightly)")


def pytest_collection_modifyitems(config, items):
    """While samples are being (re)written, the requirements check would see a
    half-written folder; it runs in the normal test pass instead. The `full` tests run only in
    the full run."""
    if config.getoption("--write-samples"):
        items[:] = [i for i in items if "test_requirements" not in i.nodeid]
    if not FULL:
        skip = pytest.mark.skip(reason="the full run only (make test-api-full)")
        for item in items:
            if "full" in item.keywords:
                item.add_marker(skip)


def few_per_group(urls: list[str], group: Callable[[str], str], n: int = 3) -> list[str]:
    """The first n URLs of each endpoint group: what `make check` holds a parity gate to (the full
    run holds every URL)."""
    seen: dict[str, int] = {}
    out = []
    for url in urls:
        g = group(url)
        if seen.get(g, 0) < n:
            seen[g] = seen.get(g, 0) + 1
            out.append(url)
    return out


def make_client(store, **kwargs) -> TestClient:
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
        # One output per xdist worker: workers build at once, and must not write the same file.
        worker = os.environ.get("PYTEST_XDIST_WORKER", "main")
        default = REPO / ".cache" / "test-bin" / worker / "aintercept"
        out = pathlib.Path(os.environ.get("AINTERCEPT_TEST_BIN") or default)
        out.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["go", "build", "-o", str(out), "./cmd/aintercept"], cwd=REPO / "aintercept", check=True)  # noqa: S603, S607
        _binary.append(out)
    return _binary[0]


def raw_digest(raw: pathlib.Path) -> str:
    """The raw files' names and bytes, hashed: equal for two datasets the builders made alike."""
    h = hashlib.sha256()
    for p in sorted(raw.rglob("*.ndjson.gz")):
        h.update(str(p.relative_to(raw)).encode() + b"\0" + p.read_bytes())
    return h.hexdigest()


_laid_out: dict[str, pathlib.Path] = {}  # raw digest -> the v5 layout of it, this session
_laid_out_dir: list[pathlib.Path] = []


def _laid_out_root() -> pathlib.Path:
    if not _laid_out_dir:
        _laid_out_dir.append(pathlib.Path(tempfile.mkdtemp(prefix="aintercept-v1-")))
        atexit.register(shutil.rmtree, _laid_out_dir[0], True)
    return _laid_out_dir[0]


def lay_out(raw: pathlib.Path) -> pathlib.Path:
    """The v5 pipeline over raw (docs/PIPELINE.md): `aintercept v1layout` runs the observer's own
    code to records and calls/ detail objects, applies each key's records and commits it under
    v1/keys/, and runs one merge into v1/merge/. Deterministic, so a dataset another test already
    laid out is reused."""
    digest = raw_digest(raw)
    if digest not in _laid_out:
        out = _laid_out_root() / digest
        subprocess.run(  # noqa: S603
            [str(aintercept_binary()), "v1layout", "-src", str(raw), "-out", str(out), "-prefix", "sim"],
            check=True,
            capture_output=True,
        )
        _laid_out[digest] = out
    return _laid_out[digest]


# AINTERCEPT_TEST_STORE=oracle runs every API test on the v6 oracle's read path (tests/oracle/):
# today's derivation, frozen, over the same layout (docs/PIPELINE.md build step 2's gate).
ORACLE = os.environ.get("AINTERCEPT_TEST_STORE") == "oracle"


class LaidOutStore:
    """The API's v1 store over a test's dataset: whenever the test has written more raw files, the
    dataset is laid out again by the v5 pipeline, so a test sees exactly the files it wrote. With
    oracle, the oracle's store over the same layout."""

    def __init__(self, root: pathlib.Path, *, oracle: bool = ORACLE, **kwargs):
        self.root, self.kwargs, self.oracle = root, kwargs, oracle
        self._listing: list[tuple[str, int]] | None = None
        self._lock = threading.Lock()
        self._store: Any = None
        self._cache = pathlib.Path(tempfile.mkdtemp(prefix="aintercept-cache-"))

    def _current(self) -> Any:
        with self._lock:
            listing = sorted((str(p), p.stat().st_size) for p in self.root.glob("dt=*/hour=*/instance=*/*.ndjson.gz"))
            if self._store is None or listing != self._listing:
                if self._store is not None:
                    self._store.close()
                layout, raw = lay_out(self.root), LocalRaw(self.root)
                if self.oracle:
                    self._store = OracleStore(layout, prefix="sim/", raw=raw, **self.kwargs)
                else:
                    self._store = V1Store(LocalSource(layout), self._cache, prefix="sim/", raw=raw, **self.kwargs)
                self._listing = listing
            return self._store

    def maybe_ingest(self, min_interval_s: float) -> None:
        self._current()

    def ingest(self) -> int:
        self._current()
        return 0

    def close(self) -> None:
        if self._store is not None:
            self._store.close()
        shutil.rmtree(self._cache, ignore_errors=True)

    @property
    def data_dir(self) -> pathlib.Path:
        return self.root

    def __getattr__(self, name: str):
        return getattr(self._current(), name)


def bounded_store(root, **kwargs) -> Any:
    """The v5 store over a dataset (the endpoints' Store interface), bounded like a small
    machine, so a memory regression fails here, not in CI's runner."""
    kwargs.setdefault("threads", 2)
    return LaidOutStore(pathlib.Path(root), memory_limit="512MB", **kwargs)


@pytest.fixture
def client_for():
    """client_for(dataset) -> a TestClient over that dataset with the frozen clock."""
    stores: list[LaidOutStore] = []

    def make(ds: Dataset, **kwargs) -> TestClient:
        root = ds.write()
        root.mkdir(parents=True, exist_ok=True)
        stores.append(bounded_store(root))
        return make_client(stores[-1], **kwargs)

    yield make
    for s in stores:
        s.close()


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
