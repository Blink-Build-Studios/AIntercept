package events

import (
	"encoding/json"
	"strings"
)

// ChatGPT web (chatgpt.com in a browser, and Codex's browser surface).
//
// A turn is POST /backend-api/f/conversation. The response is SSE in OpenAI's
// own "delta_encoding: v1" format, not the Responses API: messages of a
// conversation tree are added and then patched (`{"p","o","v"}` ops), with
// typed side events (server_ste_metadata, title_generation,
// conversation_detail_metadata, …). It exposes no token counts.

// applyChatGPTTurn fills a complete row from a ChatGPT web turn: the real
// model (the request usually says "auto"), tool calls from the message tree,
// attachments, conversation linkage and the per-turn server metadata.
func (e *Event) applyChatGPTTurn(req, resp []byte, encoding string) {
	wc := &WebChat{App: "chatgpt"}
	var r struct {
		Model           string `json:"model"`
		ConversationID  string `json:"conversation_id"`
		ParentMessageID string `json:"parent_message_id"`
		TurnAttribution *struct {
			TurnTrigger string `json:"turn_trigger"`
		} `json:"turn_attribution"`
		ClientContextualInfo *struct {
			AppSurface string `json:"app_surface"`
		} `json:"client_contextual_info"`
		Messages []struct {
			Metadata struct {
				Attachments []struct {
					MimeType string `json:"mime_type"`
					Size     int    `json:"size"`
				} `json:"attachments"`
			} `json:"metadata"`
		} `json:"messages"`
	}
	if json.Unmarshal(req, &r) == nil {
		wc.RequestedModel, wc.ConversationID = r.Model, r.ConversationID
		if r.ParentMessageID != "" && r.ParentMessageID != "client-created-root" {
			e.PreviousMessageID = r.ParentMessageID
		}
		if r.TurnAttribution != nil {
			wc.Trigger = r.TurnAttribution.TurnTrigger
		}
		if r.ClientContextualInfo != nil {
			wc.Surface = r.ClientContextualInfo.AppSurface
		}
		// Files are uploaded first (see chatGPTUpload) and referenced by id.
		for _, m := range r.Messages {
			for _, a := range m.Metadata.Attachments {
				if e.Attachments == nil {
					e.Attachments = &Attachments{}
				}
				if strings.HasPrefix(a.MimeType, "image/") {
					e.Attachments.Images++
				} else {
					e.Attachments.Files++
				}
				e.Attachments.ByReference++
			}
		}
	}

	// Tool calls: each tool message (author.role "tool") is one call's result,
	// named by its author (web.run, functions.exec, …). Messages are re-sent
	// as they're patched, so count each message id once.
	toolMsgs := map[string]string{}
	var toolOrder []string
	forEachSSEData(resp, encoding, func(b []byte) {
		if len(b) == 0 || b[0] != '{' {
			return
		}
		var ev struct {
			Type           string          `json:"type"`
			ConversationID string          `json:"conversation_id"`
			Metadata       map[string]any  `json:"metadata"`
			Title          string          `json:"title"`
			LimitsProgress any             `json:"limits_progress"`
			V              json.RawMessage `json:"v"`
		}
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		if wc.ConversationID == "" {
			wc.ConversationID = ev.ConversationID
		}
		switch ev.Type {
		case "server_ste_metadata":
			wc.ServerMetadata = ev.Metadata
		case "title_generation":
			wc.Title = ev.Title
		case "conversation_detail_metadata":
			wc.Limits = ev.LimitsProgress
		}
		if len(ev.V) == 0 || ev.V[0] != '{' {
			return
		}
		var v struct {
			ConversationID string `json:"conversation_id"`
			Message        *struct {
				ID     string `json:"id"`
				Author struct {
					Role string `json:"role"`
					Name string `json:"name"`
				} `json:"author"`
				Metadata struct {
					FinishDetails *struct {
						Type string `json:"type"`
					} `json:"finish_details"`
				} `json:"metadata"`
			} `json:"message"`
		}
		if json.Unmarshal(ev.V, &v) != nil || v.Message == nil {
			return
		}
		if wc.ConversationID == "" {
			wc.ConversationID = v.ConversationID
		}
		m := v.Message
		switch m.Author.Role {
		case "tool":
			if _, seen := toolMsgs[m.ID]; !seen && m.Author.Name != "" {
				toolMsgs[m.ID] = m.Author.Name
				toolOrder = append(toolOrder, m.ID)
			}
		case "assistant":
			e.MessageID = m.ID
			if fd := m.Metadata.FinishDetails; fd != nil && fd.Type != "" {
				e.StopReason = fd.Type
			}
		}
	})
	if len(toolOrder) > 0 {
		counts := map[string]int{}
		var names []string
		for _, id := range toolOrder {
			n := toolMsgs[id]
			if counts[n] == 0 {
				names = append(names, n)
			}
			counts[n]++
		}
		e.ToolUses = nil
		for _, n := range names {
			srv, tool, kind := ClassifyTool(n)
			e.ToolUses = append(e.ToolUses, ToolCallCount{Name: n, Server: srv, Tool: tool, Kind: kind, Count: counts[n]})
		}
	}
	md := wc.ServerMetadata
	str := func(k string) string { s, _ := md[k].(string); return s }
	if s := str("model_slug"); s != "" {
		e.Model = s
	}
	wc.PlanType = str("plan_type")
	if n, ok := md["search_tool_call_count"].(float64); ok {
		wc.SearchCalls = int(n)
	}
	e.WebChat = wc
}

// chatGPTUpload reads POST /backend-api/files: the request names the file,
// the response gives its id (the bytes then go by PUT to the returned
// *.oaiusercontent.com upload URL).
func chatGPTUpload(req, resp []byte, encoding string) *WebChatUpload {
	var r struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
		MimeType string `json:"mime_type"`
		UseCase  string `json:"use_case"`
	}
	if json.Unmarshal(req, &r) != nil {
		return nil
	}
	u := &WebChatUpload{Name: r.FileName, Size: r.FileSize, MimeType: r.MimeType, UseCase: r.UseCase}
	if raw, err := decodeBody(resp, encoding); err == nil {
		var out struct {
			FileID string `json:"file_id"`
		}
		if json.Unmarshal(raw, &out) == nil {
			u.FileID = out.FileID
		}
	}
	return u
}

// isChatGPTWebAnalyticsPath matches ChatGPT web's analytics endpoints.
func isChatGPTWebAnalyticsPath(p string) bool {
	switch p {
	case "/ces/v1/rgstr", "/ces/v1/telemetry/intake", "/ces/statsc/flush":
		return true
	}
	return false
}

// parseChatGPTWebAnalytics reads one ChatGPT web analytics request:
//
//   - /ces/v1/rgstr: Statsig {"events":[{eventName, user, metadata, value, time}]}.
//     Structured product events (eventName "__protobuf_structured_event__")
//     are named by their protobuf type (metadata.eventParams["@type"], e.g.
//     ChatgptFileUploadInitiated); Statsig's own are named as sent
//     (statsig::gate_exposure, …). Every event carries the signed-in user
//     (userID, email, account/device ids, plan), promoted onto the event.
//   - /ces/v1/telemetry/intake: newline-delimited JSON logs, named
//     "<logger>:<kind or message>" (turn-analytics:Turn exchange complete).
//   - /ces/statsc/flush: {"counters":[…],"histograms":[…]}, one event per
//     metric, named "<namespace>.<metric>".
func parseChatGPTWebAnalytics(path string, raw []byte) []ClientEvent {
	switch path {
	case "/ces/v1/rgstr":
		return parseStatsigEvents(raw)
	case "/ces/v1/telemetry/intake":
		return parseIntakeLogs(raw)
	case "/ces/statsc/flush":
		return parseStatscFlush(raw)
	}
	return nil
}

func parseStatsigEvents(raw []byte) []ClientEvent {
	var batch struct {
		Events []map[string]any `json:"events"`
	}
	if json.Unmarshal(raw, &batch) != nil {
		return nil
	}
	out := make([]ClientEvent, 0, len(batch.Events))
	for _, d := range batch.Events {
		ce := ClientEvent{Type: "statsig", Data: d}
		ce.Name, _ = d["eventName"].(string)
		md, _ := d["metadata"].(map[string]any)
		if ce.Name == "__protobuf_structured_event__" {
			ce.Type = "structured"
			params, _ := md["eventParams"].(map[string]any)
			if t, _ := params["@type"].(string); t != "" {
				ce.Name = t[strings.LastIndexByte(t, '.')+1:]
			}
			ce.ID, _ = md["eventId"].(string)
			ce.Timestamp, _ = md["eventCreatedAt"].(string)
		}
		if user, ok := d["user"].(map[string]any); ok {
			ce.UserID, _ = user["userID"].(string)
			ce.Email, _ = user["email"].(string)
			if ids, ok := user["customIDs"].(map[string]any); ok {
				ce.AccountUUID, _ = ids["account_id"].(string)
				ce.DeviceID, _ = ids["stableID"].(string)
			}
		}
		out = append(out, ce)
	}
	return out
}

func parseIntakeLogs(raw []byte) []ClientEvent {
	var out []ClientEvent
	for _, line := range strings.Split(string(raw), "\n") {
		var d map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &d) != nil {
			continue
		}
		str := func(k string) string { s, _ := d[k].(string); return s }
		logger := ""
		if l, ok := d["logger"].(map[string]any); ok {
			logger, _ = l["name"].(string)
		}
		what := str("kind")
		if what == "" {
			what = str("message")
		}
		ce := ClientEvent{Type: "log", Name: logger + ":" + what, Data: d}
		if usr, ok := d["usr"].(map[string]any); ok {
			ce.UserID, _ = usr["user_id"].(string)
		}
		out = append(out, ce)
	}
	return out
}

func parseStatscFlush(raw []byte) []ClientEvent {
	var f struct {
		Counters   []map[string]any `json:"counters"`
		Histograms []map[string]any `json:"histograms"`
		ClientType string           `json:"client_type"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	var out []ClientEvent
	add := func(kind string, ms []map[string]any) {
		for _, m := range ms {
			if m == nil { // a JSON null element
				continue
			}
			ns, _ := m["namespace"].(string)
			metric, _ := m["metric"].(string)
			m["client_type"] = f.ClientType
			out = append(out, ClientEvent{Type: kind, Name: ns + "." + metric, Data: m})
		}
	}
	add("counter", f.Counters)
	add("histogram", f.Histograms)
	return out
}
