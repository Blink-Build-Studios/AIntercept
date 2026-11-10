# The query API (api/): FastAPI + DuckDB over the processor's tables. In the
# cluster it runs beside `aintercept mirror`, which keeps the tables on a shared
# volume and serves raw files over localhost (deploy/helm/aintercept/templates/api.yaml).
FROM python:3.13-slim-bookworm AS build
RUN pip install --no-cache-dir uv==0.11.9
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev --no-install-project
COPY src ./src
RUN uv sync --locked --no-dev --no-editable

FROM python:3.13-slim-bookworm
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
# DuckDB's scratch space: the chart mounts a volume here (the root filesystem is read-only).
WORKDIR /work
USER 65532:65532
ENTRYPOINT ["python", "-m", "aintercept_api.serve", "--host", "0.0.0.0", "--port", "8787"]
