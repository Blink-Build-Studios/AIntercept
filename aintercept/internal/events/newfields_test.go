package events

import (
	"reflect"
	"strings"
	"testing"
)

func TestAttachments_AllProviders(t *testing.T) {
	b64 := strings.Repeat("QUJD", 100) // 400 chars → 300 bytes
	cases := map[string]struct {
		body string
		want Attachments
	}{
		"anthropic image+document": {`{"messages":[{"role":"user","content":[{"type":"text","text":"x"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}},
			{"type":"document","source":{"type":"file","file_id":"file_1"}}]}]}`,
			Attachments{Images: 1, Files: 1, InlineBytes: 300, ByReference: 1}},
		"responses (codex) input_image + input_file": {`{"input":[{"type":"message","role":"user","content":[
			{"type":"input_text","text":"x"},
			{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `"},
			{"type":"input_file","file_id":"file_2"}]}]}`,
			Attachments{Images: 1, Files: 1, InlineBytes: 300, ByReference: 1}},
		"openai chat image_url by URL": {`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`,
			Attachments{Images: 1, ByReference: 1}},
		"gemini inlineData": {`{"contents":[{"parts":[{"text":"x"},{"inlineData":{"mimeType":"image/jpeg","data":"` + b64 + `"}}]}]}`,
			Attachments{Images: 1, InlineBytes: 300}},
	}
	for name, c := range cases {
		got := AttachmentsFromRequestBody([]byte(c.body))
		if got == nil || *got != c.want {
			t.Errorf("%s: got %+v want %+v", name, got, c.want)
		}
	}
	if AttachmentsFromRequestBody([]byte(`{"messages":[{"role":"user","content":"plain text"}],"input":"a string"}`)) != nil {
		t.Error("no attachments must be nil")
	}
}

func TestWebSearchFromBodies(t *testing.T) {
	req := []byte(`{"model":"gpt-6.1-sol","commands":{"search_query":[{"q":"secret a"},{"q":"secret b"}]},"settings":{"external_web_access":true}}`)
	resp := []byte(`{"output":"…","results":[{"domain":"nodejs.org","url":"https://nodejs.org/x","snippet":"s"},{"domain":"Nodejs.org"},{"domain":"github.com"}]}`)
	ws := WebSearchFromBodies("/backend-api/codex/alpha/search", req, resp, "")
	if ws == nil || ws.Queries != 2 || ws.Results != 3 || !reflect.DeepEqual(ws.ResultDomains, []string{"github.com", "nodejs.org"}) ||
		ws.ExternalWebAccess == nil || !*ws.ExternalWebAccess {
		t.Fatalf("got %+v", ws)
	}
	if WebSearchFromBodies("/v1/messages", req, resp, "") != nil {
		t.Fatal("other paths must yield nil")
	}
	// A parsed web search is recognized, not sampled.
	e := Event{Phase: PhaseComplete, Path: "/backend-api/codex/alpha/search"}
	e.AttachBodies(req, resp, "", nil)
	e.ExtractDeferred()
	if _, ok := e.SampleIfUnrecognized(); ok || e.WebSearch == nil {
		t.Fatal("web search must be parsed and not sampled")
	}
}

func TestOTLPMetrics(t *testing.T) {
	body := []byte(`{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex_exec"}},{"key":"os","value":{"stringValue":"Debian"}}]},
	 "scopeMetrics":[{"scope":{"name":"codex"},"metrics":[
	  {"name":"codex.turn.ttft.duration_ms","unit":"ms","histogram":{"aggregationTemporality":1,"dataPoints":[
	    {"attributes":[{"key":"model","value":{"stringValue":"gpt-6.1-sol"}},{"key":"session_source","value":{"stringValue":"exec"}}],
	     "count":"3","sum":1234.5,"min":100,"max":900,"bucketCounts":["0","3"],"explicitBounds":[500],"timeUnixNano":"1793641974000000000"}]}},
	  {"name":"codex.process.start","sum":{"isMonotonic":true,"dataPoints":[{"attributes":[{"key":"originator","value":{"stringValue":"codex_exec"}}],"asInt":"5"}]}}]}]}]}`)
	evs := ClientEventsFromRequest("", "/otlp/v1/metrics", body, "")
	if len(evs) != 2 {
		t.Fatalf("want 2 metric rows, got %d", len(evs))
	}
	h := evs[0]
	if h.Source != "otel" || h.Type != "otel_metric" || h.Name != "codex.turn.ttft.duration_ms" || h.Model != "gpt-6.1-sol" || h.AgentType != "exec" ||
		h.Data["kind"] != "histogram" || h.Data["unit"] != "ms" || h.Data["resource"].(map[string]any)["service.name"] != "codex_exec" {
		t.Fatalf("histogram row: %+v", h)
	}
	p := h.Data["points"].([]any)[0].(map[string]any)
	if p["count"] != int64(3) || p["sum"] != 1234.5 || p["timeUnixNano"] != int64(1793641974000000000) {
		t.Fatalf("histogram point: %+v", p)
	}
	s := evs[1].Data["points"].([]any)[0].(map[string]any)
	if evs[1].Data["kind"] != "sum" || evs[1].Data["monotonic"] != true || s["value"] != int64(5) {
		t.Fatalf("sum row: %+v %+v", evs[1].Data, s)
	}
	if ClientEventsFromRequest("", "/otlp/v1/metrics", []byte{0x0a, 0x01}, "") != nil {
		t.Fatal("protobuf exports aren't parsed")
	}
}

func TestPrewarmReasoningAndResponsesStatus(t *testing.T) {
	e := Event{Phase: PhaseComplete, Path: "/backend-api/codex/responses"}
	e.AttachBodies([]byte(`{"type":"response.create","model":"gpt-6.1-sol","generate":false,"input":[]}`),
		[]byte("data: "+`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":10,"output_tokens":4,"output_tokens_details":{"reasoning_tokens":3}}}}`+"\n\n"), "", nil)
	e.ExtractDeferred()
	if !e.Prewarm || e.ReasoningTokens != 3 || e.StopReason != "incomplete:interrupted" {
		t.Fatalf("prewarm=%v reasoning=%d stop=%q", e.Prewarm, e.ReasoningTokens, e.StopReason)
	}
}
