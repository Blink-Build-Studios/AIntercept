package process

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The mirror copies what the newest commit names, prunes what it no longer
// names, and serves raw objects under the raw prefix only.
func TestMirrorFollowsCommits(t *testing.T) {
	heavy(t)
	t.Parallel() // slow under -race: let it overlap the others
	ctx := context.Background()
	c := newCapture()
	raw, out := newMem(), newMem()
	for _, f := range c.sorted() {
		put(t, raw, f)
	}
	// One bucket holds both, as in the cluster.
	for k, v := range out.objs {
		raw.objs[k] = v
	}
	now := t0.Add(24 * time.Hour)
	p := newProc(raw, raw, &now, 7, 3)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := &Mirror{Src: raw, Shaped: "aintercept/shaped", Raw: "aintercept", Dir: dir}
	if changed, err := m.Sync(ctx); err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v", changed, err)
	}
	if changed, _ := m.Sync(ctx); changed {
		t.Fatal("an unchanged manifest shouldn't sync again")
	}
	// Every part the commit names is here, byte for byte, under the API's layout (no bucket prefix).
	for k := range p.man.allParts() {
		b, err := os.ReadFile(m.local(k))
		if err != nil || string(b) != string(raw.objs[k]) {
			t.Fatalf("part %s not mirrored: %v", k, err)
		}
		if !strings.HasPrefix(m.local(k), dir+"/") || strings.Contains(m.local(k), "aintercept/shaped") {
			t.Fatalf("part %s mirrored at %s", k, m.local(k))
		}
	}

	// A stray file (a part a later commit dropped) is pruned on the next change.
	stray := filepath.Join(dir, "requests", "run-old.parquet")
	if err := writeFileAtomic(stray, []byte("x")); err != nil {
		t.Fatal(err)
	}
	more := newCapture()
	for _, f := range more.sorted() {
		f.key = strings.Replace(f.key, "instance=", "instance=z", 1)
		put(t, raw, f)
	}
	if _, err := p.Poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.Sync(ctx); err != nil || !changed {
		t.Fatalf("second sync: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("a file no commit names should be pruned")
	}

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	get := func(path string) (int, string) {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	f := c.sorted()[0]
	folder := f.key[:strings.LastIndex(f.key, "/")+1]
	if code, body := get("/raw/list?prefix=" + folder); code != 200 || !strings.Contains(body, f.key) {
		t.Fatalf("list: %d %q", code, body)
	}
	if code, body := get("/raw/get?key=" + f.key); code != 200 || body != string(raw.objs[f.key]) {
		t.Fatalf("get: %d", code)
	}
	if code, _ := get("/raw/get?key=" + folder + "missing.ndjson.gz"); code != 404 {
		t.Fatalf("missing: %d", code)
	}
	for _, bad := range []string{"aintercept/shaped/manifest.json", "other/x", "aintercept/../secret"} {
		if code, _ := get("/raw/get?key=" + bad); code != 400 {
			t.Fatalf("%s: %d, want 400", bad, code)
		}
	}
	if code, _ := get("/healthz"); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
}
