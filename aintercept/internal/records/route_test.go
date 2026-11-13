package records

import (
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
)

func TestRoutingKey(t *testing.T) {
	day := time.Date(2026, 11, 12, 23, 59, 0, 0, time.UTC).UnixNano()
	cases := []struct {
		name string
		r    Record
		want string
	}{
		{"call with a session", Record{Kind: KindCallEnd, Call: &canon.Request{SessionID: "s1", KeyHash: "k", StartNS: day}}, "s:s1"},
		{"call without one: credential and day", Record{Kind: KindCallEnd, Call: &canon.Request{KeyHash: "k", StartNS: day}}, "c:k:2026-11-12"},
		{"call without start time: its end's day", Record{Kind: KindCallEnd, Call: &canon.Request{KeyHash: "k", EndNS: day}}, "c:k:2026-11-12"},
		{"no credential either", Record{Kind: KindCallStart, Call: &canon.Request{StartNS: day}}, "c::2026-11-12"},
		{"no credential, an IP: IP and hour", Record{Kind: KindCallEnd, Call: &canon.Request{IP: "10.1.2.3", StartNS: day}}, "ip:10.1.2.3:" + time.Unix(0, day).UTC().Format("2006-01-02T15")},
		{"an event with no session or credential: its carrier's IP and hour", Record{Kind: KindClientEvent, Event: &canon.Event{RowNS: day}, Call: &canon.Request{IP: "10.1.2.3"}}, "ip:10.1.2.3:" + time.Unix(0, day).UTC().Format("2006-01-02T15")},
		{"an analytics call: its batch's session", Record{Kind: KindCallEnd, BatchSession: "s2", Call: &canon.Request{SessionID: "", KeyHash: "k", StartNS: day}}, "s:s2"},
		{"an event: its own session", Record{Kind: KindClientEvent, Event: &canon.Event{SessionID: "s3", RowNS: day}}, "s:s3"},
		{"an event without one: its carrier's credential and day", Record{Kind: KindClientEvent, Event: &canon.Event{RowNS: day}, Call: &canon.Request{KeyHash: "k"}}, "c:k:2026-11-12"},
		{"an account sighting", Record{Kind: KindAccount, Account: &Account{AccountID: "a1"}}, "a:a1"},
	}
	for _, c := range cases {
		if got := RoutingKey(&c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The bucket function is shared with the API (Python): these vectors pin it.
func TestBucketVectors(t *testing.T) {
	for key, want := range map[string]int{"": 50, "s:s1": 37, "c:k:2026-11-12": 60, "a:a1": 5} {
		if got := BucketOf(key, DefaultBucketBits); got != want {
			t.Errorf("BucketOf(%q) = %d, want %d", key, got, want)
		}
	}
	for i := 0; i < 1000; i++ {
		if b := BucketOf(string(rune('a'+i%26))+string(rune(i)), DefaultBucketBits); b < 0 || b >= 1<<DefaultBucketBits {
			t.Fatalf("bucket %d out of range", b)
		}
	}
}

// One more hash bit splits each bucket in two, never reshuffling keys.
func TestBucketsSplitCleanly(t *testing.T) {
	for i := 0; i < 2000; i++ {
		k := "s:" + string(rune(i)) + "x"
		if BucketOf(k, 7)>>1 != BucketOf(k, 6) {
			t.Fatalf("%q: 7-bit bucket %d isn't a half of 6-bit bucket %d", k, BucketOf(k, 7), BucketOf(k, 6))
		}
	}
}

func TestObjectKey(t *testing.T) {
	at := time.Date(2026, 11, 12, 9, 7, 31, 0, time.UTC)
	got := ObjectKey("ailogger", 5, at, "pod-1-ab", 42)
	want := "ailogger/records/v1/bucket=05/minute=2026-11-12T09:07/pod-1-ab-000000000042.parquet"
	if got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}
