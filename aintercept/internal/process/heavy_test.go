package process

import "testing"

// heavy skips a slow test that runs on one goroutine under -race, where the
// detector has nothing to check and slows it tenfold: make check runs it again
// without -race (check-go), so it still runs on every change.
func heavy(t *testing.T) {
	t.Helper()
	if raceOn {
		t.Skip("one goroutine, slow under -race: make check runs it without -race")
	}
}
