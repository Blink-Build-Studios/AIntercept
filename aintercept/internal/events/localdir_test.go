package events

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLocalDirSinkWritesPartitionedGzipNDJSON(t *testing.T) {
	dir := t.TempDir()
	sink, w := localSink(t, dir, S3Config{InstanceID: "observer-local-0", MaxEvents: 2})
	for i := 0; i < 3; i++ {
		if err := sink.Emit(Event{Phase: PhaseComplete, CorrelationID: "c", Seq: uint64(i), Host: "api.anthropic.com"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "dt=*", "hour=*", "instance=observer-local-0", "*.ndjson.gz"))
	if len(files) != 2 { // MaxEvents=2: one full batch, then the remainder on Close
		t.Fatalf("want 2 objects, got %d: %v", len(files), files)
	}
	layout := regexp.MustCompile(`/dt=\d{4}-\d{2}-\d{2}/hour=\d{2}/instance=observer-local-0/\d+-\d{6}\.ndjson\.gz$`)
	rows := 0
	for _, f := range files {
		if !layout.MatchString(filepath.ToSlash(f)) {
			t.Fatalf("key layout differs from the S3 sink's: %s", f)
		}
		fh, _ := os.Open(f)
		gz, err := gzip.NewReader(fh)
		if err != nil {
			t.Fatalf("%s: not gzip: %v", f, err)
		}
		sc := bufio.NewScanner(gz)
		for sc.Scan() {
			var e map[string]any
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Fatalf("%s: bad NDJSON line: %v", f, err)
			}
			rows++
		}
		_ = fh.Close()
	}
	if rows != 3 {
		t.Fatalf("want 3 rows, got %d", rows)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "dt=*", "hour=*", "instance=*", ".*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestLocalDirSinkPrefixAndContentStripping(t *testing.T) {
	dir := t.TempDir()
	sink, w := localSink(t, dir, S3Config{InstanceID: "i", Prefix: "telemetry"})
	e := Event{Phase: PhaseComplete, CorrelationID: "c", BodyPreview: "SECRET-PROMPT-TEXT"}
	_ = sink.Emit(e)
	_ = w.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "telemetry", "dt=*", "hour=*", "instance=i", "*.ndjson.gz"))
	if len(files) != 1 {
		t.Fatalf("want 1 object under the prefix, got %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	gz, _ := gzip.NewReader(bytes.NewReader(raw))
	var out bytes.Buffer
	_, _ = out.ReadFrom(gz)
	if strings.Contains(out.String(), "SECRET-PROMPT-TEXT") {
		t.Fatal("body content reached the local sink in a production build")
	}
}

func TestDirPutterRefusesKeysOutsideRoot(t *testing.T) {
	root := t.TempDir()
	p, _ := NewDirTarget("local", root)
	for _, key := range []string{"../escape.ndjson.gz", "a/../../escape.ndjson.gz", ""} {
		if err := p.Put(context.Background(), Object{Key: key, Body: []byte("x")}); err == nil {
			t.Fatalf("key %q was accepted", key)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.ndjson.gz")); err == nil {
		t.Fatal("a file was written outside the root")
	}
}

func TestDirPutterNeverExposesAPartialFile(t *testing.T) {
	root := t.TempDir()
	p, _ := NewDirTarget("local", root)
	// A body that fails halfway: the destination must not appear at all.
	body := &failingReader{data: []byte("half a gzip stream"), failAfter: 5}
	if err := p.write("dt=2026-11-04/hour=18/instance=i/1-000001.ndjson.gz", body); err == nil {
		t.Fatal("want the copy error")
	}
	var found []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("partial or temp files left: %v", found)
	}
}

type failingReader struct {
	data      []byte
	failAfter int
	n         int
}

func (r *failingReader) Read(b []byte) (int, error) {
	if r.n >= r.failAfter {
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(b, r.data[r.n:min(len(r.data), r.failAfter)])
	r.n += n
	return n, nil
}

// localSink is an S3 sink on a writer whose target is a local directory.
func localSink(t *testing.T, dir string, cfg S3Config) (*S3Sink, *Writer) {
	t.Helper()
	tgt, err := NewDirTarget("local", dir)
	if err != nil {
		t.Fatal(err)
	}
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}})
	cfg.Target = "local"
	return NewS3Sink(w, cfg), w
}
