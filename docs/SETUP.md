# Setting up aintercept on one machine

This takes a fresh clone to the People page showing a person, with your own Claude Code traffic flowing through the proxy. It's written so a coding agent can follow it step by step; the steps that need a person say so.

What runs where:

| Piece | Runs in | Port | What it does |
|---|---|---|---|
| Envoy | Docker | 443 | Terminates TLS for `api.anthropic.com` with a certificate from the local dev CA, forwards to the real upstream |
| certsvc | Docker | — | Mints those certificates (`aintercept certsvc`) |
| observer | Docker | 9001 | Records each request and response, body-stripped, as raw files under `data/telemetry/raw/` (`aintercept observer`) |
| Postgres | Docker | 15432 (this machine only) | The queryable data ([PIPELINE.md](PIPELINE.md)): the workers write it, the API reads it. `make up` generates its passwords into the gitignored `.env` |
| workers | Docker | 9005 | List the observer's raw files (`data/telemetry/raw/`), load each into Postgres's fact tables (`calls`, `events`, …) and refresh the rollups; retention every hour (`aintercept worker`, [PIPELINE.md](PIPELINE.md) §2.2, §5) |
| API | this machine | 8787 | Serves the pages (`api/`) from Postgres, through the API login (its DSN built from `.env`), and each call's raw member from `data/telemetry/raw/`. It starts once the workers have applied the schema, and refuses to start on a schema that isn't its build's |
| UI | this machine | 5173 | The app (`ui/`) |

Nothing leaves the machine except the forwarded requests themselves; captured data stays in `data/` (gitignored).

## 1. Prerequisites

| Need | Check | Install (macOS) |
|---|---|---|
| Docker with Compose v2, running | `docker compose version && docker info` | Docker Desktop |
| uv | `uv --version` | `brew install uv` |
| Node 24 (the UI's `ui/.nvmrc`) | `node -v` | `nvm install 24` (`make up` uses nvm when it's there) |
| pnpm | `pnpm -v` | `corepack enable` (`make up` tries this) |
| Port 443 free | `lsof -nP -iTCP:443 -sTCP:LISTEN` shows nothing, or only Docker | stop whatever holds it |

Go isn't needed: the Go services build inside Docker.

## 2. Start everything

```bash
make up
```

The first run builds the images (a few minutes) and installs the API's and UI's dependencies. It ends with `Up.` and the URLs. It's safe to re-run; what's already up stays up. If it fails, it prints the reason and the log to read (`.cache/run/`).

## 3. Prove the pipeline

```bash
make verify
```

This sends one request through the proxy to the real `api.anthropic.com` (no key, so the upstream answers 401) and one analytics batch with a made-up event, and waits until the observer has written them to a raw file, a worker has loaded them into Postgres (`calls`, `events`), the workers' `/lag` has settled under 30 seconds, and the API shows them on their pages (the request's drawer, `/api/v1/requests/<id>`; the batch's session's Events tab; `/status`). It also reads the call's raw range back on its own, and checks that the observer holds no Postgres connection. It changes nothing on the machine. Expect ten `ok` lines and `Verified.` within about 30 seconds.

To look at the rows yourself: `docker compose exec postgres psql -U postgres -d aintercept`.

## 4. Route Claude Code through the proxy (a person runs this)

This edits `/etc/hosts`, so it needs sudo. An agent should ask the person to run it, for example by typing `! sudo scripts/route-claude-through-proxy.sh setup 127.0.0.1` in Claude Code:

```bash
sudo scripts/route-claude-through-proxy.sh setup 127.0.0.1
```

It adds `api.anthropic.com` entries (IPv4 and IPv6) pointing at the local proxy and checks the route. From then on, every process on the machine that calls `api.anthropic.com` goes through the proxy.

Then start Claude Code in a shell that trusts the dev CA (only that shell trusts it; nothing system-wide changes):

```bash
export NODE_EXTRA_CA_CERTS="$PWD/certs/root-ca.crt"
claude
```

Use it for a minute (any prompt). An agent can confirm traffic is arriving:

```bash
curl -s localhost:8787/api/v1/people | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]), "people")'
```

`1 people` (or more) means the person page has data. It can take about 15 seconds after the first prompt: the observer writes a raw file after 5 seconds locally, the workers list new files every 5 seconds and load them at once, then refresh the rollups.

**Important:** while the machine is routed, Claude Code depends on the proxy. Stopping the stack (`make down`) without reverting first leaves Claude Code unable to reach Anthropic, and that includes an agent following this doc. Always revert first (step 6).

## 5. Open the app

<http://localhost:5173>. The People page lists the person; click through to their sessions, a session's prompts and steps, and a request's details.

## 6. Stop

```bash
sudo scripts/route-claude-through-proxy.sh revert   # first: give Claude Code its direct route back
make down
```

Captured data stays in `data/` and Postgres's Docker volume (`<checkout folder>_pgdata`). To start over, delete both: `rm -rf data/telemetry` and `docker volume rm <checkout folder>_pgdata`. The schema has no migrations: after pulling a schema change the workers reset Postgres to the new schema and reload every raw file in `data/telemetry/raw/`. Locally, retention keeps 15 days of facts in Postgres; the raw files in `data/telemetry/raw/` stay until you delete them.

## The cluster's data instead

In a cluster the query API runs next to the data (the chart's `api.enabled=true`): an `api` Deployment that reads Postgres (and each call's raw member from the bucket), behind a ClusterIP Service that no other pod may reach. The UI connects to it through a tunnel:

```bash
make remote
```

It needs `kubectl` pointed at the cluster. It runs `kubectl port-forward svc/api 8787` (restarting itself when the API pod restarts) and the UI on :5173, which proxies `/api` to the tunnel as usual. The request drawer, raw view and events all work, since the API reads each call's raw member from the bucket. `make down` stops the tunnel and the UI. It can't run alongside `make up`; both use ports 8787 and 5173.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `make up`: port 443 is in use | another proxy or web server | stop it, or run the stack where 443 is free |
| `make verify`: TLS through the proxy failed | Docker isn't up, or Envoy failed to start | `docker compose logs envoy` |
| `make verify`: expected 401, got 000 | no internet, or `api.anthropic.com` unreachable from Docker | check the network; `docker compose logs envoy` |
| Claude Code: certificate errors | `NODE_EXTRA_CA_CERTS` isn't set in that shell | export it (step 4) and restart `claude` |
| No people after using Claude Code | Claude Code isn't routed (or bypasses over IPv6) | `scripts/route-claude-through-proxy.sh status`; both host lines must be present |
| `make verify`: the workers can't read Postgres | Postgres is down, or its volume predates `.env`'s passwords | `docker compose logs postgres worker`; start over (step 6) |
| The workers restart: the database has tables but no schema_version | the volume holds something that isn't AIntercept's | start over (step 6) |
| The API answers 503 `schema_not_ready` | the workers haven't applied this build's schema yet, or the API is older than the workers | wait for the workers (`docker compose logs worker`); pull and re-run |
| `make api`: no `AINTERCEPT_PG_API_PASSWORD` | `make api` runs the API alone, on the Postgres `make up` started | `make up` first (or `make down` and `make api` with Docker's Postgres up) |
| The UI shows "Delayed" | no new data for 3 minutes | `curl localhost:9001/stats` (observer) and `curl localhost:9005/lag` (workers: the age of the oldest unloaded file or dirty rollup row) |
| Anything else | | logs: `.cache/run/api.log`, `.cache/run/ui.log`, `docker compose logs observer worker postgres envoy` |
