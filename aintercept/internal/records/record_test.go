package records

import (
	"strings"
	"testing"
)

func TestIncarnationIsUniquePerStart(t *testing.T) {
	a, b := NewIncarnation("envoy-7b9c-2vbpd"), NewIncarnation("envoy-7b9c-2vbpd")
	if a == b || !strings.HasSuffix(a, "-envoy-7b9c-2vbpd") {
		t.Fatalf("incarnations %q %q", a, b)
	}
}
