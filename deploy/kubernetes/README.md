# Kubernetes deployment (M4)

> Status: committed manifests providing the M4 path. **Not yet exercised
> end-to-end** in a live cluster (see `docs/limitations.md`).

These manifests deploy the same components as the local Compose stack, honoring
FR-06: issuing-CA key in a **Secret** (never a ConfigMap), non-root containers,
resource requests/limits, and readiness/liveness probes.

## Prerequisites

Build and push the images (replace the registry):

```bash
export REG=your-registry.example.com/aintercept
docker build -t $REG/certsvc:dev ./certsvc && docker push $REG/certsvc:dev
docker build -t $REG/observer:dev ./observer && docker push $REG/observer:dev
# fixture is for local M1; a cluster test can reuse it or target a real upstream.
```

Update the `image:` fields in the manifests accordingly.

## Secrets (supplied externally, never committed)

The issuing intermediate key and chain are provided as a Secret. Create it from
your generated CA material (the root key must NOT be included):

```bash
kubectl create secret generic interception-ca \
  --from-file=intermediate-ca.crt=certs/intermediate-ca.crt \
  --from-file=intermediate-ca.key=certs/intermediate-ca.key \
  --from-file=ca-chain.crt=certs/ca-chain.crt
```

## Apply

```bash
kubectl apply -f deploy/kubernetes/
```

## Reaching the proxy / client DNS

A cluster Service can expose TCP/443 without binding a node's privileged port.
Reach it from a test client via LoadBalancer, NodePort, or `kubectl port-forward
svc/envoy 443:443`. **Kubernetes does not rewrite the external hostname** — the
test client still needs the DNS override + dev-root trust from
`docs/claude-code-test.md`. Envoy's upstream resolution and TLS verification
must continue to target the real provider, isolated from that override.
