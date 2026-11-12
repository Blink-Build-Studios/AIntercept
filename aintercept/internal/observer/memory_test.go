package observer

import (
	"testing"
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
