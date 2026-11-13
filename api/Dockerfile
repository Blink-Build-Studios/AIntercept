# The query API (api/) and the built web UI (ui/), served together: the UI at /,
# the API at /api/v1, one origin. FastAPI + DuckDB. In the cluster it queries the
# object store's v1/ folders in place through httpfs (docs/PIPELINE.md §4,
# deploy/helm/aintercept/templates/api.yaml).
#
# Build from the repository root (the UI is part of the image):
#   docker build -f api/Dockerfile -t <registry>/aintercept-api:<tag> .

# The UI's production build (`vite build`), with the lockfile and release-age rules
# the repo uses everywhere (ui/pnpm-workspace.yaml).
FROM node:24-bookworm-slim AS ui
RUN corepack enable && corepack prepare pnpm@10.24.0 --activate
WORKDIR /ui
COPY ui/package.json ui/pnpm-lock.yaml ui/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY ui/ ./
RUN pnpm build

FROM python:3.13-slim-bookworm AS build
RUN pip install --no-cache-dir uv==0.11.9
WORKDIR /app
COPY api/pyproject.toml api/uv.lock ./
RUN uv sync --locked --no-dev --no-install-project
COPY api/src ./src
RUN uv sync --locked --no-dev --no-editable
# DuckDB's S3 reader (httpfs) and the AWS credential chain (aws), installed here, at build
# time, for exactly the DuckDB version the lockfile pins: the API never downloads an
# extension (buckets.connect turns autoinstall off and loads them from this directory).
RUN /app/.venv/bin/python -c "import duckdb; c = duckdb.connect(config={'extension_directory': '/app/duckdb-extensions'}); c.execute('INSTALL httpfs'); c.execute('INSTALL aws')"

FROM python:3.13-slim-bookworm
COPY --from=build /app/.venv /app/.venv
COPY --from=build /app/duckdb-extensions /app/duckdb-extensions
COPY --from=ui /ui/dist /app/ui
ENV PATH=/app/.venv/bin:$PATH PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 AINTERCEPT_UI_DIR=/app/ui \
    AINTERCEPT_DUCKDB_EXTENSIONS=/app/duckdb-extensions
# DuckDB's scratch space: the chart mounts a volume here (the root filesystem is read-only).
WORKDIR /work
USER 65532:65532
ENTRYPOINT ["python", "-m", "aintercept_api.serve", "--host", "0.0.0.0", "--port", "8787"]
