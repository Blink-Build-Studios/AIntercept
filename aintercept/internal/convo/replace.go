package convo

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// IDs are the messages' ids, stable within a session: the same message has
// the same id on every call that resends it.
//
// Each id hashes the message's role and content (not its bytes: clients move
// cache markers between calls), chained to the id before it; the first
// message's chains to seed (the session) and, for a chained request, Prev.
// So the same words twice are two ids, and a message's id names its place in
// the conversation, which an index alone can't: a chained request's indexes
// restart at 0, and a compacted conversation shifts them.
func (r Request) IDs(seed string) []string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field(seed)
	field(r.Prev)
	prev := h.Sum(nil)
	out := make([]string, len(r.Messages))
	for i, m := range r.Messages {
		h.Reset()
		h.Write(prev)
		field(string(m.Role))
		for _, p := range m.Parts {
			field(string(p.Kind))
			field(p.MediaType)
			field(p.URL)
			field(p.CallID)
			field(p.Name)
			if p.IsError {
				field("error")
			}
			if p.Kind == KindOpaque {
				field(string(r.Body[p.Span.Start:p.Span.End]))
			} else {
				field(p.content)
			}
		}
		prev = h.Sum(nil)
		out[i] = hex.EncodeToString(prev[:16])
	}
	return out
}

// Edit is new content for one part: a text part's text, a tool result's
// text, or a tool call's arguments (JSON).
type Edit struct {
	Message int // index into Request.Messages
	Part    int // index into its Parts
	Content string
}

// Apply returns the body with the edits in place, in the request's own
// format. Only the edited values change: every other byte, inside the
// message or out, is as sent. A tool result's text, when the result holds
// several text blocks, becomes the first of them (the others are removed;
// media stays). Media, opaque parts and server tools' structured results
// can't be edited.
func (r Request) Apply(edits []Edit) ([]byte, error) {
	type splice struct {
		span Span
		with []byte
	}
	var ss []splice
	for _, e := range edits {
		if e.Message < 0 || e.Message >= len(r.Messages) {
			return nil, fmt.Errorf("no message %d", e.Message)
		}
		m := r.Messages[e.Message]
		if e.Part < 0 || e.Part >= len(m.Parts) {
			return nil, fmt.Errorf("message %d: no part %d", e.Message, e.Part)
		}
		span, with, err := r.rewrite(m, m.Parts[e.Part], e.Content)
		if err != nil {
			return nil, fmt.Errorf("message %d part %d: %w", e.Message, e.Part, err)
		}
		ss = append(ss, splice{span, with})
	}
	slices.SortFunc(ss, func(a, b splice) int { return a.span.Start - b.span.Start })
	out := make([]byte, 0, len(r.Body))
	at := 0
	for _, s := range ss {
		if s.span.Start < at {
			return nil, errors.New("two edits to the same part")
		}
		out = append(append(out, r.Body[at:s.span.Start]...), s.with...)
		at = s.span.End
	}
	out = append(out, r.Body[at:]...)
	if !json.Valid(out) {
		return nil, errors.New("the edited request isn't valid JSON")
	}
	return out, nil
}

// rewrite is the bytes to put where, for one part's new content.
func (r Request) rewrite(m Message, p Part, s string) (Span, []byte, error) {
	v := r.at(p.Span)
	switch p.Kind {
	case KindText:
		if v.kind() == '"' { // the content is a plain string
			return v.span, jsonString(s), nil
		}
		mem, _ := members(v)
		for _, k := range []string{"text", "refusal"} {
			if t := mem[k]; t.kind() == '"' {
				return t.span, jsonString(s), nil
			}
		}
		return Span{}, nil, errors.New("no text to replace")

	case KindToolCall:
		if !json.Valid([]byte(s)) {
			return Span{}, nil, errors.New("tool call arguments must be JSON")
		}
		mem, _ := members(v)
		var typ string
		if t, ok := mem["type"].str(); ok {
			typ = t
		}
		switch {
		case r.Format == FormatAnthropic, typ == "custom_tool_call": // the input, as JSON
			return mem["input"].span, []byte(s), nil
		case r.Format == FormatChat && mem["function"].kind() == '{':
			fn, _ := members(mem["function"])
			if fn["arguments"].kind() == '"' {
				return fn["arguments"].span, jsonString(s), nil
			}
		case typ == "function_call", typ == "mcp_call": // the arguments, as a JSON string
			if mem["arguments"].kind() == '"' {
				return mem["arguments"].span, jsonString(s), nil
			}
		default: // the item is the arguments (a built-in tool's call, a custom Chat call)
			return v.span, []byte(s), nil
		}
		return Span{}, nil, errors.New("no arguments to replace")

	case KindToolResult:
		mem, _ := members(v)
		typ, _ := mem["type"].str()
		key := "content"
		switch {
		case r.Format == FormatResponses:
			key = "output"
		case r.Format == FormatAnthropic && typ != "tool_result":
			return Span{}, nil, errors.New("a server tool's result is structured; it can't be replaced")
		}
		return r.rewriteResult(m, mem[key], s)
	}
	return Span{}, nil, fmt.Errorf("a %s part can't be replaced", p.Kind)
}

// rewriteResult replaces a tool result's content: a string (or null) becomes
// the new text; a list keeps its media and gets the new text in its first
// text block, its other text blocks removed.
func (r Request) rewriteResult(m Message, c value, s string) (Span, []byte, error) {
	switch c.kind() {
	case '"', 'n':
		return c.span, jsonString(s), nil
	case '[':
	default:
		return Span{}, nil, errors.New("no content to replace")
	}
	media := map[Span]bool{}
	for _, p := range m.Parts {
		if p.Kind == KindMedia {
			media[p.Span] = true
		}
	}
	els, _ := elements(c)
	var kept [][]byte
	text := false
	for _, e := range els {
		if media[e.span] {
			kept = append(kept, e.raw)
			continue
		}
		mem, _ := members(e)
		typ, _ := mem["type"].str()
		if typ != "text" && typ != "input_text" && typ != "output_text" || mem["text"].kind() != '"' {
			return Span{}, nil, fmt.Errorf("the result holds a %q block, which can't be rewritten as text", typ)
		}
		if text {
			continue
		}
		text = true
		t := mem["text"].span
		el := append(append(append([]byte{}, r.Body[e.span.Start:t.Start]...), jsonString(s)...), r.Body[t.End:e.span.End]...)
		kept = append(kept, el)
	}
	if !text {
		return Span{}, nil, errors.New("the result has no text to replace")
	}
	return c.span, append(append([]byte{'['}, bytes.Join(kept, []byte{','})...), ']'), nil
}

// at is the JSON value at span in the body.
func (r Request) at(s Span) value {
	return value{raw: r.Body[s.Start:s.End], span: s}
}

// jsonString is s as a JSON string, without HTML escaping.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}
