package convo

import (
	"bytes"
	"encoding/json"
)

// Chat Completions replies: chunks of the first choice's delta, its content
// (text) and tool_calls (each by index, its arguments in pieces), then a
// chunk with finish_reason, maybe one with usage, and [DONE]. A tool call is
// complete when the next one starts or the reply finishes; text when a tool
// call starts or the reply finishes.

type chatChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (s *Stream) chat(name string, data, raw []byte) []byte {
	h := held{name: name, data: data, wire: raw}
	if string(bytes.TrimSpace(data)) == "[DONE]" {
		return s.after(s.chatEnd(), h)
	}
	var c chatChunk
	if json.Unmarshal(data, &c) != nil {
		return s.pass(h)
	}
	if s.replyID == "" {
		s.replyID = c.ID
	}
	if len(c.Choices) != 1 { // usage, or more than one choice: ends what's open
		return s.after(s.chatEnd(), h)
	}
	ch := c.Choices[0]
	var out []byte
	switch {
	case len(ch.Delta.ToolCalls) > 0 && s.dec.ToolCall != nil:
		tc := ch.Delta.ToolCalls[0]
		if out = s.endText(); s.ended {
			return out
		}
		if s.open && tc.Index != s.index {
			if out = append(out, s.decideChat()...); s.ended {
				return out
			}
		}
		if !s.open {
			s.open, s.index = true, tc.Index
			s.call = ToolCall{ID: tc.ID, Name: tc.Function.Name}
		}
		s.args.WriteString(tc.Function.Arguments)
		s.held = append(s.held, held{typ: "tool", name: name, data: clone(data), wire: clone(raw)})
		if s.heldN += len(raw); s.heldN > holdCap {
			s.unheld++
			return append(out, s.release()...)
		}
		if ch.FinishReason != nil {
			out = append(out, s.decideChat()...)
		}
		return out
	case ch.Delta.Content != nil && *ch.Delta.Content != "" && s.dec.Text != nil:
		if s.open {
			if out = s.decideChat(); s.ended {
				return out
			}
		}
		out = append(out, s.piece(textKey{index: ch.Index}, blockID(c.ID, ch.Index), *ch.Delta.Content, h)...)
		if ch.FinishReason != nil && !s.ended {
			out = append(out, s.endText()...)
		}
		return out
	case ch.FinishReason != nil:
		return s.after(s.chatEnd(), h)
	case s.open: // waits behind the held call
		s.held = append(s.held, held{typ: "other", name: name, data: clone(data), wire: clone(raw)})
		return nil
	}
	return s.pass(h)
}

// chatEnd ends the open text and decides the open tool call.
func (s *Stream) chatEnd() []byte {
	out := s.endText()
	if s.open && !s.ended {
		out = append(out, s.decideChat()...)
	}
	return out
}

// decideChat decides the open tool call.
func (s *Stream) decideChat() []byte {
	tc := s.call
	tc.Arguments = s.args.String()
	a := s.dec.ToolCall(tc)
	switch a.Verdict {
	case Replace:
		var out []byte
		for i, h := range s.held {
			switch {
			case i == 0:
				out = append(out, s.frame(h.name, chatSetArguments(h.data, a.Arguments))...)
			case h.typ == "other":
				out = append(out, h.wire...)
			case chatMore(h.data):
				out = append(out, s.frame(h.name, chatSet(h.data, "tool_calls", []byte("[]")))...)
			}
		}
		s.reset()
		return out
	case Respond:
		first := s.held[0].data
		out := s.frame("", chatSet(first, "tool_calls", nil, "content", jsonString(a.Text)))
		out = append(out, s.chatStop(first)...)
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

// chatStop is the chunk that finishes a reply ("stop"), made from a chunk of
// it, and [DONE].
func (s *Stream) chatStop(template []byte) []byte {
	top, _ := members(root(template))
	chs, _ := elements(top["choices"])
	if len(chs) == 0 {
		return s.frame("", []byte("[DONE]"))
	}
	ch := chs[0]
	choice := set(set(ch.raw, "delta", []byte("{}")), "finish_reason", jsonString("stop"))
	stop := spliceAll(template, []splice{{ch.span, choice}})
	return append(s.frame("", stop), s.frame("", []byte("[DONE]"))...)
}

// chatMore reports whether a chunk does more than add to the delta: it
// finishes the reply or carries usage.
func chatMore(data []byte) bool {
	var c struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(data, &c)
	return len(c.Choices) > 0 && c.Choices[0].FinishReason != nil || len(c.Usage) > 0 && string(c.Usage) != "null"
}

// chatSet sets keys of a chunk's first choice's delta (nil deletes one: it
// becomes absent).
func chatSet(data []byte, kv ...any) []byte {
	top, _ := members(root(data))
	chs, _ := elements(top["choices"])
	if len(chs) == 0 {
		return data
	}
	ch, _ := members(chs[0])
	d, ok := ch["delta"]
	if !ok {
		return data
	}
	delta := d.raw
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		if v, _ := kv[i+1].([]byte); v != nil {
			delta = set(delta, k, v)
		} else {
			delta = unset(delta, k)
		}
	}
	return spliceAll(data, []splice{{d.span, delta}})
}

// chatSetArguments sets the arguments of a chunk's first tool call.
func chatSetArguments(data []byte, args string) []byte {
	top, _ := members(root(data))
	chs, _ := elements(top["choices"])
	if len(chs) == 0 {
		return data
	}
	ch, _ := members(chs[0])
	d, _ := members(ch["delta"])
	tcs, _ := elements(d["tool_calls"])
	if len(tcs) == 0 {
		return data
	}
	tc, _ := members(tcs[0])
	fn, ok := tc["function"]
	if !ok {
		return data
	}
	return spliceAll(data, []splice{{fn.span, set(fn.raw, "arguments", jsonString(args))}})
}

// unset returns the JSON object o without key.
func unset(o []byte, key string) []byte {
	r := root(o)
	m, ok := members(r)
	if !ok {
		return o
	}
	if _, ok := m[key]; !ok {
		return o
	}
	var kv []any
	// Rebuild in the original order.
	elems := orderedMembers(r)
	for _, e := range elems {
		if e.key != key {
			kv = append(kv, e.key, raw(e.val.raw))
		}
	}
	return obj(kv...)
}

type member struct {
	key string
	val value
}

// orderedMembers is an object's members in order.
func orderedMembers(v value) []member {
	var out []member
	b := v.raw
	i := skipSpace(b, 1)
	for i < len(b) && b[i] == '"' {
		kEnd := skipString(b, i)
		var key string
		_ = json.Unmarshal(b[i:kEnd], &key)
		i = skipSpace(b, kEnd)
		if i >= len(b) || b[i] != ':' {
			return out
		}
		i = skipSpace(b, i+1)
		end := skipValue(b, i)
		out = append(out, member{key, value{raw: b[i:end], span: Span{v.span.Start + i, v.span.Start + end}}})
		i = skipSpace(b, end)
		if i < len(b) && b[i] == ',' {
			i = skipSpace(b, i+1)
		}
	}
	return out
}
