package chart

import (
	"fmt"
	"strings"
	"testing"
)

// v5 (docs/PIPELINE.md): Redis runs in the cluster, one replica with a small
// persistent volume and append-only persistence, for coordination only.
func TestRedisStatefulSet(t *testing.T) {
	docs := render(t)
	ss := find(docs, "StatefulSet", "redis")
	if ss == nil {
		t.Fatal("no redis StatefulSet")
	}
	if r := get(ss, "spec", "replicas"); r != 1 {
		t.Errorf("redis replicas %v, want 1", r)
	}
	if s := get(ss, "spec", "volumeClaimTemplates", "0", "spec", "resources", "requests", "storage"); s != "1Gi" {
		t.Errorf("redis PVC size %v, want 1Gi", s)
	}
	c := byName(get(ss, "spec", "template", "spec", "containers"))["redis"]
	if c == nil {
		t.Fatal("no redis container")
	}
	img, _ := c["image"].(string)
	if !strings.Contains(img, ":") || strings.HasSuffix(img, ":latest") {
		t.Errorf("redis image %q is not pinned to an exact version", img)
	}
	args := fmt.Sprint(get(c, "args"))
	if !strings.Contains(args, "--appendonly yes") && !strings.Contains(args, "--appendonly") {
		t.Errorf("redis args %s: want --appendonly yes", args)
	}
	if find(docs, "Service", "redis") == nil {
		t.Error("no redis Service")
	}
	if docs := render(t, "redis.persistence.size=2Gi"); get(find(docs, "StatefulSet", "redis"), "spec", "volumeClaimTemplates", "0", "spec", "resources", "requests", "storage") != "2Gi" {
		t.Error("redis.persistence.size is not honoured")
	}
}

func envOf(c map[string]any) map[string]any {
	out := map[string]any{}
	for n, e := range byName(get(c, "env")) {
		out[n] = e["value"]
	}
	return out
}

// The rollup Deployment (docs/PIPELINE.md §2): `aintercept rollup`, at least two
// replicas, scaled by KEDA on GET /lag (oldest_unapplied_seconds), with a plain
// HPA when KEDA is off.
func TestRollupDeployment(t *testing.T) {
	docs := render(t)
	dep := find(docs, "Deployment", "rollup")
	if dep == nil {
		t.Fatal("no rollup Deployment")
	}
	c := byName(get(dep, "spec", "template", "spec", "containers"))["rollup"]
	if c == nil {
		t.Fatal("no rollup container")
	}
	if a := fmt.Sprint(get(c, "args")); a != "[rollup]" {
		t.Errorf("rollup args %s, want [rollup]", a)
	}
	env := envOf(c)
	for name, want := range map[string]string{"ROLLUP_REDIS_ADDR": "redis:6379", "ROLLUP_S3_BUCKET": "b", "ROLLUP_S3_REGION": "r"} {
		if fmt.Sprint(env[name]) != want {
			t.Errorf("%s = %v, want %q", name, env[name], want)
		}
	}
	if p := get(c, "readinessProbe", "httpGet", "path"); p != "/healthz" {
		t.Errorf("rollup readiness %v, want /healthz", p)
	}
	if p := get(c, "livenessProbe", "httpGet", "path"); p != "/healthz" {
		t.Errorf("rollup liveness %v, want /healthz", p)
	}
	so := find(docs, "ScaledObject", "rollup")
	if so == nil {
		t.Fatal("no rollup ScaledObject")
	}
	if m, _ := get(so, "spec", "minReplicaCount").(int); m < 2 {
		t.Errorf("rollup minReplicaCount %v, want >= 2", get(so, "spec", "minReplicaCount"))
	}
	if typ := get(so, "spec", "triggers", "0", "type"); typ != "metrics-api" {
		t.Errorf("rollup trigger %v, want metrics-api", typ)
	}
	if u, _ := get(so, "spec", "triggers", "0", "metadata", "url").(string); !strings.HasSuffix(u, "/lag") {
		t.Errorf("rollup trigger url %q, want .../lag", u)
	}
	if v := get(so, "spec", "triggers", "0", "metadata", "valueLocation"); v != "oldest_unapplied_seconds" {
		t.Errorf("rollup valueLocation %v, want oldest_unapplied_seconds", v)
	}
	if find(docs, "Service", "rollup") == nil {
		t.Error("no rollup Service for /lag")
	}

	docs = render(t, "rollup.autoscaling.keda=false")
	if find(docs, "ScaledObject", "rollup") != nil {
		t.Error("a ScaledObject with KEDA off")
	}
	hpa := find(docs, "HorizontalPodAutoscaler", "rollup")
	if hpa == nil {
		t.Fatal("no fallback HPA for the rollup with KEDA off")
	}
	if m, _ := get(hpa, "spec", "minReplicas").(int); m < 2 {
		t.Errorf("rollup HPA minReplicas %v, want >= 2", get(hpa, "spec", "minReplicas"))
	}
}

// The observer flags dirty keys in the in-cluster Redis.
func TestObserverPointsAtRedis(t *testing.T) {
	envoy := find(render(t), "Deployment", "envoy")
	obs := byName(get(envoy, "spec", "template", "spec", "containers"))["observer"]
	if v := envOf(obs)["OBSERVER_REDIS_ADDR"]; v != "redis:6379" {
		t.Errorf("OBSERVER_REDIS_ADDR = %v, want redis:6379", v)
	}
}

// The old path is gone: no single processor, no bucketed processor, and the API
// never copies data to local disk (no mirror sidecar, no table volumes).
func TestOldPathIsGone(t *testing.T) {
	docs := render(t, "api.enabled=true")
	for _, n := range []string{"process", "stream"} {
		if find(docs, "Deployment", n) != nil {
			t.Errorf("a %s Deployment still exists", n)
		}
	}
	dep := find(docs, "Deployment", "api")
	if dep == nil {
		t.Fatal("no api Deployment")
	}
	if _, ok := byName(get(dep, "spec", "template", "spec", "containers"))["mirror"]; ok {
		t.Error("the api still has a mirror sidecar")
	}
	vols := byName(get(dep, "spec", "template", "spec", "volumes"))
	for _, v := range []string{"shaped", "db", "work"} {
		if _, ok := vols[v]; ok {
			t.Errorf("the api still has a %q volume", v)
		}
	}
}

// Only the observer (in the Envoy pod) and the rollup may reach Redis.
func TestRedisNetworkPolicy(t *testing.T) {
	np := find(render(t), "NetworkPolicy", "redis-restrict")
	if np == nil {
		t.Fatal("no redis-restrict NetworkPolicy")
	}
	if a := get(np, "spec", "podSelector", "matchLabels", "app"); a != "redis" {
		t.Errorf("podSelector app %v, want redis", a)
	}
	apps := map[any]bool{}
	for _, f := range get(np, "spec", "ingress", "0", "from").([]any) {
		apps[get(f, "podSelector", "matchLabels", "app")] = true
	}
	if !apps["envoy"] || !apps["rollup"] || len(apps) != 2 {
		t.Errorf("redis admits %v, want exactly envoy and rollup", apps)
	}
	if p := get(np, "spec", "ingress", "0", "ports", "0", "port"); p != 6379 {
		t.Errorf("redis port %v, want 6379", p)
	}
}
