package process

import (
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// RefInput is everything the derived tables are built from, in memory: the
// ended requests, the in-flight ones, the analytics events (one batch numbering:
// all in one part) and the accounts.
type RefInput struct {
	Requests []Request
	Pending  []Pending
	Events   []Event
	Accounts []Account
}

// DeriveRows is the derived tables from rows in memory: the single-pass
// reference (Decision 4) that the streaming processor's per-key updates
// (internal/stream/state) are tested against. It's the same builders the
// processor runs on its committed parts.
func DeriveRows(in RefInput) *Derived {
	st, out := newState(), &batch{}
	in2 := interner{}
	var calls []*rowCall
	for _, r := range in.Requests {
		st.addRequest(r, out)
		if !r.Synthetic {
			c := callFromRequest(colsOf(r), in2)
			calls = append(calls, &c)
		}
	}
	for _, p := range in.Pending {
		if !p.Done && p.StartNS != 0 {
			c := callFromPending(p)
			calls = append(calls, &c)
		}
	}
	agg := newEventAgg()
	agg.in = in2
	for _, e := range in.Events {
		st.addEvent(e, out)
		agg.add(eventColsOf(e))
	}
	agg.numberEvents()
	agg.numberUnnamed()
	sessions, _, _, people, _ := st.snapshot()
	d := newRowData(calls, []*eventAgg{agg}, in.Accounts)
	ids := map[string]bool{unattributed: true}
	for _, a := range in.Accounts {
		ids[a.AccountID] = true
	}
	for _, x := range people {
		ids[x.PersonID] = true
	}
	o := &Derived{}
	o.RequestRows, o.PersonLast = d.rows(sortedKeys(ids))
	o.PromptRows, o.PromptSteps, o.SessionMarks = d.promptRows()
	o.SessionRows = d.sessionRows(sessions)
	o.Presence = d.presenceSegments(o.SessionRows)
	o.EventRows, o.SessionTools = d.eventRows()
	o.SessionReqs = d.sessionRequestRows(o.SessionRows)
	pt := d.personTables(sortedKeys(ids))
	o.PersonMap, o.PersonSess, o.AcctFacts, o.AcctDevices, o.CallTools = pt.Map, pt.Sessions, pt.Facts, pt.Devices, pt.Tools
	return o
}

// colsOf is a request row's columns as the builders read them from a part.
func colsOf(r Request) requestCols {
	return requestCols{
		CallID: r.CallID, CorrelationID: r.CorrelationID, WSTurn: r.WSTurn, StartNS: r.StartNS, EndNS: r.EndNS,
		Host: r.Host, Method: r.Method, Path: r.Path, Status: r.Status, Synthetic: r.Synthetic, KeyHash: r.KeyHash,
		Error: r.Error, SessionID: r.SessionID, PromptID: r.PromptID, PersonID: r.PersonID, RequestClass: r.RequestClass,
		ClientRequestID: r.ClientRequestID, RequestID: r.RequestID, Model: r.Model, StopReason: r.StopReason,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens,
		CacheWriteTokens: r.CacheWriteTokens, RequestBytes: r.RequestBytes, ResponseBytes: r.ResponseBytes,
		ContentEncoding: r.ContentEncoding, ToolUsesJSON: r.ToolUsesJSON, AgentType: r.AgentType, Product: r.Product,
		Client: r.Client, HiddenKind: r.HiddenKind, Effort: r.Effort, DetailJSON: r.DetailJSON, DeviceID: r.DeviceID,
		IP: r.IP, ClientVersion: r.ClientVersion, ExtJSON: r.ExtJSON, ProviderJSON: r.ProviderJSON,
		UserAgent: r.UserAgent, ResponseStartNS: r.ResponseStartNS,
	}
}

// eventColsOf is an event row's columns as the builders read them.
func eventColsOf(e Event) eventCols {
	return eventCols{
		CorrelationID: e.CorrelationID, RowNS: e.RowNS, NS: e.NS, Seq: e.Seq, Source: e.Source, Name: e.Name,
		SessionID: e.SessionID, PromptID: e.PromptID, PersonID: e.PersonID, MetadataJSON: e.MetadataJSON,
		DeviceID: e.DeviceID, EnvJSON: e.EnvJSON, HeadSHA: e.HeadSHA, OrgID: e.OrgID, EventKey: e.EventKey,
	}
}

// AccountSighting folds one sighting of who an account is into the accounts
// (state.account: each field the latest sighting's that says it, the greatest
// value at the same time). The reference's accounts, and the streaming
// processor's dim_account, from the same rule.
func AccountSighting(accounts map[string]*Account, id, email, name, org string, at int64) {
	s := &state{accounts: accounts}
	s.account(&events.AuthIdentity{AccountID: id, Email: email, Name: name, Org: org}, at)
}

// SubscriptionType is an event's subscription_type, as account_facts reads it.
func SubscriptionType(metadataJSON string) (string, bool) {
	if !strings.Contains(metadataJSON, "subscription_type") {
		return "", false
	}
	return jsonString(jsonFields(metadataJSON, "subscription_type")["subscription_type"])
}

// EnvFields is an event's env platform, terminal and shell, as the device rows read them.
func EnvFields(envJSON string) (v [3]string, ok [3]bool) {
	if envJSON == "" {
		return
	}
	md := jsonFields(envJSON, envKeys[:]...)
	for i, k := range envKeys {
		v[i], ok[i] = jsonString(md[k])
	}
	return
}

// CodexPlanOf is a Codex call's x-codex-plan-type, as account_facts reads it.
func CodexPlanOf(providerJSON string) (string, bool) {
	if providerJSON == "" {
		return "", false
	}
	return jsonString(jsonFields(providerJSON, "x-codex-plan-type")["x-codex-plan-type"])
}
