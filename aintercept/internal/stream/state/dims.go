package state

import (
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The dimension tables (docs/STREAMING_PIPELINE.md §4.6): small facts each key
// states about sessions, accounts, credentials and devices. Nothing is stored
// per person: the query resolves people from these (email groups over
// dim_account, then accounts → sessions → credentials), so a new link changes
// one row, not history.
const (
	TableAccounts          = "dim_account"            // per account key: who it is, each field the latest sighting's
	TableSessionAccounts   = "dim_session_account"    // an account a session's visible call or Claude Code event names
	TableSessionVisible    = "dim_session_visible"    // a session with a visible model call (unattributed when no account)
	TableSessionPerson     = "dim_session_person"     // a session's person, as the people list counts it (least account named)
	TablePersonDevices     = "dim_person_device"      // a person and device a model call or event names together
	TableSessionCredential = "dim_session_credential" // a credential a session's visible calls or analytics batches used
	TableAccountEvents     = "dim_account_events"     // per key: what an account's Claude Code events here say (org, plan)
	TableAccountDevices    = "dim_account_device"     // per key: an account's device, from its events (with env) or calls
	TableCodexPlans        = "dim_codex_plan"         // per key: an account's newest Codex plan here
)

// Pick is a value and when it was seen; the query keeps the newest (the
// greatest at the same time).
type Pick struct {
	V  string `parquet:"v"`
	TS int64  `parquet:"ts"`
	OK bool   `parquet:"ok"`
}

type SessionAccount struct {
	SessionID string `parquet:"session_id"`
	AccountID string `parquet:"account_id"`
}

type SessionVisible struct {
	SessionID string `parquet:"session_id"`
}

type SessionPerson struct {
	SessionID string `parquet:"session_id"`
	PersonID  string `parquet:"person_id"`
}

type PersonDevice struct {
	PersonID string `parquet:"person_id"`
	DeviceID string `parquet:"device_id"`
}

type SessionCredential struct {
	SessionID string `parquet:"session_id"`
	KeyHash   string `parquet:"key_hash"`
}

// AccountEvents is what one key's Claude Code events say about an account: its
// earliest org (the least at the same time) and newest plan.
type AccountEvents struct {
	AccountID string `parquet:"account_id"`
	Org       string `parquet:"org"`
	OrgNS     int64  `parquet:"org_ns"`
	Plan      Pick   `parquet:"plan"`
}

// AccountDevice is an account's device as one key's records name it: OnEvents
// when an event does, with each env field's newest value.
type AccountDevice struct {
	AccountID string `parquet:"account_id"`
	DeviceID  string `parquet:"device_id"`
	OnEvents  bool   `parquet:"on_events"`
	Platform  Pick   `parquet:"platform"`
	Terminal  Pick   `parquet:"terminal"`
	Shell     Pick   `parquet:"shell"`
}

// env is the device's platform, terminal and shell, in process envKeys order.
func (a *AccountDevice) env() [3]*Pick { return [3]*Pick{&a.Platform, &a.Terminal, &a.Shell} }

// CodexPlan is an account's newest Codex plan in one key.
type CodexPlan struct {
	AccountID string `parquet:"account_id"`
	Plan      Pick   `parquet:"plan"`
}

// dimsState is a key's indexes for the dimension tables.
type dimsState struct {
	account *process.Account             // an account key's account
	visible bool                         // the session had a visible call when its rows were last written
	person  string                       // the session's least account
	creds   map[string]map[string]bool   // credential → what uses it (a call's id, an analytics batch's)
	credOf  map[string]string            // a call's id → its credential
	acctEv  map[string]*AccountEvents    // by account
	acctDev map[[2]string]*AccountDevice // by account and device
	codex   map[string]*CodexPlan        // by account
}

func newDimsState() *dimsState {
	return &dimsState{creds: map[string]map[string]bool{}, credOf: map[string]string{},
		acctEv: map[string]*AccountEvents{}, acctDev: map[[2]string]*AccountDevice{}, codex: map[string]*CodexPlan{}}
}

func (p *Pick) offer(ts int64, v string) bool {
	if p.OK && !(ts > p.TS || ts == p.TS && v > p.V) {
		return false
	}
	*p = Pick{v, ts, true}
	return true
}

func (s *KeyState) dims(r records.Record, d *Delta) {
	x, sid := s.dm, sessionOf(s.Key)
	switch {
	case r.Kind == records.KindAccount && r.Account != nil:
		a := r.Account
		if x.account == nil {
			x.account = &process.Account{AccountID: a.AccountID}
		}
		m := map[string]*process.Account{a.AccountID: x.account}
		process.AccountSighting(m, a.AccountID, a.Email, a.Name, a.Org, a.NS)
		d.Cases = append(d.Cases, CaseAccount)
		s.rows.put(d, TableAccounts, "acct", *x.account)
	case r.Kind == records.KindClientEvent && r.Event != nil:
		e := r.Event
		if e.PersonID != "" && e.DeviceID != "" {
			s.personDevice(d, e.PersonID, e.DeviceID)
		}
		if (e.Source == canon.ProductClaudeCode || e.Source == canon.ProductCodex) && e.SessionID != "" && e.SessionID == sid && e.PersonID != "" {
			s.sessionPerson(d, sid, e.PersonID)
		}
		if e.Source != canon.ProductClaudeCode {
			break
		}
		if e.SessionID != "" && e.SessionID == sid {
			if e.PersonID != "" {
				d.Cases = append(d.Cases, CaseSessionAccount)
				s.rows.put(d, TableSessionAccounts, "sa:"+e.PersonID, SessionAccount{sid, e.PersonID})
			}
			if r.Call != nil && r.Call.KeyHash != "" {
				s.useCred(d, sid, r.Call.KeyHash, "b:"+e.CorrelationID)
			}
		}
		if e.PersonID == "" {
			break
		}
		d.Cases = append(d.Cases, CaseAccountEvents)
		ae := x.acctEv[e.PersonID]
		if ae == nil {
			ae = &AccountEvents{AccountID: e.PersonID}
			x.acctEv[e.PersonID] = ae
		}
		next := *ae
		if e.OrgID != "" && (next.Org == "" || e.NS < next.OrgNS || e.NS == next.OrgNS && e.OrgID < next.Org) {
			next.Org, next.OrgNS = e.OrgID, e.NS
		}
		if v, ok := process.SubscriptionType(e.MetadataJSON); ok {
			next.Plan.offer(e.NS, v)
		}
		*ae = next
		s.rows.put(d, TableAccountEvents, "ae:"+e.PersonID, next)
		if e.DeviceID != "" {
			v, ok := process.EnvFields(e.EnvJSON)
			s.accountDevice(d, e.PersonID, e.DeviceID, true, e.NS, v, ok)
		}
	case r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection:
		c := r.Call
		s.dropCred(d, sid, c.CallID)
		ts := firstNZ(c.StartNS, c.EndNS)
		if canon.IsModelCall(c) && c.SessionID == sid {
			if c.PersonID != "" {
				s.sessionPerson(d, sid, c.PersonID)
			}
			if c.PersonID != "" && c.DeviceID != "" {
				s.personDevice(d, c.PersonID, c.DeviceID)
			}
		}
		agent := agentCall(c.Product, c.Host, c.Method, c.Path, c.SessionID, c.WSTurn, c.Synthetic)
		if agent && c.PersonID != "" && c.Product == canon.ProductCodex {
			if v, ok := process.CodexPlanOf(c.ProviderJSON); ok {
				cp := x.codex[c.PersonID]
				if cp == nil {
					cp = &CodexPlan{AccountID: c.PersonID}
					x.codex[c.PersonID] = cp
				}
				d.Cases = append(d.Cases, CaseCodexPlan)
				if cp.Plan.offer(ts, v) {
					s.rows.put(d, TableCodexPlans, "cp:"+c.PersonID, *cp)
				}
			}
		}
		if visibleEnded(c) && c.SessionID == sid {
			if c.PersonID != "" {
				d.Cases = append(d.Cases, CaseSessionAccount)
				s.rows.put(d, TableSessionAccounts, "sa:"+c.PersonID, SessionAccount{sid, c.PersonID})
				if c.DeviceID != "" {
					s.accountDevice(d, c.PersonID, c.DeviceID, false, 0, [3]string{}, [3]bool{})
				}
			}
			if c.KeyHash != "" {
				s.useCredCall(d, sid, c.KeyHash, c.CallID)
			}
		}
	case r.Kind == records.KindCallStart:
		c := r.Call
		if !s.ses.ended[c.CallID] && visibleStart(c) && c.SessionID == sid && c.KeyHash != "" {
			s.useCredCall(d, sid, c.KeyHash, c.CallID)
		}
	}
	if v := len(s.ses.callAt) > 0; v != x.visible && sid != "" {
		x.visible = v
		d.Cases = append(d.Cases, CaseSessionVisible)
		if v {
			s.rows.put(d, TableSessionVisible, "sv", SessionVisible{sid})
		} else {
			s.rows.drop(d, TableSessionVisible, "sv")
		}
	}
}

func (s *KeyState) sessionPerson(d *Delta, sid, person string) {
	d.Cases = append(d.Cases, CaseSessionPerson)
	if s.dm.person == "" || person < s.dm.person {
		s.dm.person = person
		s.rows.put(d, TableSessionPerson, "sp", SessionPerson{sid, person})
	}
}

func (s *KeyState) personDevice(d *Delta, person, device string) {
	d.Cases = append(d.Cases, CasePersonDevice)
	s.rows.put(d, TablePersonDevices, "pd:"+person+"\x00"+device, PersonDevice{person, device})
}

func (s *KeyState) accountDevice(d *Delta, acct, dev string, onEvents bool, ts int64, v [3]string, ok [3]bool) {
	d.Cases = append(d.Cases, CaseAccountDevice)
	k := [2]string{acct, dev}
	ad := s.dm.acctDev[k]
	if ad == nil {
		ad = &AccountDevice{AccountID: acct, DeviceID: dev}
		s.dm.acctDev[k] = ad
	}
	ad.OnEvents = ad.OnEvents || onEvents
	env := ad.env()
	for i := range v {
		if ok[i] {
			env[i].offer(ts, v[i])
		}
	}
	s.rows.put(d, TableAccountDevices, "ad:"+acct+"\x00"+dev, *ad)
}

// useCred notes that a source (a call, an analytics batch) used a credential in the session.
func (s *KeyState) useCred(d *Delta, sid, hash, source string) {
	d.Cases = append(d.Cases, CaseSessionCredential)
	set := s.dm.creds[hash]
	if set == nil {
		set = map[string]bool{}
		s.dm.creds[hash] = set
	}
	set[source] = true
	s.rows.put(d, TableSessionCredential, "sc:"+hash, SessionCredential{sid, hash})
}

func (s *KeyState) useCredCall(d *Delta, sid, hash, callID string) {
	s.dm.credOf[callID] = hash
	s.useCred(d, sid, hash, "c:"+callID)
}

// dropCred takes back a call's use of its credential (its start's, now that its
// ending is here; the ending puts it back when it's visible).
func (s *KeyState) dropCred(d *Delta, sid, callID string) {
	hash, ok := s.dm.credOf[callID]
	if !ok {
		return
	}
	delete(s.dm.credOf, callID)
	set := s.dm.creds[hash]
	delete(set, "c:"+callID)
	if len(set) == 0 {
		delete(s.dm.creds, hash)
		s.rows.drop(d, TableSessionCredential, "sc:"+hash)
	}
}
