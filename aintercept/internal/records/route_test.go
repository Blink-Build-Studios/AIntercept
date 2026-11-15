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
