package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The dimension tables (§4.6) and the person tables the query rebuilds from
// them: person_map, person_sessions, account_facts and account_devices. The
// rebuild here is the API's SQL, step for step; both are checked against the
// reference.

func rowsOf[T any](o *streamOut, table string) []T {
	var out []T
	for _, r := range o.rows[table] {
		out = append(out, r.(T))
	}
	return out
}

// personTables rebuilds the person tables from the dimensions.
func personTables(o *streamOut) ([]process.PersonMapRow, []process.PersonSession, []process.AccountFact, []process.AccountDevice) {
	// dim_account, folded across keys (one key per account: one row).
	email := map[string]string{}
	ids := map[string]bool{"unattributed": true}
	for _, a := range rowsOf[process.Account](o, TableAccounts) {
		ids[a.AccountID] = true
		if a.Email != "" {
			email[a.AccountID] = strings.ToLower(a.Email)
		}
	}
	for _, p := range rowsOf[SessionPerson](o, TableSessionPerson) {
		ids[p.PersonID] = true
	}
	for _, p := range rowsOf[PersonDevice](o, TablePersonDevices) {
		ids[p.PersonID] = true
	}
	sessOf := map[string]map[string]bool{} // account → its sessions
	known := map[string]bool{}
	for _, sa := range rowsOf[SessionAccount](o, TableSessionAccounts) {
		if sessOf[sa.AccountID] == nil {
			sessOf[sa.AccountID] = map[string]bool{}
		}
		sessOf[sa.AccountID][sa.SessionID] = true
		known[sa.SessionID] = true
		ids[sa.AccountID] = true
	}
	linked := func(id string) []string {
		out := []string{id}
		if e := email[id]; e != "" {
			for a, x := range email {
				if a != id && x == e {
					out = append(out, a)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	// The People page's rows: each email group's accounts with a session, under the first.
	group := func(a string) []string {
		if email[a] == "" {
			return nil
		}
		var g []string
		for x, e := range email {
			if e == email[a] {
				g = append(g, x)
			}
		}
		if len(g) < 2 {
			return nil
		}
		sort.Strings(g)
		return g
	}
	var owned []string
	for a, ss := range sessOf {
		if len(ss) > 0 {
			owned = append(owned, a)
		}
	}
	sort.Strings(owned)
	members := map[string]map[string]bool{}
	for _, a := range owned {
		g := []string{a}
		if gr := group(a); gr != nil {
			g = nil
			for _, x := range gr {
				if len(sessOf[x]) > 0 {
					g = append(g, x)
				}
			}
		}
		if g[0] == a {
			members[a] = map[string]bool{}
			for _, x := range g {
				members[a][x] = true
			}
		}
	}
	var pm []process.PersonMapRow
	var ps []process.PersonSession
	var idl []string
	for id := range ids {
		idl = append(idl, id)
	}
	sort.Strings(idl)
	for _, id := range idl {
		sids := map[string]bool{}
		if id == "unattributed" {
			for _, v := range rowsOf[SessionVisible](o, TableSessionVisible) {
				if !known[v.SessionID] {
					sids[v.SessionID] = true
				}
			}
		} else {
			for _, a := range linked(id) {
				pm = append(pm, process.PersonMapRow{PersonID: id, AccountID: a, Directory: members[id][a]})
				for s := range sessOf[a] {
					sids[s] = true
				}
			}
		}
		var sl []string
		for s := range sids {
			sl = append(sl, s)
		}
		sort.Strings(sl)
		for _, s := range sl {
			ps = append(ps, process.PersonSession{PersonID: id, SessionID: s})
		}
	}

	// account_facts and account_devices: the per-key partials, combined.
	type acct struct {
		org             string
		orgNS           int64
		plan, codexPlan pick
		any             bool
	}
	accts := map[string]*acct{}
	get := func(a string) *acct {
		x := accts[a]
		if x == nil {
			x = &acct{}
			accts[a] = x
		}
		return x
	}
	for _, f := range rowsOf[AccountEvents](o, TableAccountEvents) {
		x := get(f.AccountID)
		x.any = true
		if f.Org != "" && (x.org == "" || f.OrgNS < x.orgNS || f.OrgNS == x.orgNS && f.Org < x.org) {
			x.org, x.orgNS = f.Org, f.OrgNS
		}
		x.plan.offer(f.Plan)
	}
	for _, c := range rowsOf[CodexPlan](o, TableCodexPlans) {
		get(c.AccountID).codexPlan.offer(c.Plan)
	}
	type dev struct {
		onEvents bool
		env      [3]pick
	}
	devs := map[[2]string]*dev{}
	for _, d := range rowsOf[AccountDevice](o, TableAccountDevices) {
		get(d.AccountID)
		k := [2]string{d.AccountID, d.DeviceID}
		x := devs[k]
		if x == nil {
			x = &dev{}
			devs[k] = x
		}
		x.onEvents = x.onEvents || d.OnEvents
		for i := range x.env {
			x.env[i].offer(d.Env[i])
		}
	}
	var facts []process.AccountFact
	var adevs []process.AccountDevice
	var al []string
	for a := range accts {
		al = append(al, a)
	}
	sort.Strings(al)
	for _, a := range al {
		x := accts[a]
		f := process.AccountFact{AccountID: a, Plan: x.plan.val(), CodexPlan: x.codexPlan.val()}
		if x.org != "" {
			f.Org = &x.org
		}
		facts = append(facts, f)
		var dl []string
		for k := range devs {
			if k[0] == a {
				dl = append(dl, k[1])
			}
		}
		sort.Strings(dl)
		for _, d := range dl {
			x := devs[[2]string{a, d}]
			r := process.AccountDevice{AccountID: a, DeviceID: d, OnEvents: x.onEvents}
			if x.onEvents {
				r.Platform, r.Terminal, r.Shell = x.env[0].val(), x.env[1].val(), x.env[2].val()
			}
			adevs = append(adevs, r)
		}
	}
	return pm, ps, facts, adevs
}

// pick is a value and when it was seen: the newest wins, the greatest at the
// same time (process newest).
type pick struct {
	v  string
	ts int64
	ok bool
}

func (p *pick) offer(o Pick) {
	if o.OK && (!p.ok || o.TS > p.ts || o.TS == p.ts && o.V > p.v) {
		*p = pick{o.V, o.TS, true}
	}
}

func (p pick) val() *string {
	if !p.ok {
		return nil
	}
	v := p.v
	return &v
}

func checkDims(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	ref := reference(rs)
	pm, ps, af, ad := personTables(o)
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"person_map", pm, ref.PersonMap}, {"person_sessions", ps, ref.PersonSess},
		{"account_facts", af, ref.AcctFacts}, {"account_devices", ad, ref.AcctDevices},
	} {
		g, w := reflect.ValueOf(c.got), reflect.ValueOf(c.want)
		if g.Len() == 0 && w.Len() == 0 {
			continue
		}
		if !reflect.DeepEqual(c.got, c.want) {
			t.Fatalf("%s: %s differ\n got %+v\nwant %+v", label, c.name, c.got, c.want)
		}
	}
	var accts []process.Account
	for _, a := range rowsOf[process.Account](o, TableAccounts) {
		accts = append(accts, a)
	}
	sort.Slice(accts, func(i, j int) bool { return accts[i].AccountID < accts[j].AccountID })
	if want := refAccounts(rs); !(len(accts) == 0 && len(want) == 0) && !reflect.DeepEqual(accts, want) {
		t.Fatalf("%s: accounts differ\n got %+v\nwant %+v", label, accts, want)
	}
}

func account(id, email, name, org string, t int64) records.Record {
	return rec(records.Record{Kind: records.KindAccount, Account: &records.Account{AccountID: id, Email: email, Name: name, Org: org, NS: t * ms}})
}

func ccCallOf(id, s, acct, dev string, t int64) *canon.Request {
	c := ccCall(id, s, t)
	c.PersonID, c.DeviceID = acct, dev
	return c
}

func acctEvent(s, acct, dev, name string, t int64, md, env string) records.Record {
	return ccEventWith(s, name, t, md, func(e *canon.Event) { e.PersonID, e.DeviceID, e.EnvJSON, e.OrgID = acct, dev, env, "org-"+acct })
}

var dimCases = []atomicCase{
	{"an account's first sighting", nil,
		func() records.Record { return account("a1", "x@y.z", "X", "", 1000) }, []string{CaseAccount}},
	{"a later sighting changes a field",
		func() []records.Record { return []records.Record{account("a1", "x@y.z", "X", "", 1000)} },
		func() records.Record { return account("a1", "w@y.z", "", "Org", 2000) }, []string{CaseAccount}},
	{"an earlier sighting doesn't",
		func() []records.Record { return []records.Record{account("a1", "x@y.z", "X", "", 2000)} },
		func() records.Record { return account("a1", "w@y.z", "W", "", 1000) }, []string{CaseAccount}},
	{"two accounts with one email link",
		func() []records.Record {
			return []records.Record{account("a1", "X@y.z", "", "", 1), callEnd(ccCallOf("c1", "s1", "a1", "", 10)), callEnd(ccCallOf("c2", "s2", "a2", "", 20))}
		},
		func() records.Record { return account("a2", "x@Y.z", "", "", 2) }, []string{CaseAccount}},
	{"a visible call names its session's account", nil,
		func() records.Record { return callEnd(ccCallOf("c1", "s1", "a1", "d1", 10)) },
		[]string{CaseSessionAccount, CaseSessionPerson, CasePersonDevice, CaseAccountDevice}},
	{"a hidden call names no session account", nil,
		func() records.Record { return callEnd(hidden(ccCallOf("c1", "s1", "a1", "d1", 10))) },
		[]string{CaseSessionPerson, CasePersonDevice}},
	{"a call in flight makes a session visible, unattributed", nil,
		func() records.Record { return callStart(ccCall("c1", "s1", 10)) }, []string{CaseSessionVisible}},
	{"its hidden ending takes that back",
		func() []records.Record { return []records.Record{callStart(ccCall("c1", "s1", 10))} },
		func() records.Record { return callEnd(hidden(ccCall("c1", "s1", 10))) },
		[]string{CaseSessionVisible}},
	{"a Claude Code event names its session's account, org, plan and device",
		nil, func() records.Record {
			return acctEvent("s1", "a1", "d1", "tengu_api_query", 10, `{"subscription_type":"max"}`, `{"platform":"darwin","terminal":"iterm","shell":"zsh"}`)
		},
		[]string{CaseSessionAccount, CaseSessionPerson, CasePersonDevice, CaseAccountEvents, CaseAccountDevice}},
	{"an earlier event's org wins, a newer one's plan and env",
		func() []records.Record {
			return []records.Record{acctEvent("s1", "a1", "d1", "tengu_api_query", 20, `{"subscription_type":"pro"}`, `{"platform":"linux"}`)}
		},
		func() records.Record {
			return acctEvent("s1", "a1", "d1", "tengu_api_query", 10, `{"subscription_type":"max"}`, `{"platform":"darwin","shell":"zsh"}`)
		},
		[]string{CaseSessionAccount, CaseSessionPerson, CasePersonDevice, CaseAccountEvents, CaseAccountDevice}},
	{"an event of no session still speaks for its account", nil,
		func() records.Record {
			return acctEvent("", "a1", "d1", "tengu_api_query", 10, `{}`, `{"terminal":"vt"}`)
		},
		[]string{CasePersonDevice, CaseAccountEvents, CaseAccountDevice}},
	{"a Codex call's plan", nil,
		func() records.Record {
			c := codexCall("x1", "t1", "turn", 10)
			c.PersonID, c.ProviderJSON = "a1", `{"x-codex-plan-type":"plus"}`
			return callEnd(c)
		},
		[]string{CaseSessionAccount, CaseSessionPerson, CaseCodexPlan}},
	{"an analytics batch's credential speaks for its sessions", nil,
		func() records.Record {
			r := ccEvent("s1", "tengu_api_query", 10, `{}`)
			r.Call = &canon.Request{KeyHash: "kb"}
			return r
		},
		[]string{CaseSessionCredential}},
	{"a visible call's credential", nil,
		func() records.Record { return callEnd(ccCall("c1", "s1", 10)) },
		[]string{CaseSessionVisible, CaseSessionCredential}},
}

var dimCaseSet = map[string]bool{
	CaseAccount: true, CaseSessionAccount: true, CaseSessionPerson: true, CasePersonDevice: true, CaseAccountEvents: true,
	CaseAccountDevice: true, CaseCodexPlan: true, CaseSessionVisible: true, CaseSessionCredential: true,
}

func TestDimsAtomic(t *testing.T) {
	for _, c := range dimCases {
		t.Run(c.name, func(t *testing.T) {
			batchN = map[string]int64{}
			var before []records.Record
			if c.before != nil {
				before = c.before()
			}
			r := c.rec()
			o := stream(before)
			d := o.apply(r)
			checkDims(t, c.name, append(before, r), o)
			if got := dedupe(only(d.Cases, dimCaseSet)); !reflect.DeepEqual(got, c.cases) {
				t.Fatalf("cases %v, want %v", got, c.cases)
			}
			if d2 := o.apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed something: %+v", d2)
			}
		})
	}
}

// dedupe is the cases each once, in first-taken order.
func dedupe(cs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cs {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func TestDimsShortSequencesEveryOrder(t *testing.T) {
	sets := map[string]func() []records.Record{
		"linked accounts and their sessions": func() []records.Record {
			return []records.Record{account("a1", "x@y.z", "", "", 1), account("a2", "X@y.z", "N", "", 3),
				callEnd(ccCallOf("c1", "s1", "a2", "d1", 10)), acctEvent("s2", "a1", "d2", "tengu_api_query", 5, `{}`, `{"shell":"bash"}`)}
		},
		"visible then hidden, an account sighting in between": func() []records.Record {
			return []records.Record{callStart(ccCall("c1", "s1", 10)), callEnd(hidden(ccCall("c1", "s1", 10))),
				account("a1", "", "A", "", 2), callEnd(ccCallOf("c2", "s1", "", "d9", 11))}
		},
	}
	for name, mk := range sets {
		batchN = map[string]int64{}
		rs := mk()
		permute(len(rs), func(p []int) {
			ordered := make([]records.Record, len(rs))
			for i, j := range p {
				ordered[i] = rs[j]
			}
			checkDims(t, fmt.Sprintf("%s %v", name, p), rs, stream(ordered))
		})
	}
}

// generateDims is accounts with shared and differing emails, their sessions'
// calls and events naming accounts and devices, events of no session, Codex
// plans, and calls in flight.
func generateDims(rng *rand.Rand, n int) []records.Record {
	var out []records.Record
	accts := []string{"a1", "a2", "a3", "a4", "a5"}
	emails := []string{"x@y.z", "X@Y.Z", "w@y.z", "", "v@y.z"}
	for i := 0; i < n; i++ {
		a := accts[rng.Intn(len(accts))]
		s := fmt.Sprintf("ds-%d", rng.Intn(8))
		dev := fmt.Sprintf("dev%d", rng.Intn(3))
		t := int64(1000 + rng.Intn(100000))
		switch rng.Intn(6) {
		case 0:
			out = append(out, account(a, emails[rng.Intn(len(emails))], []string{"", "N1", "N2"}[rng.Intn(3)], []string{"", "O"}[rng.Intn(2)], t))
		case 1:
			c := ccCallOf(fmt.Sprintf("dc-%d", i), s, []string{a, ""}[rng.Intn(2)], []string{dev, ""}[rng.Intn(2)], t)
			if rng.Intn(4) == 0 {
				c.HiddenKind = "prewarm"
			}
			if rng.Intn(3) > 0 {
				out = append(out, callStart(c))
			}
			if rng.Intn(5) > 0 {
				out = append(out, callEnd(c))
			}
		case 2, 3:
			md := []string{`{}`, `{"subscription_type":"pro"}`, `{"subscription_type":"max"}`}[rng.Intn(3)]
			env := []string{"", `{"platform":"darwin"}`, `{"platform":"linux","shell":"zsh"}`, `{"terminal":"t"}`}[rng.Intn(4)]
			out = append(out, acctEvent([]string{s, ""}[rng.Intn(4)/3], a, []string{dev, ""}[rng.Intn(2)], "tengu_api_query", t, md, env))
		case 4:
			c := codexCall(fmt.Sprintf("dx-%d", i), "th-"+s, "turn", t)
			c.PersonID, c.ProviderJSON = a, []string{"", `{"x-codex-plan-type":"plus"}`, `{"x-codex-plan-type":"pro"}`}[rng.Intn(3)]
			out = append(out, callEnd(c))
		default:
			r := ccEvent(s, "tengu_api_query", t, `{}`)
			r.Call = &canon.Request{KeyHash: fmt.Sprintf("k%d", rng.Intn(3))}
			out = append(out, r)
		}
	}
	return out
}

func TestDimsGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		rng := rand.New(rand.NewSource(seed))
		batchN = map[string]int64{}
		rs := generateDims(rng, 300)
		in := shuffled(rng, rs, 30)
		o := stream(in[:len(in)/2])
		for k, s := range o.states {
			b, err := Encode(s)
			if err != nil {
				t.Fatal(err)
			}
			if o.states[k], err = Decode(b); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range in[len(in)/2:] {
			o.apply(r)
		}
		checkDims(t, fmt.Sprintf("seed %d", seed), rs, o)
	}
}

func TestDimsFlatCost(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100,000-record session")
	}
	measure := func(n int) (float64, time.Duration) {
		s := New("s:big")
		for i := 0; i < n; i++ {
			if i%2 == 0 {
				s.Apply(callEnd(ccCallOf(fmt.Sprintf("c%d", i), "big", fmt.Sprintf("a%d", i%7), fmt.Sprintf("d%d", i%3), int64(i)*10)))
			} else {
				s.Apply(acctEvent("big", fmt.Sprintf("a%d", i%7), "d1", "tengu_api_query", int64(i)*10, `{"subscription_type":"pro"}`, `{"platform":"darwin"}`))
			}
		}
		pre := make([]records.Record, 3000)
		for k := range pre {
			at := int64(n)*10 + int64(k)
			if k%2 == 0 {
				pre[k] = callEnd(ccCallOf(fmt.Sprintf("cx%d", k), "big", "a1", "d1", at))
			} else {
				pre[k] = acctEvent("big", "a1", "d1", "tengu_api_query", at, `{"subscription_type":"max"}`, `{"shell":"zsh"}`)
			}
		}
		k := 0
		allocs := testing.AllocsPerRun(1000, func() { s.Apply(pre[k]); k++ })
		start := time.Now()
		for ; k < len(pre); k++ {
			s.Apply(pre[k])
		}
		return allocs, time.Since(start) / time.Duration(len(pre)-1000)
	}
	a10, t10 := measure(10)
	a1k, t1k := measure(1_000)
	a100k, t100k := measure(100_000)
	t.Logf("per record: n=10 %.1f allocs %v; n=1,000 %.1f allocs %v; n=100,000 %.1f allocs %v", a10, t10, a1k, t1k, a100k, t100k)
	if a100k > a10*1.5+4 {
		t.Errorf("allocations grow with the session: %.1f at 10, %.1f at 100,000", a10, a100k)
	}
	if t100k > 8*t10+20*time.Microsecond {
		t.Errorf("time grows with the session: %v at 10, %v at 100,000", t10, t100k)
	}
}

// refAccounts is the reference's accounts: every sighting folded in.
func refAccounts(rs []records.Record) []process.Account {
	m := map[string]*process.Account{}
	seen := map[string]bool{}
	for _, r := range rs {
		if r.Kind == records.KindAccount && !seen[r.RecordID] {
			seen[r.RecordID] = true
			a := r.Account
			process.AccountSighting(m, a.AccountID, a.Email, a.Name, a.Org, a.NS)
		}
	}
	var out []process.Account
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}
