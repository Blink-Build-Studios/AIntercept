package records

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

type memTarget struct {
	mu   sync.Mutex
	objs map[string]events.Object
}

func (m *memTarget) Name() string { return "store" }
func (m *memTarget) Put(_ context.Context, o events.Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objs == nil {
		m.objs = map[string]events.Object{}
	}
	m.objs[o.Key] = o
	return nil
}

// Records and call detail objects reach the store only through the observer's
// single writer: a records sink wired to its object streams uploads both, and
// no file in this package uploads by itself.
func TestRecordsUploadOnlyThroughTheWriter(t *testing.T) {
	tgt := &memTarget{}
	w, err := events.NewWriter(events.WriterConfig{SpoolDir: t.TempDir(), SkipFsync: true, Targets: []events.Target{tgt}})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSink(SinkConfig{Putter: WriterPutter(w, "store"), Prefix: "p", Incarnation: "i", Details: true,
		MaxAge: time.Hour})
	for _, e := range sampleRows() {
		if err := s.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	var recs, details int
	for k, o := range tgt.objs {
		switch {
		case strings.HasPrefix(k, "p/v1/keys/"):
			recs++
			if o.ContentType != "application/vnd.apache.parquet" {
				t.Errorf("%s content type %q", k, o.ContentType)
			}
		case strings.HasPrefix(k, "p/calls/"):
			details++
			if o.ContentEncoding != "gzip" {
				t.Errorf("%s encoding %q", k, o.ContentEncoding)
			}
		default:
			t.Errorf("unexpected object %s", k)
		}
	}
	if recs == 0 || details < 3 {
		t.Fatalf("records objects %d, detail objects %d", recs, details)
	}
	_ = w.Close()

	// Structural guard: nothing here talks to object storage directly.
	files, _ := filepath.Glob("*.go")
	direct := regexp.MustCompile(`PutObject|aws-sdk-go|S3Sink|s3\.`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		if direct.Match(src) {
			t.Errorf("%s uploads by itself; records go through the observer's writer (WriterPutter)", f)
		}
	}
}

// What a call in flight holds for its detail object is charged to the
// observer's memory budget, and released when the object is written or the
// call is forgotten.
func TestDetailBytesAreChargedToTheBudget(t *testing.T) {
	b := membudget.New(1 << 30)
	a := NewAssembler("inc")
	a.KeepDetail = true
	a.Budget = b
	rows := bodiesOf("d1", `{"messages":[{"role":"user","content":"hi"}]}`, "data: x\n\n")
	var held int64
	for i := range rows {
		l := line(rows[i])
		held += int64(len(l))
		a.Apply(&rows[i], l, "f")
	}
	if b.Held() != held {
		t.Fatalf("budget holds %d, the call's rows are %d", b.Held(), held)
	}
	end := complete("d1", ccClient)
	a.Apply(&end, line(end), "f")
	if b.Held() != 0 {
		t.Fatalf("after the call ended the budget still holds %d", b.Held())
	}

	open := hdr("o1", false, ccHeaders)
	a.Apply(&open, line(open), "f")
	if b.Held() == 0 {
		t.Fatal("an open call's rows aren't charged")
	}
	a.newestNS = at(30 * 24 * time.Hour)
	a.sweep()
	if b.Held() != 0 {
		t.Fatalf("a forgotten call still holds %d", b.Held())
	}
}
