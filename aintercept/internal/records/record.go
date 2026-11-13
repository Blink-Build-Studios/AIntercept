// Package records is the observer's canonical output (docs/STREAMING_PIPELINE.md
// §3): each finished or started call, each analytics event and each account
// sighting as one record, routed by key into a bucket, written as Parquet
// under records/v1/bucket=NN/minute=<upload minute>/. The bucketed processor
// reads only these. The raw log is written as before, beside them.
package records

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/zcodec"
	"time"

	"github.com/parquet-go/parquet-go"

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
)

// Record is one canonical record. Exactly one of Call, Event and Account is set,
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
	// EventKey is a client event's key: its record id.
	EventKey string `parquet:"event_key,optional"`

	Call    *canon.Request `parquet:"call,optional"`
	Event   *canon.Event   `parquet:"event,optional"`
	Account *Account       `parquet:"account,optional"`
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

// Encode is records as a Parquet file (zstd).
func Encode(rs []Record) ([]byte, error) {
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[Record](&buf, parquet.Compression(zcodec.Shared))
	if _, err := w.Write(rs); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode reads a records file.
func Decode(b []byte) ([]Record, error) {
	return parquet.Read[Record](bytes.NewReader(b), int64(len(b)))
}

// EncodeWithNewest is Encode, with the newest raw row time its pod had seen
// (stub: not yet written).
func EncodeWithNewest(rs []Record, newestRowNS int64) ([]byte, error) { return Encode(rs) }

// DecodeWithNewest is Decode, with the file's newest raw row time (0: none).
func DecodeWithNewest(b []byte) ([]Record, int64, error) {
	rs, err := Decode(b)
	return rs, 0, err
}
