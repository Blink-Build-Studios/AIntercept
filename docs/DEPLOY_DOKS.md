# Deploy to DigitalOcean Kubernetes (DOKS)

> Goal: a running aintercept stack on DOKS writing telemetry to a DigitalOcean
> Space, ready to route test traffic through.
>
> **Prereq floor (be honest):** you need `doctl` (authenticated), `kubectl`,
> `helm`, and `docker`. Standing up a fresh DOKS cluster is ~5–8 minutes by
> itself; the steps below assume that time. Spaces is only available in certain
> regions (nyc3, ams3, sgp1, sfo3, fra1, syd1, blr1) — the cluster and Space
> must be in the **same Spaces-capable region**.

## 1. Provision the Space + a scoped key

A Space (bucket) holds telemetry; a Spaces access key (separate from your API
token) lets the observer write to it.

```bash
REGION=nyc3
SPACE=aintercept-telemetry            # globally unique; pick your own
ENDPOINT="https://${REGION}.digitaloceanspaces.com"

# Mint a temporary full-access Spaces key to create the bucket:
doctl spaces keys create aintercept-bootstrap --grants 'bucket=;permission=fullaccess' --output json

# Create the Space via the S3 API with that key:
AWS_ACCESS_KEY_ID=<bootstrap-access-key> AWS_SECRET_ACCESS_KEY=<bootstrap-secret> \
  aws s3api create-bucket --bucket "$SPACE" --endpoint-url "$ENDPOINT"

# Mint the SCOPED key the observer will actually use (readwrite on just this Space):
doctl spaces keys create aintercept-observer \
  --grants "bucket=${SPACE};permission=readwrite" --output json
# → note the access_key and secret_key in the output.
```

(The `deploy/terraform/digitalocean` module provisions the cluster + bucket via
Terraform; the scoped Spaces key is this one `doctl` step, because the DO
Terraform provider does not yet manage per-bucket Spaces keys.)

## 2. Create the DOKS cluster

```bash
doctl kubernetes cluster create aintercept-demo \
  --region $REGION \
  --version latest \
  --node-pool "name=default;size=s-2vcpu-4gb;count=2"
# doctl saves kubeconfig and sets the context automatically on completion.
kubectl get nodes   # confirm Ready
```

## 3. Push images to a registry the cluster can pull

```bash
doctl registry login
REG=registry.digitalocean.com/<your-registry>
# One image; the Envoy pod's observer, certsvc and the worker Deployment (the workers) run it in
# their own mode. Postgres is the stock upstream image (images.postgres, pinned by digest), pulled
# from Docker Hub.
docker buildx build --platform linux/amd64 -t $REG/aintercept:dev --push ./aintercept
# Let the cluster pull from the DO registry:
doctl registry kubernetes-manifest | kubectl apply -f -   # creates the pull secret
```

## 4. Create the interception-CA Secret

The issuing intermediate key/chain is supplied as a Secret (never a ConfigMap).
Generate the dev CA if you haven't, then create the Secret:

```bash
bash scripts/generate-dev-ca.sh   # if certs/ is empty
kubectl create secret generic interception-ca \
  --from-file=intermediate-ca.crt=certs/intermediate-ca.crt \
  --from-file=intermediate-ca.key=certs/intermediate-ca.key \
  --from-file=ca-chain.crt=certs/ca-chain.crt
# The root PRIVATE key is NOT uploaded. Only clients install root-ca.crt (public).
```

## 5. Create the Secrets (keep credentials off the CLI)

Pass the Spaces credentials and the key-hash salt through Secrets, NOT through
`helm --set` (which leaks them into shell history, `/proc/<pid>/cmdline`, and the
Helm release metadata).

```bash
# Object-store credentials → referenced by objectStore.existingSecret:
kubectl create secret generic aintercept-objectstore \
  --from-literal=accessKeyId=<scoped-access-key> \
  --from-literal=secretAccessKey=<scoped-secret-key>

# Key-hash salt → referenced by observer.keyHashSaltSecret. Without a salt the
# install is refused (key_hash would be a reversible sha256(key)):
kubectl create secret generic aintercept-keyhash-salt \
  --from-literal=salt=$(openssl rand -hex 32)
```

Postgres runs in the cluster on the demo (`postgres.inCluster.enabled=true`; production uses
RDS, docs/PRODUCTION.md). Its Secret holds three generated passwords and one DSN per component
(the workers' `worker-url`, the API's `api-url`); each pod reads only its own, and ingest none:

```bash
bash deploy/digitalocean-bootstrap.sh postgres-secret   # aintercept-postgres; nothing printed
```

It keeps an existing Secret: the passwords are set when the Postgres volume is first initialized,
so a new Secret over an old volume would lock the workers out.

(The `deploy/digitalocean-bootstrap.sh` script does all of this for you.)

## 6. Install the Helm chart

```bash
helm install aintercept deploy/helm/aintercept \
  --set cloud=digitalocean \
  --set images.aintercept.repository=$REG/aintercept \
  --set imagePullSecret=registry-<your-registry> \
  --set objectStore.bucket=$SPACE \
  --set objectStore.region=$REGION \
  --set objectStore.endpoint=$ENDPOINT \
  --set objectStore.existingSecret=aintercept-objectstore \
  --set observer.keyHashSaltSecret=aintercept-keyhash-salt \
  --set postgres.inCluster.enabled=true

kubectl rollout status statefulset/postgres
kubectl rollout status deploy/worker
kubectl rollout status deploy/certsvc
kubectl rollout status deploy/envoy
kubectl get svc envoy -w   # wait for the LoadBalancer EXTERNAL-IP
```

## 6. Verify end-to-end

```bash
EXTERNAL_IP=$(kubectl get svc envoy -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

# Drive a request at an allowlisted host through the proxy, trusting the dev root:
curl -sS --cacert certs/root-ca.crt \
  --resolve api.anthropic.com:443:$EXTERNAL_IP \
  https://api.anthropic.com/v1/messages -X POST --data '{}' -i | head

# Confirm a raw file landed in the Space (within about observer.rawFiles.flushSeconds, 30 s):
AWS_ACCESS_KEY_ID=<scoped-access-key> AWS_SECRET_ACCESS_KEY=<scoped-secret> \
  aws s3 ls "s3://$SPACE/aintercept/raw/" --recursive --endpoint-url $ENDPOINT
```

You should see objects keyed like
`aintercept/raw/YYYY-MM-DD/HH/<pod>/<ns>-<seq>.ndjson.gz` (docs/PIPELINE.md §2.1).
Each pod writes under its own name, so replicas never clobber.

Then the workers: within seconds the request is a row in Postgres, and their lag settles under
30 s:

```bash
kubectl exec statefulset/postgres -- psql -qtA -U postgres -d aintercept \
  -c "SELECT count(*) FROM ingest_files WHERE loaded_at IS NOT NULL" -c "SELECT count(*) FROM calls"
kubectl run lag --rm -i --restart=Never --image=curlimages/curl -- -s http://worker:9005/lag
```

With the query API on (`api.enabled=true`), it reads Postgres too, through the Secret's
`api-url`. It checks the schema's hash every few seconds and answers 503 (unready) until the
database holds the schema the workers apply, so it's ready only once they've run. Then the request shows up in it:

```bash
kubectl rollout status deploy/api
kubectl port-forward svc/api 8787 &   # or make remote, which also starts the UI
ID=$(kubectl exec statefulset/postgres -- psql -qtA -U postgres -d aintercept \
  -c "SELECT call_id FROM calls WHERE host = 'api.anthropic.com' ORDER BY start_ns DESC NULLS LAST LIMIT 1")
curl -s "localhost:8787/api/v1/requests/$ID" | head -c 300; echo
curl -s localhost:8787/api/v1/status
```

## Upgrading

Always `helm upgrade`, **never `helm uninstall`**: uninstalling deletes the Envoy Service and its
load balancer (a new IP for every client), and once took down the tunnel people reach the UI
through.

A release that changes the schema is just `helm upgrade` too. The schema has no migrations: the
workers' Deployment is `Recreate` (every old worker stops before a new one starts), and the first new
worker finds another build's schema hash, drops the old schema and applies its own in one transaction
(with the grants `logins.sql` gives), logging an `ALARM` line. `ingest_files` starts empty, so the
workers reload every raw file still in `<prefix>/raw/` and the pages fill back in as they load. The API
is unready (503 on `/api/v1/status`) for the seconds the reset takes, never restarted, and ready again
once the database holds its schema. `worker.schemaReset: false` turns the reset off: the workers then
refuse another build's schema.

## Teardown

```bash
helm uninstall aintercept
doctl kubernetes cluster delete aintercept-demo
# Delete the Space + keys in the DO console / via the S3 API when done.
```
