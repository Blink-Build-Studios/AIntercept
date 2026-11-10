package events

import (
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CallDetail is what a model call did, read from its recorded bodies. It exists only
// in full interception mode, where every body is recorded anyway: never derive it
// from a body that isn't kept (docs/harness-parity.md, handoff §11L.7).
type CallDetail struct {
	// Calls are the tools the response asked to run, in order.
	Calls []ToolCallDetail `json:"calls,omitempty"`
	// Text is the start of the response's text output (the first words a step said).
	Text string `json:"text,omitempty"`
	// Results are the outcomes of earlier calls this request carries back, by call id.
	Results map[string]ToolResult `json:"results,omitempty"`
	// Prompt is the person's newest message in the request (what the prompt said), when
	// the request ends with one.
	Prompt string `json:"prompt,omitempty"`
}

// ToolCallDetail is one tool call: a Codex exec script can run several commands and patches.
type ToolCallDetail struct {
	ID       string      `json:"id,omitempty"`
	Tool     string      `json:"tool"`
	Commands []string    `json:"commands,omitempty"` // each command's program, plus a runner's subcommand ("npm test")
	Patches  []PatchFile `json:"patches,omitempty"`
}

// PatchFile is one file a call changed, with its added and removed lines.
type PatchFile struct {
	File    string `json:"file"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// ToolResult is how an earlier tool call ended: the exit codes of the commands it ran.
type ToolResult struct {
	Exit  []int `json:"exit,omitempty"`
	Error bool  `json:"error,omitempty"` // the client marked the result an error (Claude Code's is_error)
}

// textChars bounds Text: enough for the first words, never the whole answer.
const textChars = 120

// promptChars bounds Prompt: a prompt row shows its start; the drawer has the body.
const promptChars = 2000

// DetailFromBodies reads a model call's recorded request and response bodies (each as
// sent, decoded per its content-encoding). Nil when they hold nothing it reads.
func DetailFromBodies(req []byte, reqEnc string, resp []byte, respEnc string) *CallDetail {
	d := &CallDetail{}
	if raw, err := decodeBody(resp, respEnc); err == nil && len(raw) > 0 {
		d.fromResponse(raw)
	}
	if raw, err := decodeBody(req, reqEnc); err == nil && len(raw) > 0 {
		d.Results = resultsFromRequest(raw)
		d.Prompt = promptFromRequest(raw)
	}
	if len(d.Calls) == 0 && d.Text == "" && len(d.Results) == 0 && d.Prompt == "" {
		return nil
	}
	return d
}

// detailItem is an Anthropic content block or an OpenAI Responses output item.
type detailItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`     // custom_tool_call script (string), tool_use input (object)
	Arguments string          `json:"arguments"` // function_call arguments (JSON text)
	Text      string          `json:"text"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"` // a Responses message's parts
}

func (d *CallDetail) fromResponse(raw []byte) {
	text := string(raw)
	if !strings.Contains(text, "data:") {
		var whole struct {
			Content []detailItem `json:"content"` // Anthropic JSON
			Output  []detailItem `json:"output"`  // Responses JSON
		}
		if json.Unmarshal(raw, &whole) == nil {
			for _, it := range append(whole.Content, whole.Output...) {
				d.addItem(it)
			}
		}
		return
	}
	// Anthropic streams a tool_use's input as JSON fragments, and text as deltas.
	type open struct {
		item  detailItem
		input strings.Builder
	}
	blocks := map[int]*open{}
	var order []int
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Type         string      `json:"type"`
			Index        int         `json:"index"`
			ContentBlock *detailItem `json:"content_block"`
			Delta        *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Item *detailItem `json:"item"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "content_block_start" && ev.ContentBlock != nil:
			blocks[ev.Index] = &open{item: *ev.ContentBlock}
			order = append(order, ev.Index)
		case ev.Type == "content_block_delta" && ev.Delta != nil:
			if b := blocks[ev.Index]; b != nil {
				b.input.WriteString(ev.Delta.PartialJSON)
				if ev.Delta.Type == "text_delta" && utf8.RuneCountInString(b.item.Text) < textChars {
					b.item.Text += ev.Delta.Text
				}
			}
		case ev.Type == "response.output_item.done" && ev.Item != nil: // Responses: each item, complete
			d.addItem(*ev.Item)
		}
	}
	for _, i := range order {
		b := blocks[i]
		if s := b.input.String(); s != "" {
			b.item.Input = json.RawMessage(s)
		}
		d.addItem(b.item)
	}
}

func (d *CallDetail) addItem(it detailItem) {
	switch it.Type {
	case "text":
		d.addText(it.Text)
	case "message":
		for _, p := range it.Content {
			if p.Type == "output_text" || p.Type == "text" {
				d.addText(p.Text)
			}
		}
	case "custom_tool_call": // Codex exec: a script that calls tools.<name>(…)
		var script string
		if json.Unmarshal(it.Input, &script) == nil {
			d.Calls = append(d.Calls, scriptCall(firstOf(it.CallID, it.ID), it.Name, script))
		}
	case "function_call": // Responses: exec_command {cmd}, shell {command}, apply_patch {input}
		c := ToolCallDetail{ID: firstOf(it.CallID, it.ID), Tool: it.Name}
		var args map[string]any
		_ = json.Unmarshal([]byte(it.Arguments), &args)
		if cmd := commandArg(args); cmd != "" {
			c.Commands = commandWords(cmd)
		}
		if p, ok := args["input"].(string); ok {
			c.Patches = patchFiles(p)
		}
		d.Calls = append(d.Calls, c)
	case "tool_use": // Anthropic: Bash {command}, Edit/MultiEdit/Write {file_path, …}
		c := ToolCallDetail{ID: it.ID, Tool: it.Name}
		var in map[string]any
		_ = json.Unmarshal(it.Input, &in)
		if cmd, ok := in["command"].(string); ok && cmd != "" {
			c.Commands = commandWords(cmd)
		}
		if f, ok := in["file_path"].(string); ok && f != "" {
			c.Patches = []PatchFile{editLines(f, in)}
		}
		d.Calls = append(d.Calls, c)
	}
}

func (d *CallDetail) addText(s string) {
	if d.Text == "" && strings.TrimSpace(s) != "" {
		d.Text = truncateRunes(strings.Join(strings.Fields(s), " "), textChars)
	}
}

// scriptCall reads a Codex exec script: the commands it runs (tools.exec_command({cmd})) and
// the patches it applies (tools.apply_patch(…)), in order.
func scriptCall(id, name, script string) ToolCallDetail {
	c := ToolCallDetail{ID: id, Tool: name}
	for _, m := range scriptCmd.FindAllStringSubmatch(script, -1) {
		c.Commands = append(c.Commands, commandWords(jsUnquote(firstOf(m[1], m[2], m[3])))...)
	}
	if i := strings.Index(script, "*** Begin Patch"); i >= 0 {
		c.Patches = patchFiles(jsUnquote(script[i:]))
	}
	return c
}

// scriptCmd matches a command in an exec script: cmd: "…", cmd: '…' or cmd: `…`.
var scriptCmd = regexp.MustCompile("cmd\\s*:\\s*(?:\"((?:[^\"\\\\]|\\\\.)*)\"|'((?:[^'\\\\]|\\\\.)*)'|`((?:[^`\\\\]|\\\\.)*)`)")

// jsUnquote undoes a JavaScript string's common escapes (enough to read commands and patches).
func jsUnquote(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\'`, `'`, "\\`", "`", `\\`, `\`)
	return r.Replace(s)
}

func commandArg(args map[string]any) string {
	for _, k := range []string{"cmd", "command"} {
		switch v := args[k].(type) {
		case string:
			return v
		case []any: // ["bash", "-lc", "npm test"]: the script is the last part
			if len(v) > 0 {
				if s, ok := v[len(v)-1].(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// runners are programs whose first argument says what ran: "npm test", "go test", "git diff".
var runners = map[string]bool{
	"npm": true, "pnpm": true, "yarn": true, "npx": true, "bun": true, "go": true, "cargo": true,
	"make": true, "uv": true, "git": true, "docker": true, "kubectl": true, "gh": true, "pip": true,
	"helm": true, "terraform": true, "poetry": true, "dotnet": true, "mvn": true, "gradle": true,
}

// skipped are commands that only set up the one that matters (cd x && npm test).
var skipped = map[string]bool{"cd": true, "export": true, "set": true, "source": true, ".": true, "true": true}

// commandWords is each command in a shell line as its program, plus a runner's
// subcommand: "cd app && npm test -- --watch | tee log" -> ["npm test", "tee"]. A heredoc's
// body and shell syntax (loops, conditionals) aren't commands.
func commandWords(line string) []string {
	var out []string
	heredoc := ""
	for _, ln := range strings.Split(strings.ReplaceAll(line, `\n`, "\n"), "\n") {
		if heredoc != "" {
			if strings.TrimSpace(ln) == heredoc {
				heredoc = ""
			}
			continue
		}
		if m := heredocStart.FindStringSubmatch(ln); m != nil {
			heredoc = m[1]
		}
		for _, seg := range shellSeparators.Split(ln, -1) {
			if w := commandWord(seg); w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

func commandWord(seg string) string {
	f := strings.Fields(seg)
	for len(f) > 0 && (strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-") || prefixes[f[0]]) {
		f = f[1:]
	}
	if len(f) == 0 {
		return ""
	}
	prog := path.Base(strings.Trim(f[0], `"'()`))
	if skipped[prog] || keywords[prog] || !programName.MatchString(prog) {
		return ""
	}
	if runners[prog] && len(f) > 1 && programName.MatchString(f[1]) && !strings.HasPrefix(f[1], "-") {
		prog += " " + f[1]
	}
	if (prog == "python" || prog == "python3") && len(f) > 2 && f[1] == "-m" {
		prog += " -m " + f[2]
	}
	return prog
}

var (
	heredocStart = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	programName  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]*$`)
	prefixes     = map[string]bool{
		"sudo": true, "env": true, "time": true, "nohup": true, "exec": true, "(": true, "{": true, "!": true,
		"do": true, "then": true, "else": true, // a loop or branch's body: the command follows
	}
	keywords = map[string]bool{
		"for": true, "while": true, "until": true, "done": true, "if": true, "elif": true,
		"fi": true, "case": true, "esac": true, "in": true, "function": true, "return": true,
		"local": true, "let": true, "declare": true, "break": true, "continue": true, "select": true,
	}
)

var shellSeparators = regexp.MustCompile(`&&|\|\||;|\||\n`)

// patchFiles reads an apply_patch body: each *** Add/Update/Delete File and its +/- lines.
func patchFiles(p string) []PatchFile {
	var out []PatchFile
	var cur *PatchFile
	for _, line := range strings.Split(p, "\n") {
		if m := patchHeader.FindStringSubmatch(line); m != nil {
			out = append(out, PatchFile{File: strings.TrimSpace(m[2])})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil || strings.HasPrefix(line, "***") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			cur.Added++
		case strings.HasPrefix(line, "-"):
			cur.Removed++
		}
	}
	return out
}

var patchHeader = regexp.MustCompile(`^\*\*\* (Add|Update|Delete) File: (.+)$`)

// editLines is a Claude Code file edit's added and removed lines (Edit, MultiEdit, Write).
func editLines(file string, in map[string]any) PatchFile {
	lines := func(v any) int {
		s, _ := v.(string)
		if s == "" {
			return 0
		}
		return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
	}
	p := PatchFile{File: file}
	if c, ok := in["content"]; ok { // Write
		p.Added = lines(c)
	}
	p.Removed += lines(in["old_string"])
	p.Added += lines(in["new_string"])
	if edits, ok := in["edits"].([]any); ok { // MultiEdit
		for _, e := range edits {
			if m, ok := e.(map[string]any); ok {
				p.Removed += lines(m["old_string"])
				p.Added += lines(m["new_string"])
			}
		}
	}
	return p
}

// resultsFromRequest reads the tool results a request carries back: Codex's
// *_call_output items and Anthropic's tool_result blocks, by call id.
func resultsFromRequest(raw []byte) map[string]ToolResult {
	var body struct {
		Input    json.RawMessage `json:"input"` // Responses
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"` // Anthropic
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	out := map[string]ToolResult{}
	var items []struct {
		Type    string          `json:"type"`
		CallID  string          `json:"call_id"`
		Output  json.RawMessage `json:"output"`
		ID      string          `json:"tool_use_id"`
		IsError bool            `json:"is_error"`
		Content json.RawMessage `json:"content"`
	}
	if len(body.Input) > 0 && body.Input[0] == '[' {
		_ = json.Unmarshal(body.Input, &items)
		// A Codex request resends the conversation: only its tail of tool calls and their
		// outputs (after the last reasoning or message) answers the previous response.
		i := len(items)
		for i > 0 && (strings.HasSuffix(items[i-1].Type, "_call_output") || strings.HasSuffix(items[i-1].Type, "_call")) {
			i--
		}
		items = items[i:]
	}
	if n := len(body.Messages); n > 0 { // the newest message carries the results of the last response
		_ = json.Unmarshal(body.Messages[n-1].Content, &items)
	}
	for _, it := range items {
		switch {
		case strings.HasSuffix(it.Type, "_call_output") && it.CallID != "":
			out[it.CallID] = ToolResult{Exit: exitCodes(string(it.Output))}
		case it.Type == "tool_result" && it.ID != "":
			out[it.ID] = ToolResult{Exit: exitCodes(string(it.Content)), Error: it.IsError}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// promptFromRequest is what the person said, when the request carries a new message: its
// trailing messages (no tool call or answer after them) from the user. Codex follows the
// prompt with its own context messages (<environment_context>, a developer message), and
// Claude Code adds <system-reminder> blocks: those aren't the prompt, so they're skipped.
// Tool results aren't a prompt either: a request ending in them has none.
func promptFromRequest(raw []byte) string {
	var body struct {
		Input    json.RawMessage   `json:"input"`
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	var items []json.RawMessage
	switch {
	case len(body.Input) > 0 && body.Input[0] == '[':
		_ = json.Unmarshal(body.Input, &items)
	case len(body.Messages) > 0:
		items = body.Messages
	}
	for i := len(items) - 1; i >= 0; i-- {
		var msg struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(items[i], &msg) != nil || (msg.Type != "" && msg.Type != "message") {
			return "" // a tool call or its output: this request continues a turn
		}
		switch msg.Role {
		case "developer", "system":
			continue
		case "user":
			if text, results := userText(msg.Content); results {
				return ""
			} else if text != "" {
				return truncateRunes(text, promptChars)
			}
		default:
			return "" // the model's answer comes after: no new message
		}
	}
	return ""
}

// contextBlock is a block the agent adds to a user message, not something the person (or an
// agent messaging it) said: Codex's environment and instructions, Claude Code's reminders.
var contextBlock = regexp.MustCompile(`^<(environment_context|user_instructions|developer_instructions|permissions|` +
	`system-reminder|turn_aborted|local-command-stdout|local-command-stderr|INSTRUCTIONS)\b`)

// userText is a user message's own words (context blocks left out), and whether it carries
// tool results instead.
func userText(content json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		if contextBlock.MatchString(strings.TrimSpace(text)) {
			return "", false
		}
		return strings.TrimSpace(text), false
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &parts)
	var b []string
	for _, p := range parts {
		switch {
		case p.Type == "tool_result":
			return "", true
		case (p.Type == "input_text" || p.Type == "text") && !contextBlock.MatchString(strings.TrimSpace(p.Text)):
			b = append(b, p.Text)
		}
	}
	return strings.TrimSpace(strings.Join(b, "\n")), false
}

// exitCodes finds the exit codes a tool output reports: Codex's structured
// "exit_code": N, or the text forms "Process exited with code N" / "Exit code N".
func exitCodes(s string) []int {
	var out []int
	for _, m := range exitCode.FindAllStringSubmatch(s, -1) {
		if n, err := strconv.Atoi(firstOf(m[1], m[2])); err == nil {
			out = append(out, n)
		}
	}
	return out
}

var exitCode = regexp.MustCompile(`(?i)\\*"?exit_code\\*"?\s*[:=]\s*(-?\d+)|(?:exited with code|exit code:?)\s+(-?\d+)`)

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
