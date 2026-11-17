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

// The pipeline in the chart (docs/PIPELINE.md §1): the workers (`worker`) and
// the API read Postgres through a DSN each, from one Secret; Postgres runs in
// the cluster on the DigitalOcean demo (postgres.inCluster) and is RDS in
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

// The workers (docs/PIPELINE.md §2.2): `aintercept worker`, scaled by KEDA on
// GET /lag (oldest_unloaded_seconds), with a plain HPA when KEDA is off; the
// worker login's DSN, the bucket and the retention, as their own
// ServiceAccount. The observer writes the raw files they load, at 8 MiB or
// 30 s.
func TestWorker(t *testing.T) {
	docs := render(t)
	c := container(docs, "Deployment", "worker", "worker")
	if c == nil {
		t.Fatal("no worker Deployment")
	}
	if a := fmt.Sprint(get(c, "args")); a != "[worker]" {
		t.Errorf("worker args %s, want [worker]", a)
	}
	env := envOf(c)
	if env["WORKER_S3_BUCKET"] == nil || env["WORKER_HTTP_LISTEN"] != "0.0.0.0:9005" || env["WORKER_LOADERS"] != "2" {
		t.Errorf("worker env %v", env)
	}
	if r := secretRef(c, "WORKER_POSTGRES_URL"); r != "aintercept-postgres/worker-url" {
		t.Errorf("WORKER_POSTGRES_URL from %q, want the Secret's worker-url", r)
	}
	if sa := get(find(docs, "Deployment", "worker"), "spec", "template", "spec", "serviceAccountName"); sa != "aintercept-worker" {
		t.Errorf("worker ServiceAccount %v", sa)
	}
	if find(docs, "ServiceAccount", "aintercept-worker") == nil {
		t.Error("no worker ServiceAccount")
	}
	so := find(docs, "ScaledObject", "worker")
	if so == nil {
		t.Fatal("no worker ScaledObject")
	}
	if u := get(so, "spec", "triggers", "0", "metadata", "url"); !strings.HasSuffix(fmt.Sprint(u), ":9005/lag") {
		t.Errorf("worker trigger url %v", u)
	}
	if v := get(so, "spec", "triggers", "0", "metadata", "valueLocation"); v != "oldest_unloaded_seconds" {
		t.Errorf("worker valueLocation %v", v)
	}
	// A schema change is just helm upgrade: the old workers all stop before the
	// new ones start, and the first new one resets the database to its schema.
	if s := get(find(docs, "Deployment", "worker"), "spec", "strategy"); fmt.Sprint(s) != "map[type:Recreate]" {
		t.Errorf("worker strategy %v, want Recreate alone", s)
	}
	if env["WORKER_SCHEMA_RESET"] != "true" {
		t.Errorf("WORKER_SCHEMA_RESET = %v, want true", env["WORKER_SCHEMA_RESET"])
	}
	if v := envOf(container(render(t, "worker.schemaReset=false"), "Deployment", "worker", "worker"))["WORKER_SCHEMA_RESET"]; v != "false" {
		t.Errorf("worker.schemaReset=false: WORKER_SCHEMA_RESET = %v", v)
	}
	if env["WORKER_RETENTION_DAYS"] != "15" {
		t.Errorf("WORKER_RETENTION_DAYS = %v, want 15", env["WORKER_RETENTION_DAYS"])
	}
	docs = render(t, "retention.days=7", "postgres.existingSecret=pg")
	c = container(docs, "Deployment", "worker", "worker")
	if envOf(c)["WORKER_RETENTION_DAYS"] != "7" || secretRef(c, "WORKER_POSTGRES_URL") != "pg/worker-url" {
		t.Errorf("retention.days and postgres.existingSecret: %v %v", envOf(c)["WORKER_RETENTION_DAYS"], secretRef(c, "WORKER_POSTGRES_URL"))
	}
	for _, kind := range []string{"Deployment", "Service", "ScaledObject", "HorizontalPodAutoscaler", "ServiceAccount"} {
		for _, name := range []string{"rollup", "aintercept-rollup"} {
			if find(render(t, "rollup.autoscaling.keda=false"), kind, name) != nil {
				t.Errorf("%s %s: the old workers are gone", kind, name)
			}
		}
	}
	docs = render(t, "worker.autoscaling.keda=false")
	if find(docs, "ScaledObject", "worker") != nil || find(docs, "HorizontalPodAutoscaler", "worker") == nil {
		t.Error("KEDA off: want a CPU HPA for the worker, no ScaledObject")
	}
	if find(render(t, "worker.enabled=false"), "Deployment", "worker") != nil {
		t.Error("worker.enabled=false still renders it")
	}
	obs := envOf(container(render(t), "Deployment", "envoy", "observer"))
	if obs["OBSERVER_RAW_FLUSH_BYTES"] != "8388608" || obs["OBSERVER_RAW_FLUSH_SECONDS"] != "30" {
		t.Errorf("observer raw files env: %v %v", obs["OBSERVER_RAW_FLUSH_BYTES"], obs["OBSERVER_RAW_FLUSH_SECONDS"])
	}
	for _, gone := range []string{"OBSERVER_RAW", "OBSERVER_KEY_FLUSH_BYTES", "OBSERVER_KEY_FLUSH_SECONDS", "OBSERVER_KEY_BUFFER_MAX_BYTES",
		"OBSERVER_S3_MAX_BYTES", "OBSERVER_S3_MAX_EVENTS"} {
		if _, ok := obs[gone]; ok {
			t.Errorf("observer env %s: the old path's", gone)
		}
	}
}

// Each pod gets only its own component's DSN: the workers the worker login's,
// the API the API login's, ingest (the observer) and certsvc none.
func TestEachPodGetsOnlyItsOwnDSN(t *testing.T) {
	docs := render(t, "api.enabled=true", "postgres.inCluster.enabled=true")
	for _, tc := range []struct{ kind, name, container, want string }{
		{"Deployment", "worker", "worker", "aintercept-postgres/worker-url"},
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

// The API reads Postgres, through the API login's DSN, and its readiness probe
// is /status, which reads Postgres.
func TestAPIStore(t *testing.T) {
	api := container(render(t, "api.enabled=true"), "Deployment", "api", "api")
	if r := secretRef(api, "AINTERCEPT_PG_DSN"); r != "aintercept-postgres/api-url" {
		t.Errorf("AINTERCEPT_PG_DSN from %q, want the Secret's api-url", r)
	}
	if p := get(api, "readinessProbe", "httpGet", "path"); p != "/api/v1/status" {
		t.Errorf("api readiness %v, want /api/v1/status", p)
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
	if !strings.Contains(args, "max_connections=200") || strings.Contains(args, "max_locks_per_transaction") {
		t.Errorf("postgres args %s: want max_connections=200 and the default lock table", args)
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
	if !apps["worker"] || !apps["api"] || len(apps) != 2 {
		t.Errorf("postgres admits %v, want exactly worker and api", apps)
	}
	if p := get(np, "spec", "ingress", "0", "ports", "0", "port"); p != 5432 {
		t.Errorf("postgres port %v, want 5432", p)
	}
}

// RDS (production): TLS with the RDS CA bundle, from a ConfigMap mounted in
// the workers and the API, which their DSNs name (sslrootcert).
func TestPostgresCABundle(t *testing.T) {
	for _, docs := range [][]map[string]any{render(t, "api.enabled=true")} {
		for _, n := range []string{"worker", "api"} {
			if _, ok := byName(get(find(docs, "Deployment", n), "spec", "template", "spec", "volumes"))["postgres-ca"]; ok {
				t.Errorf("%s mounts a CA bundle that isn't configured", n)
			}
		}
	}
	docs := render(t, "api.enabled=true", "postgres.caConfigMap=rds-ca")
	for _, n := range []string{"worker", "api"} {
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

// The old path is gone: no single processor, no bucketed processor, no rollup
// workers, and the API never copies data to local disk (no mirror sidecar, no
// table or scratch volumes).
func TestOldPathIsGone(t *testing.T) {
	docs := render(t, "api.enabled=true")
	for _, n := range []string{"process", "stream", "rollup"} {
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
	for _, v := range []string{"shaped", "db", "work", "cache"} {
		if _, ok := vols[v]; ok {
			t.Errorf("the api still has a %q volume", v)
		}
	}
}
