package chart

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The v7 pipeline in the chart (docs/PIPELINE.md §6): the workers (`rollup`)
// and the API read Postgres through a DSN each, from one Secret; Postgres runs
// in the cluster on the DigitalOcean demo (postgres.inCluster) and is RDS in
// production; Redis is gone.

func envOf(c map[string]any) map[string]any {
	out := map[string]any{}
	for n, e := range byName(get(c, "env")) {
		out[n] = e["value"]
	}
	return out
}

// secretRef is an env var's secretKeyRef as "secret/key" ("" if it has none).
func secretRef(c map[string]any, name string) string {
	e := byName(get(c, "env"))[name]
	if ref := get(e, "valueFrom", "secretKeyRef"); ref != nil {
		return fmt.Sprintf("%v/%v", get(ref, "name"), get(ref, "key"))
	}
	return ""
}

// secretRefs is every secret/key a container's env reads.
func secretRefs(c map[string]any) []string {
	var out []string
	for n := range byName(get(c, "env")) {
		if r := secretRef(c, n); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func container(docs []map[string]any, kind, name, c string) map[string]any {
	return byName(get(find(docs, kind, name), "spec", "template", "spec", "containers"))[c]
}

// No Redis anywhere: no StatefulSet, Service or NetworkPolicy, and no
// component is given an address for it.
func TestNoRedis(t *testing.T) {
	docs := render(t, "api.enabled=true", "postgres.inCluster.enabled=true")
	for _, d := range docs {
		if n, _ := get(d, "metadata", "name").(string); strings.Contains(n, "redis") {
			t.Errorf("%v %s is still rendered", d["kind"], n)
		}
		for _, c := range asList(get(d, "spec", "template", "spec", "containers")) {
			for name, v := range envOf(c.(map[string]any)) {
				if strings.Contains(strings.ToUpper(name+" "+fmt.Sprint(v)), "REDIS") {
					t.Errorf("%v %v: %s = %v", d["kind"], get(d, "metadata", "name"), name, v)
				}
			}
		}
	}
	values, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?im)^\s*redis`).Match(values) {
		t.Error("values.yaml still has Redis settings")
	}
}

func asList(v any) []any { l, _ := v.([]any); return l }

// The workers (docs/PIPELINE.md §2): `aintercept rollup`, at least two
// replicas, scaled by KEDA on GET /lag (oldest_unapplied_seconds), with a plain
// HPA when KEDA is off. They get the worker login's DSN from the Postgres
// Secret, and the retention settings.
func TestRollupDeployment(t *testing.T) {
	docs := render(t)
	dep := find(docs, "Deployment", "rollup")
	if dep == nil {
		t.Fatal("no rollup Deployment")
	}
	c := container(docs, "Deployment", "rollup", "rollup")
	if c == nil {
		t.Fatal("no rollup container")
	}
	if a := fmt.Sprint(get(c, "args")); a != "[rollup]" {
		t.Errorf("rollup args %s, want [rollup]", a)
	}
	env := envOf(c)
	for name, want := range map[string]string{"ROLLUP_S3_BUCKET": "b", "ROLLUP_S3_REGION": "r",
		"ROLLUP_RETENTION_DAYS": "15", "ROLLUP_RETENTION_RAW_DAYS": "0"} {
		if fmt.Sprint(env[name]) != want {
			t.Errorf("%s = %v, want %q", name, env[name], want)
		}
	}
	if r := secretRef(c, "ROLLUP_POSTGRES_URL"); r != "aintercept-postgres/worker-url" {
		t.Errorf("ROLLUP_POSTGRES_URL from %q, want the Secret's worker-url", r)
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

	// Retention (§3.6) and the Secret's name are values.
	docs = render(t, "retention.days=7", "retention.rawDays=3", "postgres.existingSecret=pg")
	c = container(docs, "Deployment", "rollup", "rollup")
	env = envOf(c)
	if env["ROLLUP_RETENTION_DAYS"] != "7" || env["ROLLUP_RETENTION_RAW_DAYS"] != "3" {
		t.Errorf("retention env %v %v, want 7 and 3", env["ROLLUP_RETENTION_DAYS"], env["ROLLUP_RETENTION_RAW_DAYS"])
	}
	if r := secretRef(c, "ROLLUP_POSTGRES_URL"); r != "pg/worker-url" {
		t.Errorf("postgres.existingSecret: ROLLUP_POSTGRES_URL from %q, want pg/worker-url", r)
	}
}

// Each pod gets only its own component's DSN: the workers the worker login's,
// the API the API login's, ingest (the observer) and certsvc none.
func TestEachPodGetsOnlyItsOwnDSN(t *testing.T) {
	docs := render(t, "api.enabled=true", "postgres.inCluster.enabled=true")
	for _, tc := range []struct{ kind, name, container, want string }{
		{"Deployment", "rollup", "rollup", "aintercept-postgres/worker-url"},
		{"Deployment", "api", "api", "aintercept-postgres/api-url"},
		{"Deployment", "envoy", "observer", ""},
		{"Deployment", "envoy", "envoy", ""},
		{"Deployment", "certsvc", "certsvc", ""},
	} {
		c := container(docs, tc.kind, tc.name, tc.container)
		if c == nil {
			t.Fatalf("no %s container in %s", tc.container, tc.name)
		}
		var pg []string
		for _, r := range secretRefs(c) {
			if strings.HasPrefix(r, "aintercept-postgres/") {
				pg = append(pg, r)
			}
		}
		if got := strings.Join(pg, ","); got != tc.want {
			t.Errorf("%s/%s reads %q from the Postgres Secret, want %q", tc.name, tc.container, got, tc.want)
		}
	}
	api := container(docs, "Deployment", "api", "api")
	if r := secretRef(api, "AINTERCEPT_PG_DSN"); r != "aintercept-postgres/api-url" {
		t.Errorf("AINTERCEPT_PG_DSN from %q, want the Secret's api-url", r)
	}
}

// Ingest has no database path (docs/PIPELINE.md §1): the observer gets no
// Redis or Postgres address, DSN or Secret.
func TestObserverHasNoDatabase(t *testing.T) {
	obs := container(render(t), "Deployment", "envoy", "observer")
	for name, v := range envOf(obs) {
		if s := strings.ToUpper(name + " " + fmt.Sprint(v)); strings.Contains(s, "REDIS") || strings.Contains(s, "POSTGRES") || strings.Contains(s, "DSN") {
			t.Errorf("the observer has %s = %v", name, v)
		}
	}
}

// The API reads Postgres (api.store pg, the default since build step 8); v1
// (the v5 folders) stays selectable. It gets the API login's DSN either way,
// and its readiness probe is /status, which reads Postgres.
func TestAPIStore(t *testing.T) {
	for set, want := range map[string]string{"": "pg", "api.store=v1": "v1"} {
		args := []string{"api.enabled=true"}
		if set != "" {
			args = append(args, set)
		}
		api := container(render(t, args...), "Deployment", "api", "api")
		if v := envOf(api)["AINTERCEPT_STORE"]; v != want {
			t.Errorf("%q: AINTERCEPT_STORE = %v, want %s", set, v, want)
		}
		if r := secretRef(api, "AINTERCEPT_PG_DSN"); r != "aintercept-postgres/api-url" {
			t.Errorf("%q: AINTERCEPT_PG_DSN from %q, want the Secret's api-url", set, r)
		}
		if p := get(api, "readinessProbe", "httpGet", "path"); p != "/api/v1/status" {
			t.Errorf("%q: api readiness %v, want /api/v1/status", set, p)
		}
	}
}

// On the DigitalOcean demo Postgres runs in the cluster (§6): one replica, a
// PVC, the pinned image (Compose's), non-root with a read-only root
// filesystem, the server settings, the built-in C.UTF-8 locale, and an init
// script that creates the two logins with passwords from the Secret.
func TestPostgresInCluster(t *testing.T) {
	if find(render(t), "StatefulSet", "postgres") != nil {
		t.Error("an in-cluster Postgres by default (production is RDS)")
	}
	docs := render(t, "postgres.inCluster.enabled=true")
	ss := find(docs, "StatefulSet", "postgres")
	if ss == nil {
		t.Fatal("no postgres StatefulSet")
	}
	if r := get(ss, "spec", "replicas"); r != 1 {
		t.Errorf("postgres replicas %v, want 1", r)
	}
	if s := get(ss, "spec", "volumeClaimTemplates", "0", "spec", "resources", "requests", "storage"); s != "20Gi" {
		t.Errorf("postgres PVC size %v, want 20Gi", s)
	}
	spec := get(ss, "spec", "template", "spec")
	if get(spec, "securityContext", "runAsNonRoot") != true {
		t.Error("postgres may run as root")
	}
	c := byName(get(spec, "containers"))["postgres"]
	if c == nil {
		t.Fatal("no postgres container")
	}
	compose, err := os.ReadFile("../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*image:\s*(postgres:\S+)`).FindSubmatch(compose)
	if m == nil {
		t.Fatal("no postgres image in docker-compose.yml")
	}
	repoTag, digest, _ := strings.Cut(string(m[1]), "@")
	if img := c["image"]; img != strings.SplitN(repoTag, ":", 2)[0]+"@"+digest {
		t.Errorf("postgres image %v, want Compose's %s by digest", img, m[1])
	}
	if get(c, "securityContext", "readOnlyRootFilesystem") != true || get(c, "securityContext", "allowPrivilegeEscalation") != false {
		t.Error("postgres's root filesystem is writable, or it may escalate")
	}
	args := fmt.Sprint(get(c, "args"))
	for _, want := range []string{"max_locks_per_transaction=256", "max_connections=200"} {
		if !strings.Contains(args, want) {
			t.Errorf("postgres args %s: want %s", args, want)
		}
	}
	env := envOf(c)
	if v, _ := env["POSTGRES_INITDB_ARGS"].(string); !strings.Contains(v, "--locale-provider=builtin") || !strings.Contains(v, "--builtin-locale=C.UTF-8") {
		t.Errorf("POSTGRES_INITDB_ARGS %q: want the built-in C.UTF-8 locale", v)
	}
	if env["POSTGRES_DB"] != "aintercept" {
		t.Errorf("POSTGRES_DB %v, want aintercept", env["POSTGRES_DB"])
	}
	for name, want := range map[string]string{"POSTGRES_PASSWORD": "postgres-password",
		"AINTERCEPT_PG_WORKER_PASSWORD": "worker-password", "AINTERCEPT_PG_API_PASSWORD": "api-password"} {
		if r := secretRef(c, name); r != "aintercept-postgres/"+want {
			t.Errorf("%s from %q, want aintercept-postgres/%s", name, r, want)
		}
	}
	mounts := map[any]any{}
	for _, mt := range asList(get(c, "volumeMounts")) {
		mounts[get(mt, "mountPath")] = get(mt, "name")
	}
	vols := byName(get(spec, "volumes"))
	for _, path := range []string{"/var/run/postgresql", "/tmp"} {
		if n, ok := mounts[path].(string); !ok || get(vols[n], "emptyDir") == nil {
			t.Errorf("%s isn't an emptyDir", path)
		}
	}
	if n, ok := mounts["/var/lib/postgresql/data"].(string); !ok || n != "data" {
		t.Error("the data directory isn't the PVC")
	}
	if p := env["PGDATA"]; p == nil || !strings.HasPrefix(fmt.Sprint(p), "/var/lib/postgresql/data/") {
		t.Errorf("PGDATA %v: want a folder inside the volume (its root holds lost+found)", p)
	}
	init := ""
	for path, n := range mounts {
		if strings.HasPrefix(fmt.Sprint(path), "/docker-entrypoint-initdb.d") {
			init = fmt.Sprint(n)
		}
	}
	cm := get(vols[init], "configMap", "name")
	if cm == nil {
		t.Fatal("no init script mounted from a ConfigMap")
	}
	cmDoc := find(docs, "ConfigMap", fmt.Sprint(cm))
	data, _ := get(cmDoc, "data").(map[string]any)
	if data == nil {
		t.Fatalf("no ConfigMap %v", cm)
	}
	logins, err := os.ReadFile("../pg/logins.sql")
	if err != nil {
		t.Fatal(err)
	}
	if data["logins.sql"] != string(logins) {
		t.Error("the chart's logins.sql differs from internal/pg/logins.sql (files/postgres/logins.sql: copy it)")
	}
	initSh, err := os.ReadFile(filepath.Join(chartDir, "files/postgres/initdb.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var script string
	for k, v := range data {
		if strings.HasSuffix(k, ".sh") {
			script = fmt.Sprint(v)
		}
	}
	if !bytes.Equal([]byte(script), initSh) {
		t.Error("the init script isn't files/postgres/initdb.sh")
	}
	if mode, _ := get(vols[init], "configMap", "defaultMode").(int); mode&0o111 == 0 {
		t.Errorf("the init script's mode %o isn't executable: the entrypoint would source it", mode)
	}
	if svc := find(docs, "Service", "postgres"); svc == nil || get(svc, "spec", "ports", "0", "port") != 5432 {
		t.Error("no postgres Service on 5432")
	}
	docs = render(t, "postgres.inCluster.enabled=true", "postgres.inCluster.persistence.size=40Gi",
		"postgres.inCluster.persistence.storageClass=fast")
	ss = find(docs, "StatefulSet", "postgres")
	if get(ss, "spec", "volumeClaimTemplates", "0", "spec", "resources", "requests", "storage") != "40Gi" ||
		get(ss, "spec", "volumeClaimTemplates", "0", "spec", "storageClassName") != "fast" {
		t.Error("postgres.inCluster.persistence is not honoured")
	}
}

// Only the workers and the API may reach the in-cluster Postgres.
func TestPostgresNetworkPolicy(t *testing.T) {
	if find(render(t), "NetworkPolicy", "postgres-restrict") != nil {
		t.Error("a postgres NetworkPolicy without an in-cluster Postgres")
	}
	np := find(render(t, "postgres.inCluster.enabled=true"), "NetworkPolicy", "postgres-restrict")
	if np == nil {
		t.Fatal("no postgres-restrict NetworkPolicy")
	}
	if a := get(np, "spec", "podSelector", "matchLabels", "app"); a != "postgres" {
		t.Errorf("podSelector app %v, want postgres", a)
	}
	apps := map[any]bool{}
	for _, f := range asList(get(np, "spec", "ingress", "0", "from")) {
		apps[get(f, "podSelector", "matchLabels", "app")] = true
	}
	if !apps["rollup"] || !apps["api"] || len(apps) != 2 {
		t.Errorf("postgres admits %v, want exactly rollup and api", apps)
	}
	if p := get(np, "spec", "ingress", "0", "ports", "0", "port"); p != 5432 {
		t.Errorf("postgres port %v, want 5432", p)
	}
}

// RDS (production): TLS with the RDS CA bundle, from a ConfigMap mounted in
// the workers and the API, which their DSNs name (sslrootcert).
func TestPostgresCABundle(t *testing.T) {
	for _, docs := range [][]map[string]any{render(t, "api.enabled=true")} {
		for _, n := range []string{"rollup", "api"} {
			if _, ok := byName(get(find(docs, "Deployment", n), "spec", "template", "spec", "volumes"))["postgres-ca"]; ok {
				t.Errorf("%s mounts a CA bundle that isn't configured", n)
			}
		}
	}
	docs := render(t, "api.enabled=true", "postgres.caConfigMap=rds-ca")
	for _, n := range []string{"rollup", "api"} {
		spec := get(find(docs, "Deployment", n), "spec", "template", "spec")
		if cm := get(byName(get(spec, "volumes"))["postgres-ca"], "configMap", "name"); cm != "rds-ca" {
			t.Errorf("%s: CA volume from %v, want rds-ca", n, cm)
		}
		mt := byName(get(byName(get(spec, "containers"))[n], "volumeMounts"))["postgres-ca"]
		if get(mt, "mountPath") != "/etc/aintercept/postgres-ca" || get(mt, "readOnly") != true {
			t.Errorf("%s: CA mount %v, want /etc/aintercept/postgres-ca read-only", n, mt)
		}
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
