package convo

import (
	"encoding/json"
	"errors"
)

// ── Whole JSON replies ───────────────────────────────────────────────────────

// DecideBody decides each message in a whole JSON reply (Anthropic Messages,
// Responses or Chat Completions), in order: each text block (as finished)
// and each tool call; opaque parts are announced. It returns the reply as
// released. stopped: a Stop ended it, and nothing of it goes out. A body
// that isn't a reply in format is returned as it is.
func DecideBody(format string, body []byte, dec Deciders) (out []byte, stopped bool, err error) {
	if !json.Valid(body) {
		return body, false, nil
	}
	top, ok := members(root(body))
	if !ok {
		return body, false, nil
	}
	if format == FormatChat {
		return decideChatBody(body, top, dec)
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
	replyID, _ := top["id"].str()
	var ss []splice
	// respond ends the reply at block k with text: the blocks before it, then
	// the text.
	respond := func(k int, text string) ([]byte, bool, error) {
		kept := make([][]byte, 0, k+1)
		for _, p := range blocks[:k] {
			kept = append(kept, p.raw)
		}
		if format == FormatAnthropic {
			kept = append(kept, obj("type", "text", "text", text))
			ss = append(ss, splice{top[key].span, arr(kept...)})
			if sr, ok := top["stop_reason"]; ok {
				ss = append(ss, splice{sr.span, jsonString("end_turn")})
			}
		} else {
			kept = append(kept, responsesItem(openaiID("msg"), "completed", text))
			ss = append(ss, splice{top[key].span, arr(kept...)})
		}
		return spliceAll(body, dropInside(ss, top[key].span)), false, nil
	}
	for k, b := range blocks {
		m, _ := members(b)
		var it responsesItemFields
		_ = json.Unmarshal(b.raw, &it)
		// Text: an Anthropic text block, or a Responses message's output_text
		// parts.
		var texts []value
		var ids []string
		switch {
		case format == FormatAnthropic && it.Type == "text":
			texts, ids = []value{b}, []string{blockID(replyID, k)}
		case format == FormatResponses && it.Type == "message":
			parts, _ := elements(m["content"])
			for j, p := range parts {
				var pt struct{ Type string }
				if json.Unmarshal(p.raw, &pt) == nil && pt.Type == "output_text" {
					texts, ids = append(texts, p), append(ids, blockID(it.ID, j))
				}
			}
		case it.Type == "thinking", it.Type == "redacted_thinking", it.Type == "reasoning":
			if dec.Opaque != nil {
				id := it.ID
				if format == FormatAnthropic {
					id = blockID(replyID, k)
				}
				dec.Opaque(id)
			}
			continue
		}
		if len(texts) > 0 {
			if dec.Text == nil {
				continue
			}
			for j, p := range texts {
				pm, _ := members(p)
				text, _ := pm["text"].str()
				if text == "" {
					continue
				}
				a := dec.Text(TextPiece{ID: ids[j], Pending: text, Done: true})
				switch a.Verdict {
				case Replace:
					ss = append(ss, splice{pm["text"].span, jsonString(a.Text)})
				case Respond:
					return respond(k, a.Text)
				case Stop:
					return nil, true, nil
				}
			}
			continue
		}
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
		if dec.ToolCall == nil {
			continue
		}
		a := dec.ToolCall(tc)
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
			return respond(k, a.Text)
		case Stop:
			return nil, true, nil
		}
	}
	return spliceAll(body, ss), false, nil
}

// decideChatBody decides a Chat Completion: the first choice's text, then
// its tool calls.
func decideChatBody(body []byte, top map[string]value, dec Deciders) ([]byte, bool, error) {
	chs, _ := elements(top["choices"])
	if len(chs) == 0 {
		return body, false, nil
	}
	ch, _ := members(chs[0])
	msg, ok := members(ch["message"])
	if !ok {
		return body, false, nil
	}
	id, _ := top["id"].str()
	var ss []splice
	// respond makes the message the text alone, finished with "stop".
	respond := func(text string) ([]byte, bool, error) {
		m := set(ch["message"].raw, "content", jsonString(text))
		m = unset(m, "tool_calls")
		c := set(set(chs[0].raw, "message", m), "finish_reason", jsonString("stop"))
		return spliceAll(body, []splice{{chs[0].span, c}}), false, nil
	}
	if text, _ := msg["content"].str(); text != "" && dec.Text != nil {
		a := dec.Text(TextPiece{ID: blockID(id, 0), Pending: text, Done: true})
		switch a.Verdict {
		case Replace:
			ss = append(ss, splice{msg["content"].span, jsonString(a.Text)})
		case Respond:
			return respond(a.Text)
		case Stop:
			return nil, true, nil
		}
	}
	tcs, _ := elements(msg["tool_calls"])
	for _, t := range tcs {
		if dec.ToolCall == nil {
			break
		}
		tm, _ := members(t)
		fn, _ := members(tm["function"])
		tc := ToolCall{}
		tc.ID, _ = tm["id"].str()
		tc.Name, _ = fn["name"].str()
		tc.Arguments, _ = fn["arguments"].str()
		a := dec.ToolCall(tc)
		switch a.Verdict {
		case Replace:
			if !json.Valid([]byte(a.Arguments)) {
				return nil, false, errors.New("tool call arguments must be JSON")
			}
			ss = append(ss, splice{fn["arguments"].span, jsonString(a.Arguments)})
		case Respond:
			return respond(a.Text)
		case Stop:
			return nil, true, nil
		}
	}
	return spliceAll(body, ss), false, nil
}
