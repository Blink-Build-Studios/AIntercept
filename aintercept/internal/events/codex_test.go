package events

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Fixtures mirror Codex 0.164.0 traffic shapes (chatgpt.com/backend-api/codex/*),
// with synthetic values.

const codexTurnMetadataFixture = `{"installation_id":"inst-1","session_id":"sess-1","thread_id":"sess-1","turn_id":"turn-1","request_kind":"turn","turn_trigger":"exec","sandbox_mode":"danger-full-access","workspaces":{"/work":{"has_changes":true}},"model":"gpt-6.1-sol","reasoning_effort":"low"}`

func codexRequest() []byte {
	return []byte(`{"model":"gpt-6.1-sol","stream":true,"store":false,
	 "reasoning":{"effort":"low","context":"all_turns"},
	 "client_metadata":{"x-codex-turn-metadata":` + jsonString(codexTurnMetadataFixture) + `,"x-codex-installation-id":"inst-1"},
	 "input":[
	  {"type":"additional_tools","role":"developer","tools":[
	    {"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"},{"type":"function","name":"wait"}]},
	    {"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"secret prompt"}]},
	  {"type":"custom_tool_call","name":"exec","call_id":"c1","input":"const r = await tools.mcp__fs__list_directory({path:\"/work\"}); text(await tools.exec_command({cmd:\"ls\"}));"},
	  {"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"secret output"}]}]}`)
}

func jsonString(s string) string {
	var b bytes.Buffer
	b.WriteByte('"')
	b.WriteString(strings.ReplaceAll(s, `"`, `\"`))
	b.WriteByte('"')
	return b.String()
}

func codexSSE() []byte {
	return []byte(strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"in_progress","service_tier":"default","reasoning":{"effort":"low"}}}`,
		`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","name":"exec","call_id":"c2"}}`,
		`data: {"type":"response.custom_tool_call_input.delta","delta":"text(await tools.mcp__fs__"}`,
		`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"exec","call_id":"c2","input":"text(await tools.mcp__fs__read_text_file({path:\"/work/notes.txt\"}));"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","service_tier":"default","reasoning":{"effort":"low"},"output":[{"type":"custom_tool_call","name":"exec","input":"text(await tools.mcp__fs__read_text_file({path:\"/work/notes.txt\"}));"}],"usage":{"input_tokens":23235,"input_tokens_details":{"cached_tokens":14080},"output_tokens":58}}}`,
	}, "\n"))
}

func zstdBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func uses(u []ToolCallCount) []string {
	var out []string
	for _, x := range u {
		out = append(out, x.Kind+":"+x.Server+":"+x.Tool)
	}
	sort.Strings(out)
	return out
}

func TestCodex_ExtractDeferredFromZstdRequestAndSSE(t *testing.T) {
	e := Event{Phase: PhaseComplete, Method: "POST", Host: "chatgpt.com", Path: "/backend-api/codex/responses"}
	e.AttachBodies(zstdBytes(t, codexRequest()), codexSSE(), "", nil)
	e.AttachRequestHeaders(map[string]string{
		"content-encoding":      "zstd",
		"chatgpt-account-id":    "acct-1",
		"x-codex-turn-metadata": codexTurnMetadataFixture,
		"originator":            "codex_exec",
	})
	e.ExtractDeferred()

	if e.Model != "gpt-6.1-sol" || e.Effort != "low" || !e.Stream || e.ServiceTier != "default" || e.MessageID != "resp_1" {
		t.Fatalf("request/response meta: model=%q effort=%q stream=%v tier=%q msg=%q", e.Model, e.Effort, e.Stream, e.ServiceTier, e.MessageID)
	}
	if e.InputTokens != 23235 || e.OutputTokens != 58 || e.CacheReadTokens != 14080 {
		t.Fatalf("usage: %+v", e)
	}
	if e.SessionID != "sess-1" || e.DeviceID != "inst-1" || e.AccountUUID != "acct-1" ||
		e.ClientMetadata["turn_id"] != "turn-1" || e.ClientMetadata["sandbox_mode"] != "danger-full-access" {
		t.Fatalf("identity: session=%q device=%q account=%q md=%v", e.SessionID, e.DeviceID, e.AccountUUID, e.ClientMetadata)
	}
	// The response's exec call plus the MCP tool its script calls.
	if got, want := uses(e.ToolUses), []string{"builtin::exec", "mcp:fs:read_text_file"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool_uses: got %v want %v", got, want)
	}
	// Inline catalog from additional_tools.
	var avail []string
	for _, a := range e.ToolsAvailable {
		avail = append(avail, a.Name)
	}
	sort.Strings(avail)
	if want := []string{"collaboration.spawn_agent", "exec", "wait"}; !reflect.DeepEqual(avail, want) {
		t.Fatalf("tools_available: got %v want %v", avail, want)
	}
	// History: exec + the tools its script called.
	if got, want := uses(e.ToolCalls), []string{"builtin::exec", "builtin::exec_command", "mcp:fs:list_directory"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool_calls: got %v want %v", got, want)
	}
	if e.Client["chatgpt-account-id"] != "acct-1" || e.Client["originator"] != "codex_exec" {
		t.Fatalf("client headers: %v", e.Client)
	}
}

func TestCodex_TurnMetadataFromBodyWhenNoHeader(t *testing.T) {
	md := codexTurnMetadata("", codexRequest())
	if md["session_id"] != "sess-1" || md["installation_id"] != "inst-1" {
		t.Fatalf("got %v", md)
	}
	if codexTurnMetadata("", []byte(`{"model":"m"}`)) != nil {
		t.Fatal("no metadata must yield nil")
	}
}

func TestCodex_AnalyticsEvents(t *testing.T) {
	batch := []byte(`{"events":[
	 {"event_type":"codex_mcp_tool_call_event","event_params":{"thread_id":"sess-1","session_id":"sess-1","mcp_server_name":"fs","mcp_tool_name":"read_text_file","duration_ms":12,"terminal_status":"completed"}},
	 {"event_type":"codex_turn_event","event_params":{"session_id":"sess-1","model":"gpt-6.1-sol","input_tokens":23235,"mcp_tool_call_count":3,"sandbox_policy":"danger-full-access"}}]}`)
	evs := ClientEventsFromRequest("", "/backend-api/codex/analytics-events/events", batch, "")
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	m := evs[0]
	if m.Source != "codex" || m.Name != "codex_mcp_tool_call_event" || m.SessionID != "sess-1" || m.Data["mcp_server_name"] != "fs" {
		t.Fatalf("mcp event: %+v", m)
	}
	if evs[1].Model != "gpt-6.1-sol" || evs[1].Data["mcp_tool_call_count"] != float64(3) {
		t.Fatalf("turn event: %+v", evs[1])
	}
}

func TestMCPFromBodies(t *testing.T) {
	list := MCPFromBodies([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`),
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"sites.add_custom_domain","description":"x"},{"name":"docs.search"}]}}`), "")
	if list == nil || list.Method != "tools/list" || !reflect.DeepEqual(list.ToolsListed, []string{"sites.add_custom_domain", "docs.search"}) {
		t.Fatalf("tools/list: %+v", list)
	}
	call := MCPFromBodies([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"docs.search","arguments":{"q":"secret"}}}`),
		[]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"error\":{\"code\":-32602,\"message\":\"bad\"}}\n"), "")
	if call == nil || call.Tool != "docs.search" || call.ErrorCode != -32602 {
		t.Fatalf("tools/call: %+v", call)
	}
	if MCPFromBodies([]byte(`{"model":"m"}`), nil, "") != nil {
		t.Fatal("non-JSON-RPC must yield nil")
	}
}

func TestDecodeBody_ZstdAndCap(t *testing.T) {
	big := bytes.Repeat([]byte("a"), maxDecodedBody+1024)
	out, err := decodeBody(zstdBytes(t, big), "zstd")
	if err != nil || len(out) != maxDecodedBody {
		t.Fatalf("decompressed size must be capped at %d, got %d (%v)", maxDecodedBody, len(out), err)
	}
}

// A parsed MCP call is recognized, not an unrecognized shape.
func TestSample_ParsedMCPNotSampled(t *testing.T) {
	e := Event{Phase: PhaseComplete, Method: "POST", Path: "/backend-api/ps/mcp"}
	e.AttachBodies([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`), "", nil)
	e.ExtractDeferred()
	if _, ok := e.SampleIfUnrecognized(); ok {
		t.Fatal("a parsed MCP call must not be sampled")
	}
}
