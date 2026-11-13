package chart

import "testing"

// The API is stateless (docs/PIPELINE.md §4): it reads the object store's v1/
// folders itself, so it has no fixed replica count (an HPA on CPU scales it), no
// mirror sidecar and no table volumes, only scratch space.
func TestStatelessAPI(t *testing.T) {
	docs := render(t, "api.enabled=true")
	dep := find(docs, "Deployment", "api")
	if dep == nil {
		t.Fatal("no api Deployment")
	}
	if r := get(dep, "spec", "replicas"); r != nil {
		t.Errorf("replicas %v: the HPA owns the count", r)
	}
	cs := byName(get(dep, "spec", "template", "spec", "containers"))
	if len(cs) != 1 || cs["api"] == nil {
		t.Errorf("containers %v: want the api alone (no mirror sidecar)", cs)
	}
	vols := byName(get(dep, "spec", "template", "spec", "volumes"))
	for _, v := range []string{"shaped", "db", "work"} {
		if _, ok := vols[v]; ok {
			t.Errorf("volume %q is still there", v)
		}
	}
	if vols["cache"] == nil || get(vols["cache"], "emptyDir") == nil {
		t.Error("no scratch emptyDir")
	}
	env := byName(get(cs["api"], "env"))
	for name, want := range map[string]string{"AINTERCEPT_STORE": "v1", "AINTERCEPT_S3_BUCKET": "b", "AINTERCEPT_S3_REGION": "r"} {
		if env[name] == nil || env[name]["value"] != want {
			t.Errorf("env %s = %v, want %q", name, env[name], want)
		}
	}
	for _, gone := range []string{"AINTERCEPT_RAW_URL", "AINTERCEPT_SHAPED_DIR", "AINTERCEPT_DB_DIR"} {
		if env[gone] != nil {
			t.Errorf("env %s is still set", gone)
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
