"""Checks the API against docs/ui/requirements.json (the definition of done).

Every implemented route must be in the manifest with the same path, path
parameter names and operationId, and must have a generated sample for each
required scenario (plus `demo`, when the manifest asks for it). Unimplemented
endpoints are fine: a requirement applies once its route exists.
"""

import json
import pathlib

from aintercept_api.app import create_app
from tests.conftest import SAMPLES_DIR, bounded_store

REPO = pathlib.Path(__file__).resolve().parents[2]
MANIFEST = json.loads((REPO / "docs" / "ui" / "requirements.json").read_text())
BY_PATH = {e["path"]: e for e in MANIFEST["endpoints"]}


def implemented(tmp_path) -> dict[str, dict]:
    spec = create_app(store=bounded_store(tmp_path)).openapi()
    return {path: ops["get"] for path, ops in spec["paths"].items() if "get" in ops}


def test_every_route_is_in_the_manifest(tmp_path):
    for path, op in implemented(tmp_path).items():
        assert path in BY_PATH, f"{path} isn't in docs/ui/requirements.json (handoff §5.1): add it or fix the path"
        assert op["operationId"] == BY_PATH[path]["operationId"], f"{path}: operationId differs from the manifest"


def test_every_implemented_route_has_its_scenarios(tmp_path):
    for path, op in implemented(tmp_path).items():
        want = BY_PATH[path]["scenarios"] + (["demo"] if BY_PATH[path]["demo"] else [])
        folder = SAMPLES_DIR / op["operationId"].removeprefix("get_")
        have = {p.stem for p in folder.glob("*.ts")}
        missing = [s for s in want if s not in have]
        assert not missing, f"{path}: no generated sample for {missing} (write_sample in a test, then make regen)"


def test_manifest_is_consistent():
    ids = [e["operationId"] for e in MANIFEST["endpoints"]]
    assert len(set(ids)) == len(ids)
    for e in MANIFEST["endpoints"]:
        assert e["operationId"].startswith("get_") and e["path"].startswith("/api/v1/")
        assert "error" in e["scenarios"], f"{e['path']}: every endpoint needs an error scenario"
