package chart

import "testing"

// The stateless API (docs/STREAMING_PIPELINE.md §5.3): with api.stateless.enabled the API reads
// the bucketed tables from the object store itself, so it has no fixed replica count (an HPA
// on CPU scales it), no mirror sidecar and no table volumes, only a cache. Off by default until
// the cutover (§6); then the chart is as it was.
func TestStatelessAPI(t *testing.T) {
	docs := render(t, "api.enabled=true", "api.stateless.enabled=true")
	dep := find(docs, "Deployment", "api")
	if dep == nil {
		t.Fatal("no api Deployment")
	}
	if r := get(dep, "spec", "replicas"); r != nil {
		t.Errorf("replicas %v: the HPA owns the count", r)
	}
	cs := byName(get(dep, "spec", "template", "spec", "containers"))
	if _, ok := cs["mirror"]; ok {
		t.Error("the mirror sidecar is still there")
	}
	vols := byName(get(dep, "spec", "template", "spec", "volumes"))
	for _, v := range []string{"shaped", "db"} {
		if _, ok := vols[v]; ok {
			t.Errorf("volume %q is still there", v)
		}
	}
	if vols["cache"] == nil || get(vols["cache"], "emptyDir") == nil {
		t.Error("no cache emptyDir")
	}
	env := byName(get(cs["api"], "env"))
	for name, want := range map[string]string{"AINTERCEPT_STORE": "buckets", "AINTERCEPT_S3_BUCKET": "b", "AINTERCEPT_S3_REGION": "r"} {
		if env[name] == nil || env[name]["value"] != want {
			t.Errorf("env %s = %v, want %q", name, env[name], want)
		}
	}
	hpa := find(docs, "HorizontalPodAutoscaler", "api")
	if hpa == nil {
		t.Fatal("no HPA for the api")
	}
	if get(hpa, "spec", "scaleTargetRef", "name") != "api" || get(hpa, "spec", "metrics", "0", "resource", "name") != "cpu" {
		t.Errorf("HPA %v: want CPU on the api Deployment", hpa["spec"])
	}
}

func TestAPIIsUnchangedUntilTheCutover(t *testing.T) {
	docs := render(t, "api.enabled=true")
	dep := find(docs, "Deployment", "api")
	if dep == nil || get(dep, "spec", "replicas") != 1 {
		t.Fatalf("api Deployment: want one replica, got %v", get(dep, "spec", "replicas"))
	}
	if _, ok := byName(get(dep, "spec", "template", "spec", "containers"))["mirror"]; !ok {
		t.Error("the mirror sidecar is gone before the cutover")
	}
	if find(docs, "HorizontalPodAutoscaler", "api") != nil {
		t.Error("an HPA before the cutover")
	}
}
