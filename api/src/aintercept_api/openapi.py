"""Print the OpenAPI schema (`uv run python -m aintercept_api.openapi`). `make regen`
turns it into the UI's generated `spec.ts`."""

import contextlib
import json

from aintercept_api import pages
from aintercept_api.app import create_app


class NoStore:
    """The schema reads no data: a store that serves no page."""

    def page_reader(self, page: str, path: str) -> contextlib.AbstractContextManager[pages.Pages]:
        raise RuntimeError("the OpenAPI schema reads no data")


if __name__ == "__main__":
    print(json.dumps(create_app(store=NoStore()).openapi(), indent=2, sort_keys=True))
