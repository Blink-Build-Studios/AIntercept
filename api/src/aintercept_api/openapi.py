"""Print the OpenAPI schema (`uv run python -m aintercept_api.openapi`). `make regen`
turns it into the UI's generated `spec.ts`."""

import json
import pathlib
import tempfile

from aintercept_api.app import create_app
from aintercept_api.buckets import LocalSource
from aintercept_api.v1 import V1Store

if __name__ == "__main__":
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="aintercept-openapi-"))
    app = create_app(store=V1Store(LocalSource(tmp), tmp / ".cache"))
    print(json.dumps(app.openapi(), indent=2, sort_keys=True))
