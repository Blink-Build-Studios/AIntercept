package schemareport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// sampleLine builds a schema-sample NDJSON line like the observer emits.
func sampleLine(ts int64, host, method, path string, status int, reason, reqSkeleton string) []byte {
	m := map[string]interface{}{
		"timestamp_ns": ts,
		"host":         host,
		"method":       method,
		"path":         path,
		"status":       status,
		"schema_fingerprint": map[string]interface{}{
			"reason": reason,
			"request": map[string]interface{}{
				"byte_len": len(reqSkeleton),
				"kind":     "json",
				"skeleton": json.RawMessage(reqSkeleton),
			},
		},
	}
	b, _ := json.Marshal(m)
	return b
}

func TestAggregator_GroupsAndCounts(t *testing.T) {
	agg := NewAggregator(false, 0)
	agg.Add(sampleLine(100, "api.x.com", "POST", "/v1/infer", 200, "no_usage", `{"model":"str(<=64)"}`))
	agg.Add(sampleLine(200, "api.x.com", "POST", "/v1/infer", 200, "no_usage", `{"model":"str(<=64)"}`))
	agg.Add(sampleLine(300, "api.y.com", "POST", "/other", 500, "no_model,no_usage", `{"q":"str(<=8)"}`))
	r := agg.Finish()

	if len(r.Endpoints) != 2 {
		t.Fatalf("want 2 endpoints, got %d", len(r.Endpoints))
	}
	// Busiest first.
	if r.Endpoints[0].Path != "/v1/infer" || r.Endpoints[0].Count != 2 {
		t.Fatalf("busiest endpoint wrong: %+v", r.Endpoints[0])
	}
	// The two identical request skeletons fold to a single variant with count 2.
	if len(r.Endpoints[0].RequestShapes) != 1 || r.Endpoints[0].RequestShapes[0].Count != 2 {
		t.Fatalf("skeleton folding wrong: %+v", r.Endpoints[0].RequestShapes)
	}
	if r.WatermarkNS != 300 {
		t.Fatalf("watermark = %d, want 300", r.WatermarkNS)
	}
	if r.SamplesScanned != 3 {
		t.Fatalf("samples scanned = %d, want 3", r.SamplesScanned)
	}
}

func TestAggregator_SinceFiltersOld(t *testing.T) {
	agg := NewAggregator(false, 150)
	if agg.Add(sampleLine(100, "h", "POST", "/p", 200, "no_usage", `{"a":"int"}`)) {
		t.Fatal("sample at/<=since must be skipped")
	}
	if !agg.Add(sampleLine(200, "h", "POST", "/p", 200, "no_usage", `{"a":"int"}`)) {
		t.Fatal("newer sample must be included")
	}
	r := agg.Finish()
	if r.SamplesScanned != 1 || r.WatermarkNS != 200 {
		t.Fatalf("since filter wrong: scanned=%d watermark=%d", r.SamplesScanned, r.WatermarkNS)
	}
}

func TestAggregator_RedactKeysScrubsKeys(t *testing.T) {
	// A sensitive-looking KEY; with redactKeys it must not survive into the report.
	agg := NewAggregator(true, 0)
	agg.Add(sampleLine(1, "h", "POST", "/p", 200, "no_usage",
		`{"user_email_alice@x.com":"str(<=64)","n":"int"}`))
	r := agg.Finish()
	b, _ := json.Marshal(r)
	if bytes.Contains(b, []byte("user_email_alice@x.com")) {
		t.Fatalf("redact-keys failed, key leaked into report: %s", b)
	}
	// Structure preserved: there should be a bucketed key.
	if !bytes.Contains(b, []byte("key(")) {
		t.Fatalf("redacted report should carry bucketed keys: %s", b)
	}
}

func TestAggregator_SkipsMalformedAndNoFingerprint(t *testing.T) {
	agg := NewAggregator(false, 0)
	if agg.Add([]byte("not json")) {
		t.Fatal("malformed line must be skipped")
	}
	if agg.Add([]byte(`{"host":"h","timestamp_ns":1}`)) {
		t.Fatal("line without fingerprint must be skipped")
	}
	if agg.Finish().SamplesScanned != 0 {
		t.Fatal("nothing should have been counted")
	}
}

// ── fake S3 for Scan ───────────────────────────────────────────────────────────

type fakeS3 struct {
	objects map[string][]byte // key -> raw (gzipped) bytes
	mtimes  map[string]time.Time
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var contents []s3types.Object
	for k := range f.objects {
		if in.Prefix != nil && !strings.HasPrefix(k, *in.Prefix) {
			continue
		}
		mt := f.mtimes[k]
		contents = append(contents, s3types.Object{Key: aws.String(k), LastModified: aws.Time(mt)})
	}
	return &s3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(false)}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	raw := f.objects[*in.Key]
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(raw))}, nil
}

func gz(lines ...[]byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	for _, l := range lines {
		gw.Write(l)
		gw.Write([]byte("\n"))
	}
	gw.Close()
	return buf.Bytes()
}

func TestScan_ReadsGzippedObjectsAndFolds(t *testing.T) {
	f := &fakeS3{
		objects: map[string][]byte{
			"schema-samples/a.ndjson.gz": gz(
				sampleLine(10, "h", "POST", "/p", 200, "no_usage", `{"a":"int"}`),
				sampleLine(20, "h", "POST", "/p", 200, "no_usage", `{"a":"int"}`),
			),
			"schema-samples/b.ndjson.gz": gz(
				sampleLine(30, "h2", "POST", "/q", 404, "no_model,no_usage", `{"b":"str(<=8)"}`),
			),
			"other-prefix/c.ndjson.gz": gz(sampleLine(40, "zzz", "GET", "/z", 200, "no_usage", `{}`)),
		},
		mtimes: map[string]time.Time{
			"schema-samples/a.ndjson.gz": time.Unix(0, 25),
			"schema-samples/b.ndjson.gz": time.Unix(0, 35),
			"other-prefix/c.ndjson.gz":   time.Unix(0, 45),
		},
	}
	agg := NewAggregator(false, 0)
	n, err := Scan(context.Background(), f, ScanConfig{Bucket: "bkt", Prefix: "schema-samples", SinceNS: 0}, agg)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("read %d objects, want 2 (other-prefix excluded)", n)
	}
	r := agg.Finish()
	if r.SamplesScanned != 3 {
		t.Fatalf("samples = %d, want 3", r.SamplesScanned)
	}
	// "zzz" host from other-prefix must not appear.
	b, _ := json.Marshal(r)
	if bytes.Contains(b, []byte("zzz")) {
		t.Fatal("scan read outside its prefix")
	}
}

func TestScan_SinceSkipsOldObjectsByMtime(t *testing.T) {
	f := &fakeS3{
		objects: map[string][]byte{
			"schema-samples/old.ndjson.gz": gz(sampleLine(10, "h", "POST", "/p", 200, "no_usage", `{"a":"int"}`)),
			"schema-samples/new.ndjson.gz": gz(sampleLine(100, "h", "POST", "/p", 200, "no_usage", `{"a":"int"}`)),
		},
		mtimes: map[string]time.Time{
			"schema-samples/old.ndjson.gz": time.Unix(0, 15),
			"schema-samples/new.ndjson.gz": time.Unix(0, 105),
		},
	}
	agg := NewAggregator(false, 50) // cursor between the two
	n, err := Scan(context.Background(), f, ScanConfig{Bucket: "bkt", Prefix: "schema-samples", SinceNS: 50}, agg)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("read %d objects, want 1 (old skipped by mtime)", n)
	}
	if agg.Finish().SamplesScanned != 1 {
		t.Fatal("old sample should not be counted")
	}
}
