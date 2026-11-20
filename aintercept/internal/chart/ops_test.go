package chart

import (
	"strings"
	"testing"
)

// podAnnotations of a Deployment's pod template.
func podAnnotations(docs []map[string]any, name string) map[string]any {
	a, _ := get(find(docs, "Deployment", name), "spec", "template", "metadata", "annotations").(map[string]any)
	return a
}

// The workers and the API take podAnnotations, like the other per-component
// values, so a deployer can roll them on their Secret's checksum or version.
func TestDSNReadersPodAnnotations(t *testing.T) {
	docs := render(t, "api.enabled=true")
	for _, n := range []string{"worker", "api"} {
		if a := podAnnotations(docs, n); len(a) != 0 {
			t.Errorf("%s: annotations %v by default, want none", n, a)
		}
	}
	docs = render(t, "api.enabled=true", "worker.podAnnotations.example\\.com/w=one", "api.podAnnotations.example\\.com/a=two")
	if v := podAnnotations(docs, "worker")["example.com/w"]; v != "one" {
		t.Errorf("worker annotation %v, want one", v)
	}
	if v := podAnnotations(docs, "api")["example.com/a"]; v != "two" {
		t.Errorf("api annotation %v, want two", v)
	}
}

// postgres.secretVersion is written into both pod templates, so changing it
// (after rotating the Secret in place) rolls the workers and the API.
func TestSecretVersionRollsDSNReaders(t *testing.T) {
	const key = "aintercept/postgres-secret-version"
	v1 := render(t, "api.enabled=true", "postgres.secretVersion=1", "worker.podAnnotations.keep=yes")
	v2 := render(t, "api.enabled=true", "postgres.secretVersion=2")
	for _, n := range []string{"worker", "api"} {
		if a, b := podAnnotations(v1, n)[key], podAnnotations(v2, n)[key]; a != "1" || b != "2" {
			t.Errorf("%s: %s = %v then %v, want \"1\" then \"2\"", n, key, a, b)
		}
	}
	if v := podAnnotations(v1, "worker")["keep"]; v != "yes" {
		t.Errorf("secretVersion replaced worker.podAnnotations: keep = %v", v)
	}
	if _, ok := podAnnotations(render(t, "api.enabled=true"), "worker")[key]; ok {
		t.Errorf("%s is set without postgres.secretVersion", key)
	}
}

// On AWS with RDS (postgres.inCluster.enabled false), the render fails without
// the RDS CA bundle or the DSN Secret's name, instead of a crash loop later.
func TestRDSGuards(t *testing.T) {
	for name, tc := range map[string]struct {
		set  []string
		want string
	}{
		"no CA bundle": {[]string{"postgres.caConfigMap="}, "postgres.caConfigMap"},
		"no Secret":    {[]string{"postgres.existingSecret="}, "postgres.existingSecret"},
	} {
		t.Run(name, func(t *testing.T) {
			if out := renderFails(t, tc.set...); !strings.Contains(out, tc.want) {
				t.Errorf("failure %q doesn't name %s", strings.TrimSpace(out), tc.want)
			}
		})
	}
	// Set, the render passes (render's defaults set both); in the cluster or on
	// DigitalOcean, the CA bundle isn't needed.
	render(t)
	render(t, "postgres.inCluster.enabled=true", "postgres.caConfigMap=")
	render(t, "cloud=digitalocean", "objectStore.endpoint=https://nyc3.digitaloceanspaces.com",
		"objectStore.existingSecret=os", "postgres.caConfigMap=")
}

// certsvc is Ready only once it has signed a leaf: its readiness probe asks
// its HTTP port, which the NetworkPolicy leaves open for the kubelet.
func TestCertsvcReadiness(t *testing.T) {
	docs := render(t)
	c := container(docs, "Deployment", "certsvc", "certsvc")
	if c == nil {
		t.Fatal("no certsvc Deployment")
	}
	if p := get(c, "readinessProbe", "httpGet", "path"); p != "/healthz/ready" {
		t.Errorf("certsvc readiness path %v, want /healthz/ready", p)
	}
	if p := get(c, "readinessProbe", "httpGet", "port"); p != 18001 {
		t.Errorf("certsvc readiness port %v, want 18001", p)
	}
	if v := envOf(c)["CERTSVC_HTTP_LISTEN"]; v != "0.0.0.0:18001" {
		t.Errorf("CERTSVC_HTTP_LISTEN = %v, want 0.0.0.0:18001", v)
	}
	open := false
	for _, r := range asList(get(find(docs, "NetworkPolicy", "certsvc-sds-restrict"), "spec", "ingress")) {
		if get(r, "from") == nil && get(r, "ports", "0", "port") == 18001 {
			open = true
		}
	}
	if !open {
		t.Error("certsvc's NetworkPolicy doesn't admit the kubelet's probe on 18001")
	}
}

// The observer's memory limit defaults to 1Gi (its budget, half the limit, to
// 512Mi); its request stays small.
func TestObserverMemoryDefaults(t *testing.T) {
	obs := container(render(t), "Deployment", "envoy", "observer")
	if l := get(obs, "resources", "limits", "memory"); l != "1Gi" {
		t.Errorf("observer memory limit %v, want 1Gi", l)
	}
	if r := get(obs, "resources", "requests", "memory"); r != "64Mi" {
		t.Errorf("observer memory request %v, want 64Mi", r)
	}
}
