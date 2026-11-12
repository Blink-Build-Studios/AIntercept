package observer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

// stuckTarget never accepts an upload, so the spool only fills.
type stuckTarget struct{}

func (stuckTarget) Name() string { return "s3" }
func (stuckTarget) Put(ctx context.Context, _ events.Object) error {
	<-ctx.Done()
	return ctx.Err()
}

// A full spool never takes the pod out of rotation, even with enforcement on:
// only enforced calls fail closed (they're refused per stream), and observed
// traffic must keep flowing. Fullness is reported on /stats instead.
func TestReadyWithAFullSpoolEvenWhenEnforcing(t *testing.T) {
	t.Setenv("OBSERVER_UNREADY_WHEN_FULL", "true") // the old enforce-mode setting must have no effect
	w, err := events.NewWriter(events.WriterConfig{
		SpoolDir: t.TempDir(), SpoolMaxBytes: 1000, MaxBufferBytes: 1, SkipFsync: true,
		Targets: []events.Target{stuckTarget{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abandon()
	s := events.NewS3Sink(w, events.S3Config{Target: "s3", InstanceID: "pod"})
	deadline := time.Now().Add(5 * time.Second)
	for !w.Full() {
		if time.Now().After(deadline) {
			t.Fatal("spool never filled")
		}
		_ = s.Emit(events.Event{CorrelationID: strings.Repeat("x", 100), Phase: events.PhaseComplete})
		time.Sleep(time.Millisecond)
	}

	proc := events.NewProcessor(events.NewStdoutSink(discard{}), events.ProcessorConfig{Full: w.Full})
	defer proc.Close()
	mux := statsMux(proc, membudget.New(1<<20), nil, w)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz/ready = %d with a full spool; want 200 (readiness must never fail on a full spool)", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	var st struct {
		Writer struct {
			Full bool `json:"full"`
		} `json:"writer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.Writer.Full {
		t.Fatalf("/stats doesn't report the full spool: %s", rec.Body)
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }
