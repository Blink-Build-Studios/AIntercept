package extproc

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/capture"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// corpusShapes are the traffic shapes the corpus must hold (docs/TAXONOMY_*.md):
// every request kind of each client, and every error shape seen. A shape
// with no real capture is listed in KNOWN_GAPS instead.
var corpusShapes = []string{
	// Claude Code (api.anthropic.com): model calls, their side requests, and the control plane.
	"anthropic/claude-code/model-stream",          // a plain streamed reply
	"anthropic/claude-code/model-thread-create",   // a message thread's first request: the tool catalog, tool additions
	"anthropic/claude-code/model-thread-continue", // a thread's next request: only the new messages
	"anthropic/claude-code/model-stream-tools",    // a subagent's call that ends in a tool call
	"anthropic/claude-code/model-mcp-tools",       // MCP tools in the catalog
	"anthropic/claude-code/model-thinking",
	"anthropic/claude-code/model-image",
	"anthropic/claude-code/model-system-role", // a system-role message mid-conversation (#93)
	"anthropic/claude-code/model-interrupted", // the harness's note that the turn before was interrupted
	"anthropic/claude-code/model-title",       // the <session> title side request
	"anthropic/claude-code/model-json",        // a reply that isn't streamed
	"anthropic/claude-code/model-websearch",   // a server tool (web search)
	"anthropic/claude-code/model-disconnect",  // the client hung up before a response
	"anthropic/claude-code/count-tokens",
	"anthropic/claude-code/account-profile", // the identity lookup's own endpoint
	"anthropic/claude-code/account-bootstrap",
	"anthropic/claude-code/account-usage",
	"anthropic/claude-code/account-plugins", // a control-plane call naming the organization
	"anthropic/claude-code/analytics",
	"anthropic/claude-code/flags",
	"anthropic/claude-code/ws-close",  // a WebSocket closed with a close frame
	"anthropic/claude-code/error-400", // a continue request with no new messages, rejected
	"anthropic/claude-code/error-404",
	"anthropic/claude-code/error-503",
	"anthropic/claude-code/error-429",
	"anthropic/claude-code/error-529",
	"anthropic/claude-code/error-midstream",
	"anthropic/curl/error-401",
	// Codex (chatgpt.com, ab.chatgpt.com).
	"openai/codex/model-http", // the HTTP fallback: zstd request, event stream reply
	"openai/codex/model-ws",
	"openai/codex/model-ws-prewarm",
	"openai/codex/model-ws-tools",    // an exec script (custom tool call)
	"openai/codex/model-ws-function", // a function call
	"openai/codex/model-ws-fork",     // a forked subagent's first turn: history with tool calls
	"openai/codex/model-ws-interrupted",
	"openai/codex/model-title",
	"openai/codex/analytics",
	"openai/codex/metrics",
	"openai/codex/usage",
	"openai/codex/accounts-check", // the identity lookup's own endpoint
	"openai/codex/catalog-models",
	"openai/codex/catalog-plugins",
	"openai/codex/mcp",
	"openai/codex/web-search",
	"openai/codex/error-400",
	"openai/codex/error-ws",
	"openai/curl/codex-error-401",     // chatgpt.com's Codex endpoint, without a valid token
	"openai/curl/responses-error-401", // api.openai.com: the Responses API's error
	"openai/curl/chat-error-401",      // and Chat Completions'
	// Gemini (generativelanguage.googleapis.com).
	"gemini/api/model-generate",
	"gemini/api/model-stream",
}

// The APIs a case's meta.json names (capture.APIOf: host and path).
const (
	apiAnthropic = "anthropic_messages"      // api.anthropic.com /v1/messages
	apiChatGPT   = "chatgpt_backend"         // chatgpt.com /backend-api (Codex)
	apiResponses = "openai_responses"        // api.openai.com /v1/responses
	apiChat      = "openai_chat_completions" // api.openai.com /v1/chat/completions
	apiGemini    = "gemini"                  // generativelanguage.googleapis.com
)

// corpusRequire are field families the corpus must assert per API: for each
// API listed (nil: every case's), some case of that API asserts each field
// with a value, or with Each every case of it does. A gap is listed in
// KNOWN_GAPS as `field <api>:<field>` (Each: `field <case>:<field>`). A
// family a contract adds (a key × every API that carries it) is a row here.
var corpusRequire = []struct {
	APIs   []string
	Each   bool
	Fields []string
}{
	// The fields every model API carries (docs/harness-parity.md, "common").
	{APIs: []string{apiAnthropic, apiChatGPT}, Fields: []string{
		"row.model", "row.input_tokens", "row.output_tokens", "row.stop_reason", "row.tool_uses", "row.key_hint",
		"call.product", "call.client_version", "call.session_id", "call.model_call",
		"on_call_start.call.identity.reported_account_id", "on_call_start.request.model",
		"on_request.messages", "on_tool_call.message", "on_reply.message", "on_call_complete.response.input_tokens",
	}},
	// The reasoning effort a request asks for, and the tier a response says served it.
	{APIs: []string{apiAnthropic, apiChatGPT}, Fields: []string{"row.effort", "row.service_tier"}},
	// A real error in each API's own shape.
	{APIs: []string{apiAnthropic, apiChatGPT, apiResponses, apiChat, apiGemini}, Fields: []string{"row.error"}},
	// What every call has, whatever it is.
	{Each: true, Fields: []string{"call.kind", "interceptor.callbacks"}},
}

// Fields no call's row can hold (other rows' payloads, or set only outside
// the observer), so they're not the corpus's to cover.
var notCallFields = corpusSet("row.client_event", "row.verdict", "row.credential", "row.schema_fingerprint",
	"row.synthetic", "row.intercept_mode", "call.synthetic", "call.swept", "call.hidden_kind_note",
	"on_request.fetched", "on_call_start.call.mode", "on_call_complete.call.mode",
	// The Interceptor's own decisions, not the traffic's.
	"on_call_complete.respond", "on_call_complete.verdicts")

// TestCorpusCoverage: every shape has a case, every field the code produces
// is asserted (with a value) by some case, and each API asserts the field
// families corpusRequire lists; or KNOWN_GAPS says why not. A gap KNOWN_GAPS
// lists that a case now covers fails too, so the list stays true.
func TestCorpusCoverage(t *testing.T) {
	gaps := readGaps(t, filepath.Join(corpusRoot, "KNOWN_GAPS"))
	dirs, err := capture.Cases(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	covered := map[string]bool{}          // field
	byAPI := map[string]map[string]bool{} // api → field
	byCase := map[string]map[string]bool{}
	apiOf := map[string]string{} // case → api
	for _, dir := range dirs {
		c, err := capture.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		name := c.Name(corpusRoot)
		have[name], apiOf[name] = true, c.Meta.API
		want, err := readExpect(filepath.Join(dir, "expect.json"))
		if err != nil {
			t.Fatal(err)
		}
		if byAPI[c.Meta.API] == nil {
			byAPI[c.Meta.API] = map[string]bool{}
		}
		byCase[name] = map[string]bool{}
		for k, v := range want {
			if isZero(v) {
				continue
			}
			for _, f := range fieldsOf(k, v) {
				covered[f], byAPI[c.Meta.API][f], byCase[name][f] = true, true, true
			}
		}
	}
	check := func(gap string, ok bool) {
		reason, listed := gaps[gap]
		delete(gaps, gap)
		switch {
		case !ok && !listed:
			t.Errorf("%s: no case covers it (add one, or list it in KNOWN_GAPS with the reason)", gap)
		case ok && listed:
			t.Errorf("%s: KNOWN_GAPS lists it (%s), but a case covers it now: remove the line", gap, reason)
		}
	}
	for _, s := range corpusShapes {
		check("shape "+s, have[s])
	}
	for _, f := range corpusCatalog() {
		check("field "+f, covered[f])
	}
	cases := make([]string, 0, len(byCase))
	for n := range byCase {
		cases = append(cases, n)
	}
	sort.Strings(cases)
	for _, r := range corpusRequire {
		for _, f := range r.Fields {
			switch {
			case r.Each:
				for _, n := range cases {
					if r.APIs == nil || slices.Contains(r.APIs, apiOf[n]) {
						check("field "+n+":"+f, byCase[n][f])
					}
				}
			default:
				for _, api := range r.APIs {
					check("field "+api+":"+f, byAPI[api][f])
				}
			}
		}
	}
	for g := range gaps {
		t.Errorf("KNOWN_GAPS lists %q, which isn't a shape or field the coverage check knows", g)
	}
}

// readGaps reads KNOWN_GAPS: `<shape|field> <name>: <reason>` per line, # comments.
func readGaps(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		gap, reason, ok := strings.Cut(l, ": ")
		if !ok || strings.TrimSpace(reason) == "" || !(strings.HasPrefix(gap, "shape ") || strings.HasPrefix(gap, "field ")) {
			t.Fatalf("KNOWN_GAPS:%d: want `shape <case>: <reason>` or `field <name>: <reason>`", n)
		}
		out[gap] = reason
	}
	return out
}

var turnPrefix = regexp.MustCompile(`^turn\d+\.`)
var listIndex = regexp.MustCompile(`\.\d+(\.|$)`)

// fieldsOf names the catalog fields an expect.json key (and its value) covers.
func fieldsOf(k string, v any) []string {
	k = turnPrefix.ReplaceAllString(k, "")
	k = strings.Replace(k, "ws_connection.", "row.", 1)
	if strings.HasPrefix(k, "row.call.") {
		k = strings.TrimPrefix(k, "row.")
	}
	k = listIndex.ReplaceAllString(k, "$1")
	out := []string{k}
	for p := k; strings.Contains(p, "."); {
		p = p[:strings.LastIndex(p, ".")]
		out = append(out, p)
	}
	switch {
	case k == "client_events":
		for _, e := range v.([]any) {
			for f, fv := range e.(map[string]any) {
				if !isZero(fv) {
					out = append(out, "client_events."+f)
				}
			}
		}
	case k == "on_request.messages":
		for _, m := range v.([]any) {
			mm := m.(map[string]any)
			role := str(mm["role"])
			if role == "" {
				role = "ROLE_UNSPECIFIED" // protojson leaves out the zero value
			}
			out = append(out, "on_request.messages.role:"+role)
			for _, p := range asList(mm["parts"]) {
				out = append(out, "on_request.messages.part:"+partKind(p))
			}
		}
	case strings.HasPrefix(k, "on_reply.message") || strings.HasPrefix(k, "on_tool_call.message"):
		for _, p := range asList(v.(map[string]any)["parts"]) {
			out = append(out, k+".part:"+partKind(p))
		}
	}
	return out
}

func asList(v any) []any { l, _ := v.([]any); return l }

func str(v any) string { s, _ := v.(string); return s }

func partKind(p any) string {
	for k := range p.(map[string]any) {
		switch k {
		case "id", "size", "ref", "preview":
		default:
			return k
		}
	}
	return "?"
}

// corpusCatalog is every field the replay can read, from the types the code
// fills: a field added to one of them needs a case (or a KNOWN_GAPS line).
func corpusCatalog() []string {
	var out []string
	add := func(f string) {
		if !notCallFields[f] {
			out = append(out, f)
		}
	}
	for _, f := range jsonFields(reflect.TypeOf(events.Event{})) {
		if !rowSkip[f] {
			add("row." + f)
		}
	}
	rt := reflect.TypeOf(canon.Request{})
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("parquet"), ",")
		if !callSkip[name] {
			add("call." + name)
		}
	}
	for _, f := range []string{"kind", "model_call", "tool_uses", "ext", "tools_offered"} {
		add("call." + f)
	}
	for _, f := range jsonFields(reflect.TypeOf(events.CallDetail{})) {
		add("detail." + f)
	}
	for _, f := range jsonFields(reflect.TypeOf(events.ClientEvent{})) {
		if f != "data" {
			add("client_events." + f)
		}
	}
	for _, f := range []string{"format", "messages", "new", "chained", "prev"} {
		add("convo." + f)
	}
	for _, f := range protoFields(&pb2.CallStart{}, "", "call.call_id", "call.start_unix_nano", "headers", "request.request_bytes") {
		add("on_call_start." + f)
	}
	for _, f := range protoFields(&pb2.CallRecord{}, "", "call.call_id", "call.start_unix_nano", "call.client.headers", "end_unix_nano",
		"request.request_bytes", "response.response_bytes", "response.ttfb_ms", "response.duration_ms") {
		add("on_call_complete." + f)
	}
	for _, f := range []string{"format", "chained", "total_messages", "messages", "fetched"} {
		add("on_request." + f)
	}
	for _, r := range []pb2.Role{pb2.Role_ROLE_USER, pb2.Role_ROLE_ASSISTANT, pb2.Role_ROLE_TOOL, pb2.Role_ROLE_SYSTEM, pb2.Role_ROLE_UNSPECIFIED} {
		add("on_request.messages.role:" + r.String())
	}
	for _, k := range partKinds() {
		add("on_request.messages.part:" + k)
	}
	add("on_tool_call.message.part:tool_call")
	add("on_reply.message.part:text")
	add("on_reply.message.part:opaque")
	add("on_reply.released")
	add("on_reply.done")
	add("identity.learned")
	add("record.account")
	add("interceptor.callbacks")
	add("interceptor.unparsed")
	add("interceptor.no_new_messages")
	add("disconnect.error")
	sort.Strings(out)
	return out
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.IsExported() && name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// protoFields are a message's leaf fields (nested messages walked), as paths.
func protoFields(m proto.Message, prefix string, skip ...string) []string {
	sk := corpusSet(skip...)
	var out []string
	var walk func(md protoreflect.MessageDescriptor, prefix string)
	walk = func(md protoreflect.MessageDescriptor, prefix string) {
		fs := md.Fields()
		for i := 0; i < fs.Len(); i++ {
			f := fs.Get(i)
			p := prefix + string(f.Name())
			if sk[p] || f.Options().(*descriptorpb.FieldOptions).GetDeprecated() {
				continue
			}
			if f.Kind() == protoreflect.MessageKind && !f.IsMap() && !f.IsList() {
				walk(f.Message(), p+".")
				continue
			}
			out = append(out, p)
		}
	}
	walk(m.ProtoReflect().Descriptor(), prefix)
	return out
}

// partKinds are the Part oneof's cases (text, media, tool_call, …).
func partKinds() []string {
	var out []string
	od := (&pb2.Part{}).ProtoReflect().Descriptor().Oneofs().ByName("kind")
	for i := 0; i < od.Fields().Len(); i++ {
		out = append(out, string(od.Fields().Get(i).Name()))
	}
	return out
}
