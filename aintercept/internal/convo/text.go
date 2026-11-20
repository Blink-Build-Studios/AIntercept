package convo

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Reply text, piece by piece (docs/INTERCEPTOR.md, "Reply text"). Each
// text delta is held, not sent; the text block's pending text is decided
// when the block ends, or sooner when the caller Pumps (when no more of the
// reply is waiting), so pieces that arrive while a decision is outstanding
// go in the next one. Accept releases the held events as received; Replace
// one delta carrying the new text in their place; Hold keeps them for the
// next decision; Respond ends the reply with its text. Events that aren't
// text while text is held wait behind it, in order.

// textKey identifies a text block: Anthropic's block index; the Responses
// item, its output and content index; Chat's choice index.
type textKey struct {
	index, content int
	item           string
}

// textBlock is the text block being decided.
type textBlock struct {
	key      textKey
	id       string
	released strings.Builder
	changed  bool
	held     []held // the events since the first undecided piece (held[0])
	pending  strings.Builder
	heldN    int
	fresh    bool // a piece came since the last decision
}

// piece holds one piece of text (h, its event), opening its block.
func (s *Stream) piece(key textKey, id, text string, h held) []byte {
	var out []byte
	if s.text != nil && s.text.key != key {
		if out = s.endText(); s.ended {
			return out
		}
	}
	if s.text == nil {
		s.text = &textBlock{key: key, id: id}
	}
	t := s.text
	if text == "" && len(t.held) == 0 {
		return append(out, h.wire...) // nothing to decide
	}
	h.typ, h.text = "text", text
	h.data, h.wire = clone(h.data), clone(h.wire)
	t.held = append(t.held, h)
	t.pending.WriteString(text)
	t.fresh = true
	if t.heldN += len(h.wire); t.heldN > holdCap {
		s.unheld++
		out = append(out, s.answerText(Answer{}, false)...)
	}
	return out
}

// pass sends an event that isn't held text: behind any held text, else now.
func (s *Stream) pass(h held) []byte {
	if t := s.text; t != nil && len(t.held) > 0 {
		h.data, h.wire = clone(h.data), clone(h.wire)
		t.held = append(t.held, h)
		return nil
	}
	return h.wire
}

// after is out, then h unless the reply has ended.
func (s *Stream) after(out []byte, h held) []byte {
	if s.ended {
		return out
	}
	return append(out, s.pass(h)...)
}

// opaque tells the decider of an opaque part.
func (s *Stream) opaque(id string) {
	if s.dec.Opaque != nil {
		s.dec.Opaque(id)
	}
}

// Pump asks about the pending text of the block being decided, when a piece
// has come since the last decision, and returns what goes to the client. The
// caller pumps when no more of the reply is waiting to be fed.
func (s *Stream) Pump() []byte {
	if !s.Pending() {
		return nil
	}
	return s.decideText(false)
}

// Pending reports whether text is waiting for a decision: a piece has come
// since the last one.
func (s *Stream) Pending() bool { return !s.ended && s.text != nil && s.text.fresh }

// endText decides the block's pending text as finished, and closes it.
func (s *Stream) endText() []byte {
	t := s.text
	if t == nil {
		return nil
	}
	out := s.decideText(true)
	s.text = nil
	if t.changed && t.key.item != "" {
		if s.texts[t.key.item] == nil {
			s.texts[t.key.item] = map[int]string{}
		}
		s.texts[t.key.item][t.key.content] = t.released.String()
	}
	return out
}

func (s *Stream) decideText(done bool) []byte {
	t := s.text
	if t.pending.Len() == 0 {
		return nil
	}
	t.fresh = false
	a := s.dec.Text(TextPiece{ID: t.id, Released: t.released.String(), Pending: t.pending.String(), Done: done})
	return s.answerText(a, done)
}

// answerText applies an answer to the pending text.
func (s *Stream) answerText(a Answer, done bool) []byte {
	t := s.text
	pending := t.pending.String()
	switch a.Verdict {
	case Hold:
		if !done {
			return nil
		}
		a = Answer{}
	case Respond:
		out := append(s.frame(t.held[0].name, s.textData(t.held[0], a.Text)), s.textEnd(t, t.released.String()+a.Text)...)
		s.ended = true
		t.held, t.heldN = nil, 0
		t.pending.Reset()
		return out
	case Stop:
		s.ended = true
		return nil
	}
	keep := 0
	if !done && a.Keep > 0 {
		keep = min(a.Keep, len(pending))
	}
	cut := len(pending) - keep // the text released (or replaced) ends here
	replace := a.Verdict == Replace
	var out []byte
	if replace && a.Text != "" {
		out = s.frame(t.held[0].name, s.textData(t.held[0], a.Text))
	}
	var rest []held
	n, split := 0, false
	for i, h := range t.held {
		switch {
		case len(rest) > 0: // after the cut: still held
			rest = append(rest, h)
		case h.typ != "text":
			out = append(out, h.wire...)
		case n+len(h.text) <= cut && (cut > n || h.text == ""): // released whole
			n += len(h.text)
			if !replace {
				out = append(out, h.wire...)
			} else if i > 0 {
				out = append(out, s.dropped(h)...)
			}
		case cut == n: // held whole
			rest = append(rest, h)
		default: // the cut is inside
			head, tail := h.text[:cut-n], h.text[cut-n:]
			if !replace {
				out = append(out, s.frame(h.name, s.textData(h, head))...)
			}
			n = cut
			h.text, h.data = tail, s.textData(h, tail)
			h.wire = s.frame(h.name, h.data)
			rest = append(rest, h)
			split = true
		}
	}
	if split && s.format == FormatResponses { // one event became two: number the rest after it
		for i := range rest {
			rest[i] = s.renumber(rest[i])
		}
		s.shift++
		s.seq++
	}
	if replace {
		t.released.WriteString(a.Text)
		t.changed = true
	} else {
		t.released.WriteString(pending[:cut])
	}
	t.held, t.heldN = rest, 0
	for _, h := range rest {
		t.heldN += len(h.wire)
	}
	t.pending.Reset()
	t.pending.WriteString(pending[cut:])
	return out
}

// renumber adds one to a held Responses event's sequence number.
func (s *Stream) renumber(h held) held {
	var ev struct {
		SequenceNumber *int64 `json:"sequence_number"`
	}
	if json.Unmarshal(h.data, &ev) != nil || ev.SequenceNumber == nil {
		return h
	}
	h.data = set(h.data, "sequence_number", []byte(strconv.FormatInt(*ev.SequenceNumber+1, 10)))
	h.wire = s.frame(h.name, h.data)
	return h
}

// textData is h's data (a text delta) carrying text instead.
func (s *Stream) textData(h held, text string) []byte {
	switch s.format {
	case FormatAnthropic:
		return set(h.data, "delta", obj("type", "text_delta", "text", text))
	case FormatResponses:
		return set(h.data, "delta", jsonString(text))
	}
	return chatSet(h.data, "content", jsonString(text))
}

// dropped is what's left of a text delta a Replace dropped: nothing, or in
// Chat Completions, a chunk that also finished the reply or carried usage,
// without its text.
func (s *Stream) dropped(h held) []byte {
	if s.format == FormatChat && chatMore(h.data) {
		return s.frame(h.name, chatSet(h.data, "content", jsonString("")))
	}
	return nil
}

// textEnd is the events that end the reply after a Respond's text in t's
// block: full is the block's whole text.
func (s *Stream) textEnd(t *textBlock, full string) []byte {
	var out []byte
	switch s.format {
	case FormatAnthropic:
		evs := anthropicText(t.key.index, "")[2:] // the block's stop, end_turn
		for _, ev := range evs {
			out = append(out, s.frame(eventType(ev), ev)...)
		}
	case FormatResponses:
		k, seq := t.key, s.seq+1
		item := responsesItem(k.item, "completed", full)
		evs := [][]byte{
			obj("type", "response.output_text.done", "sequence_number", seq, "item_id", k.item, "output_index", k.index, "content_index", k.content, "text", full),
			obj("type", "response.content_part.done", "sequence_number", seq+1, "item_id", k.item, "output_index", k.index, "content_index", k.content, "part", raw(outputText(full))),
			obj("type", "response.output_item.done", "sequence_number", seq+2, "output_index", k.index, "item", raw(item)),
		}
		out = s.complete(evs, item, seq+3)
	case FormatChat:
		out = s.chatStop(t.held[0].data)
	}
	return out
}
