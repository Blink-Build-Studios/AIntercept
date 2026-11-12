package process

import (
	"encoding/json"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Every pick among records at the same time breaks the tie on the records
// themselves (time, then event key, call id or value), never on the order rows
// were read in, so the tables depend only on the set of records. Each case feeds
// the same records in every order and wants one answer.

// permutations is every ordering of xs.
func permutations[T any](xs []T) [][]T {
	if len(xs) <= 1 {
		return [][]T{append([]T(nil), xs...)}
	}
	var out [][]T
	for i := range xs {
		rest := append(append([]T(nil), xs[:i]...), xs[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]T{xs[i]}, p...))
		}
	}
	return out
}

// sameEveryOrder runs f on each ordering and fails unless all give what the first does.
func sameEveryOrder[T any](t *testing.T, name string, xs []T, f func([]T) any) {
	t.Helper()
	var want string
	for i, p := range permutations(xs) {
		b, _ := json.Marshal(f(p))
		if i == 0 {
			want = string(b)
		} else if string(b) != want {
			t.Fatalf("%s depends on order:\n got %s\nwant %s", name, b, want)
		}
	}
}

// An account's fields are each the latest sighting's that says it: a later
// sighting without an email doesn't hide an earlier one's.
func TestAccountFieldsAnyOrder(t *testing.T) {
	type sighting struct {
		a  events.AuthIdentity
		at int64
	}
	ss := []sighting{
		{events.AuthIdentity{AccountID: "x", Email: "a@example.com"}, 1},
		{events.AuthIdentity{AccountID: "x", Email: "b@example.com"}, 2},
		{events.AuthIdentity{AccountID: "x", Name: "Pat"}, 3},
		{events.AuthIdentity{AccountID: "x", Org: "Acme"}, 2},
		{events.AuthIdentity{AccountID: "x", Org: "Beta"}, 2},
	}
	sameEveryOrder(t, "account", ss, func(p []sighting) any {
		s := newState()
		for _, x := range p {
			s.account(&x.a, x.at)
		}
		a := s.accounts["x"]
		if a.Email != "b@example.com" || a.Name != "Pat" || a.Org != "Beta" || a.SeenNS != 3 {
			t.Fatalf("account %+v", a)
		}
		return a
	})
}

// A snapshot written before the per-field times keeps its fields until a newer
// sighting says otherwise.
func TestAccountFromOldSnapshot(t *testing.T) {
	s := newState()
	s.loadAccounts([]Account{{AccountID: "x", Email: "old@example.com", SeenNS: 10}})
	s.account(&events.AuthIdentity{AccountID: "x", Email: "older@example.com"}, 5)
	if a := s.accounts["x"]; a.Email != "old@example.com" {
		t.Errorf("an older sighting replaced the snapshot's email: %+v", a)
	}
	s.account(&events.AuthIdentity{AccountID: "x", Email: "new@example.com"}, 11)
	if a := s.accounts["x"]; a.Email != "new@example.com" || a.EmailNS != 11 {
		t.Errorf("a newer sighting didn't: %+v", a)
	}
}

// Reports, retries and prompt numbers at the same time pick by event key.
func TestRequestRowsTiesAnyOrder(t *testing.T) {
	call := ccCall("c1", "s", "acct", "k", 1000*msNS)
	call.reqID, call.prompt = "r1", "p1"
	failed := ccCall("c0", "s", "acct", "k", 500*msNS)
	failed.status, failed.end, failed.prompt = 529, 600*msNS, "p1"
	ev := func(key, name string, ts int64, md string) eventCols {
		e := ccEvent("b", "s", "acct", name, ts, md)
		e.EventKey = key
		return e
	}
	evs := []eventCols{
		ev("b/1", "tengu_api_success", 2000*msNS, `{"requestId":"r1","ttftMs":100}`),
		ev("b/2", "tengu_api_success", 2000*msNS, `{"requestId":"r1","ttftMs":200}`),
		ev("b/3", "tengu_input_prompt", 900*msNS, `{"cc_prompt_id":"p1","prompt_index":3}`),
		ev("b/4", "tengu_input_prompt", 900*msNS, `{"cc_prompt_id":"p1","prompt_index":4}`),
		ev("b/5", "tengu_api_retry", 700*msNS, `{"cc_prompt_id":"p1","attempt":1}`),
		ev("b/6", "tengu_api_retry", 700*msNS, `{"cc_prompt_id":"p1","attempt":2}`),
	}
	sameEveryOrder(t, "request rows", evs, func(p []eventCols) any {
		rows := byID(buildRows([]rowCall{failed, call}, p, nil, "acct")["acct"])
		r, f := rows["c1"], rows["c0"]
		if r.TTFTMS == nil || *r.TTFTMS != 100 || r.PromptIndex == nil || *r.PromptIndex != 4 ||
			f.Retries == nil || *f.Retries != 1 {
			t.Fatalf("ttft %v, prompt index %v, retries %v", r.TTFTMS, r.PromptIndex, f.Retries)
		}
		return rows
	})
}

// A session's device is the earliest event's (the least at the same time), and
// its env the newest (the greatest at the same time), however events are split
// into parts or ordered within them.
func TestSessionDeviceAndEnvAnyOrder(t *testing.T) {
	ev := func(dev, platform string, ts int64) eventCols {
		e := ccEvent("b", "s", "acct", "tengu_cancel", ts, "{}")
		e.DeviceID, e.EnvJSON = dev, `{"platform":"`+platform+`"}`
		return e
	}
	evs := []eventCols{ev("dev-b", "linux", 900), ev("dev-a", "darwin", 900), ev("dev-c", "win32", 1000), ev("dev-d", "aix", 1000)}
	call := ccCall("c", "s", "acct", "k", 2000)
	sameEveryOrder(t, "session device", evs, func(p []eventCols) any {
		for _, parts := range [][][]eventCols{{p}, {p[:1], p[1:]}, {p[:2], p[2:]}} {
			r := buildSessions([]rowCall{call}, parts, nil)["s"]
			if deref(r.DeviceID) != "dev-a" || deref(r.DevicePlatform) != "win32" {
				t.Fatalf("device %s, platform %s", deref(r.DeviceID), deref(r.DevicePlatform))
			}
		}
		return buildSessions([]rowCall{call}, [][]eventCols{p}, nil)["s"]
	})
	// With no event naming a device, the earliest call's.
	c1, c2 := ccCall("c1", "s", "acct", "k", 1000), ccCall("c2", "s", "acct", "k", 1000)
	c1.device, c2.device = "dev-y", "dev-x"
	sameEveryOrder(t, "call device", []rowCall{c1, c2}, func(p []rowCall) any {
		r := buildSessions(p, nil, nil)["s"]
		if deref(r.DeviceID) != "dev-x" {
			t.Fatalf("device %s", deref(r.DeviceID))
		}
		return r
	})
}

// A Codex thread's limits are the newest response's; at the same time, the
// greatest call id's.
func TestCodexLimitsAnyOrder(t *testing.T) {
	a := codexCall("ca", "th", "acct", 1000)
	a.provider = `{"x-codex-primary-used-percent":"10","x-codex-primary-window-minutes":"300"}`
	b := codexCall("cb", "th", "acct", 1000)
	b.provider = `{"x-codex-primary-used-percent":"20","x-codex-primary-window-minutes":"300"}`
	sameEveryOrder(t, "codex limits", []rowCall{a, b}, func(p []rowCall) any {
		r := buildSessions(p, nil, nil)["th"]
		if r.LimitsJSON == nil {
			t.Fatal("no limits")
		}
		return *r.LimitsJSON
	})
}
