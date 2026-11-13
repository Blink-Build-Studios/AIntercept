package state

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The harness: records built the way the observer writes them, the single-pass
// reference over a set of them (process.DeriveRows), and the streaming state's
// rows after applying them one at a time, per routing key.

const ms = int64(1_000_000)

var recSeq int

// rec is a record with a fresh id and its routing key.
func rec(r records.Record) records.Record {
	recSeq++
	r.Version = records.Version
	if r.RecordID == "" {
		r.RecordID = fmt.Sprintf("inc-%06d", recSeq)
	}
	r.RoutingKey = records.RoutingKey(&r)
	r.Bucket = int32(records.BucketOf(r.RoutingKey, records.DefaultBucketBits))
	return r
}

var batchN = map[string]int64{}

// ccEvent is a Claude Code analytics event of session s at t milliseconds, in
// its session's batch (numbered in order: the reference ranks a batch's events
// by seq, so seq is the number).
func ccEvent(s, name string, t int64, md string) records.Record {
	return ccEventWith(s, name, t, md, nil)
}

func ccEventWith(s, name string, t int64, md string, set func(*canon.Event)) records.Record {
	recSeq++
	key := fmt.Sprintf("ev-%06d", recSeq)
	b := "batch-" + s
	batchN[b]++
	e := &canon.Event{
		EventKey: key, CorrelationID: b, RowNS: t * ms, NS: t * ms, Seq: batchN[b], Source: canon.ProductClaudeCode,
		Name: name, SessionID: s, MetadataJSON: md,
	}
	if set != nil {
		set(e)
	}
	return rec(records.Record{Kind: records.KindClientEvent, EventKey: key, BatchID: b, BatchN: batchN[b], BatchCCN: batchN[b], Event: e})
}

// blur is an event ending a blur window of d ms at t.
func blur(s string, t, d int64) records.Record {
	return ccEvent(s, "tengu_return_to_session", t, fmt.Sprintf(`{"blurDurationMs":%d}`, d))
}

// sleep is an event ending a sleep window of d ms at t.
func sleep(s string, t, d int64) records.Record {
	return ccEvent(s, "tengu_event_loop_stall", t, fmt.Sprintf(`{"likely_sleep":true,"clock_jump_ms":%d}`, d))
}

// ccCall is a Claude Code model call's request row: session s, at t ms.
func ccCall(id, s string, t int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: t * ms, EndNS: (t + 500) * ms,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Status: 200, SessionID: s,
		Product: canon.ProductClaudeCode, KeyHash: "k1"}
}

func callEnd(c *canon.Request) records.Record {
	return rec(records.Record{Kind: records.KindCallEnd, CallID: c.CallID, Call: c})
}

// callStart is the call's start record: what the observer knows of it in flight.
func callStart(c *canon.Request) records.Record {
	x := *c
	x.EndNS, x.Status, x.Product, x.HiddenKind, x.OutputTokens = 0, 0, "", "", 0
	return rec(records.Record{Kind: records.KindCallStart, CallID: c.CallID, Call: &x})
}

// reference is the single-pass reference over a set of records: each call's
// ending (or its start, for a call still in flight), every event and account.
func reference(rs []records.Record) *process.Derived { return process.DeriveRows(refInput(rs)) }

// refInput is the reference's input: the fact rows today's processor holds.
func refInput(rs []records.Record) process.RefInput {
	// One pass in capture order: a batch's events in the order ingest numbered
	// them, so events tied on every ordering key take the ranks ingest gave them
	// (records' batch_n), not whatever order a shuffled test fed them in.
	rs = append([]records.Record(nil), rs...)
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if (a.Kind == records.KindClientEvent) != (b.Kind == records.KindClientEvent) {
			return a.Kind != records.KindClientEvent
		}
		if a.Kind != records.KindClientEvent {
			return false
		}
		return a.BatchID < b.BatchID || a.BatchID == b.BatchID && a.BatchN < b.BatchN
	})
	ended := map[string]bool{}
	for _, r := range rs {
		if r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection {
			ended[r.CallID] = true
		}
	}
	var in process.RefInput
	seenCall, seenRec := map[string]bool{}, map[string]bool{}
	var accts map[string]*process.Account
	for _, r := range rs {
		if seenRec[r.RecordID] {
			continue
		}
		seenRec[r.RecordID] = true
		switch r.Kind {
		case records.KindCallEnd, records.KindWSConnection:
			if !seenCall[r.CallID] {
				seenCall[r.CallID] = true
				in.Requests = append(in.Requests, *r.Call)
			}
		case records.KindCallStart:
			if !ended[r.CallID] && !seenCall["start:"+r.CallID] && !strings.Contains(r.CallID, "#") {
				seenCall["start:"+r.CallID] = true
				in.Pending = append(in.Pending, pendingOf(r.Call))
			}
		case records.KindClientEvent:
			in.Events = append(in.Events, *r.Event)
		case records.KindAccount:
			if accts == nil {
				accts = map[string]*process.Account{}
			}
			a := r.Account
			process.AccountSighting(accts, a.AccountID, a.Email, a.Name, a.Org, a.NS)
		}
	}
	for _, a := range accts {
		in.Accounts = append(in.Accounts, *a)
	}
	sort.Slice(in.Accounts, func(i, j int) bool { return in.Accounts[i].AccountID < in.Accounts[j].AccountID })
	// in.Events is already in capture order (by batch, then place: the sort above).
	return in
}

// pendingOf is a call in flight as the processor holds it.
func pendingOf(c *canon.Request) canon.Pending { return canon.PendingOfStart(c) }

// stream applies the records in order to a state per routing key, and is every
// key's rows (folded from the deltas) and the cases taken.
type streamOut struct {
	states map[string]*KeyState
	rows   map[string]map[string]any // table → key/id → row, from the deltas alone
	cases  map[string]int
}

func stream(rs []records.Record) *streamOut {
	o := &streamOut{states: map[string]*KeyState{}, rows: map[string]map[string]any{}, cases: map[string]int{}}
	for _, r := range rs {
		o.apply(r)
	}
	return o
}

func (o *streamOut) apply(r records.Record) Delta {
	s := o.states[r.RoutingKey]
	if s == nil {
		s = New(r.RoutingKey)
		o.states[r.RoutingKey] = s
	}
	d := s.Apply(r)
	for _, c := range d.Cases {
		o.cases[c]++
	}
	for _, c := range d.Changes {
		t := o.rows[c.Table]
		if t == nil {
			t = map[string]any{}
			o.rows[c.Table] = t
		}
		if c.Row == nil {
			delete(t, r.RoutingKey+"/"+c.ID)
		} else {
			t[r.RoutingKey+"/"+c.ID] = c.Row
		}
	}
	return d
}

// presenceRows is the stream's presence rows in stored order: session, group, start.
func (o *streamOut) presenceRows() []Presence {
	var out []Presence
	for _, r := range o.rows[TablePresence] {
		out = append(out, r.(Presence))
	}
	sortPresence(out)
	return out
}

func sortPresence(p []Presence) {
	sort.Slice(p, func(i, j int) bool {
		a, b := p[i], p[j]
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.FromNS < b.FromNS
	})
}

// refPresence is the reference's presence rows in the stored shape. It checks
// that the reference's seq is the (group, start) order the query numbers by.
func refPresence(d *process.Derived) ([]Presence, error) {
	var out []Presence
	last := map[string]Presence{}
	for _, r := range d.Presence {
		g := int32(0)
		if r.State == "asleep" {
			g = 1
		}
		p := Presence{SessionID: r.SessionID, Group: g, FromNS: r.FromNS, ToNS: r.ToNS, State: r.State}
		if prev, ok := last[r.SessionID]; ok && !(prev.Group < g || prev.Group == g && prev.FromNS < p.FromNS) {
			return nil, fmt.Errorf("session %s: seq order isn't (group, start) at %+v", r.SessionID, p)
		}
		last[r.SessionID] = p
		out = append(out, p)
	}
	sortPresence(out)
	return out, nil
}

// shuffled is rs in a random order, some duplicated.
func shuffled(rng *rand.Rand, rs []records.Record, dupes int) []records.Record {
	out := append([]records.Record(nil), rs...)
	for i := 0; i < dupes && len(rs) > 0; i++ {
		out = append(out, rs[rng.Intn(len(rs))])
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
