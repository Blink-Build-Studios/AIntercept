package convo

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// Every message from the model, before the client sees it
// (docs/INTERCEPTOR_V2.md, "Callbacks"): a reply's text is held piece by
// piece until it's decided, each tool call from its first event until it's
// complete, and each is released as it was, changed, or replaced (with the
// rest of the reply) by text. Opaque parts (reasoning) are announced and go
// on as they came.
//
// Stream does this event by event for Anthropic Messages, the Responses API
// (over SSE or Codex's WebSocket) and Chat Completions; DecideBody for a
// whole JSON reply.

// ToolCall is one complete tool call in a reply.
type ToolCall struct {
	ID   string // the provider's call id (toolu_…, call_…)
	Name string
	// Arguments as JSON; a custom tool's free-form input as a JSON string.
	Arguments string
	Custom    bool // a Responses custom_tool_call
}

// TextPiece asks about a text block's pending text.
type TextPiece struct {
	ID       string // the block's id: "<the reply's id>:<its index>"
	Released string // the block's text already released (never asked about again)
	Pending  string // the new pieces, plus anything held back
	Done     bool   // the block is finished: nothing more of it will come
}

// Verdict is what to do with a held tool call or text.
type Verdict int

const (
	Accept  Verdict = iota // release it as it was
	Replace                // release Answer.Arguments (a tool call) or Answer.Text (text) instead
	Respond                // end the reply here with Answer.Text instead
	Stop                   // end the reply here with nothing more (the caller ends it with an error)
	Hold                   // text: keep it, and ask again when more arrives (at the block's end, Accept)
)

// Answer is the decision on one tool call or piece of text.
type Answer struct {
	Verdict   Verdict
	Arguments string
	Text      string
	// Keep (text, with Accept or Replace, before the block is done): the
	// last Keep bytes of the pending text stay held for the next decision.
	Keep int
}

// Deciders are asked about a reply's messages; a nil one lets its kind pass
// undecided.
type Deciders struct {
	ToolCall func(ToolCall) Answer
	Text     func(TextPiece) Answer
	// Opaque is told of each opaque part (reasoning) by id; it can't change it.
	Opaque func(id string)
}

// holdCap bounds the bytes held for one tool call or text; past it, they go
// on undecided.
var holdCap = 32 << 20

// Stream follows one streamed reply.
type Stream struct {
	format string
	frame  func(name string, data []byte) []byte
	dec    Deciders

	open  bool   // a tool call is held
	held  []held // its events (and any that came while it was open)
	heldN int
	index int // its content block index (Anthropic), output index (Responses) or tool call index (Chat)
	call  ToolCall
	start []byte // Anthropic: the block's input as it started
	args  strings.Builder

	text    *textBlock // the text block being decided
	replyID string     // Anthropic: the message id; Chat: the completion id
	// Responses: the texts of the blocks a Replace changed, by item id and
	// content index, for the events and items that repeat them.
	texts map[string]map[int]string

	ended  bool
	unheld int

	// Responses: the response object (from response.created), the output
	// items released so far, and the replaced ones by id, for the completed
	// response; the last sequence number.
	resp     []byte
	items    [][]byte
	replaced map[string][]byte
	seq      int64
	// shift is added to the upstream's sequence numbers once a held piece
	// was split in two (Keep), to keep them ascending.
	shift int64
}

type held struct {
	typ        string
	index      int
	name       string
	data, wire []byte
	text       string // a text piece's text
}

// NewStream follows a reply in format (FormatAnthropic, FormatResponses or
// FormatChat; any other passes through). frame encodes an event the Stream
// makes (its SSE event name and JSON data) as the transport carries it.
func NewStream(format string, frame func(name string, data []byte) []byte, dec Deciders) *Stream {
	return &Stream{format: format, frame: frame, dec: dec, replaced: map[string][]byte{}, texts: map[string]map[int]string{}}
}

// Feed takes the reply's next event, its SSE event name (or ""), its JSON
// data, and its bytes as received, and returns what goes to the client now.
// It asks about text only when a block ends; Pump asks about the rest.
func (s *Stream) Feed(name string, data, raw []byte) []byte {
	if s.ended {
		return nil
	}
	switch s.format {
	case FormatAnthropic:
		return s.anthropic(name, data, raw)
	case FormatResponses:
		return s.responses(name, data, raw)
	case FormatChat:
		return s.chat(name, data, raw)
	}
	return raw
}

// Ended reports that a Respond or Stop ended the reply: nothing more of the
// upstream's goes out.
func (s *Stream) Ended() bool { return s.ended }

// Unheld is how many tool calls and texts went out undecided, over the hold
// cap.
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

// Flush ends the reply's open text (decided, as finished) and releases a
// held tool call, undecided: the reply ended mid-call.
func (s *Stream) Flush() []byte {
	out := s.endText()
	if s.ended {
		return out
	}
	return append(out, s.release()...)
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

// blockID is a text or opaque block's id: the reply's id and its index.
func blockID(reply string, index int) string { return reply + ":" + strconv.Itoa(index) }

// ── Anthropic: text and tool_use content blocks ─────────────────────────────

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
			Text        string `json:"text"`
		} `json:"delta"`
		Message *struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	_ = json.Unmarshal(data, &ev)
	if s.open {
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
	h := held{typ: ev.Type, index: ev.Index, name: name, data: data, wire: raw}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			s.replyID = ev.Message.ID
		}
	case "content_block_start":
		b := ev.ContentBlock
		switch {
		case b == nil:
		case b.Type == "tool_use" && s.dec.ToolCall != nil:
			out := s.endText()
			if s.ended {
				return out
			}
			s.open, s.index = true, ev.Index
			s.call = ToolCall{ID: b.ID, Name: b.Name}
			s.start = clone(b.Input)
			return append(out, s.hold(ev.Type, ev.Index, name, data, raw)...)
		case b.Type == "thinking" || b.Type == "redacted_thinking":
			s.opaque(blockID(s.replyID, ev.Index))
		}
	case "content_block_delta":
		if ev.Delta != nil && ev.Delta.Type == "text_delta" && s.dec.Text != nil {
			return s.piece(textKey{index: ev.Index}, blockID(s.replyID, ev.Index), ev.Delta.Text, h)
		}
	case "content_block_stop":
		if s.text != nil && s.text.key.index == ev.Index {
			return s.after(s.endText(), h)
		}
	case "message_delta", "message_stop", "error":
		return s.after(s.endText(), h)
	}
	return s.pass(h)
}

func (s *Stream) decideAnthropic() []byte {
	tc := s.call
	tc.Arguments = s.args.String()
	if strings.TrimSpace(tc.Arguments) == "" {
		tc.Arguments = string(s.start)
	}
	a := s.dec.ToolCall(tc)
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

// ── Responses: message text, function_call and custom_tool_call items ──────

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
		ItemID         string          `json:"item_id"`
		ContentIndex   int             `json:"content_index"`
		Delta          string          `json:"delta"`
		Item           json.RawMessage `json:"item"`
		Response       json.RawMessage `json:"response"`
	}
	_ = json.Unmarshal(data, &ev)
	if ev.SequenceNumber != nil {
		s.seq = *ev.SequenceNumber + s.shift
		if s.shift > 0 {
			data = set(data, "sequence_number", []byte(strconv.FormatInt(s.seq, 10)))
			raw = s.frame(name, data)
		}
	}
	if s.open {
		if out := s.hold(ev.Type, ev.OutputIndex, name, data, raw); !s.open {
			return out
		}
		if ev.Type == "response.output_item.done" && ev.OutputIndex == s.index {
			return s.decideResponses(clone(ev.Item))
		}
		return nil
	}
	h := held{typ: ev.Type, index: ev.OutputIndex, name: name, data: data, wire: raw}
	key := textKey{index: ev.OutputIndex, content: ev.ContentIndex, item: ev.ItemID}
	switch ev.Type {
	case "response.created", "response.in_progress":
		s.resp = clone(ev.Response)
	case "response.output_item.added":
		var it responsesItemFields
		_ = json.Unmarshal(ev.Item, &it)
		switch {
		case (it.Type == "function_call" || it.Type == "custom_tool_call") && s.dec.ToolCall != nil:
			out := s.endText()
			if s.ended {
				return out
			}
			s.open, s.index = true, ev.OutputIndex
			return append(out, s.hold(ev.Type, ev.OutputIndex, name, data, raw)...)
		case it.Type == "reasoning":
			s.opaque(it.ID)
		}
	case "response.output_text.delta":
		if s.dec.Text != nil {
			return s.piece(key, blockID(ev.ItemID, ev.ContentIndex), ev.Delta, h)
		}
	case "response.output_text.done":
		if s.text != nil && s.text.key == key {
			out := s.endText()
			if s.ended {
				return out
			}
			if t := s.texts[ev.ItemID]; t != nil {
				if v, ok := t[ev.ContentIndex]; ok {
					return append(out, s.frame(name, set(data, "text", jsonString(v)))...)
				}
			}
			return append(out, raw...)
		}
	case "response.content_part.done":
		if v, ok := s.texts[ev.ItemID][ev.ContentIndex]; ok {
			part, _ := members(root(data))
			return s.after(s.endText(), held{wire: s.frame(name, set(data, "part", set(part["part"].raw, "text", jsonString(v))))})
		}
	case "response.output_item.done":
		out := s.endText()
		if s.ended {
			return out
		}
		item := clone(ev.Item)
		var it responsesItemFields
		_ = json.Unmarshal(item, &it)
		if t := s.texts[it.ID]; t != nil {
			item = patchTexts(item, t)
			s.replaced[it.ID] = item
			h.wire = s.frame(name, set(data, "item", item))
		}
		s.items = append(s.items, item)
		return append(out, s.pass(h)...)
	case "response.completed", "response.failed", "response.incomplete", "error":
		out := s.endText()
		if ev.Type == "response.completed" && len(s.replaced) > 0 {
			h.wire = s.frame(name, patchOutput(data, s.replaced))
		}
		return s.after(out, h)
	}
	return s.pass(h)
}

func (s *Stream) decideResponses(item []byte) []byte {
	var it responsesItemFields
	_ = json.Unmarshal(item, &it)
	tc := ToolCall{ID: firstOf(it.CallID, it.ID), Name: it.Name, Arguments: it.Arguments}
	if it.Type == "custom_tool_call" {
		tc.Arguments, tc.Custom = string(it.Input), true
	}
	a := s.dec.ToolCall(tc)
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
		out := s.responsesEnd(s.index, s.seq+1, a.Text)
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

// responsesEnd is the events that end a reply with a new message carrying
// text at output index i, numbered from seq: the message, then the
// completed response listing the items already sent and it.
func (s *Stream) responsesEnd(i int, seq int64, text string) []byte {
	evs, msg, seq := responsesMessage(i, seq, text)
	return s.complete(evs, msg, seq)
}

// complete adds response.completed (its output the items sent, then msg) to
// evs, and frames them.
func (s *Stream) complete(evs [][]byte, msg []byte, seq int64) []byte {
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
	return out
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

// patchTexts sets a message item's content parts' text, by content index.
func patchTexts(item []byte, texts map[int]string) []byte {
	m, _ := members(root(item))
	parts, _ := elements(m["content"])
	var ss []splice
	for i, p := range parts {
		if v, ok := texts[i]; ok {
			pm, _ := members(p)
			if t, ok := pm["text"]; ok {
				ss = append(ss, splice{t.span, jsonString(v)})
			}
		}
	}
	return spliceAll(item, ss)
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
