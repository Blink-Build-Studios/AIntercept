package convo

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Respond (docs/INTERCEPTOR.md, "Respond"): the client gets an
// Interceptor's text as the model's reply. When the client keeps the
// conversation, AIntercept synthesizes the reply in the provider's format
// (Synthesize, SynthesizeEvents); when the provider keeps it (a chained
// request), it rewrites the request so the model replies with the text
// (Rewrite).

// RespondInstruction is what a rewritten request asks the model, followed by
// the text.
const RespondInstruction = "Reply with exactly this text and nothing else: "

// Rewrite returns the body with its newest new user message asking the model
// to reply with exactly text: that message's last text part (or, when the
// new messages are only tool results, the last result's text) becomes the
// instruction. Every other byte is as sent.
func (r Request) Rewrite(text string) ([]byte, error) {
	for _, kind := range []Kind{KindText, KindToolResult} {
		for k := len(r.New) - 1; k >= 0; k-- {
			m := r.Messages[r.New[k]]
			if kind == KindText && m.Role != RoleUser {
				continue
			}
			for j := len(m.Parts) - 1; j >= 0; j-- {
				if m.Parts[j].Kind == kind {
					return r.Apply([]Edit{{Message: m.Index, Part: j, Content: RespondInstruction + text}})
				}
			}
		}
	}
	return nil, errors.New("the request's new messages have no text to rewrite")
}

// Synthesize is a complete reply carrying text, in the request's format: an
// event stream when the request asked for one, else a JSON body. Usage is
// zero; ids are fresh, in the provider's style.
func Synthesize(format, model, text string, stream bool) (contentType string, body []byte) {
	if !stream {
		switch format {
		case FormatAnthropic:
			return "application/json", anthropicMessage(model, text)
		case FormatResponses:
			return "application/json", responseObject(openaiID("resp"), model, "completed", [][]byte{responsesItem(openaiID("msg"), "completed", text)}, zeroResponsesUsage)
		case FormatChat:
			return "application/json", chatCompletion(model, text)
		}
		return "", nil
	}
	var out []byte
	switch format {
	case FormatAnthropic:
		for _, ev := range anthropicEvents(model, text) {
			out = append(out, SSE(eventType(ev), ev)...)
		}
	case FormatResponses:
		for _, ev := range SynthesizeEvents(model, text) {
			out = append(out, SSE(eventType(ev), ev)...)
		}
	case FormatChat:
		for _, ev := range chatChunks(model, text) {
			out = append(out, SSE("", ev)...)
		}
		out = append(out, SSE("", []byte("[DONE]"))...)
	default:
		return "", nil
	}
	return "text/event-stream", out
}

// SynthesizeEvents is a Responses reply as its events, one JSON message
// each: what Codex's WebSocket carries.
func SynthesizeEvents(model, text string) [][]byte {
	id := openaiID("resp")
	evs := [][]byte{
		obj("type", "response.created", "sequence_number", 0, "response", raw(responseObject(id, model, "in_progress", nil, nil))),
		obj("type", "response.in_progress", "sequence_number", 1, "response", raw(responseObject(id, model, "in_progress", nil, nil))),
	}
	msg, item, seq := responsesMessage(0, 2, text)
	evs = append(evs, msg...)
	return append(evs, obj("type", "response.completed", "sequence_number", seq,
		"response", raw(responseObject(id, model, "completed", [][]byte{item}, zeroResponsesUsage))))
}

// SSE frames one server-sent event (no event line when name is "").
func SSE(name string, data []byte) []byte {
	var b []byte
	if name != "" {
		b = append(append(append(b, "event: "...), name...), '\n')
	}
	return append(append(append(b, "data: "...), data...), '\n', '\n')
}

// ── Anthropic Messages ──────────────────────────────────────────────────────

func anthropicMessage(model, text string) []byte {
	return obj("id", anthropicID(), "type", "message", "role", "assistant", "model", model,
		"content", raw(arr(obj("type", "text", "text", text))),
		"stop_reason", "end_turn", "stop_sequence", nil,
		"usage", raw(obj("input_tokens", 0, "output_tokens", 0)))
}

func anthropicEvents(model, text string) [][]byte {
	start := obj("type", "message_start", "message", raw(obj("id", anthropicID(), "type", "message", "role", "assistant",
		"model", model, "content", raw(arr()), "stop_reason", nil, "stop_sequence", nil,
		"usage", raw(obj("input_tokens", 0, "output_tokens", 0)))))
	return append([][]byte{start}, anthropicText(0, text)...)
}

// anthropicText is the events that end a reply with a text block at index:
// the block, then end_turn.
func anthropicText(index int, text string) [][]byte {
	return [][]byte{
		obj("type", "content_block_start", "index", index, "content_block", raw(obj("type", "text", "text", ""))),
		obj("type", "content_block_delta", "index", index, "delta", raw(obj("type", "text_delta", "text", text))),
		obj("type", "content_block_stop", "index", index),
		obj("type", "message_delta", "delta", raw(obj("stop_reason", "end_turn", "stop_sequence", nil)),
			"usage", raw(obj("output_tokens", 0))),
		obj("type", "message_stop"),
	}
}

// ── OpenAI Responses ─────────────────────────────────────────────────────────

var zeroResponsesUsage = obj("input_tokens", 0, "input_tokens_details", raw(obj("cached_tokens", 0)),
	"output_tokens", 0, "output_tokens_details", raw(obj("reasoning_tokens", 0)), "total_tokens", 0)

// responseObject is a Response; nil usage is null.
func responseObject(id, model, status string, output [][]byte, usage []byte) []byte {
	u := json.RawMessage("null")
	if usage != nil {
		u = usage
	}
	return obj("id", id, "object", "response", "created_at", time.Now().Unix(), "status", status,
		"model", model, "output", raw(arr(output...)), "usage", u)
}

func responsesItem(id, status, text string) []byte {
	content := arr()
	if status == "completed" {
		content = arr(outputText(text))
	}
	return obj("id", id, "type", "message", "status", status, "content", raw(content), "role", "assistant")
}

func outputText(text string) []byte {
	return obj("type", "output_text", "annotations", raw(arr()), "text", text)
}

// responsesMessage is the events that add an assistant message carrying text
// at output index i, numbered from seq: the events, the finished item, and
// the next sequence number.
func responsesMessage(i int, seq int64, text string) ([][]byte, []byte, int64) {
	id := openaiID("msg")
	item := responsesItem(id, "completed", text)
	evs := [][]byte{
		obj("type", "response.output_item.added", "sequence_number", seq, "output_index", i, "item", raw(responsesItem(id, "in_progress", ""))),
		obj("type", "response.content_part.added", "sequence_number", seq+1, "item_id", id, "output_index", i, "content_index", 0, "part", raw(outputText(""))),
		obj("type", "response.output_text.delta", "sequence_number", seq+2, "item_id", id, "output_index", i, "content_index", 0, "delta", text),
		obj("type", "response.output_text.done", "sequence_number", seq+3, "item_id", id, "output_index", i, "content_index", 0, "text", text),
		obj("type", "response.content_part.done", "sequence_number", seq+4, "item_id", id, "output_index", i, "content_index", 0, "part", raw(outputText(text))),
		obj("type", "response.output_item.done", "sequence_number", seq+5, "output_index", i, "item", raw(item)),
	}
	return evs, item, seq + 6
}

// ── OpenAI Chat Completions ─────────────────────────────────────────────────

func chatCompletion(model, text string) []byte {
	return obj("id", chatID(), "object", "chat.completion", "created", time.Now().Unix(), "model", model,
		"choices", raw(arr(obj("index", 0, "message", raw(obj("role", "assistant", "content", text, "refusal", nil)),
			"logprobs", nil, "finish_reason", "stop"))),
		"usage", raw(obj("prompt_tokens", 0, "completion_tokens", 0, "total_tokens", 0)))
}

func chatChunks(model, text string) [][]byte {
	id, created := chatID(), time.Now().Unix()
	chunk := func(delta []byte, finish any) []byte {
		return obj("id", id, "object", "chat.completion.chunk", "created", created, "model", model,
			"choices", raw(arr(obj("index", 0, "delta", raw(delta), "logprobs", nil, "finish_reason", finish))))
	}
	return [][]byte{
		chunk(obj("role", "assistant", "content", ""), nil),
		chunk(obj("content", text), nil),
		chunk(obj(), "stop"),
	}
}

// ── JSON, built in order ─────────────────────────────────────────────────────

// raw marks bytes that are JSON already.
func raw(b []byte) json.RawMessage { return json.RawMessage(b) }

// obj is a JSON object with its keys in the order given: key, value, ….
// Values are encoded as encoding/json does (without HTML escaping);
// json.RawMessage goes in as it is.
func obj(kv ...any) []byte {
	b := []byte{'{'}
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(append(append(b, jsonString(kv[i].(string))...), ':'), encode(kv[i+1])...)
	}
	return append(b, '}')
}

func arr(vs ...[]byte) []byte {
	return append(append([]byte{'['}, bytes.Join(vs, []byte{','})...), ']')
}

func encode(v any) []byte {
	if r, ok := v.(json.RawMessage); ok {
		return r
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}

// eventType is an event's "type".
func eventType(ev []byte) string {
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(ev, &t)
	return t.Type
}

// ── Ids, in each provider's style ────────────────────────────────────────────

const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func randomFrom(alphabet string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func anthropicID() string { return "msg_01" + randomFrom(base58, 22) }

func openaiID(prefix string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func chatID() string {
	return "chatcmpl-" + randomFrom("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", 29)
}
