package records

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Hostile input (docs/PIPELINE.md §3.1): records are built with no NUL in any
// string and no client-controlled id over 512 bytes, so the state, the oracle
// and the API all see the same sanitized values and no row can wedge a key in
// Postgres.

// idColumns are the client-controlled ids canon caps: session, request, call,
// event and prompt ids, by column.
var idColumns = map[string]bool{
	"call_id": true, "correlation_id": true, "batch_id": true,
	"session_id": true, "batch_session": true, "batch_other_sessions": true,
	"prompt_id": true, "request_id": true, "client_request_id": true,
	"event_key": true, "client_event_id": true,
}

// eachString calls f with every string a record holds, by column (nested structs included).
func eachString(v reflect.Value, col string, f func(col, s string)) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			eachString(v.Elem(), col, f)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("parquet"), ",")
			eachString(v.Field(i), name, f)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			eachString(v.Index(i), col, f)
		}
	case reflect.String:
		f(col, v.String())
	}
}

func checkClean(t *testing.T, rs []Record) {
	t.Helper()
	for _, r := range rs {
		eachString(reflect.ValueOf(r), "", func(col, s string) {
			if strings.ContainsRune(s, 0) {
				t.Errorf("%s record: %s holds NUL: %q", r.Kind, col, s[:min(len(s), 40)])
			}
			if idColumns[col] && len(s) > canon.MaxIDBytes {
				t.Errorf("%s record: %s is %d bytes", r.Kind, col, len(s))
			}
			if strings.HasSuffix(col, "_json") && strings.Contains(s, `\u0000`) {
				t.Errorf("%s record: %s holds a NUL escape: %q", r.Kind, col, s[:min(len(s), 40)])
			}
		})
	}
}

func TestEveryStringFieldIsCleaned(t *testing.T) {
	// Every string field of a record, set to NUL and over the cap: whatever field
	// is added later is cleaned too.
	var r Record
	r.Call, r.Event, r.Account = &canon.Request{}, &canon.Event{}, &Account{}
	long := "a\x00" + strings.Repeat("b", 5000)
	var fill func(v reflect.Value)
	fill = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			fill(v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				fill(v.Field(i))
			}
		case reflect.Slice:
			if v.Type().Elem().Kind() == reflect.String {
				v.Set(reflect.ValueOf([]string{long, "x\x00"}))
			}
		case reflect.String:
			v.SetString(long)
		}
	}
	fill(reflect.ValueOf(&r))
	r.Call.ClientJSON = `{"a":"\u0000"}`
	Clean(&r)
	checkClean(t, []Record{r})
	if r.Call.ClientJSON != `{"a":"\ufffd"}` {
		t.Errorf("client_json = %q", r.Call.ClientJSON)
	}
	if r.Call.Path != strings.Replace(long, "\x00", "\uFFFD", 1) {
		t.Errorf("a long value that isn't an id keeps its length: path is %d bytes", len(r.Call.Path))
	}
	if !strings.HasPrefix(r.Call.SessionID, "sha256:") || r.Call.SessionID != r.Event.SessionID {
		t.Errorf("session ids: %q and %q", r.Call.SessionID, r.Event.SessionID)
	}
}

func TestHostileInputMakesCleanRecords(t *testing.T) {
	big := func(c string) string { return strings.Repeat(c, 5<<10) }
	sid, corr, rid, pid, evID := big("s"), big("c"), big("r"), big("p"), big("e")
	h := map[string]string{"x-claude-code-session-id": sid, "x-claude-code-prompt-id": pid, "user-agent": "claude-cli/2.1.3 (external\x00, cli)"}
	call := []events.Event{hdr(corr, true, h), respHdr(corr, rid), complete(corr, h)}
	call[2].SessionID, call[2].Path = sid, "/v1/messages?x=\x00"
	call[2].Provider = map[string]string{"request-id": rid}
	nul := hdr("n\x00ul", true, map[string]string{"x-claude-code-session-id": "s\x001"})
	batch := analytics("b" + big("b"))
	ev := clientEvent(batch[0].CorrelationID, evID, sid, time.Second)
	ev.ClientEvent.Name = "tengu\x00api_success"
	ev.ClientEvent.Data = map[string]any{"additional_metadata": map[string]any{"requestId": rid, "cc_prompt_id": pid, "note": "x\x00y"}}
	acct := complete("acct-call", nil)
	acct.Auth = &events.AuthIdentity{AccountID: "acct-1", Email: "a\x00@example.com", Name: "N\x00"}

	a := NewAssembler("inc")
	var rs []Record
	for _, e := range append(append(append(call, nul, acct), batch...), ev) {
		rs = append(rs, a.Apply(&e, line(e), "")...)
	}
	rs = append(rs, a.Flush()...)
	checkClean(t, rs)

	cleanSID := canon.CleanID(sid)
	var sawCall, sawEvent bool
	for _, r := range rs {
		switch {
		case r.Kind == KindCallEnd && r.Call.CorrelationID == canon.CleanID(corr):
			sawCall = true
			if r.Call.SessionID != cleanSID || r.RoutingKey != "s:"+cleanSID || r.Call.RequestID != canon.CleanID(rid) ||
				r.Call.PromptID != canon.CleanID(pid) || r.CallID != r.Call.CallID {
				t.Errorf("call: session %q, key %q, request %q, prompt %q", r.Call.SessionID, r.RoutingKey, r.Call.RequestID, r.Call.PromptID)
			}
		case r.Kind == KindClientEvent:
			sawEvent = true
			if r.Event.SessionID != cleanSID || r.RoutingKey != "s:"+cleanSID || r.Event.RequestID != canon.CleanID(rid) ||
				r.Event.CorrelationID != canon.CleanID(batch[0].CorrelationID) || r.BatchID != r.Event.CorrelationID ||
				r.Event.Name != "tengu\uFFFDapi_success" {
				t.Errorf("event: session %q, request %q, batch %q/%q, name %q", r.Event.SessionID, r.Event.RequestID,
					r.Event.CorrelationID, r.BatchID, r.Event.Name)
			}
		case r.Kind == KindCallEnd && r.BatchID != "":
			if r.BatchID != canon.CleanID(batch[0].CorrelationID) || r.BatchSession != cleanSID {
				t.Errorf("batch call: batch %q, session %q", r.BatchID, r.BatchSession)
			}
		}
	}
	if !sawCall || !sawEvent {
		t.Fatalf("records: %s", summary(rs))
	}
	// The detail objects are named by the cleaned call id, as the drawer reads them.
	a = NewAssembler("inc")
	a.KeepDetail = true
	for _, e := range call {
		a.Apply(&e, line(e), "")
	}
	for _, d := range a.TakeDetails() {
		if d.CorrelationID != canon.CleanID(corr) {
			t.Errorf("detail object for %q…", d.CorrelationID[:min(len(d.CorrelationID), 20)])
		}
	}
}

func TestCleanRecordsAreUnchanged(t *testing.T) {
	// Ordinary input is untouched: the same records as before.
	a := NewAssembler("inc")
	var rs []Record
	for _, e := range []events.Event{hdr("c1", true, ccHeaders), respHdr("c1", "req_1"), complete("c1", ccClient)} {
		rs = append(rs, a.Apply(&e, line(e), "")...)
	}
	for _, r := range rs {
		before := r
		c := *r.Call
		before.Call = &c
		Clean(&r)
		if !reflect.DeepEqual(before, r) {
			t.Errorf("a clean record changed:\n%+v\n%+v", before, r)
		}
	}
	if len(rs) != 2 || rs[1].Call.SessionID != "s1" || rs[1].RoutingKey != "s:s1" {
		t.Fatalf("records: %s", summary(rs))
	}
}
