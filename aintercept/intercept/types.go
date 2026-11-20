// Package intercept is the library for writing an AIntercept Interceptor
// (docs/BUILDING_AN_INTERCEPTOR_V2.md). It speaks contract v2
// (interceptor/v2) for you: every message of every call, in both directions,
// in one shape for Claude Code, Codex and the OpenAI APIs.
//
// Rules cover the common case:
//
//	intercept.Run(
//		intercept.OnRequest(intercept.Contains("magic word")).Reply("No magic here."),
//		intercept.OnResponse(intercept.Contains("swordfish")).Replace("[password]"),
//	)
//
// Hooks give full control, and watches find a phrase in the model's reply
// text even when it's split across streamed pieces. Rules and hooks combine;
// rules run first, in order. The raw protocol (interceptor/v2) stays usable
// directly.
package intercept

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// Mode is how the call is intercepted.
type Mode int

const (
	// Observe: answers are recorded, never applied.
	Observe Mode = iota + 1
	// Enforce: answers are applied before anything goes on.
	Enforce
)

// Direction is which way a message goes.
type Direction int

const (
	// ToModel: the request (prompts, tool results, media).
	ToModel Direction = iota + 1
	// FromModel: the reply (its text, tool calls, opaque parts).
	FromModel
)

// Kind is what a message is.
type Kind int

const (
	KindPrompt     Kind = iota + 1 // the user's text, to the model
	KindToolResult                 // a tool's result, to the model
	KindText                       // the model's reply text: one streamed piece
	KindToolCall                   // the model asks to run a tool
	KindMedia                      // an image or a file, to the model
	KindOpaque                     // reasoning, encrypted content: present, never readable or changeable
	// KindHarness is the harness's own text going to the model, not the
	// user's words: system and developer messages in the conversation (Claude
	// Code's notes when tools are added or a file changes), and the notes it
	// writes into the user's turn (a system reminder, an interrupted request,
	// Codex's environment). It can be replaced like any message.
	KindHarness
)

func (k Kind) String() string {
	return [...]string{"", "prompt", "tool result", "text", "tool call", "media", "opaque", "harness"}[k]
}

// Agent is the client making the call.
type Agent string

const (
	ClaudeCode Agent = "claude_code"
	Codex      Agent = "codex"
)

// Transport is how the call arrived.
type Transport int

const (
	HTTP Transport = iota + 1
	WebSocket
)

// Person is who made the call. Verified is true when the provider confirmed
// Email, Name and Account for the call's credential: grant privileges on a
// verified Person only. Otherwise they come from the token's claims or from
// what the client reports, which it can set to anything.
type Person struct {
	Email, Name string
	// Account is the provider account: the verified one, else the one the
	// client reports, else the organization.
	Account  string
	Verified bool
}

// Option is a provider setting (docs/INTERCEPTOR.md, Options).
type Option = pb.Option

const (
	// Effort: how hard the model reasons (low, medium, high, …).
	Effort = pb.Option_OPTION_EFFORT
	// Speed: Anthropic's fast mode ("fast"; absent: standard).
	Speed = pb.Option_OPTION_SPEED
	// ServiceTier: the priority tier (Anthropic's and OpenAI's service_tier).
	ServiceTier = pb.Option_OPTION_SERVICE_TIER
	// Thinking: Anthropic's thinking mode (enabled, adaptive, disabled).
	Thinking = pb.Option_OPTION_THINKING
)

// Options are a call's settings, as the provider sends them. A setting the
// request doesn't make isn't there.
type Options map[Option]string

// Get is the option's value, "" when the call doesn't set it.
func (o Options) Get(k Option) string { return o[k] }

func optionsOf(vs []*pb.OptionValue) Options {
	if len(vs) == 0 {
		return nil
	}
	o := Options{}
	for _, v := range vs {
		o[v.GetKey()] = v.GetValue()
	}
	return o
}

// Provider is who serves the call, and API which of its APIs it's on (how
// its options' values read).
type (
	Provider = pb.Provider
	API      = pb.Api
)

// Call is one intercepted call: the same object in every hook of the call.
// A Codex WebSocket connection is one call for all its turns.
type Call struct {
	ID        string
	Mode      Mode
	Person    Person
	Agent     Agent
	Model     string
	Session   string
	Transport Transport
	Provider  Provider
	API       API
	// Options are what the request asks for (OnCallStart). A WebSocket's
	// upgrade has none: each turn's are its Turn's.
	Options Options

	mu    sync.Mutex
	state map[string]any
	seen  time.Time
}

// Set keeps a value for the rest of the call (freed after OnCallComplete).
func (c *Call) Set(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil {
		c.state = map[string]any{}
	}
	c.state[key] = v
}

// Get returns a value Set earlier in the call, or nil.
func (c *Call) Get(key string) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state[key]
}

// Message is one message of the call, in either direction: one part of a
// conversation turn (a prompt, a tool result, an image), one piece of the
// model's reply text, or one tool call.
type Message struct {
	Direction Direction
	Kind      Kind
	// Text is the content: the prompt (the user's own words, without the
	// harness's wrapper around a message sent mid-turn), the tool result, the
	// harness's note, the reply text piece; a tool call's arguments as JSON. Empty for media and opaque parts, and
	// past the preview when Truncated.
	Text string
	// Tool is a tool call's name, or the name of the tool a result answers.
	Tool string
	Call *Call
	// Newest marks the newest prompt: what the user just asked.
	Newest bool
	// Truncated: the content is over the inline limit and Text is its start
	// (answer NeedFull for all of it). Size is the whole content's size.
	Truncated bool
	Size      int
	// Reply text: Before is the block's text already released to the client,
	// and Final marks the block's last piece.
	Before string
	Final  bool
	// ID is the message's id: stable across the calls that resend it.
	ID      string
	IsError bool // a tool result marked an error
}

// Changeable reports whether an answer can change the message: enforce mode,
// and not media or opaque.
func (m *Message) Changeable() bool {
	return m.Call != nil && m.Call.Mode == Enforce && m.Kind != KindMedia && m.Kind != KindOpaque
}

// Contains reports whether the text contains s, ignoring case.
func (m *Message) Contains(s string) bool {
	return strings.Contains(strings.ToLower(m.Text), strings.ToLower(s))
}

// Matches reports whether the text matches re.
func (m *Message) Matches(re *regexp.Regexp) bool { return re.MatchString(m.Text) }

// IsPrompt reports whether the message is a prompt.
func (m *Message) IsPrompt() bool { return m.Kind == KindPrompt }

// IsToolCall reports whether the message is a call of the tool name (any
// tool for "").
func (m *Message) IsToolCall(name string) bool {
	return m.Kind == KindToolCall && (name == "" || m.Tool == name)
}

// Command is a tool call's shell command, or "".
func (m *Message) Command() string { return command(m.Text) }

// Path is a tool call's file path, or "".
func (m *Message) Path() string { return path(m.Text) }

// ReplaceAll answers Replace with re's matches replaced by repl (in a tool
// call, inside its arguments' string values), or Accept when nothing
// matches.
func (m *Message) ReplaceAll(re *regexp.Regexp, repl string) Answer {
	if m.Kind == KindToolCall {
		out := mapStrings(m.Text, func(s string) string { return re.ReplaceAllString(s, repl) })
		if out == m.Text {
			return Accept()
		}
		return ReplaceArgs(out)
	}
	out := re.ReplaceAllString(m.Text, repl)
	if out == m.Text {
		return Accept()
	}
	return Replace(out)
}

// ToolCall is the model asking to run a tool: a tool call request, in the
// model's response, before the agent sees it.
type ToolCall struct {
	// Name is the tool's name as the model called it (an MCP tool's whole
	// name, mcp__<server>__<tool>); Server is the MCP server, if any.
	Name, Server string
	// Args is the arguments decoded (a free-form tool's input is "input").
	Args    map[string]any
	Message *Message
	Call    *Call
}

// Arg is the argument name as text: a string as it is, anything else as
// JSON; "" when absent.
func (t *ToolCall) Arg(name string) string {
	v, ok := t.Args[name]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Command is the call's shell command, or "".
func (t *ToolCall) Command() string { return command(t.Message.Text) }

// Path is the call's file path, or "".
func (t *ToolCall) Path() string { return path(t.Message.Text) }

// ToolResult is a tool's result going back to the model: a tool call
// response, in the next request.
type ToolResult struct {
	// Name is the tool it answers (by call id; "" when the request doesn't
	// carry the call).
	Name    string
	Text    string
	IsError bool
	// ExitCode is the command's exit code, when the result says (nil
	// otherwise).
	ExitCode *int
	Message  *Message
	Call     *Call
}

// Turn is one request on a WebSocket (Codex's response.create): OnTurnStart.
type Turn struct {
	// N is its number on the connection: 1, 2, …
	N       int
	Model   string
	Options Options
	Call    *Call
}

// Usage is a call's token counts.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int64
}

// Decision is one answer recorded on a call.
type Decision struct {
	// Callback: "call_start", "turn_start", "request", "reply" or
	// "tool_call".
	Callback string
	// Action: "accept" (with notes), "replace", "respond", "hold", "refuse"
	// (an Error), or "error" (it failed).
	Action string
	// Applied: enforce mode, and it took effect.
	Applied     bool
	MessageID   string
	Reason      string
	Notes       map[string]string
	RespondMode string
}

// Record is a finished call (or a WebSocket turn).
type Record struct {
	Call       *Call
	Usage      Usage
	Status     int
	Model      string
	StopReason string
	// Asked are the settings the request asked for, Served the ones the
	// provider reports serving it with (the tier, the speed).
	Asked, Served Options
	TTFB          time.Duration
	Duration      time.Duration
	// Answers is every decision recorded in the call, per message.
	Answers []Decision
	// Replied is how a Reply was delivered: "synthesized" (the call never
	// went upstream, or the reply was ended with the text), "rewritten" (the
	// model was asked for exactly the text), or "".
	Replied string
	// WSTurn is the WebSocket turn (1, 2, …), or 0 for an HTTP call or the
	// connection's last record.
	WSTurn int
}

// ── Tool arguments ───────────────────────────────────────────────────────────

func decodeArgs(args string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		return m
	}
	var s string
	if json.Unmarshal([]byte(args), &s) == nil {
		return map[string]any{"input": s}
	}
	return map[string]any{}
}

// command reads a shell command from tool arguments: "command" as a string,
// or as an argv (the script of `bash -lc <script>`); a free-form input.
func command(args string) string {
	m := decodeArgs(args)
	switch c := m["command"].(type) {
	case string:
		return c
	case []any:
		var argv []string
		for _, a := range c {
			s, _ := a.(string)
			argv = append(argv, s)
		}
		if n := len(argv); n >= 3 && (argv[n-2] == "-lc" || argv[n-2] == "-c") {
			return argv[n-1]
		}
		return strings.Join(argv, " ")
	}
	if s, ok := m["cmd"].(string); ok {
		return s
	}
	s, _ := m["input"].(string)
	return s
}

func path(args string) string {
	m := decodeArgs(args)
	for _, k := range []string{"file_path", "path", "notebook_path"} {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

// mapStrings applies f to every string value in JSON (keys untouched); JSON
// that doesn't parse, or that f doesn't change, is returned as it is.
func mapStrings(js string, f func(string) string) string {
	var v any
	if json.Unmarshal([]byte(js), &v) != nil {
		return js
	}
	changed := false
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			s := f(x)
			changed = changed || s != x
			return s
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
		}
		return v
	}
	out := walk(v)
	if !changed {
		return js
	}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if enc.Encode(out) != nil {
		return js
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
