"""/status over the v1 layout matches today's store byte for byte (tests/test_status.py's cases):
the merge commit carries what today's processor reads from the raw rows, without a table read.

- The proxy's probe calls (synthetic) are never shown, but /status counts them: "waiting since"
  is the first one, and the newest one is data (the badge says whether the pipeline flows).
- `contentRetained` is the deployment's mode: true once any raw row carried a body's content."""

import pathlib

from aintercept_api.buckets import LocalSource
from aintercept_api.raw import LocalRaw
from aintercept_api.v1 import V1Store
from tests.builders import Dataset, body_chunks
from tests.conftest import bounded_store, make_client
from tests.test_v1_pipeline import v1_layout


def _both(ds: Dataset, tmp: pathlib.Path) -> tuple[bytes, bytes]:
    """/status's body from today's store and from the v1 layout of the same raw rows."""
    root = pathlib.Path(ds.write())
    old = bounded_store(root)
    old.ingest()
    layout = tmp / f"layout{len(list(tmp.glob('layout*')))}"
    v1_layout(root, layout)
    new = V1Store(LocalSource(layout), tmp / f"cache-{layout.name}", prefix="sim/", raw=LocalRaw(root))
    try:
        return make_client(old).get("/api/v1/status").content, make_client(new).get("/api/v1/status").content
    finally:
        old.close()
        new.close()


def test_probes_only_is_still_waiting(dataset, tmp_path):
    dataset.transaction("2026-11-04T17:40:00", path="/", method="GET", synthetic=True)
    dataset.transaction("2026-11-04T18:00:00", path="/", method="GET", synthetic=True)
    old, new = _both(dataset, tmp_path)
    assert b'"waitingSince":"2026-11-04T17:40:00Z"' in old
    assert new == old


def test_probes_before_traffic(dataset, tmp_path):
    dataset.transaction("2026-11-04T17:40:00", path="/", method="GET", synthetic=True)
    dataset.transaction("2026-11-04T18:04:30")
    dataset.transaction("2026-11-04T18:10:00", path="/", method="GET", synthetic=True)
    old, new = _both(dataset, tmp_path)
    assert new == old


def test_retention_mode(dataset, tmp_path):
    cid = dataset.transaction("2026-11-04T18:00:00")
    old, new = _both(dataset, tmp_path)
    assert b'"contentRetained":false' in old
    assert new == old
    body_chunks(dataset, cid, "request_body_chunk", [b'{"model":"x"}'], start_ms=0.5, step_ms=0.1)
    old, new = _both(dataset, tmp_path)
    assert b'"contentRetained":true' in old
    assert new == old
