package rollup

import (
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
)

// Atomic: the template ingest's writer fills in (keyflag.InTemplate) names a
// file exactly as the workers' layout does.
func TestInTemplateResolvesToTheLayoutsName(t *testing.T) {
	t0 := time.Date(2027, 3, 1, 10, 7, 35, 0, time.UTC)
	got := events.ResolveUploadKey(keyflag.InTemplate("p", "s:a/b", "inc", 7), t0)
	if want := InPrefix("p", "s:a/b") + InName(t0, "inc", 7); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if events.UploadTimeFormat != TimeFormat || keyflag.TimeFormat != TimeFormat {
		t.Fatalf("writer time format %q, index %q, layout %q", events.UploadTimeFormat, keyflag.TimeFormat, TimeFormat)
	}
}
