package events

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func claudeCodeBatch() []byte {
	return []byte(`{"events":[
	 {"event_type":"ClaudeCodeInternalEvent","event_data":{"event_name":"tengu_api_success","event_id":"e1",
	  "client_timestamp":"2026-11-02T14:20:35.902Z","session_id":"s1","device_id":"d1","model":"claude-opus-5-5",
	  "auth":{"account_uuid":"a1","organization_uuid":"o1"},"env":{"terminal":"iTerm.app","version":"2.1.308"},
	  "additional_metadata":"` + b64(`{"costUSD":0.12,"attributionMcpServer":"claude_ai_Vercel","subscription_type":"max"}`) + `",
	  "process":"` + b64(`{"rss":321573248,"uptime":3727.9}`) + `"}},
	 {"event_type":"ClaudeCodeInternalEvent","event_data":{"event_name":"tengu_bash_tool_command_executed","event_id":"e2",
	  "session_id":"s1","agent_id":"ag1","agent_type":"subagent",
	  "additional_metadata":"` + b64(`{"bash_argv0":"kubectl","exit_code":0,"fileExtension":"py,argv[1]"}`) + `"}}]}`)
}

func TestClientEventsFromRequest_ClaudeCode(t *testing.T) {
	evs := ClientEventsFromRequest("", "/api/event_logging/v2/batch", claudeCodeBatch(), "")
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	a := evs[0]
	if a.Name != "tengu_api_success" || a.SessionID != "s1" || a.DeviceID != "d1" || a.AccountUUID != "a1" ||
		a.OrganizationUUID != "o1" || a.Model != "claude-opus-5-5" || a.Type != "ClaudeCodeInternalEvent" || a.Source != "claude_code" {
		t.Fatalf("columns: %+v", a)
	}
	md, ok := a.Data["additional_metadata"].(map[string]any)
	if !ok || md["costUSD"] != 0.12 || md["attributionMcpServer"] != "claude_ai_Vercel" {
		t.Fatalf("additional_metadata not decoded: %#v", a.Data["additional_metadata"])
	}
	if pr, ok := a.Data["process"].(map[string]any); !ok || pr["uptime"] != 3727.9 {
		t.Fatalf("process not decoded: %#v", a.Data["process"])
	}
	b := evs[1]
	// Kept in full in experimentation mode, fileExtension included.
	if md, _ := b.Data["additional_metadata"].(map[string]any); md["bash_argv0"] != "kubectl" || md["fileExtension"] != "py,argv[1]" || b.AgentID != "ag1" {
		t.Fatalf("bash event: %+v", b)
	}
}

func TestClientEventsFromRequest_GzipAndNonBatch(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(claudeCodeBatch())
	_ = w.Close()
	if n := len(ClientEventsFromRequest("", "/api/event_logging/v2/batch", buf.Bytes(), "gzip")); n != 2 {
		t.Fatalf("gzip batch: want 2, got %d", n)
	}
	if ClientEventsFromRequest("", "/v1/messages", claudeCodeBatch(), "") != nil {
		t.Fatal("only analytics paths are parsed")
	}
	if ClientEventsFromRequest("", "/api/event_logging/v2/batch", []byte("garbage"), "") != nil {
		t.Fatal("garbage must yield nil")
	}
}

func TestParseClientUserID(t *testing.T) {
	u := ParseClientUserID(`{"device_id":"8b9f","account_uuid":"e803b32a-9c22","session_id":"69ec40e4-2175"}`)
	if u.DeviceID != "8b9f" || u.AccountUUID != "e803b32a-9c22" || u.SessionID != "69ec40e4-2175" {
		t.Fatalf("json format: %+v", u)
	}
	u = ParseClientUserID("user_8b9f0a_account_e803b32a-9c22_session_69ec40e4-2175")
	if u.DeviceID != "8b9f0a" || u.AccountUUID != "e803b32a-9c22" || u.SessionID != "69ec40e4-2175" {
		t.Fatalf("legacy format: %+v", u)
	}
	u = ParseClientUserID("someone-else")
	if u.Raw != "someone-else" || u.DeviceID != "" {
		t.Fatalf("unknown format kept raw: %+v", u)
	}
}

func TestProcessor_FansOutClientEvents(t *testing.T) {
	main := &recordingSink{}
	schema := &recordingSink{}
	p := NewProcessor(main, ProcessorConfig{BufferSize: 16, SchemaSink: schema})
	e := Event{Phase: PhaseComplete, Method: "POST", Path: "/api/event_logging/v2/batch", IP: "203.0.113.86", CorrelationID: "c1"}
	e.AttachBodies(claudeCodeBatch(), []byte(`{"accepted_count":2,"rejected_count":0}`), "", nil)
	p.Offer(e)
	p.Close()
	got := main.events()
	if len(got) != 3 || got[0].Phase != PhaseComplete || got[1].Phase != PhaseClientEvent || got[2].Phase != PhaseClientEvent {
		t.Fatalf("want complete + 2 client_event rows, got %d", len(got))
	}
	if got[1].IP != "203.0.113.86" || got[1].CorrelationID != "c1" || got[1].ClientEvent.Name != "tengu_api_success" {
		t.Fatalf("client_event row missing transaction identity: %+v", got[1])
	}
	// The batch itself is a recognized request shape (client-event registry
	// samples, reason new_client_event*, are a separate mechanism).
	for _, smp := range schema.events() {
		if r := smp.SchemaFingerprint.Reason; !strings.HasPrefix(r, "new_client_event") {
			t.Fatalf("a parsed analytics batch must not be sampled as an unrecognized request; got reason %q", r)
		}
	}
}

func TestExtractDeferred_UserID(t *testing.T) {
	e := Event{Phase: PhaseComplete, Path: "/v1/messages"}
	e.AttachBodies([]byte(`{"model":"m","messages":[],"tools":[{"name":"Bash"}],"metadata":{"user_id":"{\"device_id\":\"d\",\"account_uuid\":\"a\",\"session_id\":\"s\"}"}}`), nil, "", nil)
	e.ExtractDeferred()
	if e.DeviceID != "d" || e.AccountUUID != "a" || e.SessionID != "s" || e.UserID == "" {
		t.Fatalf("got %+v", e)
	}
}
