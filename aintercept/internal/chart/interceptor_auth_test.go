package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// renderFails runs `helm template` expecting it to fail, and returns its
// output (the chart's fail message).
func renderFails(t *testing.T, set ...string) string {
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
	if err == nil {
		t.Fatalf("helm template succeeded with %v, want a failure", set)
	}
	return string(out)
}

const tokenFile = "/var/run/secrets/aintercept/interceptor/token"

// serviceAccountToken: the observer reads its own projected service-account
// token, for the Interceptor's audience only, from a file the kubelet renews.
func TestInterceptorServiceAccountToken(t *testing.T) {
	docs := render(t, "interceptor.endpoint=decision:443",
		"interceptor.serviceAccountToken.enabled=true", "interceptor.serviceAccountToken.audience=decision",
		"interceptor.serviceAccountToken.expirationSeconds=900")
	obs := container(docs, "Deployment", "envoy", "observer")
	env := envOf(obs)
	if env["INTERCEPTOR_TOKEN_FILE"] != tokenFile {
		t.Errorf("INTERCEPTOR_TOKEN_FILE = %v, want %s", env["INTERCEPTOR_TOKEN_FILE"], tokenFile)
	}
	if _, ok := byName(get(obs, "env"))["INTERCEPTOR_TOKEN"]; ok {
		t.Error("a static INTERCEPTOR_TOKEN is set too")
	}
	m := byName(get(obs, "volumeMounts"))["interceptor-token"]
	if m == nil || get(m, "mountPath") != filepath.Dir(tokenFile) || get(m, "readOnly") != true {
		t.Errorf("observer mount: %v", m)
	}
	v := byName(get(find(docs, "Deployment", "envoy"), "spec", "template", "spec", "volumes"))["interceptor-token"]
	src := get(v, "projected", "sources", "0", "serviceAccountToken")
	if get(src, "path") != "token" || get(src, "audience") != "decision" || get(src, "expirationSeconds") != 900 {
		t.Errorf("projected token: %v", src)
	}
	// Only the observer gets it: not envoy, which faces every client.
	if m := byName(get(container(docs, "Deployment", "envoy", "envoy"), "volumeMounts"))["interceptor-token"]; m != nil {
		t.Error("the envoy container mounts the Interceptor token")
	}
}

// tokenSecret is unchanged, and without an endpoint neither credential is
// wired at all.
func TestInterceptorTokenSecret(t *testing.T) {
	docs := render(t, "interceptor.endpoint=decision:443", "interceptor.tokenSecret=ic")
	obs := container(docs, "Deployment", "envoy", "observer")
	if got := secretRef(obs, "INTERCEPTOR_TOKEN"); got != "ic/token" {
		t.Errorf("INTERCEPTOR_TOKEN from %q, want ic/token", got)
	}
	if _, ok := envOf(obs)["INTERCEPTOR_TOKEN_FILE"]; ok {
		t.Error("INTERCEPTOR_TOKEN_FILE set with tokenSecret")
	}
	if v := byName(get(find(docs, "Deployment", "envoy"), "spec", "template", "spec", "volumes"))["interceptor-token"]; v != nil {
		t.Error("a projected token without serviceAccountToken")
	}

	docs = render(t, "interceptor.serviceAccountToken.enabled=true", "interceptor.serviceAccountToken.audience=decision")
	obs = container(docs, "Deployment", "envoy", "observer")
	if _, ok := envOf(obs)["INTERCEPTOR_TOKEN_FILE"]; ok {
		t.Error("INTERCEPTOR_TOKEN_FILE set without an endpoint")
	}
	if v := byName(get(find(docs, "Deployment", "envoy"), "spec", "template", "spec", "volumes"))["interceptor-token"]; v != nil {
		t.Error("a projected token without an endpoint")
	}
}

func TestInterceptorAuthGuards(t *testing.T) {
	for name, tc := range map[string]struct {
		set  []string
		want string
	}{
		"both": {[]string{"interceptor.endpoint=d:443", "interceptor.tokenSecret=ic",
			"interceptor.serviceAccountToken.enabled=true", "interceptor.serviceAccountToken.audience=d"}, "not both"},
		"no audience": {[]string{"interceptor.endpoint=d:443", "interceptor.serviceAccountToken.enabled=true"}, "audience is required"},
		"short-lived": {[]string{"interceptor.endpoint=d:443", "interceptor.serviceAccountToken.enabled=true",
			"interceptor.serviceAccountToken.audience=d", "interceptor.serviceAccountToken.expirationSeconds=300"}, "at least 600"},
	} {
		t.Run(name, func(t *testing.T) {
			if out := renderFails(t, tc.set...); !strings.Contains(out, tc.want) {
				t.Errorf("failure %q doesn't say %q", strings.TrimSpace(out), tc.want)
			}
		})
	}
}
