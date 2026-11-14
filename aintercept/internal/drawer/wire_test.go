package drawer

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func detailObject(t *testing.T, lines ...string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	for _, l := range lines {
		_, _ = zw.Write([]byte(l + "\n"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func keys(v Value) []string {
	out := []string{}
	for _, m := range v.O {
		out = append(out, m.Key)
	}
	return out
}

// A wire is kept with the key's state for every call that ended (§3.7), so it
// holds only what the render reads of the call's headers: the request's
// prompt, class, client request id and session; the response's request id,
// its unified rate limit headers (any case, in order: the first one a name
// matches is read) and Codex's. The bodies' views are already all it keeps of
// them.
func TestAWireKeepsOnlyTheHeadersTheDrawerReads(t *testing.T) {
	gz := detailObject(t,
		`{"correlation_id":"c1","seq":1,"phase":"request_headers","timestamp_ns":10,"method":"POST","path":"/v1/messages",`+
			`"headers":{"user-agent":"x","x-claude-code-prompt-id":"p","authorization":"<redacted>","x-claude-code-session-id":"s",`+
			`"x-client-request-id":"r","x-claude-code-request-class":"main","anthropic-beta":"b"}}`,
		`{"correlation_id":"c1","seq":2,"phase":"response_headers","timestamp_ns":20,"status":200,`+
			`"headers":{"content-type":"application/json","request-id":"req_1","Anthropic-Ratelimit-Unified-Status":"allowed",`+
			`"anthropic-ratelimit-unified-status":"later","x-codex-plan-type":"pro","date":"d","cf-ray":"r"}}`,
		`{"correlation_id":"c1","seq":3,"phase":"complete","timestamp_ns":30,"model":"m","output_tokens":5}`,
	)
	w := ParseDetail("c1", gz)
	if w == nil {
		t.Fatal("no wire")
	}
	if got := keys(w.ReqHeaders); len(got) != 4 || got[0] != "x-claude-code-prompt-id" || got[1] != "x-claude-code-session-id" {
		t.Errorf("request headers kept: %v", got)
	}
	want := []string{"request-id", "Anthropic-Ratelimit-Unified-Status", "anthropic-ratelimit-unified-status", "x-codex-plan-type"}
	if got := keys(w.RespHeaders); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("response headers kept: %v", got)
	}
	if v := limit(w.RespHeaders, "status"); v.S != "allowed" {
		t.Errorf("the first unified status: %v", v)
	}
	if w.requestID().S != "req_1" || w.prompt().S != "p" || w.clientRequestID().S != "r" || w.requestClass().S != "main" {
		t.Error("a header the drawer reads is gone")
	}
	// No response headers row: None stays None; an empty dict stays a dict.
	w = ParseDetail("c2", detailObject(t,
		`{"correlation_id":"c2","seq":1,"phase":"request_headers","timestamp_ns":10,"headers":{"user-agent":"x"}}`))
	if w.RespHeaders.K != Null || w.ReqHeaders.K != Object || len(w.ReqHeaders.O) != 0 {
		t.Errorf("kinds: %v %v", w.RespHeaders, w.ReqHeaders)
	}
}
