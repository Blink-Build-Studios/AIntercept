# Production deployment requirements

What a production (design-partner) deployment must set, beyond the demo path in
`docs/DEPLOY_DOKS.md`. Each item names the chart value or build setting that
implements it and how to confirm it.

## 1. The issuing CA (supplied, not minted here)

The chart does not create CA material. The deploying organization's PKI issues
an intermediate and the chart consumes it from the `interception.caSecretName`
Secret (`intermediate-ca.crt`, `ca-chain.crt`, and `intermediate-ca.key` unless
the key is in KMS).

- **Keep the intermediate's key in AWS KMS** (recommended on AWS): create an
  asymmetric `SIGN_VERIFY` key (RSA or ECC NIST), have the root issue the
  intermediate certificate for that key's public key, and set
  `certsvc.kms.keyId`. certsvc then asks KMS to sign each leaf and never holds
  the key; every signature is a KMS call recorded in CloudTrail. certsvc's
  identity needs `kms:GetPublicKey` and `kms:Sign` on that key only. It refuses
  to start if the KMS key doesn't match the intermediate certificate.

- **Name-constrain the intermediate** to the routed domains (`nameConstraints =
  critical, permitted;DNS:anthropic.com, …`; a domain covers its subdomains).
  Clients then reject any certificate for another site even if the key leaks.
  Adding a domain outside the constraints means issuing a new intermediate.
  The default allowlist needs: `anthropic.com`, `claude.ai`, `openai.com`,
  `chatgpt.com`, `oaiusercontent.com` (ChatGPT file uploads),
  `generativelanguage.googleapis.com`, and `cloudflare-ech.com` (the ECH
  public name, see `docs/INTERCEPTION_ISSUES.md` C4). If the intermediate
  can't cover one, drop that host from both the allowlist and
  `files/envoy.multi.yaml`. The chart refuses to render when the two differ,
  and certsvc refuses to start on a host outside the constraints.
- **`requireNameConstraints` counts permitted names only.** An intermediate
  with only excluded names (e.g. `excluded;DNS:.mil`) can sign for almost
  anything and is rejected.
- **`ca-chain.crt` = intermediate followed by the root.** The root is the trust
  anchor certsvc verifies against at startup.
- **Set `interception.requireNameConstraints: true`.** certsvc always mints a
  test leaf per allowlisted host at startup and verifies it like a client
  would, refusing to start if any host would be rejected (e.g. outside the
  constraints, expired intermediate). With this on it also refuses an
  intermediate that carries no name constraints.
- **Readiness:** certsvc's readiness probe (`/healthz/ready` on port 18001)
  passes only once that startup check has signed its test leaves (with KMS,
  each is a KMS `Sign` call). A certsvc that can't sign (missing KMS
  permissions, an intermediate it refuses) exits and never goes Ready, so its
  rollout stalls with the old pods still serving.
- Confirm: `kubectl logs -l app=certsvc` shows `SDS listening` and no
  `WARNING: the issuing CA has no name constraints`.

`scripts/generate-dev-ca.sh` produces a dev CA shaped the same way, and
`scripts/dev-aws-emulators.sh` issues one for a KMS key (the same ceremony,
against a local KMS emulator).

## 2. Per-device attribution (source IP)

The observer attributes each request to the TCP peer address Envoy observed,
not to any request header: a client can forge headers, not its address. For
that address to be the laptop rather than a load balancer or node:

| Load balancer | Settings |
|---|---|
| Pass-through, keeps client IP (DO Network LB, AWS NLB instance targets) | `envoy.service.externalTrafficPolicy: Local` (default), `envoy.proxyProtocol.enabled: false` (default) |
| Terminates TCP (DO regional LB, AWS NLB IP targets) | `envoy.proxyProtocol.enabled: true` (sets the LB annotation and the Envoy listener filter together) |

`observer.clientIPHeaders` (e.g. `["cf-connecting-ip"]`) trusts a header ahead
of the peer address. Use it only when an L7 proxy you control sets that header.
With WARP Connector and Do-Not-Inspect, Cloudflare can't add headers, so leave it
empty.

Confirm: a request from a known machine produces `complete` events whose `ip`
is that machine's address.

## 2a. Routing must cover IPv6

However traffic is steered to the proxy (corporate DNS, MDM-pushed hosts
entries, a resolver policy), it has to cover **AAAA as well as A records**.
The providers publish IPv6 addresses (`api.anthropic.com` → `2607:6bc0::10`).
An A-only override leaves the real AAAA in place, and on any IPv6-capable
network clients connect to the provider directly. That happened in testing: on
one network, every Claude Code model call bypassed the proxy for hours while
its analytics still came through (`docs/INTERCEPTION_ISSUES.md` C6).

| Steering | What to do |
|---|---|
| Corporate DNS (RPZ, split-horizon) | For each routed name, answer AAAA with the proxy's IPv6 address if the load balancer has one, otherwise with no records (NODATA). Never pass the real AAAA through |
| Hosts entries (MDM, scripts) | Add `::ffff:<proxy IPv4>  <host>` next to `<proxy IPv4>  <host>`. The IPv4-mapped address reaches the proxy over IPv4, so the load balancer needs no IPv6 support. `scripts/route-claude-through-proxy.sh` does this |
| Egress firewall (belt and braces) | Block outbound IPv6 to the providers' ranges, so a missed record fails closed instead of bypassing |

Confirm, on an IPv6-capable network: `scripts/route-claude-through-proxy.sh
status` (or `dscacheutil -q host -a name <host>` / `getent ahosts <host>`)
lists only the proxy's addresses. In the data, Claude Code's own
`tengu_api_success` events should each match a captured model call by
`requestId`; unmatched ones mean traffic went around the proxy.

## 3. Traffic is forwarded unmodified

Envoy is configured not to add `x-request-id`, `x-forwarded-proto` or
`x-envoy-*` headers, and passes the provider's `server` header through. The one
deliberate change is removing `Alt-Svc` from responses, so browsers don't
attempt HTTP/3 (QUIC), which the proxy does not intercept.

## 4. Images

- Deploy every image by digest: `images.<name>.digest` renders
  `repository@digest`. Envoy ships pinned (`v1.39.6` by digest).
- Every release publishes its images to ghcr.io for amd64 and arm64:
  `ghcr.io/blink-build-studios/aintercept` (observer, certsvc and worker; one
  image serves both interception modes) and
  `ghcr.io/blink-build-studios/aintercept-api`, tagged `vX.Y.Z`. The chart
  deploys its own release's images by default; set `images.<name>.digest` to
  the release's digest to pin them. To mirror them into ECR, use an ECR
  pull-through cache for ghcr.io or copy them, and set
  `images.<name>.repository`.
- The in-cluster Postgres (`postgres.inCluster`, the DigitalOcean demo only) is
  the stock upstream image, pinned by digest (`images.postgres`), the same pin
  as Compose's and CI's.

## 4b. Interception mode: what is recorded

`interceptionMode` (chart) / `INTERCEPTION_MODE` (observer) chooses what
AIntercept records, per deployment:

| Mode | Recorded |
|---|---|
| `content-free` (default) | Metadata only: headers (credentials redacted), token counts, timings, model and tool names with counts per call, and the agents' own analytics events as sent. No request or response body. |
| `full` | Everything above, plus every request and response body. The observer's raw files (`<prefix>/raw/`) carry the bodies, and the workers and the API read them, so the UI shows prompt text, commands, patches and exit codes. |

Full interception records far more data, and that data includes everything
people send to AI tools. Size the object store, set its retention, and restrict
who can read the bucket accordingly. Credentials (`Authorization`, API keys,
cookies) are redacted in both modes.

The mode only decides what is **recorded**. Whether a customer's Interceptor
sees bodies, to act on them, is a separate setting (`interceptor.content`;
see `docs/INTERCEPTOR.md`): a content-free deployment can still redact inline.

## 4a. Observe mode and WebSockets

The observer runs in Envoy's ext_proc **observability mode**: Envoy streams
headers and body chunks to it and never waits for a reply, so the observer
adds no latency and an observer outage (verified: both pods deleted
mid-session) never affects traffic. WebSocket upgrades are routed to
HTTP/1.1 sibling upstreams and pass at full function; the observer decodes
them (see `design/WEBSOCKET_AND_POLICY_SPEC.md`).

## 4c. Enforce mode (the Interceptor)

Hosts and paths listed in `interceptor.enforce` go through a second ext_proc
filter in full-duplex mode instead. Envoy forwards only what the observer
returns, so a customer's Interceptor can allow, rewrite or refuse each call
(`docs/INTERCEPTOR.md`). That puts the observer, and the Interceptor, in the
request path for those calls:

- **Failure:** enforced calls are refused when the observer is down (Envoy
  fails closed) or when the Interceptor misses its deadline or fails
  (`onError: deny`, the default; `allow` lets them through). Observed calls are
  never affected.
- **Availability:** with enforce scopes set, the chart requires at least 2
  observer replicas and adds a PodDisruptionBudget. Run 3 or more, and the
  Interceptor with the same availability, close to the observer.
- **Latency:** a request is held until its body has arrived, then each
  callback costs a round trip to the Interceptor. With Interceptor content on,
  that's one round trip per event of a streamed response.
- **Scope:** start narrow (one path), and check the verdicts recorded in
  observe mode first. Rolling back is removing the scope.

## 5. Health check (kill-switch signal)

The observer probes every allowlisted host through Envoy every
`observer.pathProbe.interval`. Each probe does a TLS handshake with the
provider's SNI, verified against the interception root, checks that the served
leaf has at least `minLeafValidity` left, and sends one request that must be
answered by the real provider.

- `GET http://observer:9001/healthz/path` → `200` when every host passes,
  `503` otherwise, with JSON per-host detail. (`/healthz` stays the observer's
  own liveness.)
- Set `observer.pathProbe.target` to the LB address to include the LB in the
  check.
- Expose it (e.g. an ingress to that path) when an external checker such as a
  Cloudflare health check needs to poll it. Wiring that checker to disable
  steering is outside this chart.

## 5a. Ingest pods: capacity, data completeness, shutdown

Each `envoy` pod is the whole ingest stack: Envoy plus the observer, its
ext_proc server, over localhost (docs/PIPELINE.md §1, §2.1). They
scale together on Envoy's autoscaling (`envoy.autoscaling`); size
`observer.resources` for the traffic one Envoy pod carries.

- **Memory budget.** Each observer measures the bytes it holds for traffic in
  flight (buffered bodies, WebSocket decoder state and turns, bodies queued
  for extraction) against a budget: `observer.memoryBudget`, default half the
  observer's memory limit. `GOMEMLIMIT` defaults to 90% of the limit. `/stats`
  shows `memory.held_bytes`, `peak_bytes`, `limit_bytes`, `utilization`. The
  budget only measures: nothing refuses traffic on it, and no autoscaler reads
  it (KEDA scales Envoy on active connections, `mode: cpu` on CPU). Watch it to
  size the limit.
- **Size the observer's memory limit** (`observer.resources.limits.memory`,
  default 1Gi; the request stays at 64Mi). The limit is the only bound on
  what the observer holds: past it the container is OOM-killed, and Envoy's
  ext_proc fails open, so traffic keeps flowing **unrecorded** until the
  observer restarts. LLM traffic is light on CPU for the bytes it holds (long
  streamed replies, request bodies of several MB), so memory can fill before
  a CPU autoscaler reacts. The observer holds about **4 seconds of its pod's
  throughput** in flight (bodies are buffered until parsed), so its memory is
  roughly `4 s × per-pod throughput`, plus Go's overhead: allow as much again
  for the garbage collector, and about 64 MiB for the runtime.
  - Example: 100 MB/s across 3 envoy pods is about 33 MB/s per pod, so about
    135 MB in flight, and a limit of `2 × 135 MB + 64 MiB`, about 320 MiB:
    over the old 256Mi default. One user at 1 MB/s holds about 4 MB
    (`peak_bytes`). The 1Gi default covers about 100 MB/s per pod. Check
    `peak_bytes` against `limit_bytes` on `/stats` under real load.
- **Nothing captured is dropped.** All output goes through one writer, which
  seals what it holds every `objectStore.maxAgeSeconds` (10 s), and each raw
  file at `observer.rawFiles.flushBytes` (8 MiB) or `flushSeconds` (30 s),
  into an on-disk spool (an emptyDir, `observer.spool`) and uploads from there,
  retrying each file until the store accepts it. A full event buffer makes the
  observer wait rather than drop. `/stats` shows `writer.spool_files`,
  `spool_bytes`, `oldest_spool_age_seconds`, `upload_failures` and `full`.
- **When the spool is full** (past 95% of `observer.spool.maxBytes`, until it
  falls under 80%): observed (async) traffic keeps flowing and new events are
  dropped as a counted last resort (`LastResortDrops` on `/stats`; alert on any
  value above zero). Enforced calls are refused per call, so none goes
  through unrecorded. Readiness never depends on the spool: it gates the whole
  pod, and observed traffic must keep flowing, so alert on `writer.full` and
  `LastResortDrops` instead. Size the spool for the longest object-store outage
  you want to ride out: at peak, about the observer's write rate times the
  outage.
- **What a hard kill can lose.** On SIGKILL (OOM, node failure, spot
  reclaim) the observer loses its unsealed buffer: at most `maxAgeSeconds` of
  data. Sealed files are in the spool and are uploaded by the restarted
  container. If the node itself is lost, the emptyDir goes with it, along with
  anything not yet uploaded.
- **Shutdown order.** Envoy's preStop drains, then exits (closing what's still
  open, WebSockets included). The observer's preStop (`/prestop`) returns only
  once Envoy has exited, so every stream Envoy serves while draining is
  recorded; then the observer drains, flushes its writer and exits. The grace
  period covers Envoy's drain plus `observer.flushSeconds`, at least
  `observer.terminationGracePeriodSeconds` (300).

## 5b. Query API capacity

The API (`api.enabled`) is stateless (docs/PIPELINE.md §6): each request reads
its rows from Postgres in one snapshot, through the API login (the Secret's
`api-url`), and the drawer reads a call's raw member with one ranged GET of its
raw file, so it keeps nothing, and an HPA on CPU sets its replica count
(`api.stateless`):
- **Startup and readiness:** the API refuses to start without its DSN. It
  checks the schema's hash every few seconds and answers 503 until the
  database holds this build's schema (on an upgrade that changes it, the
  workers reset the database and reload from the raw files: just
  `helm upgrade`, docs/DEPLOY_DOKS.md "Upgrading"). Its readiness probe,
  `/api/v1/status`, reads Postgres, so a pod that can't reach it, or whose
  schema isn't there, leaves the Service; its liveness probe, `/healthz`,
  never reads the database.
- **Memory:** request = limit (`api.resources`, 1.5 Gi), so the scheduler
  reserves all of it and the pod is the last evicted. A page reads tens to a
  few hundred rows (docs/PIPELINE.md §6).
- **Disk:** none: the root filesystem is read-only and the pod mounts no volume.
- **Size the nodes for the requests:** a node runs short when the pods on it
  use more than it has, and the kubelet then evicts whichever pod is furthest
  over its request. See `docs/INTERCEPTION_ISSUES.md` P5.

## 5b2. Workers and Postgres (docs/PIPELINE.md §2, §6)

- **Workers** (`worker.*`, docs/PIPELINE.md §2.2, §5): the `worker` Deployment
  lists the observers' raw files under `<prefix>/raw/` (one replica at a time),
  loads each into Postgres's fact tables in one transaction, and refreshes the
  rollup rows the load marked dirty. One replica at a time also sweeps calls
  that never ended and runs retention. KEDA scales it on `GET /lag` (the age of
  the oldest unloaded file or dirty rollup row, target
  `worker.autoscaling.lagTargetSeconds`); with `worker.autoscaling.keda: false`
  a CPU HPA does. Ingest never talks to Postgres: an outage only delays the
  pages, and the workers catch up from the bucket.
- **Without KEDA** (`worker.autoscaling.keda: false`), set
  `worker.autoscaling.minReplicas` to at least 2. The workers mostly wait on
  S3 and Postgres, so their CPU may never cross the HPA's target while `/lag`
  grows, and a single replica falls behind unnoticed.
- **Postgres: RDS for PostgreSQL 17** (`postgres.inCluster.enabled: false`, the
  default). You create it (the chart doesn't): gp3 with storage autoscaling,
  Multi-AZ as you choose; today's load needs the smallest class, and
  docs/PIPELINE.md's scaling budget sizes it for 1,500 people.
  1. **Parameter group:** `max_connections` ≥ 200 (the chart's in-cluster
     setting).
  2. **The database,** with the built-in `C.UTF-8` locale (text sorts and
     lowercases as the code expects), as the master user:
     `CREATE DATABASE aintercept LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8' TEMPLATE template0;`
  3. **The two logins,** connected to that database as the master user:
     `psql -v worker_password=… -v api_password=… -f aintercept/internal/pg/logins.sql`
     (from a shell that keeps the passwords out of its history). It creates
     `aintercept_worker` (owns the schema; the workers apply it on first start)
     and `aintercept_api` (read only), with their timeouts. The master user
     isn't a superuser on RDS, so the file first grants it membership in
     `aintercept_worker`: Postgres 16+ needs that to make the login the
     database's owner (without it: "must be able to SET ROLE").
  4. **The CA bundle:** every connection uses TLS. Put the RDS CA bundle in a
     ConfigMap (`kubectl create configmap rds-ca --from-file=rds-ca.pem=global-bundle.pem`)
     and set `postgres.caConfigMap: rds-ca`; the workers and the API mount it
     at `/etc/aintercept/postgres-ca`. On `cloud: aws` the chart refuses to
     render without it.
  5. **The Secret** (`postgres.existingSecret`, default `aintercept-postgres`)
     with one DSN per component, created out of band, never through `--set`:
     `worker-url` and `api-url`, each
     `postgres://<login>:<password>@<endpoint>:5432/aintercept?sslmode=verify-full&sslrootcert=/etc/aintercept/postgres-ca/rds-ca.pem&keepalives_idle=10&keepalives_interval=5&keepalives_count=3&tcp_user_timeout=30000`.
     Each pod reads only its own component's key; ingest reads none. On
     `cloud: aws` the chart refuses to render with the name empty; it can't
     check that the Secret exists or holds both keys (under GitOps it may not
     exist yet), and a missing one shows as workers and API pods that don't
     start.
  6. **Changing the Secret** (a password rotation, a new endpoint): the
     workers and the API read their DSNs at start, so pods already running
     keep the old ones. Update the Secret, then `helm upgrade` with a new
     `postgres.secretVersion` (any string, e.g. the date): the chart writes it
     into both pod templates, which rolls them. Or roll them on your own
     annotation, e.g. a checksum of the Secret, with `worker.podAnnotations`
     and `api.podAnnotations`. Keep the old password valid until the roll
     finishes.
- **Postgres in the cluster** (`postgres.inCluster.enabled: true`, the
  DigitalOcean demo): one replica with a PVC (`postgres.inCluster.persistence`,
  20 Gi), non-root with a read-only root filesystem. On its first start its
  init script creates the database and the two logins with the passwords in the
  Secret, which `deploy/digitalocean-bootstrap.sh` makes (docs/DEPLOY_DOKS.md).
  Only the workers and the API may connect (NetworkPolicy `postgres-restrict`).
  Its data is the demo's: it isn't backed up.
- **Retention** (`retention.*`, docs/PIPELINE.md §5.5): every hour one worker
  deletes the facts older than `retention.days` (15), and their rollup rows.
  "All" in the UI is that window. The raw files (`<prefix>/raw/`) aren't the
  workers' to delete: a bucket **lifecycle rule** on `<prefix>/raw/` expires
  them (`deploy/terraform`: `raw_retention_days`, 15 by default, to match).
- **Upgrading from a release before the workers** (the processor and DuckDB):
  - **The observer writes somewhere new.** Raw files go under `<prefix>/raw/`
    (before: `<prefix>/dt=…`). An observer role scoped to the old layout gets
    403 on every write and records nothing, with traffic unaffected, so
    widen it **before** the upgrade (keep the old prefix too until every pod
    has rolled). The observer now runs in the envoy pods, as the observer's
    ServiceAccount.
  - **Roles:** the `process` role goes; the worker needs its own (§7). The
    API now reads only `<prefix>/raw/`.
  - **Nothing is migrated.** The workers load `<prefix>/raw/` only, so the UI
    starts empty at the upgrade, and anything recorded between creating the
    database and the upgrade (the old layout) isn't shown. The old
    `<prefix>/shaped/` tables stay readable for a rollback; expire them, and
    the old raw partitions, with lifecycle rules once you're past it.
  - **Order:** the database, its logins and the Secret have to exist first,
    so the AWS side changes before the release does. Once the old processor
    and the API's mirror lose their access they stop refreshing, and the old
    UI shows what it last loaded until the new release replaces it.

## 5c. Rollouts, disruption and placement

- **Envoy drains in its preStop hook.** On SIGTERM Envoy closes its listeners
  at once; `--drain-time-s` only shapes a drain that has already been started.
  So the hook sleeps `envoy.drain.preStopSleepSeconds` (the LB stops sending
  new connections), calls `POST /drain_listeners?graceful`, and waits until no
  request is in flight on the proxied listener, up to
  `envoy.drain.drainTimeSeconds`. SIGTERM comes after. A rollout, scale-down or
  node replacement therefore lets streaming turns finish instead of cutting
  them. Keep the LB's own deregistration delay at least as long (AWS NLB:
  `deregistration_delay.timeout_seconds`).
- **Disruption budgets:** `<component>.podDisruptionBudget.enabled` (off by
  default) adds a PDB, so node drains and autoscaler consolidation never take a
  component to zero.
- **Placement:** every component takes `nodeSelector`, `tolerations`,
  `affinity` and `topologySpreadConstraints`. Keep Envoy off interruptible
  (spot/preemptible) capacity: an interruption ends every stream on the node.
- **Load balancer annotations:** `envoy.service.annotations` is merged over the
  per-cloud defaults, so a key there replaces the default. On AWS with the
  Load Balancer Controller, set `aws-load-balancer-type: external` (plus scheme,
  subnets and target type) to get an LBC-managed NLB instead of the legacy
  in-tree one. `envoy.service.loadBalancerSourceRanges` limits who can reach it.
- **Upstream resolvers:** `envoy.upstreamDnsResolvers` (default 1.1.1.1 /
  8.8.8.8). They must never see the client-side DNS override. A VPC's own
  resolver qualifies and avoids outbound UDP/53.

## 5d. Giving people the UI (in-cluster, behind an identity-aware proxy)

The API image also serves the built web UI: the UI at `/`, the API at
`/api/v1`, on one origin. So with `api.enabled`, the `api` Service is the whole
app, with no port-forward and no local Node. (`make up` and `make remote` still
run the UI's dev server locally, as before.)

The UI shows **per-person usage of AI tools**, and with
`interceptionMode: full`, what people sent. Give it to a small group (security,
engineering leadership), only through an identity-aware proxy such as
Cloudflare Access, Google IAP or oauth2-proxy, and never publicly. AIntercept
has no login of its own.

**Values:**

```yaml
api:
  enabled: true
  service:
    type: ClusterIP            # or LoadBalancer + the cloud's internal-LB annotation
    annotations: {}
  networkPolicy:
    ingressFrom:               # who may connect; empty = no pod (port-forward only)
      - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: cloudflared } }
  httpRoute:                   # optional, for Gateway API clusters
    enabled: true
    parentRefs: [{ name: internal-gateway, namespace: gateway }]
    hostnames: [aintercept.internal.example.com]
  auth:                        # verify the proxy's assertion (recommended)
    jwtHeader: Cf-Access-Jwt-Assertion
    jwksUrl: https://<team>.cloudflareaccess.com/cdn-cgi/access/certs
    audience: <application AUD tag>
    issuer: https://<team>.cloudflareaccess.com
```

- **Route the proxy to the `api` Service** on port 8787: a tunnel connector
  (`cloudflared`), an internal load balancer, or a Gateway (`httpRoute`). Then
  admit that component in `api.networkPolicy.ingressFrom`. By default no pod may
  connect.
- **Verify the proxy's assertion with `api.auth`.** If the proxy is
  misconfigured, or someone inside the network reaches the Service directly,
  requests without a valid signed assertion get **401**. The API checks:
  - the signature against the proxy's JWKS (keys are cached and refetched when
    rotated);
  - the audience, the issuer, if set, and the expiry.

  If the JWKS can't be fetched, requests get **503**: AIntercept fails closed.
  `/api/v1/status` stays open for probes.
- **Exposing requires the check:** the chart refuses
  `api.networkPolicy.ingressFrom`, a non-ClusterIP `api.service.type` or
  `api.httpRoute` without `api.auth`. Set `api.auth.allowUnauthenticated: true`
  only if something else verifies identity and nothing can reach the Service
  around it.
- **Set the issuer.** It's optional, but without it any token the JWKS signs
  for that audience is accepted; the API logs a warning at startup. The JWKS
  URL must be `https://`. Keys are cached for an hour and refetched early when
  a token names a new key ID (rotation), at most every 30 s.
- **Browser hardening:**
  - UI responses carry a strict Content-Security-Policy: everything from the
    same origin, no inline script, framing refused;
  - every response carries `X-Content-Type-Options: nosniff` and
    `Referrer-Policy: no-referrer`;
  - API responses are `Cache-Control: no-store`;
  - the API explorer (`/docs`, `/redoc`, `/openapi.json`) is off whenever the
    UI is served.
- **Access is logged:** each request's log line carries the verified identity
  (`identity`: the email claim, else the subject), so who looked at whose data
  is attributable.
- **Header names by provider:** `Cf-Access-Jwt-Assertion` (Cloudflare Access),
  `X-Goog-IAP-JWT-Assertion` (Google IAP; JWKS
  `https://www.gstatic.com/iap/verify/public_key-jwk`), and for oauth2-proxy
  with `--set-authorization-header`, `Authorization`: the ID token, signed by
  your identity provider (use the provider's JWKS; a `Bearer ` prefix is
  accepted).
- **The image:** `ghcr.io/blink-build-studios/aintercept-api`, published with
  every release (§4). To build your own, build from the repository root, since
  the UI is part of it: `docker build -f api/Dockerfile -t <registry>/aintercept-api:<tag> .`

## 6. Corporate vs personal accounts

`complete` events carry `provider_org_id`, taken from the provider's response
headers (`anthropic-organization-id`, `openai-organization`). Compare it against
the organization's own org IDs downstream.

## 7. AWS identity and secrets

Nothing in the chart provisions AWS resources; it consumes them.

| Component | AWS access it needs | Chart values |
|---|---|---|
| observer | `s3:PutObject` on `<prefix>/raw/*` and `<prefix>/schema-samples/*` (write-only); `secretsmanager:GetSecretValue` on the salt secret | `observer.serviceAccount`, `objectStore.*`, `observer.keyHashSaltSecretsManagerId` |
| certsvc | `kms:GetPublicKey`, `kms:Sign` on the intermediate's key | `certsvc.serviceAccount`, `certsvc.kms.keyId` |
| worker | `s3:ListBucket` on the bucket (prefix `<prefix>/raw/`), `s3:GetObject` on `<prefix>/raw/*`; Postgres through the Secret's `worker-url` | `worker.serviceAccount`, `postgres.*` |
| api | `s3:GetObject` on `<prefix>/raw/*` (a call's raw member, one ranged GET); Postgres through the Secret's `api-url` | `api.serviceAccount`, `postgres.*` |

- **EKS Pod Identity:** associate each role with its ServiceAccount
  (`aintercept-observer`, `aintercept-certsvc`, `aintercept-worker`,
  `aintercept-api` in the release namespace) and leave
  `roleArn` empty. **IRSA:** set `serviceAccount.roleArn` instead. Both use the
  standard AWS credential chain; no static keys.
- **Key-hash salt from Secrets Manager:** set
  `observer.keyHashSaltSecretsManagerId` to the secret's name or ARN. The secret
  is the salt itself or JSON with a `salt` field. It is read once at startup.
- `deploy/terraform/aws` is a reference for the S3 bucket and an IRSA role; it
  does not create the KMS key, the salt secret, or Pod Identity associations.

**Testing without AWS:** `make test-aws` runs the stack against local
emulators (local-kms for KMS, moto for Secrets Manager) via
`docker-compose.aws.yml`. certsvc signs with a KMS key that has no key file
on disk, the observer reads its salt from Secrets Manager, and the full smoke
test runs on top.


## 8. Privacy considerations to revisit (experimentation mode)

While we learn what clients send, some data is kept in full that a locked-down
production deployment should reconsider. Decided 2026-11-02: keep it as is
for now, analyze, then tighten.

- **Client analytics events are stored verbatim** (`client_event` rows from
  Claude Code's `/api/event_logging/v2/batch`). Most fields are counts,
  categories, IDs and hashes, but some carry fragments of user input:
  `additional_metadata.fileExtension` holds pieces of shell commands, and
  `head_sha` identifies the commit a user is on. **Open question:** can a
  secret (an API key typed into a shell command) reach these fields? Run a
  secret-pattern scan over captured `client_event` data before deciding what
  to drop or hash.
- **`metadata.user_id` is stored raw** alongside its parsed device, account
  and session IDs.
- **The DO demo cluster runs in full interception mode**
  (`interceptionMode: full`), writing complete request/response bodies into
  its raw files (`aintercept/raw/`) in the bucket. Switch it to `content-free`
  if traffic that shouldn't be recorded is going to pass through it; the files
  already written expire by the bucket's lifecycle rule on `raw/`, or delete
  them.
