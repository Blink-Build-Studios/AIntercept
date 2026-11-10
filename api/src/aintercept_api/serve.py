"""Run the API with structured logs: `uv run python -m aintercept_api.serve`."""

import argparse

import uvicorn

from aintercept_api.logs import UVICORN_LOG_CONFIG

if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=8787)
    p.add_argument("--reload", action="store_true")
    a = p.parse_args()
    uvicorn.run(
        "aintercept_api.app:create_app",
        factory=True,
        host=a.host,
        port=a.port,
        reload=a.reload,
        log_config=UVICORN_LOG_CONFIG,
        access_log=False,
    )
