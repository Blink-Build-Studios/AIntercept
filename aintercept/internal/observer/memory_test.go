package observer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1024": 1024, "128Mi": 128 << 20, "2Gi": 2 << 30, "64K": 64 << 10} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%q: got %d, %v", in, got, err)
		}
	}
	if _, err := parseSize("lots"); err == nil {
		t.Error("garbage must fail")
	}
}

func TestPeerUtilizationAndFleetAverage(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"memory": statsOf(func() *membudget.Budget {
			b := membudget.New(100)
			b.Add(60)
			return b
		}())})
	}))
	defer peer.Close()
	u, ok := peerUtilization(context.Background(), peer.Client(), strings.TrimPrefix(peer.URL, "http://"))
	if !ok || u != 0.6 {
		t.Fatalf("peer utilization: %v %v", u, ok)
	}
	// Without a peers name, the fleet is this pod.
	b := membudget.New(100)
	b.Add(25)
	rec := httptest.NewRecorder()
	fleetHandler(b, "", "9001")(rec, httptest.NewRequest(http.MethodGet, "/stats/fleet", nil))
	var out struct {
		Pods int     `json:"pods"`
		Avg  float64 `json:"avg_utilization_percent"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Pods != 1 || out.Avg != 25 {
		t.Fatalf("fleet: %s", rec.Body)
	}
}
