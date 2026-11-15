package certsvc

import (
	"testing"
	"time"
)

// A missed SDS push must be caught by a reconnect before the leaf it replaced
// expires: the forced reconnect comes well inside renewBefore (leafTTL/4).
func TestReconnectBeforeALeafCanExpire(t *testing.T) {
	if renewBefore := 6 * time.Hour / 4; maxConnAge+time.Minute >= renewBefore {
		t.Errorf("maxConnAge %s (+1m grace) is not under renewBefore %s", maxConnAge, renewBefore)
	}
	if n := len(serverOptions()); n == 0 {
		t.Error("certsvc's gRPC server has no keepalive options")
	}
}
