package chart

import (
	"path/filepath"
	"strings"
	"testing"
)

// renderFails runs `helm template` expecting it to fail, and returns its
// output (the chart's fail message).
func renderFails(t *testing.T, set ...string) string {
	t.Helper()
	out, err := helmTemplate(t, set...).CombinedOutput()
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

// Identity: lookups on by default with the documented limits; off, a zero
// enforce wait and a provider list carry through; the Anthropic directory's
// admin key comes only from a Secret, and only when one is named.
func TestIdentityValues(t *testing.T) {
	env := envOf(container(render(t), "Deployment", "envoy", "observer"))
	for k, want := range map[string]string{
		"OBSERVER_IDENTITY_LOOKUP": "true", "OBSERVER_IDENTITY_MAX_WAIT_MS": "300", "OBSERVER_IDENTITY_ENFORCE_MAX_WAIT_MS": "300",
		"OBSERVER_IDENTITY_LOOKUP_TIMEOUT_MS": "2000", "OBSERVER_IDENTITY_LOOKUP_MAX_IN_FLIGHT": "8", "OBSERVER_IDENTITY_LOOKUP_RATE_PER_SECOND": "5",
		"OBSERVER_IDENTITY_LOOKUP_BURST": "20", "OBSERVER_IDENTITY_CACHE_MAX_ENTRIES": "100000", "OBSERVER_IDENTITY_CACHE_TTL": "24h",
		"OBSERVER_IDENTITY_CACHE_NEGATIVE_TTL": "1h", "OBSERVER_IDENTITY_CACHE_ERROR_TTL": "60s",
	} {
		if env[k] != want {
			t.Errorf("%s = %v, want %s", k, env[k], want)
		}
	}
	if _, ok := env["OBSERVER_IDENTITY_ANTHROPIC_ADMIN_KEY"]; ok {
		t.Error("an admin key without adminKeySecret")
	}
	if _, ok := env["OBSERVER_IDENTITY_TEST_ORIGIN"]; ok {
		t.Error("the chart sets the test-only origin")
	}

	obs := container(render(t, "observer.identityLookup.enabled=false", "observer.identityLookup.enforceMaxWaitMs=0",
		"observer.identityLookup.maxWaitMs=0", "observer.identityLookup.providers={anthropic,chatgpt}",
		"observer.identityDirectory.anthropic.adminKeySecret=anthropic-admin"), "Deployment", "envoy", "observer")
	env = envOf(obs)
	if env["OBSERVER_IDENTITY_LOOKUP"] != "false" || env["OBSERVER_IDENTITY_ENFORCE_MAX_WAIT_MS"] != "0" || env["OBSERVER_IDENTITY_MAX_WAIT_MS"] != "0" ||
		env["OBSERVER_IDENTITY_LOOKUP_PROVIDERS"] != "anthropic,chatgpt" || env["OBSERVER_IDENTITY_DIRECTORY_INTERVAL"] != "15m" {
		t.Errorf("env %v", env)
	}
	if got := secretRef(obs, "OBSERVER_IDENTITY_ANTHROPIC_ADMIN_KEY"); got != "anthropic-admin/adminKey" {
		t.Errorf("admin key from %q, want anthropic-admin/adminKey", got)
	}
}
