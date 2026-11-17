// Package records is the observer's canonical output (docs/PIPELINE.md §2.1):
// each finished or started call, each analytics event and each account
// sighting as one record, written with each completed call's rows to the raw
// files the workers load.
package records

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
)

// Version is the record schema's version: a column changes only with it.
const Version = 1

// The kinds of record.
const (
	KindCallStart    = "call_start"    // a call whose request is known (HTTP request end, a WebSocket turn's response.create)
	KindCallEnd      = "call_end"      // a call's request row, as the processor writes it today
	KindWSConnection = "ws_connection" // a WebSocket connection's own row, at its close
	KindClientEvent  = "client_event"  // one analytics event a client reported
	KindAccount      = "account"       // who an account is, as a row named it
	KindVerdict      = "verdict"       // one verdict a call's Interceptor returned
)

// Record is one canonical record. Exactly one of Call, Event, Account and
// Verdict is set,
// except that a client event also carries its batch request's call (Call: the
// carrier's identity, for routing an event that names no session).
type Record struct {
	Version  int32  `parquet:"record_version"`
	RecordID string `parquet:"record_id"` // <incarnation>-<20-digit seq>: unique, and sorts in emission order
	Kind     string `parquet:"kind"`
	Instance string `parquet:"instance"` // the observer that saw it
	CallID   string `parquet:"call_id,optional"`
	// RoutingKey is the key the processor groups by (RoutingKey), Bucket its bucket.
	RoutingKey string `parquet:"routing_key"`
	Bucket     int32  `parquet:"bucket"`
	// An analytics batch: the request that carried the event (BatchID, its
	// correlation id), the event's place in the whole batch (BatchN) and its rank
	// among the batch's Claude Code events (BatchCCN), and on the
	// request's own call record the session its earliest event names.
	BatchID string `parquet:"batch_id,optional"`
	BatchN  int64  `parquet:"batch_n,optional"`
	// BatchCCN is a Claude Code event's rank among its batch's Claude Code
	// events, as the event rows number them (their event id); 0 for others.
	BatchCCN     int64  `parquet:"batch_cc_n,optional"`
	BatchSession string `parquet:"batch_session,optional"`
	// On an analytics batch's own call record, the whole batch as the request
	// log reads it (its batch_members, across every session): its Claude Code
	// and Codex events' count (BatchEvents), their least non-empty name
	// (BatchName) and the sessions they name other than BatchSession
	// (BatchOtherSessions, sorted). Records written before these columns read
	// them as none.
	BatchEvents        int64    `parquet:"batch_events,optional"`
	BatchName          string   `parquet:"batch_name,optional"`
	BatchOtherSessions []string `parquet:"batch_other_sessions"`
	// EventKey is a client event's key: its record id.
	EventKey string `parquet:"event_key,optional"`

	Call    *canon.Request `parquet:"call,optional"`
	Event   *canon.Event   `parquet:"event,optional"`
	Account *Account       `parquet:"account,optional"`
	Verdict *Verdict       `parquet:"verdict,optional"`
}

// Verdict is one Interceptor verdict (events.Verdict), as the verdicts table
// holds it: keyed by its call (CallID) and Seq, the assembler's sequence
// number for its record.
type Verdict struct {
	Seq             int64  `parquet:"seq"`
	NS              int64  `parquet:"ns"`
	Callback        string `parquet:"callback"`
	Action          string `parquet:"action"`
	Applied         bool   `parquet:"applied"`
	Mode            string `parquet:"mode,optional"`
	MessageID       string `parquet:"message_id,optional"`
	RespondMode     string `parquet:"respond_mode,optional"`
	Reason          string `parquet:"reason,optional"`
	AnnotationsJSON string `parquet:"annotations_json,optional"`
	LatencyMS       int64  `parquet:"latency_ms,optional"`
}

// Account is one sighting of who an account is.
type Account struct {
	AccountID string `parquet:"account_id"`
	Email     string `parquet:"email,optional"`
	Name      string `parquet:"name,optional"`
	Org       string `parquet:"org,optional"`
	NS        int64  `parquet:"ns"`
}

// NewIncarnation names one run of an observer: its start time (19 digits),
// random bytes (8 hex) and pod, so record ids stay unique when a container
// restarts with the same name. The fixed-width parts come first, so ids from
// different incarnations sort by when they started.
func NewIncarnation(pod string) string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%019d-%s-%s", time.Now().UnixNano(), hex.EncodeToString(b), pod)
}
