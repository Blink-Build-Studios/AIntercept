#!/usr/bin/env bash
#
# digitalocean-bootstrap.sh — agent-runnable, push-button provisioning for the
# aintercept stack on DigitalOcean. Creates (idempotently) the telemetry Space, a
# bucket-scoped Spaces key, a DOKS cluster, and the in-cluster Secrets the Helm
# chart expects — so a `helm install` is the only remaining step.
#
# Requires: doctl (authenticated), kubectl, aws CLI, helm, openssl.
#
# Usage:
#   REGION=nyc3 SPACE=aintercept-telemetry CLUSTER=aintercept-demo \
#     bash deploy/digitalocean-bootstrap.sh
#
# Prints the exact `helm install` to run at the end.
#
#   bash deploy/digitalocean-bootstrap.sh postgres-secret
#
# creates only the in-cluster Postgres's Secret (docs/PIPELINE.md §6), in the current kubectl
# context: what an existing deployment needs before the `helm upgrade` that brings Postgres in
# (docs/DEPLOY_DOKS.md, "Upgrading to Postgres").
set -euo pipefail

PG_SECRET="${PG_SECRET:-aintercept-postgres}"

log() { echo "==> $*"; }

# The in-cluster Postgres's Secret: its three passwords (openssl rand) and one DSN per component
# (worker-url, api-url), as the chart reads them. Written to files in a private temporary
# directory and handed to kubectl from there, so no password is ever on a command line or
# printed. An existing Secret is kept: its passwords are the ones the Postgres volume was
# initialized with.
postgres_secret() {
  if kubectl get secret "$PG_SECRET" >/dev/null 2>&1; then
    log "Postgres Secret ${PG_SECRET} already exists (kept: its passwords are the database's)"
    return
  fi
  log "Creating Postgres Secret ${PG_SECRET} (generated passwords, never printed)"
  local dir ka
  dir="$(mktemp -d)"
  chmod 700 "$dir"
  ka="keepalives_idle=10&keepalives_interval=5&keepalives_count=3&tcp_user_timeout=30000"
  for k in postgres-password worker-password api-password; do
    (umask 077; openssl rand -hex 24 | tr -d '\n' >"${dir}/${k}")
  done
  (umask 077
    printf 'postgres://aintercept_worker:%s@postgres:5432/aintercept?sslmode=disable&%s' "$(cat "${dir}/worker-password")" "$ka" >"${dir}/worker-url"
    printf 'postgres://aintercept_api:%s@postgres:5432/aintercept?sslmode=disable&%s' "$(cat "${dir}/api-password")" "$ka" >"${dir}/api-url")
  kubectl create secret generic "$PG_SECRET" \
    --from-file=postgres-password="${dir}/postgres-password" \
    --from-file=worker-password="${dir}/worker-password" \
    --from-file=api-password="${dir}/api-password" \
    --from-file=worker-url="${dir}/worker-url" \
    --from-file=api-url="${dir}/api-url" >/dev/null || { rm -rf "$dir"; return 1; }
  rm -rf "$dir"
}

if [[ "${1:-}" == "postgres-secret" ]]; then
  postgres_secret
  exit 0
fi

REGION="${REGION:-nyc3}"
SPACE="${SPACE:?set SPACE (globally-unique Space name)}"
CLUSTER="${CLUSTER:-aintercept-demo}"
NODE_SIZE="${NODE_SIZE:-s-2vcpu-4gb}"
NODE_COUNT="${NODE_COUNT:-2}"
ENDPOINT="https://${REGION}.digitaloceanspaces.com"
CA_SECRET="${CA_SECRET:-interception-ca}"
CREDS_SECRET="${CREDS_SECRET:-aintercept-objectstore}"
CERTS_DIR="${CERTS_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/certs}"

# ── 1. Spaces bucket (via a temporary full-access key) ───────────────────────
log "Provisioning Space ${SPACE} in ${REGION}"
BOOT_JSON=$(doctl spaces keys create "aintercept-bootstrap-$$" --grants 'bucket=;permission=fullaccess' --output json)
BOOT_AK=$(echo "$BOOT_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["access_key"])')
BOOT_SK=$(echo "$BOOT_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["secret_key"])')
cleanup() { doctl spaces keys delete "aintercept-bootstrap-$$" --force >/dev/null 2>&1 || true; }
trap cleanup EXIT

if AWS_ACCESS_KEY_ID="$BOOT_AK" AWS_SECRET_ACCESS_KEY="$BOOT_SK" \
     aws s3api head-bucket --bucket "$SPACE" --endpoint-url "$ENDPOINT" >/dev/null 2>&1; then
  log "Space ${SPACE} already exists"
else
  AWS_ACCESS_KEY_ID="$BOOT_AK" AWS_SECRET_ACCESS_KEY="$BOOT_SK" \
    aws s3api create-bucket --bucket "$SPACE" --endpoint-url "$ENDPOINT"
  log "Created Space ${SPACE}"
fi

# ── 2. Scoped Spaces key for the observer (readwrite on just this bucket) ────
log "Minting scoped observer key"
SCOPED_JSON=$(doctl spaces keys create "aintercept-observer-${SPACE}" \
  --grants "bucket=${SPACE};permission=readwrite" --output json)
OBS_AK=$(echo "$SCOPED_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["access_key"])')
OBS_SK=$(echo "$SCOPED_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["secret_key"])')

# ── 3. DOKS cluster ──────────────────────────────────────────────────────────
if doctl kubernetes cluster get "$CLUSTER" >/dev/null 2>&1; then
  log "Cluster ${CLUSTER} already exists"
else
  log "Creating DOKS cluster ${CLUSTER} (this takes several minutes)"
  doctl kubernetes cluster create "$CLUSTER" \
    --region "$REGION" --version latest \
    --node-pool "name=default;size=${NODE_SIZE};count=${NODE_COUNT};auto-scale=true;min-nodes=${NODE_COUNT};max-nodes=6" \
    --wait
fi
doctl kubernetes cluster kubeconfig save "$CLUSTER" >/dev/null
log "kubeconfig set to ${CLUSTER}"

# ── 4. metrics-server (DOKS doesn't ship it; HPAs are dead without it) ───────
if kubectl get deploy metrics-server -n kube-system >/dev/null 2>&1; then
  log "metrics-server already installed"
else
  log "Installing metrics-server (required for HPAs on DOKS)"
  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
fi

# ── 5. In-cluster Secrets the chart expects ──────────────────────────────────
log "Creating interception-CA Secret from ${CERTS_DIR}"
kubectl create secret generic "$CA_SECRET" \
  --from-file=intermediate-ca.crt="${CERTS_DIR}/intermediate-ca.crt" \
  --from-file=intermediate-ca.key="${CERTS_DIR}/intermediate-ca.key" \
  --from-file=ca-chain.crt="${CERTS_DIR}/ca-chain.crt" \
  --dry-run=client -o yaml | kubectl apply -f -

log "Creating object-store credentials Secret ${CREDS_SECRET} (keeps keys off the helm CLI)"
kubectl create secret generic "$CREDS_SECRET" \
  --from-literal=accessKeyId="$OBS_AK" \
  --from-literal=secretAccessKey="$OBS_SK" \
  --dry-run=client -o yaml | kubectl apply -f -

postgres_secret

# ── Done: print the install command ──────────────────────────────────────────
cat <<EOF

────────────────────────────────────────────────────────────────────────────
Bootstrap complete. Now install the chart (credentials come from the Secret,
never the CLI):

  helm install aintercept deploy/helm/aintercept \\
    --set cloud=digitalocean \\
    --set images.aintercept.repository=<registry>/aintercept --set images.aintercept.tag=<tag> \\
    --set imagePullSecret=<registry-pull-secret> \\
    --set objectStore.bucket=${SPACE} \\
    --set objectStore.region=${REGION} \\
    --set objectStore.endpoint=${ENDPOINT} \\
    --set objectStore.existingSecret=${CREDS_SECRET} \\
    --set postgres.inCluster.enabled=true \\
    --set postgres.existingSecret=${PG_SECRET}
────────────────────────────────────────────────────────────────────────────
EOF
