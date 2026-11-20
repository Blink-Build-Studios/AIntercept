package observer

import "testing"

// Contract v1 was removed: an observer still set to it refuses to start,
// rather than talk v2 to a v1 Interceptor and have every call fail.
func TestCheckContract(t *testing.T) {
	for c, ok := range map[string]bool{"": true, "v2": true, "v1": false, "V2": false, "v3": false} {
		if err := checkContract(c); (err == nil) != ok {
			t.Errorf("INTERCEPTOR_CONTRACT %q: %v", c, err)
		}
	}
}
