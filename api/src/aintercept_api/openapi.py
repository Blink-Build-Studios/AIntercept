"""Print the OpenAPI schema (`uv run python -m aintercept_api.openapi`). `make regen`
turns it into the UI's generated `spec.ts`."""

import json
import pathlib
import tempfile

from aintercept_api.app import create_app
from aintercept_api.store import Store

if __name__ == "__main__":
    app = create_app(store=Store(pathlib.Path(tempfile.gettempdir())))
    print(json.dumps(app.openapi(), indent=2, sort_keys=True))
