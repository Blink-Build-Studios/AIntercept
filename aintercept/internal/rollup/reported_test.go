package rollup

import (
	"context"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// A reported call nothing captured is shown under the id reported:<id>. Its
// row lives with the key of the session that reported it, so every key that
// stores one marks it: v1/reported/<escaped id>/<escaped key>. The drawer
// finds the row from the id alone.

// missedModel is the stand-in model plus one missed report per record.
type missedModel struct{ stream.CountModel }

func (m missedModel) Tables() []string {
	return append(m.CountModel.Tables(), state.TableRequestMissed)
}

func (m missedModel) Apply(st any, r *records.Record) stream.Delta {
	d := m.CountModel.Apply(st, r)
	id := "req_" + r.Call.CallID
	d[state.TableRequestMissed] = append(d[state.TableRequestMissed], stream.Row{Key: r.RoutingKey, ID: "m" + id,
		Value: state.MissedRow{SessionID: "x", ReqID: id, Row: process.RequestRow{RequestID: "reported:" + id}}})
	return d
}

func TestReportedMark(t *testing.T) {
	if got := ReportedMark("p", "req/1", "s:a"); got != "p/v1/reported/req%2F1/s:a" {
		t.Fatalf("mark %q", got)
	}
}

func TestRunMarksItsMissedReports(t *testing.T) {
	e := newEnv(t)
	e.model = missedModel{}
	e.ingest("s1", 2)
	w := e.worker("w1")
	if _, ok, err := w.RunKey(e.ctx, sessionKey("s1")); err != nil || !ok {
		t.Fatalf("run: %v %v", ok, err)
	}
	objs, err := e.store.List(e.ctx, prefix+"/v1/reported/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("%d reported marks, want 2", len(objs))
	}
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, "/"+sessionKey("s1")) {
			t.Fatalf("mark %s isn't the session's", o.Key)
		}
	}
}

func TestOnePassMarksItsMissedReports(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	rows := map[string][]stream.Row{state.TableRequestMissed: {{Key: "s:a", ID: "m1",
		Value: state.MissedRow{ReqID: "r1", Row: process.RequestRow{RequestID: "reported:r1"}}}}}
	if err := WriteOnePass(context.Background(), s, "p", "s:a", rows, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), ReportedMark("p", "r1", "s:a")); err != nil {
		t.Fatalf("no mark: %v", err)
	}
}
