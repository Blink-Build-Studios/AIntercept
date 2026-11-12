package stream

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLagIsZeroWhenCaughtUp(t *testing.T) {
	h, ctx := setup(t, 2)
	h.upload(0, h.gen.batch(20))
	for b := 0; b < 2; b++ {
		w := h.worker("a", 1)
		w.B = b
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	lag, err := ComputeLag(ctx, h.s, h.l, 2, h.clock.Now())
	if err != nil || lag.Seconds != 0 || lag.BacklogFiles != 0 {
		t.Fatalf("lag %+v %v, want zero", lag, err)
	}
}

func TestLagIsTheOldestUnprocessedFile(t *testing.T) {
	h, ctx := setup(t, 1)
	h.upload(0, h.gen.batch(5))
	h.clock.Advance(30 * time.Second)
	h.upload(0, h.gen.batch(5))
	h.clock.Advance(60 * time.Second)
	lag, err := ComputeLag(ctx, h.s, h.l, 1, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if lag.Seconds != 90 || lag.BacklogFiles != 2 {
		t.Fatalf("lag %+v, want 90 s over 2 files", lag)
	}
}

func TestLagHandlerServesJSONAndCaches(t *testing.T) {
	h, _ := setup(t, 1)
	h.upload(0, h.gen.batch(5))
	h.clock.Advance(20 * time.Second)
	hd := NewLagHandler(h.s, h.l, 1, h.clock)
	get := func() map[string]float64 {
		rec := httptest.NewRecorder()
		hd.ServeHTTP(rec, httptest.NewRequest("GET", "/lag", nil))
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		var v map[string]float64
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := get()
	if first["lag_seconds"] != 20 || first["backlog_files"] != 1 {
		t.Fatalf("got %v", first)
	}
	h.clock.Advance(5 * time.Second) // inside the 10 s cache: the same answer
	if v := get(); v["lag_seconds"] != 20 {
		t.Fatalf("not cached: %v", v)
	}
	h.clock.Advance(6 * time.Second)
	if v := get(); v["lag_seconds"] != 31 {
		t.Fatalf("not refreshed after 10 s: %v", v)
	}
}
