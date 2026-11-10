# Setting up aintercept on one machine

This takes a fresh clone to the People page showing a person, with your own Claude Code traffic flowing through the proxy. It's written so a coding agent can follow it step by step; the steps that need a person say so.

What runs where:

| Piece | Runs in | Port | What it does |
|---|---|---|---|
| Envoy | Docker | 443 | Terminates TLS for `api.anthropic.com` with a certificate from the local dev CA, forwards to the real upstream |
| certsvc | Docker | — | Mints those certificates (`aintercept certsvc`) |
| observer | Docker | 9001 | Records each request and response, body-stripped, to `data/telemetry/` (`aintercept observer`) |
| processor | Docker | 9002 | Turns `data/telemetry/` into Parquet tables in `data/shaped/` (`aintercept process`) |
| API | this machine | 8787 | Serves the tables (`api/`) |
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

This sends one request through the proxy to the real `api.anthropic.com` (no key, so the upstream answers 401) and waits until the observer, the processor and the API all show it. It changes nothing on the machine. Expect three `ok` lines and `Verified.` within about 30 seconds.

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

`1 people` (or more) means the person page has data. It can take up to about 30 seconds after the first prompt: the observer flushes every 15 seconds and the processor polls every 5.

**Important:** while the machine is routed, Claude Code depends on the proxy. Stopping the stack (`make down`) without reverting first leaves Claude Code unable to reach Anthropic, and that includes an agent following this doc. Always revert first (step 6).

## 5. Open the app

<http://localhost:5173>. The People page lists the person; click through to their sessions, a session's prompts and steps, and a request's details.

## 6. Stop

```bash
sudo scripts/route-claude-through-proxy.sh revert   # first: give Claude Code its direct route back
make down
```

Captured data stays in `data/`. To start over, delete `data/telemetry` and `data/shaped`.

## The cluster's data instead

In a cluster the query API runs next to the data (the chart's `api.enabled=true`): an `api` Deployment whose `aintercept mirror` sidecar keeps the processor's tables in the pod and serves raw files from the bucket on demand, behind a ClusterIP Service that no other pod may reach. The UI connects to it through a tunnel:

```bash
make remote
```

It needs `kubectl` pointed at the cluster. It runs `kubectl port-forward svc/api 8787` (restarting itself when the API pod restarts) and the UI on :5173, which proxies `/api` to the tunnel as usual. The request drawer, raw view and events all work, since the API reads raw files through the mirror. `make down` stops the tunnel and the UI. It can't run alongside `make up`; both use ports 8787 and 5173.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `make up`: port 443 is in use | another proxy or web server | stop it, or run the stack where 443 is free |
| `make verify`: TLS through the proxy failed | Docker isn't up, or Envoy failed to start | `docker compose logs envoy` |
| `make verify`: expected 401, got 000 | no internet, or `api.anthropic.com` unreachable from Docker | check the network; `docker compose logs envoy` |
| Claude Code: certificate errors | `NODE_EXTRA_CA_CERTS` isn't set in that shell | export it (step 4) and restart `claude` |
| No people after using Claude Code | Claude Code isn't routed (or bypasses over IPv6) | `scripts/route-claude-through-proxy.sh status`; both host lines must be present |
| The UI shows "Delayed" | no new data for 3 minutes | `curl localhost:9002/stats` (processor) and `curl localhost:9001/stats` (observer) |
| Anything else | | logs: `.cache/run/api.log`, `.cache/run/ui.log`, `docker compose logs observer process envoy` |
