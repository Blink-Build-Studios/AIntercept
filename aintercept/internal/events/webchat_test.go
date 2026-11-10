package events

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestAuthIdentityFromHeaders(t *testing.T) {
	claims := `{"https://api.openai.com/profile":{"email":"a@b.co","name":"A B"},` +
		`"https://api.openai.com/auth":{"chatgpt_user_id":"user-1","chatgpt_account_id":"acct-1","chatgpt_plan_type":"free"},` +
		`"iss":"https://auth.openai.com","sub":"google|1","client_id":"app_x","exp":1793700000}`
	tok := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	id := AuthIdentityFromHeaders(map[string]string{"Authorization": "Bearer " + tok})
	if id == nil || id.Email != "a@b.co" || id.UserID != "user-1" || id.AccountID != "acct-1" ||
		id.Plan != "free" || id.Name != "A B" || id.ClientID != "app_x" || id.ExpiresAt != 1793700000 {
		t.Fatalf("got %+v", id)
	}
	for _, h := range []map[string]string{
		{"authorization": "Bearer sk-ant-api03-xyz"}, // API key, not a JWT
		{"x-api-key": "abc"},
		{},
	} {
		if AuthIdentityFromHeaders(h) != nil {
			t.Fatalf("%v: want nil", h)
		}
	}
}

const chatGPTTurnSSE = `event: delta_encoding
data: "v1"

data: {"type":"resume_conversation_token","conversation_id":"conv-1"}

event: delta
data: {"p":"","o":"add","v":{"message":{"id":"m-call","author":{"role":"assistant"},"recipient":"web.run","content":{"content_type":"code"},"metadata":{}},"conversation_id":"conv-1"},"c":0}

event: delta
data: {"v":{"message":{"id":"m-tool","author":{"role":"tool","name":"web.run"},"content":{"content_type":"text"},"metadata":{}},"conversation_id":"conv-1"},"c":1}

event: delta
data: {"v":{"message":{"id":"m-tool","author":{"role":"tool","name":"web.run"},"content":{"content_type":"text"},"metadata":{}},"conversation_id":"conv-1"},"c":1}

event: delta
data: {"v":{"message":{"id":"m-ans","author":{"role":"assistant"},"content":{"content_type":"text"},"metadata":{"finish_details":{"type":"stop"}}},"conversation_id":"conv-1"},"c":2}

data: {"type":"title_generation","title":"Envoy Release Changes","conversation_id":"conv-1"}

data: {"type":"server_ste_metadata","metadata":{"model_slug":"gpt-6","plan_type":"free","search_tool_call_count":1,"tool_name":"SonicBrowserTool"},"conversation_id":"conv-1"}

data: {"type":"conversation_detail_metadata","limits_progress":[{"feature_name":"reason","remaining":5}],"conversation_id":"conv-1"}

data: [DONE]
`

func TestChatGPTWebTurn(t *testing.T) {
	req := `{"action":"next","model":"auto","parent_message_id":"m-prev","turn_attribution":{"turn_trigger":"composer"},` +
		`"client_contextual_info":{"app_surface":"codex_browser"},"messages":[{"author":{"role":"user"},` +
		`"content":{"content_type":"text","parts":["hi"]},"metadata":{"attachments":[` +
		`{"id":"file_1","mime_type":"image/png","size":10},{"id":"file_2","mime_type":"text/csv","size":44}]}}]}`
	e := Event{Phase: PhaseComplete, Host: "chatgpt.com", Path: "/backend-api/f/conversation"}
	e.AttachBodies([]byte(req), gz(t, chatGPTTurnSSE), "gzip", nil)
	e.ExtractDeferred()
	wc := e.WebChat
	if wc == nil || wc.App != "chatgpt" || wc.ConversationID != "conv-1" || wc.RequestedModel != "auto" ||
		wc.PlanType != "free" || wc.SearchCalls != 1 || wc.Surface != "codex_browser" || wc.Trigger != "composer" ||
		wc.Title != "Envoy Release Changes" || wc.Limits == nil || wc.ServerMetadata["tool_name"] != "SonicBrowserTool" {
		t.Fatalf("web chat: %+v", wc)
	}
	if e.Model != "gpt-6" || e.MessageID != "m-ans" || e.PreviousMessageID != "m-prev" || e.StopReason != "stop" {
		t.Fatalf("model %q msg %q prev %q stop %q", e.Model, e.MessageID, e.PreviousMessageID, e.StopReason)
	}
	if len(e.ToolUses) != 1 || e.ToolUses[0].Name != "web.run" || e.ToolUses[0].Count != 1 {
		t.Fatalf("tool uses (each tool message once): %+v", e.ToolUses)
	}
	if a := e.Attachments; a == nil || a.Images != 1 || a.Files != 1 || a.ByReference != 2 {
		t.Fatalf("attachments: %+v", a)
	}
	if e.recognitionGap() != "" {
		t.Fatalf("a parsed web chat turn is recognized, got %q", e.recognitionGap())
	}
}

func TestChatGPTWebUpload(t *testing.T) {
	e := Event{Phase: PhaseComplete, Host: "chatgpt.com", Path: "/backend-api/files"}
	e.AttachBodies([]byte(`{"file_name":"revenue.csv","file_size":44,"mime_type":"text/csv","use_case":"ace_upload"}`),
		[]byte(`{"status":"success","upload_url":"https://sdmntprcentralus.oaiusercontent.com/files/x/raw?sig=s","file_id":"file_1"}`), "", nil)
	e.ExtractDeferred()
	u := e.WebChat.Upload
	if u == nil || *u != (WebChatUpload{FileID: "file_1", Name: "revenue.csv", MimeType: "text/csv", Size: 44, UseCase: "ace_upload"}) {
		t.Fatalf("upload: %+v", u)
	}
}

func TestClaudeWebTurnAndUpload(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"chatcompl_1\",\"model\":\"claude-opus-5-5\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"view\",\"input\":{}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"event: message_limit\ndata: {\"type\":\"message_limit\",\"message_limit\":{\"type\":\"within_limit\",\"representativeClaim\":\"five_hour\"}}\n\n"
	req := `{"model":"claude-opus-5-5","effort":"medium","thinking_mode":"auto","parent_message_uuid":"m-prev",` +
		`"files":["f-1","f-2"],"attachments":[{"file_name":"a.txt","file_size":120,"extracted_content":"…"}],"prompt":"hi"}`
	e := Event{Phase: PhaseComplete, Host: "claude.ai", Path: "/api/organizations/org-1/chat_conversations/conv-1/completion"}
	e.AttachBodies([]byte(req), gz(t, sse), "gzip", nil)
	e.ExtractDeferred()
	wc := e.WebChat
	if wc == nil || wc.App != "claude" || wc.OrgID != "org-1" || wc.ConversationID != "conv-1" ||
		wc.ThinkingMode != "auto" || wc.RequestedModel != "claude-opus-5-5" || wc.Limits == nil {
		t.Fatalf("web chat: %+v", wc)
	}
	if e.Model != "claude-opus-5-5" || e.Effort != "medium" || e.StopReason != "end_turn" || e.PreviousMessageID != "m-prev" {
		t.Fatalf("model %q effort %q stop %q prev %q", e.Model, e.Effort, e.StopReason, e.PreviousMessageID)
	}
	if a := e.Attachments; a == nil || a.Files != 3 || a.ByReference != 2 || a.InlineBytes != 120 {
		t.Fatalf("attachments: %+v", a)
	}

	up := Event{Phase: PhaseComplete, Host: "claude.ai", Path: "/api/organizations/org-1/conversations/conv-1/wiggle/upload-file"}
	up.AttachBodies([]byte("--b\r\n…"), []byte(`{"success":true,"file_kind":"image","file_uuid":"f-1","file_name":"g.png","size_bytes":3858,"media_type":"image/png"}`), "", nil)
	up.ExtractDeferred()
	if u := up.WebChat.Upload; u == nil || *u != (WebChatUpload{FileID: "f-1", Name: "g.png", MimeType: "image/png", Size: 3858, Kind: "image"}) {
		t.Fatalf("upload: %+v", u)
	}
}

func TestChatGPTWebAnalytics(t *testing.T) {
	user := `"user":{"userID":"user-1","email":"a@b.co","customIDs":{"account_id":"acct-1","stableID":"dev-1"}}`
	rgstr := `{"events":[` +
		`{"eventName":"statsig::gate_exposure","metadata":{"gate":"g"},` + user + `},` +
		`{"eventName":"__protobuf_structured_event__","metadata":{"eventId":"ev-1","eventCreatedAt":"2026-11-02T22:13:51Z",` +
		`"eventParams":{"@type":"openai.buf.dev/x.v1.ChatgptFileUploadInitiated","fileCount":2}},` + user + `}]}`
	evs := ClientEventsFromRequest("chatgpt.com", "/ces/v1/rgstr", []byte(rgstr), "")
	if len(evs) != 2 || evs[0].Name != "statsig::gate_exposure" || evs[1].Name != "ChatgptFileUploadInitiated" ||
		evs[1].Type != "structured" || evs[1].ID != "ev-1" || evs[1].Source != "chatgpt_web" {
		t.Fatalf("rgstr: %+v", evs)
	}
	if e := evs[0]; e.Email != "a@b.co" || e.UserID != "user-1" || e.AccountUUID != "acct-1" || e.DeviceID != "dev-1" {
		t.Fatalf("identity: %+v", e)
	}
	intake := `{"kind":"latency","logger":{"name":"turn-analytics"},"usr":{"user_id":"user-1"}}` + "\n" +
		`{"message":"Turn exchange complete","logger":{"name":"turn-analytics"}}`
	evs = ClientEventsFromRequest("chatgpt.com", "/ces/v1/telemetry/intake", []byte(intake), "")
	if len(evs) != 2 || evs[0].Name != "turn-analytics:latency" || evs[0].UserID != "user-1" ||
		evs[1].Name != "turn-analytics:Turn exchange complete" {
		t.Fatalf("intake: %+v", evs)
	}
	flush := `{"counters":[{"namespace":"voice","metric":"a.result","value":1}],"histograms":[{"namespace":"voice","metric":"a.ms","values":[10]}],"client_type":"web"}`
	evs = ClientEventsFromRequest("chatgpt.com", "/ces/statsc/flush", []byte(flush), "")
	if len(evs) != 2 || evs[0].Name != "voice.a.result" || evs[0].Type != "counter" || evs[1].Type != "histogram" {
		t.Fatalf("statsc: %+v", evs)
	}
	// Same path on another host isn't ChatGPT web's.
	if ClientEventsFromRequest("example.com", "/ces/v1/rgstr", []byte(rgstr), "") != nil {
		t.Fatal("host must match")
	}
}

func TestClaudeWebEventsAreTheirOwnSource(t *testing.T) {
	batch := claudeCodeBatch()
	web := ClientEventsFromRequest("claude.ai", "/api/event_logging/v2/batch", batch, "")
	cli := ClientEventsFromRequest("api.anthropic.com", "/api/event_logging/v2/batch", batch, "")
	if len(web) == 0 || web[0].Source != "claude_web" || len(cli) == 0 || cli[0].Source != "claude_code" {
		t.Fatalf("sources: web %v, cli %v", web, cli)
	}
}

func TestSessionCookieKeyHash(t *testing.T) {
	a := map[string]string{"cookie": "anthropic-device-id=d1; sessionKey=sk-ant-sid01-abc; lastActiveOrg=o1"}
	b := map[string]string{"Cookie": "sessionKey=sk-ant-sid01-abc"}
	if h := HashAPIKey(a, "salt"); h == "" || h != HashAPIKey(b, "salt") {
		t.Fatalf("same session must hash the same: %q vs %q", h, HashAPIKey(b, "salt"))
	}
	if HashAPIKey(map[string]string{"cookie": "other=1"}, "salt") != "" {
		t.Fatal("no session cookie, no key hash")
	}
	// A bearer token still wins over a cookie.
	c := map[string]string{"authorization": "Bearer tok", "cookie": "sessionKey=sk-ant-sid01-abc"}
	if HashAPIKey(c, "salt") == HashAPIKey(a, "salt") {
		t.Fatal("authorization must take precedence")
	}
}

func TestClaudeAccountIdentity(t *testing.T) {
	acct := `"account":{"tagged_id":"user_01X","uuid":"acct-1","email_address":"a@b.co","full_name":"A B"}`
	// Bootstrap: the account leads a large body that the capture cap may cut.
	boot := `{` + acct + `,"statsig":{"values":{"huge":"` + strings.Repeat("x", 1000)
	e := Event{Phase: PhaseComplete, Host: "claude.ai", Path: "/edge-api/bootstrap/org-1/app_start?x=1"}
	e.AttachBodies(nil, []byte(boot), "", nil)
	e.ExtractDeferred()
	if a := e.Auth; a == nil || *a != (AuthIdentity{Email: "a@b.co", Name: "A B", UserID: "user_01X", AccountID: "acct-1"}) {
		t.Fatalf("bootstrap (truncated): %+v", e.Auth)
	}
	// /api/account: the account is the object itself.
	e = Event{Phase: PhaseComplete, Host: "claude.ai", Path: "/api/account"}
	e.AttachBodies(nil, []byte(`{"tagged_id":"user_01X","uuid":"acct-1","settings":{"a":1},"email_address":"a@b.co","full_name":"A B"}`), "", nil)
	e.ExtractDeferred()
	if e.Auth == nil || e.Auth.Email != "a@b.co" || e.Auth.AccountID != "acct-1" {
		t.Fatalf("/api/account: %+v", e.Auth)
	}
}

func TestReviewEdgeCases(t *testing.T) {
	// A null element in a statsc flush must not panic.
	if evs := ClientEventsFromRequest("chatgpt.com", "/ces/statsc/flush", []byte(`{"counters":[null,{"namespace":"a","metric":"b"}]}`), ""); len(evs) != 1 {
		t.Fatalf("statsc with null: %+v", evs)
	}
	// A chunked session cookie hashes like the joined value.
	whole := HashAPIKey(map[string]string{"cookie": "__Secure-next-auth.session-token=AAABBB"}, "s")
	chunked := HashAPIKey(map[string]string{"cookie": "__Secure-next-auth.session-token.0=AAA; x=1; __Secure-next-auth.session-token.1=BBB"}, "s")
	if whole == "" || whole != chunked {
		t.Fatalf("chunked cookie: %q vs %q", chunked, whole)
	}
	// A non-string client_metadata value doesn't drop the turn metadata.
	md := codexTurnMetadata("", []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"s1\"}","x-codex-installation-id":"i1","n":1}}`))
	if md["session_id"] != "s1" || md["installation_id"] != "i1" {
		t.Fatalf("client_metadata with a number: %v", md)
	}
}
