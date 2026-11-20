package capture

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Identifiers planted in every kind of field a capture carries. None may
// survive the anonymizer.
var planted = map[string]string{
	"email":        "jane.doe@acmecorp.com",
	"name":         "Janet Plantagenet",
	"org":          "8c1a7e52-3f0b-4d2e-9a61-5b7c2d9e4f10",
	"account":      "2f6d9b14-7a3c-4e85-b0d2-91c4e6a8f357",
	"device":       "a3f19c0e5b7d2468a3f19c0e5b7d2468a3f19c0e5b7d2468a3f19c0e5b7d2468",
	"session":      "5e2b8c71-9d4a-4f36-8b1e-c07a2d5f9e83",
	"prompt":       "7b9e1d3c-2a8f-4c65-91d7-e4b0a6c3f528",
	"request":      "req_011CVq7RkPlantedRequest99",
	"message":      "msg_01PlantedMessageIdXyz123",
	"tool_call":    "toolu_01PlantedToolCallAbc456",
	"installation": "c4d8e2f1-6a3b-4e79-a5c0-1b2d3e4f5a6b",
	"path":         "/Users/janedoe/src/acme-secret-repo/payroll.go",
	"repo":         "acme-secret-repo",
	"host":         "build07.corp.acmecorp.com",
	"ip":           "203.0.113.77",
	"ipv6":         "2001:db8:85a3:0:0:8a2e:370:7334",
	"branch":       "jdoe/fix-payroll-export",
	"cwd":          "/home/jdoe/work/payroll",
	"user":         "janedoe",
	"company":      "acmecorp",
	"mcp_server":   "acme_internal",
	"mcp_tool":     "deploy_payroll",
	"secret_word":  "Plantagenet",
	"workspace":    "/Users/janedoe/git/payroll-service",
	"commit":       "9fceb02d0ae598e95dc970b74767f19372d61af8",
	"chatgpt_user": "user-PlantedChatgptUser42",
	"cookie":       "PlantedCookieValue7f3a9c",
}

func p(k string) string { return planted[k] }

var (
	image         = []byte("\x89PNG planted image bytes " + planted["email"])
	reasoning     = []byte("encrypted reasoning about " + planted["path"])
	plantedClaims = `{"https://api.openai.com/profile":{"email":"` + planted["email"] + `"},"https://api.openai.com/auth":{"chatgpt_account_id":"` +
		planted["account"] + `","chatgpt_user_id":"` + planted["chatgpt_user"] + `","chatgpt_plan_type":"plantedplan"},"name":"` + planted["name"] + `","exp":1795000000}`
	jwt = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(plantedClaims)) + ".c2lnbmF0dXJl"
)

func anthropicRequest() string {
	userID, _ := json.Marshal(`{"device_id":"` + p("device") + `","account_uuid":"` + p("account") + `","session_id":"` + p("session") + `"}`)
	input, _ := json.Marshal(map[string]any{"command": "cd " + p("cwd") + " && git checkout " + p("branch") + " && npm test", "file_path": p("path")})
	return `{"model":"claude-opus-4-1","max_tokens":32000,"stream":true,` +
		`"system":[{"type":"text","text":"You are Claude Code. cwd: ` + p("cwd") + `, repo ` + p("repo") + ` on ` + p("host") + `"}],` +
		`"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},` +
		`{"name":"mcp__` + p("mcp_server") + `__` + p("mcp_tool") + `","description":"Deploy ` + p("repo") + `","input_schema":{"type":"object"}}],` +
		`"metadata":{"user_id":` + string(userID) + `},` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>The user is ` + p("name") + ` (` + p("email") + `), branch ` + p("branch") + `</system-reminder>"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(image) + `"}},` +
		`{"type":"text","text":"fix the bug in ` + p("path") + ` for ` + p("company") + `, token ` + jwt + `"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + p("tool_call") + `","name":"Bash","input":` + string(input) + `}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + p("tool_call") + `","content":"Exit code 1\nfatal: ` + p("path") + ` at ` + p("ip") + ` (` + p("ipv6") + `) commit ` + p("commit") + `\nSet-Cookie: sessionKey=` + p("cookie") + `; Path=/"}]}]}`
}

func anthropicResponse() string {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	piece := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	args := `{"command": "ls ` + p("workspace") + `"}`
	text := "Done for " + p("email") + " on " + p("host")
	return ev("message_start", `{"type":"message_start","message":{"id":"`+p("message")+`","type":"message","role":"assistant","model":"claude-opus-4-1","content":[],"usage":{"input_tokens":1234,"cache_read_input_tokens":567,"output_tokens":1}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"`+p("name")+` wants `+p("repo")+`"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"`+base64.StdEncoding.EncodeToString(reasoning)+`"}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":`+piece(text[:14])+`}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":`+piece(text[14:])+`}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"`+p("tool_call")+`2","name":"mcp__`+p("mcp_server")+`__`+p("mcp_tool")+`","input":{}}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":`+piece(args[:20])+`}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":`+piece(args[20:])+`}}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":89}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
}

// rawFile writes one call as the observer's raw rows (gzip NDJSON).
func rawFile(t *testing.T, reqHeaders map[string]string, reqBody string, respHeaders map[string]string, respBody string, auth *events.AuthIdentity) string {
	t.Helper()
	call := "observer-x-1-abc"
	rows := []events.Event{
		{CorrelationID: call, Phase: events.PhaseRequestHeaders, TimestampNS: 1795000000000000000, Host: reqHeaders[":authority"],
			Method: "POST", Path: reqHeaders[":path"], IP: p("ip"), KeyHint: "sk-ant…9f3a", Headers: reqHeaders},
		{CorrelationID: call, Phase: events.PhaseRequestBodyChunk, Bytes: len(reqBody), BodyB64: base64.StdEncoding.EncodeToString([]byte(reqBody)), EndOfStream: true},
		{CorrelationID: call, Phase: events.PhaseResponseHeaders, Status: 200, Headers: respHeaders},
		{CorrelationID: call, Phase: events.PhaseResponseBodyChunk, Bytes: len(respBody), BodyB64: base64.StdEncoding.EncodeToString([]byte(respBody)), EndOfStream: true},
		{CorrelationID: call, Phase: events.PhaseComplete, Status: 200, Auth: auth, IP: p("ip")},
	}
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	for _, r := range rows {
		line, _ := json.Marshal(r)
		zw.Write(append(line, '\n'))
	}
	zw.Close()
	path := filepath.Join(t.TempDir(), "raw.ndjson.gz")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeCase runs a raw file through FromRaw and returns the written files.
func writeCase(t *testing.T, raw string) map[string]string {
	t.Helper()
	c, a, err := FromRaw(raw, Options{Call: "observer-x-1-abc", Shape: "test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := c.Write(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir, a); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		files[e.Name()] = string(b)
	}
	return files
}

func noneSurvive(t *testing.T, files map[string]string) {
	t.Helper()
	for name, body := range files {
		lower := strings.ToLower(body)
		for k, v := range planted {
			if strings.Contains(lower, strings.ToLower(v)) {
				t.Errorf("%s: the planted %s survived", name, k)
			}
		}
		for _, v := range []string{string(image), string(reasoning), jwt, base64.StdEncoding.EncodeToString(image)} {
			if strings.Contains(body, v) {
				t.Errorf("%s: a planted blob survived", name)
			}
		}
	}
}

func TestAnonymizerClaudeCode(t *testing.T) {
	reqHeaders := map[string]string{
		":authority": "api.anthropic.com", ":method": "POST", ":path": "/v1/messages?beta=true",
		"authorization": "<redacted>", "cookie": "<redacted>", "content-type": "application/json",
		"user-agent": "claude-cli/2.1.326 (external, cli)", "x-claude-code-session-id": p("session"),
		"x-claude-code-prompt-id": p("prompt"), "x-organization-uuid": p("org"), "x-forwarded-for": p("ip") + ", 10.0.0.1",
		"x-client-request-id": "1d2c3b4a-5f6e-4d7c-8b9a-0f1e2d3c4b5a", "x-custom-origin": "https://" + p("host") + "/" + p("repo"),
	}
	respHeaders := map[string]string{":status": "200", "content-type": "text/event-stream", "request-id": p("request"),
		"anthropic-organization-id": p("org"), "anthropic-ratelimit-unified-status": "allowed"}
	files := writeCase(t, rawFile(t, reqHeaders, anthropicRequest(), respHeaders, anthropicResponse(), nil))
	noneSurvive(t, files)

	// Structure is kept: JSON stays JSON with its keys, model and counts; the
	// harness's markers and exit codes stay; base64 stays base64 of the same length.
	req := files["request-body.json"]
	var body map[string]any
	if err := json.Unmarshal([]byte(req), &body); err != nil {
		t.Fatalf("request body isn't JSON: %v", err)
	}
	if body["model"] != "claude-opus-4-1" || body["max_tokens"] != float64(32000) {
		t.Errorf("model or max_tokens changed: %v %v", body["model"], body["max_tokens"])
	}
	for _, want := range []string{`"command":"`, `"file_path":"`, "<system-reminder>", "</system-reminder>", "Exit code 1", `"name":"Bash"`, `"mcp__`} {
		if !strings.Contains(req, want) {
			t.Errorf("request body lost %q", want)
		}
	}
	if len(req) < len(anthropicRequest())-200 || len(req) > len(anthropicRequest())+200 {
		t.Errorf("request body length %d, want about %d", len(req), len(anthropicRequest()))
	}
	img := body["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)["source"].(map[string]any)["data"].(string)
	if dec, err := base64.StdEncoding.DecodeString(img); err != nil || len(dec) != len(image) {
		t.Errorf("image: %v, %d bytes, want valid base64 of %d", err, len(dec), len(image))
	}
	var uid map[string]string
	_ = json.Unmarshal([]byte(body["metadata"].(map[string]any)["user_id"].(string)), &uid)
	if len(uid["device_id"]) != 64 || len(uid["account_uuid"]) != 36 || uid["session_id"] == "" {
		t.Errorf("metadata.user_id lost its shape: %v", uid)
	}
	var reqJSON Request
	_ = json.Unmarshal([]byte(files["request.json"]), &reqJSON)
	if reqJSON.Headers["x-claude-code-session-id"] != uid["session_id"] {
		t.Errorf("the session id isn't the same fake in the header (%s) and the body (%s)", reqJSON.Headers["x-claude-code-session-id"], uid["session_id"])
	}
	if reqJSON.Credential == nil || reqJSON.Credential.Kind != CredAnthropicOAuth || reqJSON.Headers["authorization"] != "<redacted>" {
		t.Errorf("credential: %+v", reqJSON.Credential)
	}
	resp := files["response-body.sse"]
	if !strings.Contains(resp, `"input_tokens":1234`) || !strings.Contains(resp, `"output_tokens":89`) || !strings.Contains(resp, `"stop_reason":"tool_use"`) {
		t.Errorf("usage or stop reason changed:\n%s", resp)
	}
	// The tool input's streamed pieces still make valid JSON with its keys.
	var whole strings.Builder
	for _, l := range strings.Split(resp, "\n") {
		if d, ok := strings.CutPrefix(l, "data: "); ok && strings.Contains(d, "input_json_delta") {
			var ev struct {
				Delta struct {
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			_ = json.Unmarshal([]byte(d), &ev)
			whole.WriteString(ev.Delta.PartialJSON)
		}
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(whole.String()), &in); err != nil || in["command"] == nil {
		t.Errorf("tool input pieces %q: %v", whole.String(), err)
	}
}

func TestAnonymizerCodex(t *testing.T) {
	meta, _ := json.Marshal(map[string]any{"installation_id": p("installation"), "session_id": p("session"), "turn_id": p("prompt"),
		"thread_source": "user", "workspaces": map[string]any{p("workspace"): map[string]any{"latest_git_commit_hash": p("commit"), "has_changes": true}}})
	reqHeaders := map[string]string{
		":authority": "chatgpt.com", ":method": "POST", ":path": "/backend-api/codex/responses", "authorization": "<redacted>",
		"content-type": "application/json", "chatgpt-account-id": p("account"), "originator": "codex_exec",
		"user-agent": "codex_exec/0.164.0 (Mac OS 15.5.0; arm64) " + p("host"), "x-codex-turn-metadata": string(meta), "session_id": p("session"),
	}
	args, _ := json.Marshal(`{"cmd":"git -C ` + p("workspace") + ` log","workdir":"` + p("cwd") + `"}`)
	req := `{"model":"gpt-6.1-sol","stream":true,"instructions":"You are Codex for ` + p("company") + `",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context><cwd>` + p("cwd") + `</cwd></environment_context>"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(image) + `"}]},` +
		`{"type":"reasoning","id":"rs_PlantedReasoning0001","summary":[],"encrypted_content":"` + base64.StdEncoding.EncodeToString(reasoning) + `"},` +
		`{"type":"function_call","name":"exec_command","call_id":"call_PlantedCallId0001","arguments":` + string(args) + `},` +
		`{"type":"function_call_output","call_id":"call_PlantedCallId0001","output":"{\"output\":\"` + p("path") + `\",\"metadata\":{\"exit_code\":2}}"}],` +
		`"prompt_cache_key":"` + p("session") + `","client_metadata":` + string(meta) + `,"cookies":"__Secure-next-auth.session-token=` + p("cookie") + `",` +
		`"grants":{"` + p("account") + `:deploy":true},"connectors":[{"type":"remote","name":"` + p("secret_word") + `",` +
		`"access":{"service":"` + p("host") + `","type":"sends_data_to"}}]}`
	ev := func(data string) string { return "event: x\ndata: " + data + "\n\n" }
	argPieces := []string{`{"cmd":"cat `, p("path"), `"}`}
	resp := ev(`{"type":"response.created","response":{"id":"resp_PlantedResponse0001","model":"gpt-6.1-sol","status":"in_progress"}}`)
	for _, a := range argPieces {
		b, _ := json.Marshal(a)
		resp += ev(`{"type":"response.function_call_arguments.delta","item_id":"fc_Planted0001","output_index":0,"delta":` + string(b) + `}`)
	}
	full, _ := json.Marshal(strings.Join(argPieces, ""))
	resp += ev(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_Planted0001","name":"exec_command","call_id":"call_Planted0002","arguments":` + string(full) + `}}`)
	resp += ev(`{"type":"response.completed","response":{"id":"resp_PlantedResponse0001","model":"gpt-6.1-sol","status":"completed","usage":{"input_tokens":4000,"input_tokens_details":{"cached_tokens":3000},"output_tokens":50}}}`)
	auth := &events.AuthIdentity{Email: p("email"), Name: p("name"), AccountID: p("account"), UserID: p("chatgpt_user"), Plan: "plantedplan"}
	reqH := reqHeaders
	files := writeCase(t, rawFileHint(t, reqH, req, map[string]string{":status": "200", "content-type": "text/event-stream", "x-oai-request-id": p("request")}, resp, auth, "eyJhbG…xyzw"))
	noneSurvive(t, files)

	var r Request
	_ = json.Unmarshal([]byte(files["request.json"]), &r)
	if r.Credential == nil || r.Credential.Kind != CredChatGPT {
		t.Fatalf("credential %+v", r.Credential)
	}
	// The stand-in token's claims have the claim keys the code reads, with the
	// same fakes as the rest of the case.
	id := events.AuthIdentityFromHeaders(map[string]string{"authorization": "Bearer " + JWT(r.Credential.Claims)})
	if id == nil || id.AccountID != r.Headers["chatgpt-account-id"] || !strings.HasSuffix(id.Email, "@example.com") || id.Plan == "" || id.Plan == "plantedplan" {
		t.Errorf("claims %+v (account header %s)", id, r.Headers["chatgpt-account-id"])
	}
	var md map[string]any
	if err := json.Unmarshal([]byte(r.Headers["x-codex-turn-metadata"]), &md); err != nil || md["thread_source"] != "user" {
		t.Errorf("turn metadata %v: %v", md, err)
	}
	if !strings.Contains(files["request-body.json"], `\"exit_code\":2`) || !strings.Contains(files["request-body.json"], "<environment_context>") {
		t.Errorf("request lost an exit code or a marker:\n%s", files["request-body.json"])
	}
	// The argument pieces still add up to the arguments the item repeats.
	var pieces strings.Builder
	var done string
	for _, l := range strings.Split(files["response-body.sse"], "\n") {
		d, ok := strings.CutPrefix(l, "data: ")
		if !ok {
			continue
		}
		var e struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Item  struct {
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		_ = json.Unmarshal([]byte(d), &e)
		switch e.Type {
		case "response.function_call_arguments.delta":
			pieces.WriteString(e.Delta)
		case "response.output_item.done":
			done = e.Item.Arguments
		}
	}
	if pieces.String() != done || !json.Valid([]byte(done)) {
		t.Errorf("argument pieces %q, item's %q", pieces.String(), done)
	}
}

func rawFileHint(t *testing.T, reqHeaders map[string]string, reqBody string, respHeaders map[string]string, respBody string, auth *events.AuthIdentity, hint string) string {
	t.Helper()
	path := rawFile(t, reqHeaders, reqBody, respHeaders, respBody, auth)
	b, _ := os.ReadFile(path)
	zr, _ := gzip.NewReader(bytes.NewReader(b))
	var plain bytes.Buffer
	plain.ReadFrom(zr)
	fixed := strings.Replace(plain.String(), `"key_hint":"sk-ant…9f3a"`, `"key_hint":"`+hint+`"`, 1)
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write([]byte(fixed))
	zw.Close()
	os.WriteFile(path, out.Bytes(), 0o644)
	return path
}

// A raw row that holds a credential is refused: nothing is written.
func TestAnonymizerRefusesACredential(t *testing.T) {
	h := map[string]string{":authority": "api.anthropic.com", ":method": "POST", ":path": "/v1/messages", "authorization": "Bearer sk-ant-oat01-notredacted"}
	if _, _, err := FromRaw(rawFile(t, h, `{}`, map[string]string{":status": "200"}, `{}`, nil), Options{Call: "observer-x-1-abc"}); err == nil {
		t.Fatal("a credential in a header was accepted")
	}
}

// Text keeps its length, line breaks and punctuation, and the harness's markers.
func TestAnonymizerText(t *testing.T) {
	a := NewAnonymizer()
	in := "<system-reminder>\nHello, Wörld 42! [Image #2] tools.exec_command({cmd: \"ls\"})\n</system-reminder>"
	out := a.Text(in)
	if len(out) != len(in) || strings.Count(out, "\n") != 2 {
		t.Errorf("%q: length or lines changed", out)
	}
	for _, keep := range []string{"<system-reminder>", "</system-reminder>", "[Image #", "tools.exec_command(", "cmd:", ", ", "! "} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q lost %q", out, keep)
		}
	}
	for _, gone := range []string{"Hello", "Wörld", "42", `"ls"`} {
		if strings.Contains(out, gone) {
			t.Errorf("%q kept %q", out, gone)
		}
	}
}

// Account data planted in every place a provider says something about the
// account: its plan and limits, flags, early-access programs, the models it
// offers. None may survive; the structure around it does.
var plantedAccount = map[string]string{
	"rl_reason":   "planted_disabled_reason",
	"rl_claim":    "planted_claim_window",
	"rl_grace":    "plantedgrace",
	"rl_reset":    "1794999999",
	"rl_util":     "0.4242",
	"plan":        "plantedplan",
	"credits":     "98765.4321",
	"tier":        "planted_tier_20x",
	"flag":        "tengu_planted_codename",
	"flag_key":    "planted_threshold",
	"client_data": "planted_switch",
	"usage_key":   "planted_bucket",
	"program":     "planted_program",
	"model":       "claude-planted-9-9",
	"alias":       "claude-plantedalias",
	"codex_model": "gpt-planted-7",
	"number":      "4242",
	"category":    "PLANTED-CAT9",
	"holdback":    "planted_holdback",
	"sso":         "planted-sso-value",
	"param":       "planted_param",
}

func pa(k string) string { return plantedAccount[k] }

func noAccountSurvives(t *testing.T, files map[string]string) {
	t.Helper()
	for name, body := range files {
		for k, v := range plantedAccount {
			if strings.Contains(body, v) {
				t.Errorf("%s: the planted %s survived", name, k)
			}
		}
	}
}

func accountCase(t *testing.T, host, path, reqBody string, respHeaders map[string]string, respBody string, models ...string) map[string]string {
	t.Helper()
	reqHeaders := map[string]string{":authority": host, ":method": "POST", ":path": path, "authorization": "<redacted>",
		"content-type": "application/json", "user-agent": "claude-cli/2.1.326 (external, cli)", "anthropic-usage-limit": pa("rl_reason")}
	o := Options{Call: "observer-x-1-abc", Shape: "test", Models: map[string]bool{}}
	for _, m := range models {
		o.Models[m] = true
	}
	c, a, err := FromRaw(rawFile(t, reqHeaders, reqBody, respHeaders, respBody, nil), o)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := c.Write(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir, a); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		files[e.Name()] = string(b)
	}
	noneSurvive(t, files)
	noAccountSurvives(t, files)
	return files
}

func TestAnonymizerAccountData(t *testing.T) {
	t.Run("rate-limit headers", func(t *testing.T) {
		resp := map[string]string{":status": "200", "content-type": "application/json", "request-id": p("request"),
			"anthropic-ratelimit-unified-status": pa("plan"), "anthropic-ratelimit-unified-overage-disabled-reason": pa("rl_reason"),
			"anthropic-ratelimit-unified-grace-status": pa("rl_grace"), "anthropic-ratelimit-unified-representative-claim": pa("rl_claim"),
			"anthropic-ratelimit-unified-reset": pa("rl_reset"), "anthropic-ratelimit-unified-7d-utilization": pa("rl_util"),
			"x-codex-plan-type": pa("plan"), "x-codex-credits-balance": pa("credits"), "x-codex-credits-unlimited": "True",
			"x-codex-safety-buffering-faster-model": pa("codex_model"), "server-timing": "x-planted;dur=" + pa("number")}
		files := accountCase(t, "api.anthropic.com", "/v1/messages", `{"model":"claude-opus-4-1","messages":[]}`, resp, `{"type":"message","model":"claude-opus-4-1"}`)
		var r Response
		if err := json.Unmarshal([]byte(files["response.json"]), &r); err != nil {
			t.Fatal(err)
		}
		for k := range resp {
			if _, ok := r.Headers[k]; !ok {
				t.Errorf("header %s lost its name", k)
			}
		}
		if r.Headers["content-type"] != "application/json" || !strings.HasPrefix(r.Headers["request-id"], "req_") {
			t.Errorf("the exchange's own headers changed: %v", r.Headers)
		}
		if r.Headers["anthropic-ratelimit-unified-7d-utilization"] != "0" || r.Headers["x-codex-credits-unlimited"] != "False" {
			t.Errorf("a number or boolean isn't a neutral one of its type: %v", r.Headers)
		}
		// The same value is the same fake wherever it is.
		if r.Headers["x-codex-plan-type"] != r.Headers["anthropic-ratelimit-unified-status"] {
			t.Errorf("one value, two fakes: %v", r.Headers)
		}
	})

	t.Run("flags", func(t *testing.T) {
		body := `{"features":{"` + pa("flag") + `":{"defaultValue":true,"value":{"` + pa("flag_key") + `":0.37,"` + pa("model") + `":"` + pa("alias") + `"},` +
			`"rules":[{"id":"` + pa("usage_key") + `","force":` + pa("number") + `}]},"tengu_other":{"defaultValue":"` + pa("tier") + `"}}}`
		files := accountCase(t, "api.anthropic.com", "/api/eval/sdk-PlantedSdkKey01", `{"attributes":{"subscriptionType":"`+pa("plan")+`","rateLimitTier":"`+pa("tier")+`"}}`,
			map[string]string{":status": "200", "content-type": "application/json"}, body)
		var v struct {
			Features map[string]struct {
				DefaultValue any            `json:"defaultValue"`
				Value        map[string]any `json:"value"`
			} `json:"features"`
		}
		if err := json.Unmarshal([]byte(files["response-body.json"]), &v); err != nil {
			t.Fatal(err)
		}
		f1, ok := v.Features["flag_1"]
		if !ok || len(v.Features) != 2 {
			t.Fatalf("flag names aren't flag_1, flag_2: %s", files["response-body.json"])
		}
		if f1.DefaultValue != false || len(f1.Value) != 2 {
			t.Errorf("a flag's value lost its type or shape: %+v", f1)
		}
		if _, ok := v.Features["flag_2"].DefaultValue.(string); !ok {
			t.Errorf("a string flag isn't a string: %+v", v.Features["flag_2"])
		}
	})

	t.Run("bootstrap", func(t *testing.T) {
		body := `{"client_data":{"` + pa("client_data") + `":{"enabled":true}},"additional_model_options":[{"model":"` + pa("alias") + `","name":"` + pa("model") + `"}],` +
			`"oauth_account":{"account_uuid":"` + p("account") + `","account_email":"` + p("email") + `","organization_name":"` + p("company") + `",` +
			`"organization_uuid":"` + p("org") + `","organization_rate_limit_tier":"` + pa("tier") + `","organization_type":"` + pa("plan") + `"}}`
		files := accountCase(t, "api.anthropic.com", "/api/claude_cli/bootstrap", "", map[string]string{":status": "200", "content-type": "application/json"}, body)
		// What the account extractor reads is still there, as fakes.
		id := events.AccountFromResponse("/api/claude_cli/bootstrap", []byte(files["response-body.json"]), "")
		if id == nil || !strings.HasSuffix(id.Email, "@example.com") || !strings.HasPrefix(id.OrgID, "00000000-") {
			t.Errorf("the account it names is gone: %+v\n%s", id, files["response-body.json"])
		}
		if !strings.Contains(files["response-body.json"], `"client_data":{"flag_1":{"enabled":false}}`) {
			t.Errorf("client_data's switches aren't fakes: %s", files["response-body.json"])
		}
	})

	t.Run("usage", func(t *testing.T) {
		body := `{"plan_type":"` + pa("plan") + `","` + pa("usage_key") + `":{"eligible":true,"limit_dollars":` + pa("number") + `,"tier":"` + pa("tier") + `"},` +
			`"model_usage":{"` + pa("codex_model") + `":{"available":true}},"rate_limit":{"used_percent":` + pa("number") + `}}`
		files := accountCase(t, "chatgpt.com", "/backend-api/wham/usage", "", map[string]string{":status": "200", "content-type": "application/json"}, body)
		var v map[string]any
		if err := json.Unmarshal([]byte(files["response-body.json"]), &v); err != nil {
			t.Fatal(err)
		}
		if len(v) != 4 {
			t.Errorf("the body lost fields: %s", files["response-body.json"])
		}
	})

	t.Run("model catalog", func(t *testing.T) {
		body := `{"models":[{"slug":"gpt-6.1-sol","display_name":"gpt-6.1-sol"},{"slug":"` + pa("codex_model") + `","display_name":"` + pa("codex_model") + `"}]}`
		files := accountCase(t, "chatgpt.com", "/backend-api/codex/models", "", map[string]string{":status": "200", "content-type": "application/json"}, body, "gpt-6.1-sol")
		got := files["response-body.json"]
		if !strings.Contains(got, `"slug":"gpt-6.1-sol"`) || !strings.Contains(got, `"slug":"gpt-model-`) {
			t.Errorf("the corpus's own model isn't kept, or another isn't a fake: %s", got)
		}
	})

	t.Run("model call", func(t *testing.T) {
		ev := func(data string) string { return "event: x\ndata: " + data + "\n\n" }
		resp := ev(`{"type":"codex.rate_limits","plan_type":"`+pa("plan")+`","rate_limits":{"allowed":true,"primary":{"used_percent":`+pa("number")+`,"reset_at":`+pa("rl_reset")+`}},"credits":{"balance":"`+pa("credits")+`"}}`) +
			ev(`{"type":"codex.response.metadata","headers":{"x-codex-plan-type":"`+pa("plan")+`","x-oai-request-id":"`+p("session")+`"}}`) +
			ev(`{"type":"response.created","response":{"id":"resp_PlantedResponse0001","model":"gpt-6.1-sol","access_programs":{"`+pa("program")+`":"`+pa("tier")+`"},"status":"in_progress"}}`)
		files := accountCase(t, "chatgpt.com", "/backend-api/codex/responses", `{"model":"gpt-6.1-sol","input":[],"client_metadata":{"x":"`+pa("alias")+`"}}`,
			map[string]string{":status": "200", "content-type": "text/event-stream"}, resp)
		got := files["response-body.sse"]
		for _, want := range []string{`"type":"codex.rate_limits"`, `"allowed":false`, `"used_percent":0`, `"access_programs":{"program_1":`, `"model":"gpt-6.1-sol"`} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %s: %s", want, got)
			}
		}
	})

	t.Run("moderation and classifier context", func(t *testing.T) {
		resp := "event: x\ndata: " + `{"type":"response.completed","response":{"id":"resp_PlantedResponse0001","model":"gpt-6.1-sol","status":"completed",` +
			`"metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{"planted_classifier":{"planted_flags":{"` + pa("category") + `":true}}}}}}}` + "\n\n"
		req := `{"model":"gpt-6.1-sol","input":[],"safeguards":[{"type":"dangerous_tool_use","classifier_context":{"` + pa("holdback") + `":true,"mode":"` + pa("tier") + `"}}]}`
		files := accountCase(t, "chatgpt.com", "/backend-api/codex/responses", req, map[string]string{":status": "200", "content-type": "text/event-stream"}, resp)
		if !strings.Contains(files["response-body.sse"], `"openai_chatgpt_moderation_metadata":{"key_`) ||
			!strings.Contains(files["request-body.json"], `"classifier_context":{"key_`) {
			t.Errorf("a verdict or a classifier's context isn't its structure only:\n%s\n%s", files["request-body.json"], files["response-body.sse"])
		}
		if strings.Contains(files["response-body.sse"], ":true") {
			t.Errorf("a verdict kept its value: %s", files["response-body.sse"])
		}
	})

	t.Run("accounts check", func(t *testing.T) {
		body := `{"accounts":[{"id":"` + p("account") + `","account_id":"` + p("account") + `","name":"` + p("company") + `","is_deactivated":false,` +
			`"plan_type":"` + pa("plan") + `","planted_sso_field":"` + pa("sso") + `","planted_entitlement":{"plan_eligible":true}}]}`
		files := accountCase(t, "chatgpt.com", "/backend-api/wham/accounts/check?"+pa("param")+"=1", "", map[string]string{":status": "200", "content-type": "application/json"}, body)
		var v struct {
			Accounts []map[string]any `json:"accounts"`
		}
		if err := json.Unmarshal([]byte(files["response-body.json"]), &v); err != nil || len(v.Accounts) != 1 {
			t.Fatalf("%v: %s", err, files["response-body.json"])
		}
		a := v.Accounts[0]
		if a["id"] == "" || a["account_id"] != a["id"] || a["is_deactivated"] != false || len(a) != 7 {
			t.Errorf("what the identity lookup reads changed: %v", a)
		}
		if _, ok := a["planted_entitlement"]; ok {
			t.Errorf("an entitlement's name was kept: %v", a)
		}
	})

	t.Run("MCP tool parameters", func(t *testing.T) {
		req := `{"model":"claude-opus-4-1","messages":[],"tools":[{"name":"mcp__` + p("mcp_server") + `__` + p("mcp_tool") + `","input_schema":{"type":"object",` +
			`"properties":{"` + pa("param") + `":{"type":"string"}},"required":["` + pa("param") + `"]}},` +
			`{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`
		files := accountCase(t, "api.anthropic.com", "/v1/messages", req, map[string]string{":status": "200", "content-type": "application/json"}, `{}`)
		got := files["request-body.json"]
		if !strings.Contains(got, `"properties":{"param_1":{"type":"string"}},"required":["param_1"]`) || !strings.Contains(got, `"required":["command"]`) {
			t.Errorf("an MCP tool's parameters aren't fakes, or a built-in tool's changed: %s", got)
		}
	})

	t.Run("analytics", func(t *testing.T) {
		req := `{"events":[{"event_type":"ClaudeCodeInternalEvent","event_data":{"event_name":"tengu_init","model":"` + pa("model") + `",` +
			`"subscriptionType":"` + pa("plan") + `","rateLimitTier":"` + pa("tier") + `","session_id":"` + p("session") + `"}}]}`
		accountCase(t, "api.anthropic.com", "/api/event_logging/v2/batch", req, map[string]string{":status": "200", "content-type": "application/json"}, `{}`)
	})
}

// Only an identifier too random to guess from its hash goes in the committed
// denylist; the rest stay in the local one.
func TestHighEntropy(t *testing.T) {
	for s, want := range map[string]bool{
		p("org"): true, p("device"): true, p("commit"): true, p("chatgpt_user"): true,
		p("email"): false, p("ip"): false, p("ipv6"): false, p("name"): false, p("host"): false, p("company"): false,
		p("user"): false, "a3f19c0e5b7d2468": false, "12345678901234": false, "org-Ab3dE": false,
	} {
		if got := HighEntropy(strings.ToLower(s)); got != want && HighEntropy(s) != want {
			t.Errorf("HighEntropy(%s) = %v", s, got)
		}
	}
}
