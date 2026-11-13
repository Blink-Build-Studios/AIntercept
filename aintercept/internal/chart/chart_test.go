// Package chart tests the rendered Helm chart (deploy/helm/aintercept).
package chart

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

const chartDir = "../../../deploy/helm/aintercept"

// render runs `helm template` with the chart's required values plus extra
// --set flags, and returns the rendered objects. Without helm the test skips,
// unless CHART_TEST_REQUIRED is set (CI).
func render(t *testing.T, set ...string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		if os.Getenv("CHART_TEST_REQUIRED") != "" {
			t.Fatal("helm not found and CHART_TEST_REQUIRED is set")
		}
		t.Skip("helm not installed")
	}
	args := []string{"template", "t", filepath.Clean(chartDir),
		"--set", "objectStore.bucket=b", "--set", "objectStore.region=r", "--set", "observer.keyHashSalt=s", "--set", "cloud=aws"}
	for _, s := range set {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var d map[string]any
		if err := dec.Decode(&d); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode rendered chart: %v", err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
	return docs
}

func find(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] == kind && get(d, "metadata", "name") == name {
			return d
		}
	}
	return nil
}

// get walks nested maps and lists ("0" indexes a list).
func get(v any, path ...string) any {
	for _, p := range path {
		switch x := v.(type) {
		case map[string]any:
			v = x[p]
		case []any:
			i := 0
			for _, c := range p {
				i = i*10 + int(c-'0')
			}
			if i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func byName(list any) map[string]map[string]any {
	out := map[string]map[string]any{}
	items, _ := list.([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if n, ok := m["name"].(string); ok {
			out[n] = m
		}
	}
	return out
}

// Each ingest pod is the full stack: Envoy and the observer in one pod, the
// observer spooling to an emptyDir, both with preStop hooks, the observer with
// its own readiness path, and no separate observer Deployment or Services.
func TestIngestPodIsTheFullStack(t *testing.T) {
	docs := render(t)
	if find(docs, "Deployment", "observer") != nil {
		t.Error("a separate observer Deployment still exists")
	}
	if find(docs, "Service", "observer-pods") != nil {
		t.Error("the headless observer-pods Service still exists")
	}
	if find(docs, "ScaledObject", "observer") != nil || find(docs, "HorizontalPodAutoscaler", "observer") != nil {
		t.Error("the observer still has its own autoscaler")
	}
	envoy := find(docs, "Deployment", "envoy")
	if envoy == nil {
		t.Fatal("no envoy Deployment")
	}
	spec := get(envoy, "spec", "template", "spec")
	containers := byName(get(spec, "containers"))
	obs, env := containers["observer"], containers["envoy"]
	if obs == nil {
		t.Fatal("the observer is not a container in the Envoy pod")
	}
	vols := byName(get(spec, "volumes"))
	if get(vols["spool"], "emptyDir", "sizeLimit") == nil {
		t.Error("no spool emptyDir with a sizeLimit")
	}
	if get(byName(get(obs, "volumeMounts"))["spool"], "mountPath") != "/spool" {
		t.Error("the spool is not mounted at /spool")
	}
	envs := map[string]any{}
	for n, e := range byName(get(obs, "env")) {
		envs[n] = e["value"]
	}
	if envs["OBSERVER_SPOOL_DIR"] != "/spool" {
		t.Errorf("OBSERVER_SPOOL_DIR = %v, want /spool", envs["OBSERVER_SPOOL_DIR"])
	}
	// Per-key ingest buffers (docs/PIPELINE.md §1).
	for name, want := range map[string]string{"OBSERVER_KEY_FLUSH_BYTES": "1048576", "OBSERVER_KEY_FLUSH_SECONDS": "30",
		"OBSERVER_KEY_BUFFER_MAX_BYTES": "67108864"} {
		if v, ok := envs[name]; !ok || fmt.Sprint(v) != want {
			t.Errorf("%s = %v (set %v), want %q", name, v, ok, want)
		}
	}
	if envs["OBSERVER_S3_MAX_AGE_SECONDS"] != "10" {
		t.Errorf("OBSERVER_S3_MAX_AGE_SECONDS = %v, want 10", envs["OBSERVER_S3_MAX_AGE_SECONDS"])
	}
	if get(env, "lifecycle", "preStop") == nil {
		t.Error("Envoy has no preStop hook")
	}
	if get(obs, "lifecycle", "preStop") == nil {
		t.Error("the observer has no preStop hook")
	}
	if p := get(obs, "readinessProbe", "httpGet", "path"); p != "/healthz/ready" {
		t.Errorf("observer readiness path %v, want /healthz/ready", p)
	}
	if p := get(obs, "livenessProbe", "httpGet", "path"); p != "/healthz" {
		t.Errorf("observer liveness path %v, want /healthz", p)
	}
	if g, _ := get(spec, "terminationGracePeriodSeconds").(int); g < 300 {
		t.Errorf("terminationGracePeriodSeconds %d, want >= 300", g)
	}

	cm := find(docs, "ConfigMap", "envoy-config")
	if cm == nil {
		t.Fatal("no envoy-config")
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(get(cm, "data", "envoy.yaml").(string)), &cfg); err != nil {
		t.Fatal(err)
	}
	cl := byName(get(cfg, "static_resources", "clusters"))["observer"]
	if a := get(cl, "load_assignment", "endpoints", "0", "lb_endpoints", "0", "endpoint", "address", "socket_address", "address"); a != "127.0.0.1" {
		t.Errorf("Envoy's observer cluster points at %v, want 127.0.0.1", a)
	}
	if get(cl, "load_balancing_policy") != nil {
		t.Error("Envoy still weights observers (ORCA)")
	}
}

// Even with enforcement on, the observer never asks to be taken out of
// rotation when its spool is full: enforced calls are refused per stream, and
// observed traffic must keep flowing.
func TestFullSpoolNeverAffectsReadiness(t *testing.T) {
	docs := render(t,
		"interceptor.endpoint=ic:9100",
		"interceptor.enforce[0].host=api.openai.com",
		"interceptor.enforce[0].paths[0]=/v1/chat/completions")
	envoy := find(docs, "Deployment", "envoy")
	obs := byName(get(envoy, "spec", "template", "spec", "containers"))["observer"]
	if _, ok := byName(get(obs, "env"))["OBSERVER_UNREADY_WHEN_FULL"]; ok {
		t.Error("the observer is told to go unready when its spool is full; readiness must not depend on the spool")
	}
}
