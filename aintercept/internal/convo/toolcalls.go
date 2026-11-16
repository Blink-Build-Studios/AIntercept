package convo

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Tool calls held whole (docs/INTERCEPTOR_V2.md, "Callbacks"): a model's
// reply streams to the client, its text untouched, but each tool call is held
// from its first event until it's complete, then decided: released as it was,
// with new arguments, or replaced (with the rest of the reply) by text.
//
// Stream does this event by event for Anthropic Messages (tool_use blocks)
// and the Responses API (function_call and custom_tool_call items, over SSE
// or Codex's WebSocket); DecideBody for a whole JSON reply.

// ToolCall is one complete tool call in a reply.
type ToolCall struct {
	ID   string // the provider's call id (toolu_…, call_…)
	Name string
	// Arguments as JSON; a custom tool's free-form input as a JSON string.
	Arguments string
	Custom    bool // a Responses custom_tool_call
}

// Verdict is what to do with a held tool call.
type Verdict int

const (
	Accept  Verdict = iota // release it as it was
	Replace                // release it with Answer.Arguments (JSON; a JSON string for a custom tool)
	Respond                // end the reply here with Answer.Text instead
	Stop                   // end the reply here with nothing more (the caller ends it with an error)
)

// Answer is the decision on one tool call.
type Answer struct {
	Verdict   Verdict
	Arguments string
	Text      string
}

// holdCap bounds the bytes held for one tool call; a larger one goes on
// undecided.
var holdCap = 32 << 20

// Stream follows one streamed reply.
type Stream struct {
	format string
	frame  func(name string, data []byte) []byte
	decide func(ToolCall) Answer

	open  bool   // a tool call is held
	held  []held // its events (and any that came while it was open)
	heldN int
	index int // its content block index (Anthropic) or output index (Responses)
	call  ToolCall
	start []byte // Anthropic: the block's input as it started
	args  strings.Builder

	ended  bool
	unheld int

	// Responses: the response object (from response.created), the output
	// items released so far, and the replaced ones by id, for the completed
	// response; the last sequence number.
	resp     []byte
	items    [][]byte
	replaced map[string][]byte
	seq      int64
}

type held struct {
	typ        string
	index      int
	name       string
	data, wire []byte
}

// NewStream follows a reply in format (FormatAnthropic or FormatResponses;
// any other passes through). frame encodes an event the Stream makes (its
// SSE event name and JSON data) as the transport carries it; decide is asked
// about each complete tool call.
func NewStream(format string, frame func(name string, data []byte) []byte, decide func(ToolCall) Answer) *Stream {
	return &Stream{format: format, frame: frame, decide: decide, replaced: map[string][]byte{}}
}

// Feed takes the reply's next event, its SSE event name (or ""), its JSON
// data, and its bytes as received, and returns what goes to the client now.
func (s *Stream) Feed(name string, data, raw []byte) []byte {
	if s.ended {
		return nil
	}
	switch s.format {
	case FormatAnthropic:
		return s.anthropic(name, data, raw)
	case FormatResponses:
		return s.responses(name, data, raw)
	}
	return raw
}

// Ended reports that a Respond or Stop ended the reply: nothing more of the
// upstream's goes out.
func (s *Stream) Ended() bool { return s.ended }

// Unheld is how many tool calls went out undecided, over the hold cap.
func (s *Stream) Unheld() int { return s.unheld }

// hold keeps an event of the open call. A reply that fails before the call
// is complete, or a call past the cap, releases what's held, undecided.
func (s *Stream) hold(typ string, index int, name string, data, raw []byte) []byte {
	s.held = append(s.held, held{typ: typ, index: index, name: name, data: clone(data), wire: clone(raw)})
	s.heldN += len(raw)
	switch {
	case typ == "error", typ == "response.failed", typ == "response.incomplete":
		return s.release()
	case s.heldN > holdCap:
		s.unheld++
		return s.release()
	}
	return nil
}

// Flush releases what's held, undecided: the reply ended mid-call.
func (s *Stream) Flush() []byte {
	if s.ended {
		return nil
	}
	return s.release()
}

// release ends the hold, returning the held events as received.
func (s *Stream) release() []byte {
	var out []byte
	for _, h := range s.held {
		out = append(out, h.wire...)
	}
	s.reset()
	return out
}

func (s *Stream) reset() {
	s.open, s.held, s.heldN, s.start = false, nil, 0, nil
	s.args.Reset()
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// ── Anthropic: tool_use content blocks ──────────────────────────────────────

func (s *Stream) anthropic(name string, data, raw []byte) []byte {
	var ev struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock *struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta *struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	_ = json.Unmarshal(data, &ev)
	if !s.open {
		if b := ev.ContentBlock; ev.Type != "content_block_start" || b == nil || b.Type != "tool_use" {
			return raw
		}
		s.open, s.index = true, ev.Index
		s.call = ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
		s.start = clone(ev.ContentBlock.Input)
	}
	if out := s.hold(ev.Type, ev.Index, name, data, raw); !s.open {
		return out
	}
	switch {
	case ev.Index != s.index:
	case ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "input_json_delta":
		s.args.WriteString(ev.Delta.PartialJSON)
	case ev.Type == "content_block_stop":
		return s.decideAnthropic()
	}
	return nil
}

func (s *Stream) decideAnthropic() []byte {
	tc := s.call
	tc.Arguments = s.args.String()
	if strings.TrimSpace(tc.Arguments) == "" {
		tc.Arguments = string(s.start)
	}
	a := s.decide(tc)
	switch a.Verdict {
	case Replace:
		var out []byte
		for _, h := range s.held {
			switch {
			case h.index != s.index:
				out = append(out, h.wire...)
			case h.typ == "content_block_delta":
			case h.typ == "content_block_stop":
				d := obj("type", "content_block_delta", "index", s.index, "delta", raw(obj("type", "input_json_delta", "partial_json", a.Arguments)))
				out = append(append(out, s.frame("content_block_delta", d)...), h.wire...)
			default:
				out = append(out, h.wire...)
			}
		}
		s.reset()
		return out
	case Respond:
		var out []byte
		for _, ev := range anthropicText(s.index, a.Text) {
			out = append(out, s.frame(eventType(ev), ev)...)
		}
		s.reset()
		s.ended = true
		return out
	case Stop:
		s.reset()
		s.ended = true
		return nil
	}
	return s.release()
}

// ── Responses: function_call and custom_tool_call items ─────────────────────

type responsesItemFields struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     json.RawMessage `json:"input"`
}

func (s *Stream) responses(name string, data, raw []byte) []byte {
	var ev struct {
		Type           string          `json:"type"`
		SequenceNumber *int64          `json:"sequence_number"`
		OutputIndex    int             `json:"output_index"`
		Item           json.RawMessage `json:"item"`
		Response       json.RawMessage `json:"response"`
	}
	_ = json.Unmarshal(data, &ev)
	if ev.SequenceNumber != nil {
		s.seq = *ev.SequenceNumber
	}
	if !s.open {
		switch ev.Type {
		case "response.created", "response.in_progress":
			s.resp = clone(ev.Response)
		case "response.output_item.added":
			var it responsesItemFields
			if json.Unmarshal(ev.Item, &it) != nil || it.Type != "function_call" && it.Type != "custom_tool_call" {
				return raw
			}
			s.open, s.index = true, ev.OutputIndex
			return s.hold(ev.Type, ev.OutputIndex, name, data, raw)
		case "response.output_item.done":
			s.items = append(s.items, clone(ev.Item))
		case "response.completed":
			if len(s.replaced) > 0 {
				return s.frame(name, patchOutput(data, s.replaced))
			}
		}
		return raw
	}
	if out := s.hold(ev.Type, ev.OutputIndex, name, data, raw); !s.open {
		return out
	}
	if ev.Type == "response.output_item.done" && ev.OutputIndex == s.index {
		return s.decideResponses(clone(ev.Item))
	}
	return nil
}

func (s *Stream) decideResponses(item []byte) []byte {
	var it responsesItemFields
	_ = json.Unmarshal(item, &it)
	tc := ToolCall{ID: firstOf(it.CallID, it.ID), Name: it.Name, Arguments: it.Arguments}
	if it.Type == "custom_tool_call" {
		tc.Arguments, tc.Custom = string(it.Input), true
	}
	a := s.decide(tc)
	switch a.Verdict {
	case Replace:
		newItem, delta := set(item, "arguments", jsonString(a.Arguments)), a.Arguments
		if tc.Custom {
			newItem = set(item, "input", []byte(a.Arguments))
			_ = json.Unmarshal([]byte(a.Arguments), &delta)
		}
		var out []byte
		sentDelta := false
		for _, h := range s.held {
			switch {
			case h.index != s.index:
				out = append(out, h.wire...)
			case h.typ == "response.function_call_arguments.delta", h.typ == "response.custom_tool_call_input.delta":
				if !sentDelta {
					out = append(out, s.frame(h.name, set(h.data, "delta", jsonString(delta)))...)
					sentDelta = true
				}
			case h.typ == "response.function_call_arguments.done":
				out = append(out, s.frame(h.name, set(h.data, "arguments", jsonString(a.Arguments)))...)
			case h.typ == "response.custom_tool_call_input.done":
				out = append(out, s.frame(h.name, set(h.data, "input", []byte(a.Arguments)))...)
			case h.typ == "response.output_item.done":
				out = append(out, s.frame(h.name, set(h.data, "item", newItem))...)
			default:
				out = append(out, h.wire...)
			}
		}
		s.items = append(s.items, newItem)
		s.replaced[it.ID] = newItem
		s.reset()
		return out
	case Respond:
		evs, msg, seq := responsesMessage(s.index, s.seq+1, a.Text)
		resp := s.resp
		if resp == nil {
			resp = responseObject(openaiID("resp"), "", "in_progress", nil, nil)
		}
		resp = set(set(resp, "status", jsonString("completed")), "output", arr(append(slices.Clone(s.items), msg)...))
		evs = append(evs, obj("type", "response.completed", "sequence_number", seq, "response", raw(resp)))
		var out []byte
		for _, ev := range evs {
			out = append(out, s.frame(eventType(ev), ev)...)
		}
		s.reset()
		s.ended = true
		return out
	case Stop:
		s.reset()
		s.ended = true
		return nil
	}
	s.items = append(s.items, item)
	return s.release()
}

// patchOutput puts the replaced items in a response.completed's output, by id.
func patchOutput(data []byte, replaced map[string][]byte) []byte {
	top, _ := members(root(data))
	resp, _ := members(top["response"])
	items, _ := elements(resp["output"])
	var ss []splice
	for _, it := range items {
		m, _ := members(it)
		if id, _ := m["id"].str(); replaced[id] != nil {
			ss = append(ss, splice{it.span, replaced[id]})
		}
	}
	return spliceAll(data, ss)
}

// ── Whole JSON replies ───────────────────────────────────────────────────────

// DecideBody decides each tool call in a whole JSON reply (Anthropic
// Messages or Responses), in order, and returns the reply as released.
// stopped: a Stop ended it, and nothing of it goes out. A body that isn't a
// reply in format is returned as it is.
func DecideBody(format string, body []byte, decide func(ToolCall) Answer) (out []byte, stopped bool, err error) {
	if !json.Valid(body) {
		return body, false, nil
	}
	top, ok := members(root(body))
	if !ok {
		return body, false, nil
	}
	key := "content"
	if format == FormatResponses {
		key = "output"
	} else if format != FormatAnthropic {
		return body, false, nil
	}
	blocks, ok := elements(top[key])
	if !ok {
		return body, false, nil
	}
	var ss []splice
	for k, b := range blocks {
		m, _ := members(b)
		var it responsesItemFields
		_ = json.Unmarshal(b.raw, &it)
		tc := ToolCall{ID: firstOf(it.CallID, it.ID), Name: it.Name}
		argKey := ""
		switch {
		case format == FormatAnthropic && it.Type == "tool_use":
			tc.Arguments, argKey = string(it.Input), "input"
		case format == FormatResponses && it.Type == "function_call":
			tc.Arguments, argKey = it.Arguments, "arguments"
		case format == FormatResponses && it.Type == "custom_tool_call":
			tc.Arguments, tc.Custom, argKey = string(it.Input), true, "input"
		default:
			continue
		}
		a := decide(tc)
		switch a.Verdict {
		case Replace:
			if !json.Valid([]byte(a.Arguments)) {
				return nil, false, errors.New("tool call arguments must be JSON")
			}
			v := []byte(a.Arguments)
			if argKey == "arguments" {
				v = jsonString(a.Arguments)
			}
			ss = append(ss, splice{m[argKey].span, v})
		case Respond:
			kept := make([][]byte, 0, k+1)
			for _, p := range blocks[:k] {
				kept = append(kept, p.raw)
			}
			if format == FormatAnthropic {
				kept = append(kept, obj("type", "text", "text", a.Text))
				ss = append(ss, splice{top[key].span, arr(kept...)})
				if sr, ok := top["stop_reason"]; ok {
					ss = append(ss, splice{sr.span, jsonString("end_turn")})
				}
			} else {
				kept = append(kept, responsesItem(openaiID("msg"), "completed", a.Text))
				ss = append(ss, splice{top[key].span, arr(kept...)})
			}
			return spliceAll(body, dropInside(ss, top[key].span)), false, nil
		case Stop:
			return nil, true, nil
		}
	}
	return spliceAll(body, ss), false, nil
}

// dropInside drops the splices inside span but for the one that is span.
func dropInside(ss []splice, span Span) []splice {
	var out []splice
	for _, s := range ss {
		if s.span == span || s.span.End <= span.Start || s.span.Start >= span.End {
			out = append(out, s)
		}
	}
	return out
}

// ── Splicing JSON in place ───────────────────────────────────────────────────

type splice struct {
	span Span
	with []byte
}

// spliceAll replaces each span (none overlapping) with its bytes.
func spliceAll(b []byte, ss []splice) []byte {
	if len(ss) == 0 {
		return b
	}
	slices.SortFunc(ss, func(x, y splice) int { return x.span.Start - y.span.Start })
	out := make([]byte, 0, len(b))
	at := 0
	for _, s := range ss {
		out = append(append(out, b[at:s.span.Start]...), s.with...)
		at = s.span.End
	}
	return append(out, b[at:]...)
}

// set returns the JSON object o with key's value v: in place when o has the
// key, else added at its end.
func set(o []byte, key string, v []byte) []byte {
	r := root(o)
	m, ok := members(r)
	if !ok {
		return o
	}
	if old, ok := m[key]; ok {
		return spliceAll(o, []splice{{old.span, v}})
	}
	end := r.span.End - 1 // the closing brace
	add := append(append(jsonString(key), ':'), v...)
	if len(m) > 0 {
		add = append([]byte{','}, add...)
	}
	return spliceAll(o, []splice{{Span{end, end}, add}})
}
