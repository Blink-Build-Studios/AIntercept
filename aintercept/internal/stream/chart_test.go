package stream

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The chart renders the stream processor: a Deployment and Service, scaled by
// a KEDA ScaledObject polling /lag (metrics-api, valueLocation lag_seconds).
func TestChartRendersStreamWithLagScaling(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed")
	}
	chart := filepath.Join("..", "..", "..", "deploy", "helm", "aintercept")
	out, err := exec.Command(helm, "template", "t", chart,
		"--set", "objectStore.bucket=b", "--set", "objectStore.region=r", "--set", "objectStore.endpoint=https://e", "--set", "objectStore.existingSecret=s", "--set", "observer.keyHashSalt=x",
		"--set", "stream.enabled=true", "--set", "stream.autoscaling.enabled=true").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	docs := strings.Split(string(out), "\n---")
	find := func(kind, name string) string {
		for _, d := range docs {
			if strings.Contains(d, "kind: "+kind+"\n") && strings.Contains(d, "name: "+name+"\n") {
				return d
			}
		}
		t.Fatalf("no %s %s rendered", kind, name)
		return ""
	}
	dep := find("Deployment", "stream")
	for _, want := range []string{`args: ["stream"]`, "STREAM_BUCKET_BITS", "/healthz", "containerPort: 9003"} {
		if !strings.Contains(dep, want) {
			t.Errorf("stream Deployment lacks %q", want)
		}
	}
	if strings.Contains(dep, "replicas:") {
		t.Error("with autoscaling on, KEDA owns the replica count: no replicas field")
	}
	find("Service", "stream")
	so := find("ScaledObject", "stream")
	for _, want := range []string{"type: metrics-api", "/lag", "valueLocation: lag_seconds", "minReplicaCount: 1"} {
		if !strings.Contains(so, want) {
			t.Errorf("ScaledObject lacks %q", want)
		}
	}
	// Off by default: the default render has no stream.
	def, err := exec.Command(helm, "template", "t", chart,
		"--set", "objectStore.bucket=b", "--set", "objectStore.region=r", "--set", "objectStore.endpoint=https://e",
		"--set", "objectStore.existingSecret=s", "--set", "observer.keyHashSalt=x").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, def)
	}
	if strings.Contains(string(def), "name: stream\n") {
		t.Error("stream rendered while disabled")
	}
}
