package events

import (
	"encoding/json"
	"strconv"
	"strings"
)

// RequestMeta is the content-free metadata of a model request: settings and
// conversation linkage, never prompt text.
type RequestMeta struct {
	Stream       bool
	MaxTokens    int
	Effort       string // output_config.effort (Anthropic) / reasoning.effort (OpenAI)
	ThinkingType string // thinking.type (Anthropic)
	ThreadType   string // thread.type (Anthropic message threads)
	// PreviousMessageID links a threaded request to the response it continues
	// (that response's MessageID), so turns chain into a conversation.
	PreviousMessageID string
	// UserID is metadata.user_id (Anthropic), parsed into device/account/session.
	UserID ClientUserID
	// Prewarm: generate:false (a warm-up request that produces no output).
	Prewarm bool
}

// RequestMetaFromBody reads RequestMeta from a JSON request body. Unknown or
// absent fields stay zero; any provider's body is accepted.
func RequestMetaFromBody(body []byte) RequestMeta {
	var r struct {
		Stream          bool `json:"stream"`
		MaxTokens       int  `json:"max_tokens"`
		MaxOutputTokens int  `json:"max_output_tokens"` // OpenAI Responses
		OutputConfig    *struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Thread *struct {
			Type              string `json:"type"`
			PreviousMessageID string `json:"previous_message_id"`
		} `json:"thread"`
		PreviousResponseID string `json:"previous_response_id"` // OpenAI Responses
		Metadata           *struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		Generate *bool `json:"generate"`
	}
	var m RequestMeta
	if json.Unmarshal(body, &r) != nil {
		return m
	}
	m.Stream = r.Stream
	m.MaxTokens = r.MaxTokens
	if m.MaxTokens == 0 {
		m.MaxTokens = r.MaxOutputTokens
	}
	if r.OutputConfig != nil {
		m.Effort = r.OutputConfig.Effort
	}
	if m.Effort == "" && r.Reasoning != nil {
		m.Effort = r.Reasoning.Effort
	}
	if r.Thinking != nil {
		m.ThinkingType = r.Thinking.Type
	}
	if r.Thread != nil {
		m.ThreadType = r.Thread.Type
		m.PreviousMessageID = r.Thread.PreviousMessageID
	}
	if m.PreviousMessageID == "" {
		m.PreviousMessageID = r.PreviousResponseID
	}
	m.Prewarm = r.Generate != nil && !*r.Generate
	if r.Metadata != nil && r.Metadata.UserID != "" {
		m.UserID = ParseClientUserID(r.Metadata.UserID)
	}
	return m
}

// ResponseMeta is the content-free metadata of a model response.
type ResponseMeta struct {
	Model        string // the model that served the response
	Effort       string // OpenAI Responses reasoning.effort, as echoed back
	MessageID    string // the response's id (Anthropic msg_…, OpenAI chatcmpl-…/resp_…)
	StopReason   string // why generation stopped: end_turn, tool_use, max_tokens, refusal, stop, …
	ServiceTier  string
	InferenceGeo string
	Speed        string
}

// ResponseMetaFromBody reads ResponseMeta from a (possibly compressed, possibly
// SSE) response body. Later frames fill fields earlier ones left empty.
func ResponseMetaFromBody(body []byte, encoding string) ResponseMeta {
	var m ResponseMeta
	raw, err := decodeBody(body, encoding)
	if err != nil || len(raw) == 0 {
		return m
	}
	text := string(raw)
	if strings.Contains(text, "data:") {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				m.fold([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
			}
		}
	} else {
		m.fold(raw)
	}
	return m
}

type metaUsage struct {
	ServiceTier  string `json:"service_tier"`
	InferenceGeo string `json:"inference_geo"`
	Speed        string `json:"speed"`
}

func (m *ResponseMeta) fold(b []byte) {
	var obj struct {
		ID          string     `json:"id"`
		Model       string     `json:"model"`
		StopReason  string     `json:"stop_reason"`
		ServiceTier string     `json:"service_tier"` // OpenAI
		Usage       *metaUsage `json:"usage"`
		Message     *struct {  // Anthropic SSE message_start
			ID    string     `json:"id"`
			Model string     `json:"model"`
			Usage *metaUsage `json:"usage"`
		} `json:"message"`
		Delta *struct { // Anthropic SSE message_delta
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Choices []struct { // OpenAI Chat
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Response *struct { // OpenAI Responses SSE (response.created / .completed)
			ID          string `json:"id"`
			Model       string `json:"model"`
			Status      string `json:"status"`
			ServiceTier string `json:"service_tier"`
			Reasoning   *struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
		Type string `json:"type"`
	}
	if len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &obj) != nil {
		return
	}
	set := func(dst *string, v string) {
		if *dst == "" && v != "" {
			*dst = v
		}
	}
	if obj.Message != nil {
		set(&m.MessageID, obj.Message.ID)
		set(&m.Model, obj.Message.Model)
		if u := obj.Message.Usage; u != nil {
			set(&m.ServiceTier, u.ServiceTier)
			set(&m.InferenceGeo, u.InferenceGeo)
			set(&m.Speed, u.Speed)
		}
	}
	if r := obj.Response; r != nil {
		set(&m.MessageID, r.ID)
		set(&m.Model, r.Model)
		set(&m.ServiceTier, r.ServiceTier)
		if r.Reasoning != nil {
			set(&m.Effort, r.Reasoning.Effort)
		}
		// Responses API: the terminal event's status is the stop reason
		// (completed, failed, incomplete:<reason>, e.g. after an interrupt).
		switch obj.Type {
		case "response.completed", "response.failed", "response.incomplete":
			reason := r.Status
			if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
				reason += ":" + r.IncompleteDetails.Reason
			}
			set(&m.StopReason, reason)
		}
	}
	set(&m.MessageID, obj.ID)
	set(&m.Model, obj.Model)
	set(&m.StopReason, obj.StopReason)
	if obj.Delta != nil {
		set(&m.StopReason, obj.Delta.StopReason)
	}
	for _, c := range obj.Choices {
		set(&m.StopReason, c.FinishReason)
	}
	set(&m.ServiceTier, obj.ServiceTier)
	if u := obj.Usage; u != nil {
		set(&m.ServiceTier, u.ServiceTier)
		set(&m.InferenceGeo, u.InferenceGeo)
		set(&m.Speed, u.Speed)
	}
}

// clientHeaders are request headers whose values identify the calling client,
// session and agent. They are identifiers, not secrets, and are copied onto
// the complete event as Client. Add a client's headers here as they appear.
var clientHeaders = []string{
	"user-agent", "x-app", "anthropic-client-platform",
	"x-claude-code-session-id", "x-claude-code-agent-id", "x-claude-code-agent-type",
	"x-claude-code-request-class", "x-claude-code-prompt-id", "x-client-request-id",
	"x-organization-uuid",
	"x-stainless-os", "x-stainless-arch", "x-stainless-lang",
	"x-stainless-package-version", "x-stainless-runtime", "x-stainless-runtime-version",
	"originator", "session_id", "session-id", "thread-id", "version", // Codex
	"chatgpt-account-id", "x-codex-window-id", "x-codex-routing-hint", "x-codex-beta-features",
	"x-codex-turn-state", "oai-product-sku", "x-openai-product-sku", "openai-beta",
	"openai-organization", "openai-project", "x-goog-api-client",
}

// providerHeaders are response headers worth keeping on the complete event as
// Provider: workspace/request identity and account rate-limit state.
var providerHeaders = []string{
	"request-id", "x-request-id", "anthropic-workspace-id",
	"anthropic-ratelimit-unified-status", "anthropic-ratelimit-unified-representative-claim",
	"anthropic-ratelimit-unified-5h-utilization", "anthropic-ratelimit-unified-7d-utilization",
	"anthropic-ratelimit-unified-fallback-percentage", "anthropic-ratelimit-unified-overage-status",
	"openai-processing-ms", "x-ratelimit-remaining-requests", "x-ratelimit-remaining-tokens",
	"x-codex-plan-type", "x-codex-primary-used-percent", "x-codex-secondary-used-percent",
	"x-codex-primary-window-minutes", "x-codex-secondary-window-minutes",
	"x-codex-primary-reset-at", "x-codex-secondary-reset-at", "x-codex-active-limit",
	"x-codex-credits-balance", "x-codex-credits-has-credits", "x-codex-credits-unlimited",
	"x-oai-request-id",
}

// ProviderHeaderValues returns the allowlisted response header values
// (providerHeaders) present in hdrs: what a complete row carries as Provider.
func ProviderHeaderValues(hdrs map[string]string) map[string]string {
	return pickHeaders(hdrs, providerHeaders)
}

// pickHeaders returns the allowlisted header values present in hdrs. A header
// in the sensitive set is never returned, even if allowlisted by mistake.
// ClientHeaders is the allowlisted client header values of a request's headers.
func ClientHeaders(hdrs map[string]string) map[string]string { return pickHeaders(hdrs, clientHeaders) }

func pickHeaders(hdrs map[string]string, names []string) map[string]string {
	var out map[string]string
	for _, n := range names {
		if IsSensitiveHeader(n) {
			continue
		}
		for k, v := range hdrs {
			if strings.EqualFold(k, n) && v != "" && v != redactedMarker {
				if out == nil {
					out = map[string]string{}
				}
				out[n] = v
			}
		}
	}
	return out
}

// inBandProviderMeta reads, from a WebSocket turn's server events, the
// account and rate-limit state that the HTTP transport carries as response
// headers, keyed by those header names so both transports fill the same
// provider fields:
//   - codex.rate_limits: plan type and primary/secondary window usage.
//   - codex.response.metadata: an allowlisted subset of its "headers".
func inBandProviderMeta(sse []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(sse), "\n") {
		if !strings.HasPrefix(line, "data: {\"type\":\"codex.") {
			continue
		}
		var ev struct {
			Type       string            `json:"type"`
			PlanType   string            `json:"plan_type"`
			Headers    map[string]string `json:"headers"`
			RateLimits *struct {
				Primary, Secondary *struct {
					UsedPercent   *float64 `json:"used_percent"`
					WindowMinutes *int     `json:"window_minutes"`
					ResetAt       *int64   `json:"reset_at"`
				}
			} `json:"rate_limits"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "codex.rate_limits":
			if ev.PlanType != "" {
				out["x-codex-plan-type"] = ev.PlanType
			}
			if rl := ev.RateLimits; rl != nil {
				for name, w := range map[string]*struct {
					UsedPercent   *float64 `json:"used_percent"`
					WindowMinutes *int     `json:"window_minutes"`
					ResetAt       *int64   `json:"reset_at"`
				}{"primary": rl.Primary, "secondary": rl.Secondary} {
					if w == nil {
						continue
					}
					if w.UsedPercent != nil {
						out["x-codex-"+name+"-used-percent"] = strconv.FormatFloat(*w.UsedPercent, 'f', -1, 64)
					}
					if w.WindowMinutes != nil {
						out["x-codex-"+name+"-window-minutes"] = strconv.Itoa(*w.WindowMinutes)
					}
					if w.ResetAt != nil {
						out["x-codex-"+name+"-reset-at"] = strconv.FormatInt(*w.ResetAt, 10)
					}
				}
			}
		case "codex.response.metadata":
			for k, v := range pickHeaders(ev.Headers, providerHeaders) {
				out[k] = v
			}
		}
	}
	return out
}
