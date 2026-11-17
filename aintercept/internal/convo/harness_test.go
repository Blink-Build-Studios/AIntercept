package convo

import (
	"strings"
	"testing"
)

// What the harness writes into the conversation (docs/INTERCEPTOR_V2.md,
// "The harness's messages"): system messages mid-conversation, and the notes
// it puts in a user's turn, are messages of their own role, not the user's;
// the user's words in Claude Code's mid-turn note are a prompt; and nothing
// unknown makes a whole request unreadable.

// A turn as current Claude Code sends it: system messages mid-conversation
// (tools added, a file changed), tool results with a system reminder, an
// interrupted request, and the user's mid-turn note.
const claudeCodeTurn = `{"model":"claude-x","thread":{"type":"create"},"system":[{"type":"text","text":"You are Claude Code."}],"messages":[
	{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAs you answer, use the context.\n</system-reminder>"},{"type":"text","text":"fix the test"}]},
	{"role":"system","content":[{"type":"text","text":"The following tools just became available."},
		{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__docs__read"}}}]},
	{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"x"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test"}}]},
	{"role":"system","content":"Note: a.go changed on disk."},
	{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","content":"FAIL"},
		{"type":"text","text":"[Request interrupted by user for tool use]"},
		{"type":"text","text":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"},
		{"type":"text","text":"The user sent a new message while you were working:\nuse the other test\n\nThis is how Claude Code surfaces messages the user sends mid-turn — within the running turn, often alongside the next tool result, rather than as a separate conversation turn. Address the message above as you continue this turn."}]}]}`

func TestHarnessMessagesClaudeCode(t *testing.T) {
	runNewCases(t, anthropicHost, anthropicPath, []newCase{{
		name: "system messages and the harness's notes are system messages; the user's mid-turn words a prompt",
		body: claudeCodeTurn,
		all: []string{
			`system: text "<system-reminder>\nAs you answer, use the context.\n</system-reminder>"`,
			`user: text "fix the test"`,
			`system: text "The following tools just became available.", tool_added mcp__docs__read`,
			`assistant: opaque`,
			`assistant: call toolu_1 Bash {"command":"go test"}`,
			`system: text "Note: a.go changed on disk."`,
			`tool: result toolu_1 "FAIL"`,
			`system: text "[Request interrupted by user for tool use]"`,
			`system: text "<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"`,
			`user: text "use the other test"`,
		},
		new: []string{
			`system: text "Note: a.go changed on disk."`,
			`tool: result toolu_1 "FAIL"`,
			`system: text "[Request interrupted by user for tool use]"`,
			`system: text "<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"`,
			`user: text "use the other test"`,
		},
	}, {
		name: "another sender's mid-turn note is the harness's, not the user's",
		body: `{"messages":[{"role":"user","content":"go"},{"role":"assistant","content":"ok"},
			{"role":"user","content":[{"type":"text","text":"The coordinator sent a message while you were working:\nstop\n\nAddress this before completing your current task."}]}]}`,
		all: []string{`user: text "go"`, `assistant: text "ok"`,
			`system: text "The coordinator sent a message while you were working:\nstop\n\nAddress this before completing your current task."`},
		new: []string{`system: text "The coordinator sent a message while you were working:\nstop\n\nAddress this before completing your current task."`},
	}, {
		name: "the next-prompt suggestion request is the harness's",
		body: `{"messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":[{"type":"text","text":"[SUGGESTION MODE: Suggest what the user might naturally type next.]"}]}]}`,
		all:  []string{`user: text "fix it"`, `assistant: text "done"`, `system: text "[SUGGESTION MODE: Suggest what the user might naturally type next.]"`},
		new:  []string{`system: text "[SUGGESTION MODE: Suggest what the user might naturally type next.]"`},
	}, {
		name: "a title request quoting the session is the harness's",
		body: `{"messages":[{"role":"user","content":"<session>\nfix the test\n</session>\n\nWrite the title in the predominant language of the session."}]}`,
		all:  []string{`system: text "<session>\nfix the test\n</session>\n\nWrite the title in the predominant language of the session."`},
		new:  []string{`system: text "<session>\nfix the test\n</session>\n\nWrite the title in the predominant language of the session."`},
	}})
}

// Replacing the user's mid-turn words keeps the harness's wrapper around
// them; replacing a harness message works as for any message; every other
// byte goes as sent.
func TestHarnessReplace(t *testing.T) {
	r := mustParse(t, anthropicHost, anthropicPath, claudeCodeTurn)
	var prompt, reminder *Message
	for _, i := range r.New {
		m := &r.Messages[i]
		switch {
		case m.Role == RoleUser:
			prompt = m
		case m.Role == RoleSystem && strings.Contains(m.Parts[0].Text, "task tools"):
			reminder = m
		}
	}
	if prompt == nil || reminder == nil || string(prompt.Parts[0].Content()) != "use the other test" {
		t.Fatalf("prompt %+v reminder %+v", prompt, reminder)
	}
	out, err := r.Apply([]Edit{
		{Message: prompt.Index, Part: 0, Content: "use the flaky test"},
		{Message: reminder.Index, Part: 0, Content: "<system-reminder>\n[removed]\n</system-reminder>"},
	})
	want := strings.Replace(strings.Replace(claudeCodeTurn, "use the other test", "use the flaky test", 1),
		`The task tools haven't been used recently.`, `[removed]`, 1)
	if err != nil || string(out) != want {
		t.Fatalf("%v\n%s", err, out)
	}
	// Rewriting for a Respond asks in the user's words' place, inside the wrapper.
	b, err := r.Rewrite("ok")
	if err != nil || !strings.Contains(string(b), `while you were working:\n`+RespondInstruction+`ok\n\nThis is how`) {
		t.Fatalf("%v\n%s", err, b)
	}
}

// Codex: the developer message (its permissions) and the environment and
// instructions it puts in user items are the harness's.
func TestHarnessMessagesCodex(t *testing.T) {
	runNewCases(t, "chatgpt.com", "/backend-api/codex/responses", []newCase{{
		name: "developer and harness items are system messages",
		body: `{"model":"gpt-x","instructions":"You are Codex.","input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>sandboxed</permissions instructions>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<user_instructions>\nbe brief\n</user_instructions>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <shell>zsh</shell>\n</environment_context>"},{"type":"input_text","text":"list the files"}]}]}`,
		all: []string{
			`system: text "<permissions instructions>sandboxed</permissions instructions>"`,
			`system: text "<user_instructions>\nbe brief\n</user_instructions>"`,
			`system: text "<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"`,
			`system: text "<environment_context>\n  <shell>zsh</shell>\n</environment_context>"`,
			`user: text "list the files"`,
		},
		new: []string{
			`system: text "<permissions instructions>sandboxed</permissions instructions>"`,
			`system: text "<user_instructions>\nbe brief\n</user_instructions>"`,
			`system: text "<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"`,
			`system: text "<environment_context>\n  <shell>zsh</shell>\n</environment_context>"`,
			`user: text "list the files"`,
		},
	}})
	runNewCases(t, "api.openai.com", "/v1/chat/completions", []newCase{{
		name: "Chat Completions system and developer messages are system messages",
		body: `{"messages":[{"role":"system","content":"be brief"},{"role":"developer","content":[{"type":"text","text":"no tools"}]},{"role":"user","content":"hi"}]}`,
		all:  []string{`system: text "be brief"`, `system: text "no tools"`, `user: text "hi"`},
		new:  []string{`system: text "be brief"`, `system: text "no tools"`, `user: text "hi"`},
	}})
}

// Nothing unknown makes a whole request unreadable: an unknown role, item,
// block or shape becomes an opaque part (present, unchangeable), and the rest
// of the request is read.
func TestUnknownIsOpaque(t *testing.T) {
	runNewCases(t, anthropicHost, anthropicPath, []newCase{{
		name: "Anthropic: an unknown role, a message that isn't an object, content that isn't text or blocks, a block that isn't an object",
		body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},
			{"role":"critic","content":"too long"},
			42,
			{"role":"user","content":7},
			{"role":"user","content":["loose", {"type":"brand_new_block","x":1}, {"type":"tool_use","id":5}, {"type":"text","text":"and now?"}]}]}`,
		all: []string{`user: text "hi"`, `assistant: text "ok"`, `critic: opaque`, `: opaque`, `user: opaque`, `user: opaque, opaque, opaque, text "and now?"`},
		new: []string{`critic: opaque`, `: opaque`, `user: opaque`, `user: opaque, opaque, opaque, text "and now?"`},
	}})
	runNewCases(t, "chatgpt.com", "/backend-api/codex/responses", []newCase{{
		name: "Responses: an unknown role, an item that isn't an object, content of another shape",
		body: `{"input":[{"type":"message","role":"user","content":"hi"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},
			{"type":"message","role":"critic","content":"no"},"loose",{"type":"message","role":"user","content":{"weird":true}},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`,
		all: []string{`user: text "hi"`, `assistant: text "ok"`, `critic: opaque`, `: opaque`, `user: opaque`, `user: text "go on"`},
		new: []string{`critic: opaque`, `: opaque`, `user: opaque`, `user: text "go on"`},
	}})
	runNewCases(t, "api.openai.com", "/v1/chat/completions", []newCase{{
		name: "Chat: an unknown role, a message that isn't an object, tool calls of another shape",
		body: `{"messages":[{"role":"critic","content":"no"},"loose",{"role":"assistant","content":null,"tool_calls":"weird"},{"role":"user","content":"go"}]}`,
		all:  []string{`critic: opaque`, `: opaque`, `assistant: opaque`, `user: text "go"`},
		new:  []string{`user: text "go"`},
	}})
	// Only a body that isn't a conversation at all is unreadable.
	for _, b := range []string{`not json`, `{"model":"x"}`, `{"messages":"hi"}`, `[]`} {
		if _, why := ParseReason(anthropicHost, anthropicPath, []byte(b)); why == "" {
			t.Errorf("%s parsed", b)
		}
	}
	if _, why := ParseReason(anthropicHost, anthropicPath, []byte(claudeCodeTurn)); why != "" {
		t.Errorf("unparsed: %s", why)
	}
}

// Claude Code's tool_addition blocks (tools added mid-session, in a system
// message) are typed parts naming the tool, not opaque; a block of the same
// family it doesn't know stays opaque.
func TestToolAddition(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"go"},{"role":"assistant","content":"ok"},
		{"role":"system","content":[{"type":"text","text":"The following tools just became available."},
			{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__docs__read","description":"Read a doc.","input_schema":{"type":"object"}},"eager_input_streaming":true}},
			{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"Monitor"}}},
			{"type":"tool_addition","tool":{"type":"something_else"}}]}]}`
	runNewCases(t, anthropicHost, anthropicPath, []newCase{{
		name: "tool_addition",
		body: body,
		all:  []string{`user: text "go"`, `assistant: text "ok"`, `system: text "The following tools just became available.", tool_added mcp__docs__read, tool_added Monitor, opaque`},
		new:  []string{`system: text "The following tools just became available.", tool_added mcp__docs__read, tool_added Monitor, opaque`},
	}})
	// The tool's part can't be replaced; the text beside it can.
	r := mustParse(t, anthropicHost, anthropicPath, body)
	m := r.Messages[r.New[0]]
	if _, err := r.Apply([]Edit{{Message: m.Index, Part: 1, Content: "x"}}); err == nil {
		t.Fatal("a tool_added part was replaced")
	}
	if p := m.Parts[1]; p.Size == 0 || p.Content() != nil {
		t.Fatalf("part %+v: its size is the block's, its content not inspectable", p)
	}
	if _, err := r.Apply([]Edit{{Message: m.Index, Part: 0, Content: "Tools changed."}}); err != nil {
		t.Fatal(err)
	}
}

// Codex's items that carry no role: its own settings and markers
// (configuration_update, compaction_trigger) are the harness's; a compaction
// (the provider's encrypted summary) and any item it doesn't know are
// present and opaque, with no role, never the user's.
func TestCodexItemsWithoutARole(t *testing.T) {
	runNewCases(t, "chatgpt.com", "/backend-api/codex/responses", []newCase{{
		name: "roleless items",
		body: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},
			{"type":"compaction","id":"cmp_1","encrypted_content":"gAAAA-synthetic"},
			{"type":"configuration_update","reasoning":{"effort":"high"}},
			{"type":"compaction_trigger"},
			{"type":"item_reference","id":"msg_9"},
			{"type":"agent_note","role":"user","text":"x"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`,
		all: []string{`user: text "hi"`, `assistant: text "ok"`, `: opaque`, `system: opaque`, `system: opaque`, `: opaque`, `user: opaque`, `user: text "go on"`},
		new: []string{`: opaque`, `system: opaque`, `system: opaque`, `: opaque`, `user: opaque`, `user: text "go on"`},
	}})
}

// Claude Code's wrappers in a user's turn, by what the wire shows: a bash-mode
// command's output, a skill's body, a slash command's output and caveat are
// the harness's; the command the user typed and what they pasted are the
// user's; image placeholders ahead of the user's words are the harness's
// labels for the image blocks beside them, kept around the words on a
// Replace, and a block of placeholders alone is the harness's.
func TestHarnessWrappersClaudeCode(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":"ok"},{"role":"user","content":[
		{"type":"text","text":"<bash-input>ls</bash-input>\n"},
		{"type":"text","text":"<bash-stdout>a.go</bash-stdout><bash-stderr></bash-stderr>"},
		{"type":"text","text":"<bash-stdout></bash-stdout><bash-stderr>no such file</bash-stderr>"},
		{"type":"text","text":"Base directory for this skill: /skills/demo\n\n# Demo skill"},
		{"type":"text","text":"<local-command-caveat>Caveat: generated by local commands.</local-command-caveat>"},
		{"type":"text","text":"<command-name>/demo</command-name>"},
		{"type":"text","text":"<local-command-stdout>(no content)</local-command-stdout>"},
		{"type":"text","text":"[Image #2] [Image #3] what is in these?"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
		{"type":"text","text":"[Image #4]"},
		{"type":"text","text":"compare [Image #2] with the old one\n\n<pasted_content>notes</pasted_content>"}]}]}`
	runNewCases(t, anthropicHost, anthropicPath, []newCase{{
		name: "wrappers",
		body: body,
		all: []string{`user: text "start"`, `assistant: text "ok"`,
			`user: text "<bash-input>ls</bash-input>\n"`,
			`system: text "<bash-stdout>a.go</bash-stdout><bash-stderr></bash-stderr>"`,
			`system: text "<bash-stdout></bash-stdout><bash-stderr>no such file</bash-stderr>"`,
			`system: text "Base directory for this skill: /skills/demo\n\n# Demo skill"`,
			`system: text "<local-command-caveat>Caveat: generated by local commands.</local-command-caveat>"`,
			`user: text "<command-name>/demo</command-name>"`,
			`system: text "<local-command-stdout>(no content)</local-command-stdout>"`,
			`user: text "what is in these?", media image/png 8B`,
			`system: text "[Image #4]"`,
			`user: text "compare [Image #2] with the old one\n\n<pasted_content>notes</pasted_content>"`},
		new: []string{
			`user: text "<bash-input>ls</bash-input>\n"`,
			`system: text "<bash-stdout>a.go</bash-stdout><bash-stderr></bash-stderr>"`,
			`system: text "<bash-stdout></bash-stdout><bash-stderr>no such file</bash-stderr>"`,
			`system: text "Base directory for this skill: /skills/demo\n\n# Demo skill"`,
			`system: text "<local-command-caveat>Caveat: generated by local commands.</local-command-caveat>"`,
			`user: text "<command-name>/demo</command-name>"`,
			`system: text "<local-command-stdout>(no content)</local-command-stdout>"`,
			`user: text "what is in these?", media image/png 8B`,
			`system: text "[Image #4]"`,
			`user: text "compare [Image #2] with the old one\n\n<pasted_content>notes</pasted_content>"`},
	}})
	r := mustParse(t, anthropicHost, anthropicPath, body)
	var words *Message
	for _, i := range r.New {
		if m := &r.Messages[i]; m.Role == RoleUser && m.Parts[0].Text == "what is in these?" {
			words = m
		}
	}
	if words == nil {
		t.Fatal("no prompt")
	}
	out, err := r.Apply([]Edit{{Message: words.Index, Part: 0, Content: "describe them"}})
	if err != nil || !strings.Contains(string(out), `"[Image #2] [Image #3] describe them"`) {
		t.Fatalf("%v\n%s", err, out)
	}
}
