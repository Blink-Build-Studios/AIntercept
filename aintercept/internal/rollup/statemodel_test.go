package rollup

import (
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// The real tables (the per-key state engine) through the rollup, the codec and
// the commit: exact against one pass, across runs and a fresh worker.
func TestStateModelTables(t *testing.T) {
	e := newEnv(t)
	e.model = stream.StateModel{}
	key := sessionKey("a")
	for i := 0; i < 6; i++ {
		e.clock.Advance(time.Second)
		e.ingest("a", 3)
		w := e.worker("w1")
		if i%2 == 1 {
			w = e.worker("w2") // a fresh worker: state from the checkpoint
		}
		if _, _, err := w.RunKey(e.ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	e.check()
}
