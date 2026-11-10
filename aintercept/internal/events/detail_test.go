package events

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func sseOf(evs ...string) []byte {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString("data: " + e + "\n\n")
	}
	return []byte(b.String())
}

// A Codex exec script's commands and patch, its text, and the outputs the next request carries back.
func TestDetailFromCodexBodies(t *testing.T) {
	script := "const r = await tools.exec_command({cmd: \"cd app && npm test -- --watch | tee log\", workdir: \"/w\"});\n" +
		"await tools.exec_command({cmd: `cat <<'PY' > x.py\nfor i in range(3): print(i)\nPY\npython3 -m pytest -q`});\n" +
		"await tools.apply_patch(\"*** Begin Patch\\n*** Update File: src/timer.ts\\n@@\\n-old\\n+new\\n+more\\n*** End Patch\");"
	item, _ := json.Marshal(map[string]any{"type": "custom_tool_call", "call_id": "call_1", "name": "exec", "input": script})
	resp := sseOf(
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Tests  pass now.\nThe flake came from the clock."}]}}`,
		`{"type":"response.output_item.done","item":`+string(item)+`}`,
	)
	req := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call_0","output":"old"},{"type":"reasoning"},` +
		`{"type":"custom_tool_call","call_id":"call_9"},` +
		`{"type":"custom_tool_call_output","call_id":"call_9","output":[{"type":"text","text":"{\"wall_time_seconds\":0.4,\"exit_code\":1}"}]}]}`)
	d := DetailFromBodies(req, "", resp, "")
	if d == nil || d.Text != "Tests pass now. The flake came from the clock." {
		t.Fatalf("text: %+v", d)
	}
	want := ToolCallDetail{ID: "call_1", Tool: "exec", Commands: []string{"npm test", "tee", "cat", "python3 -m pytest"},
		Patches: []PatchFile{{File: "src/timer.ts", Added: 2, Removed: 1}}}
	if len(d.Calls) != 1 || !reflect.DeepEqual(d.Calls[0], want) {
		t.Fatalf("calls: %+v", d.Calls)
	}
	if !reflect.DeepEqual(d.Results, map[string]ToolResult{"call_9": {Exit: []int{1}}}) {
		t.Fatalf("results (only the newest step's): %+v", d.Results)
	}
}

// Claude Code: a streamed Bash tool_use and Edit, text deltas, and is_error on a tool result.
func TestDetailFromAnthropicBodies(t *testing.T) {
	resp := sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Running the "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tests."}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"FOO=1 go"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" test ./...\"}"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"Edit","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"a.go\",\"old_string\":\"x\",\"new_string\":\"y\\nz\"}"}}`,
	)
	req := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_0","is_error":true,"content":"Exit code 2"}]}]}`)
	d := DetailFromBodies(req, "", resp, "")
	if d == nil || d.Text != "Running the tests." {
		t.Fatalf("text: %+v", d)
	}
	want := []ToolCallDetail{
		{ID: "toolu_1", Tool: "Bash", Commands: []string{"go test"}},
		{ID: "toolu_2", Tool: "Edit", Patches: []PatchFile{{File: "a.go", Added: 2, Removed: 1}}},
	}
	if !reflect.DeepEqual(d.Calls, want) {
		t.Fatalf("calls: %+v", d.Calls)
	}
	if !reflect.DeepEqual(d.Results, map[string]ToolResult{"toolu_0": {Exit: []int{2}, Error: true}}) {
		t.Fatalf("results: %+v", d.Results)
	}
}

func TestCommandWords(t *testing.T) {
	for in, want := range map[string][]string{
		"rg -n foo src && sed -n 1,5p x":        {"rg", "sed"},
		"sudo /usr/bin/git diff --stat":         {"git diff"},
		"for f in *.go; do gofmt -l $f; done":   {"gofmt"},
		"if [ -f x ]; then make build; fi":      {"make build"},
		"python3 - <<'EOF'\nimport os\nEOF\nls": {"python3", "ls"},
	} {
		got := commandWords(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("commandWords(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNoDetailWithoutBodies(t *testing.T) {
	if d := DetailFromBodies(nil, "", nil, ""); d != nil {
		t.Fatalf("no bodies, no detail: %+v", d)
	}
}

// The prompt is the request's last item when it's the person's message; tool results and
// context blocks (<environment_context>, <system-reminder>) aren't.
func TestPromptFromRequest(t *testing.T) {
	for body, want := range map[string]string{
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"},{"type":"input_text","text":"fix the flaky timer test"}]}]}`:                                                                                                                           "fix the flaky timer test",
		`{"input":[{"type":"message","role":"user"},{"type":"custom_tool_call_output","call_id":"c","output":"x"}]}`:                                                                                                                                                                                                             "",
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions>"}]}]}`: "fix it",
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`:                                                                                                                                     "",
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<hcom>review PR 4</hcom>"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"<hcom>x</hcom>"}]}]}`:                                                                                                       "<hcom>review PR 4</hcom>",
		`{"messages":[{"role":"user","content":"why is CI red?"}]}`:                                          "why is CI red?",
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`: "",
	} {
		if got := promptFromRequest([]byte(body)); got != want {
			t.Errorf("promptFromRequest(%s) = %q, want %q", body, got, want)
		}
	}
}
