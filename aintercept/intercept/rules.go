package intercept

import (
	"context"
	"regexp"
	"strings"
)

// ── Answers ──────────────────────────────────────────────────────────────────

// Answer is what a hook, rule or watch decides about a message. nil is
// Accept.
type Answer interface {
	// Note records key=value with the decision (on AIntercept's verdict row).
	Note(key, value string) Answer
	get() *answer
}

type verb int

const (
	vAccept verb = iota
	vReplace
	vReplaceArgs
	vReply
	vHold
	vNeedFull
	vFail
)

func (v verb) String() string {
	return [...]string{"accept", "replace", "replace", "reply", "hold", "need full", "fail"}[v]
}

type answer struct {
	verb  verb
	text  string
	err   error
	notes [][2]string
}

func (a *answer) Note(key, value string) Answer {
	a.notes = append(a.notes, [2]string{key, value})
	return a
}

func (a *answer) get() *answer { return a }

func get(a Answer) *answer {
	if a == nil {
		return &answer{}
	}
	if x := a.get(); x != nil {
		return x
	}
	return &answer{}
}

// Accept lets the message go on unchanged.
func Accept() Answer { return &answer{} }

// Replace puts text in the message's place: a prompt's, a tool result's,
// a reply text piece's. In a watch's Match, only the match's.
func Replace(text string) Answer { return &answer{verb: vReplace, text: text} }

// ReplaceArgs gives a tool call new arguments (JSON).
func ReplaceArgs(json string) Answer { return &answer{verb: vReplaceArgs, text: json} }

// Reply answers for the model: the client gets text as the model's reply.
// On the request, the call never reaches the model (or, when the provider
// keeps the conversation, the model is asked for exactly the text); in the
// reply, it ends there with the text.
func Reply(text string) Answer { return &answer{verb: vReply, text: text} }

// Hold (reply text only) releases nothing yet: the piece comes back with
// what arrives next.
func Hold() Answer { return &answer{verb: vHold} }

// NeedFull (a Truncated message) asks for the call again with the whole
// content.
func NeedFull() Answer { return &answer{verb: vNeedFull} }

// Fail is an error: the observer's onError applies (refuse the call, or let
// it through).
func Fail(err error) Answer { return &answer{verb: vFail, err: err} }

// ── Matchers ─────────────────────────────────────────────────────────────────

// Matcher decides whether a rule applies to a message.
type Matcher interface {
	Match(m *Message) bool
}

// pattern is a Matcher on the text, which can also find the matches (for
// Replace) and become a watch.
type pattern struct {
	re     *regexp.Regexp
	phrase string // Contains: the phrase
}

func (p *pattern) Match(m *Message) bool { return p.re.MatchString(m.Text) }

// Contains matches text containing s, ignoring case.
func Contains(s string) Matcher {
	return &pattern{re: regexp.MustCompile("(?i)" + regexp.QuoteMeta(s)), phrase: s}
}

// Matches matches text matching re. On reply text, a match can be up to
// DefaultMaxLen bytes long (use a Watch for longer).
func Matches(re *regexp.Regexp) Matcher { return &pattern{re: re} }

// DefaultMaxLen bounds a Matches rule's match in reply text: the text held
// back while a match could still be completing.
const DefaultMaxLen = 256

type argMatcher struct {
	name string
	m    Matcher
}

func (a argMatcher) Match(m *Message) bool {
	if m.Kind != KindToolCall {
		return false
	}
	v := (&ToolCall{Args: decodeArgs(m.Text)}).Arg(a.name)
	return a.m.Match(&Message{Kind: m.Kind, Direction: m.Direction, Text: v, Tool: m.Tool, Call: m.Call})
}

// Arg matches a tool call whose argument name matches m.
func Arg(name string, m Matcher) Matcher { return argMatcher{name, m} }

type toolMatcher string

func (t toolMatcher) Match(m *Message) bool {
	return (m.Kind == KindToolCall || m.Kind == KindToolResult) && m.Tool == string(t)
}

// Tool matches a call of the tool name, or its result.
func Tool(name string) Matcher { return toolMatcher(name) }

type promptMatcher struct{ m Matcher }

func (p promptMatcher) Match(m *Message) bool {
	return m.Kind == KindPrompt && (p.m == nil || p.m.Match(m))
}

// Prompt matches the user's prompts that m matches (nil: every prompt): not
// tool results (a file the agent read, a command's output), media, or
// anything from the model.
func Prompt(m Matcher) Matcher { return promptMatcher{m} }

type allOf []Matcher

func (ms allOf) Match(m *Message) bool {
	for _, x := range ms {
		if !x.Match(m) {
			return false
		}
	}
	return true
}

// And matches when every one of ms does.
func And(ms ...Matcher) Matcher { return allOf(ms) }

type anyOf []Matcher

func (ms anyOf) Match(m *Message) bool {
	for _, x := range ms {
		if x.Match(m) {
			return true
		}
	}
	return false
}

// Or matches when any one of ms does.
func Or(ms ...Matcher) Matcher { return anyOf(ms) }

type not struct{ m Matcher }

func (n not) Match(m *Message) bool { return !n.m.Match(m) }

// Not matches when m doesn't.
func Not(m Matcher) Matcher { return not{m} }

// ── Rules ────────────────────────────────────────────────────────────────────

type target int

const (
	onRequest          target = iota // to the model
	onMessage                        // everything
	onResponse                       // from the model
	onToolCallRequest                // a tool call
	onToolCallResponse               // a tool result
)

// When is a rule's condition; Reply, Replace or Do make it a Rule.
type When struct {
	on   target
	tool string
	m    Matcher
}

// Rule is a condition and its answer. Rules run first, in order.
type Rule struct {
	When
	do func(m *Message) Answer
}

func (Rule) item() {}

// OnRequest: messages to the model (prompts, tool results, media) matching m.
func OnRequest(m Matcher) When { return When{on: onRequest, m: m} }

// OnMessage: messages either way matching m.
func OnMessage(m Matcher) When { return When{on: onMessage, m: m} }

// OnResponse: the model's reply text and tool calls matching m. On reply
// text, a Contains or Matches rule becomes a watch: a match split across
// streamed pieces is caught.
func OnResponse(m Matcher) When { return When{on: onResponse, m: m} }

// OnToolCallRequest: the model asking to run tool (any tool for ""), matching
// m (nil: every call). A tool call request is in the model's response.
func OnToolCallRequest(tool string, m Matcher) When {
	return When{on: onToolCallRequest, tool: tool, m: m}
}

// OnToolCallResponse: the result of tool (any tool for "") going back to the
// model, matching m (nil: every result). A tool call response is in the next
// request.
func OnToolCallResponse(tool string, m Matcher) When {
	return When{on: onToolCallResponse, tool: tool, m: m}
}

// Reply answers the matching message with Reply(text).
func (w When) Reply(text string) Rule {
	return Rule{When: w, do: func(*Message) Answer { return Reply(text) }}
}

// Replace replaces what matched with text: the matches of a Contains or
// Matches (inside a tool call's argument values), or else the whole
// message's text.
func (w When) Replace(text string) Rule {
	return Rule{When: w, do: func(m *Message) Answer {
		p := patternOf(w.m)
		if p == nil {
			if m.Kind == KindToolCall {
				return ReplaceArgs(text)
			}
			return Replace(text)
		}
		repl := strings.ReplaceAll(text, "$", "$$")
		return m.ReplaceAll(p.re, repl)
	}}
}

// Do answers the matching message with fn.
func (w When) Do(fn func(m *Message) Answer) Rule { return Rule{When: w, do: fn} }

// patternOf is the pattern a Replace replaces: m's, or an Arg's.
func patternOf(m Matcher) *pattern {
	switch x := m.(type) {
	case *pattern:
		return x
	case argMatcher:
		return patternOf(x.m)
	case promptMatcher:
		return patternOf(x.m)
	}
	return nil
}

// applies reports whether the rule's target covers the message.
func (r Rule) applies(m *Message) bool {
	switch r.on {
	case onRequest:
		return m.Direction == ToModel
	case onResponse:
		return m.Direction == FromModel
	case onToolCallRequest:
		return m.Kind == KindToolCall && (r.tool == "" || m.Tool == r.tool)
	case onToolCallResponse:
		return m.Kind == KindToolResult && (r.tool == "" || m.Tool == r.tool)
	}
	return true
}

// watch is the rule as a watch on reply text, when it is one.
func (r Rule) watch() (Watch, bool) {
	p, ok := r.m.(*pattern)
	if !ok || r.on != onResponse && r.on != onMessage {
		return Watch{}, false
	}
	w := Watch{Phrase: p.phrase, In: FromModel}
	if p.phrase == "" {
		w.Pattern, w.MaxLen = p.re, DefaultMaxLen
	}
	w.On = func(_ context.Context, w *Match) Answer {
		a := get(r.do(&Message{Kind: KindText, Direction: FromModel, Text: w.Text, Call: w.Call}))
		switch a.verb {
		case vReplace:
			// The rule's Replace on the match alone: the replacement.
			return &answer{verb: vReplace, text: a.text, notes: a.notes}
		}
		return a
	}
	return w, true
}
