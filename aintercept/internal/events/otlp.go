package events

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseOTLPMetrics reads an OTLP/HTTP JSON metrics export
// ({"resourceMetrics":[…]}), e.g. Codex's posts to ab.chatgpt.com/otlp/v1/metrics,
// into one ClientEvent per metric per export. Data holds:
//
//	kind        sum | gauge | histogram | exponentialHistogram | summary
//	unit, description
//	resource    resource attributes (service.name, os, …)
//	scope       instrumentation scope name/version
//	points[]    {attributes{…}, value | count/sum/min/max/bucketCounts/explicitBounds, startTimeUnixNano, timeUnixNano}
//
// OTLP's typed attribute values ({"stringValue": …}) are flattened to plain
// values. Protobuf-encoded exports aren't parsed (nil).
func parseOTLPMetrics(raw []byte) []ClientEvent {
	var exp struct {
		ResourceMetrics []struct {
			Resource struct {
				Attributes []otlpKV `json:"attributes"`
			} `json:"resource"`
			ScopeMetrics []struct {
				Scope   map[string]any               `json:"scope"`
				Metrics []map[string]json.RawMessage `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if json.Unmarshal(raw, &exp) != nil {
		return nil
	}
	var out []ClientEvent
	for _, rm := range exp.ResourceMetrics {
		resource := otlpAttrs(rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				out = append(out, otlpMetric(m, resource, sm.Scope))
			}
		}
	}
	return out
}

func otlpMetric(m map[string]json.RawMessage, resource map[string]any, scope map[string]any) ClientEvent {
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	data := map[string]any{"unit": str("unit"), "description": str("description"), "resource": resource}
	if len(scope) > 0 {
		data["scope"] = scope
	}
	for _, kind := range []string{"sum", "gauge", "histogram", "exponentialHistogram", "summary"} {
		body, ok := m[kind]
		if !ok {
			continue
		}
		data["kind"] = kind
		var k struct {
			DataPoints             []map[string]json.RawMessage `json:"dataPoints"`
			AggregationTemporality any                          `json:"aggregationTemporality"`
			IsMonotonic            *bool                        `json:"isMonotonic"`
		}
		_ = json.Unmarshal(body, &k)
		if k.AggregationTemporality != nil {
			data["temporality"] = k.AggregationTemporality
		}
		if k.IsMonotonic != nil {
			data["monotonic"] = *k.IsMonotonic
		}
		points := make([]any, 0, len(k.DataPoints))
		for _, dp := range k.DataPoints {
			points = append(points, otlpPoint(dp))
		}
		data["points"] = points
		break
	}
	ce := ClientEvent{Type: "otel_metric", Name: str("name"), Data: data}
	// Surface a model / service when the points carry one.
	if pts, _ := data["points"].([]any); len(pts) > 0 {
		if p, _ := pts[0].(map[string]any); p != nil {
			if a, _ := p["attributes"].(map[string]any); a != nil {
				ce.Model, _ = a["model"].(string)
				ce.AgentType, _ = a["session_source"].(string)
			}
		}
	}
	return ce
}

// otlpPoint flattens one data point: attributes to a plain map, asInt/asDouble
// to "value", int64-as-string counters to numbers.
func otlpPoint(dp map[string]json.RawMessage) map[string]any {
	p := map[string]any{}
	for k, v := range dp {
		switch k {
		case "attributes":
			var kvs []otlpKV
			_ = json.Unmarshal(v, &kvs)
			p["attributes"] = otlpAttrs(kvs)
		case "asInt", "asDouble":
			p["value"] = otlpNumber(v)
		case "count", "sum", "min", "max", "startTimeUnixNano", "timeUnixNano":
			p[k] = otlpNumber(v)
		default:
			var x any
			_ = json.Unmarshal(v, &x)
			p[k] = x
		}
	}
	return p
}

// otlpNumber decodes a number that proto3 JSON may encode as a string (int64).
func otlpNumber(raw json.RawMessage) any {
	var x any
	_ = json.Unmarshal(raw, &x)
	if s, ok := x.(string); ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return x
}

type otlpKV struct {
	Key   string                     `json:"key"`
	Value map[string]json.RawMessage `json:"value"`
}

// otlpAttrs flattens OTLP attributes: {"key":k,"value":{"stringValue":v}} → k: v.
func otlpAttrs(kvs []otlpKV) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		for typ, v := range kv.Value {
			switch typ {
			case "intValue", "doubleValue":
				out[kv.Key] = otlpNumber(v)
			default: // stringValue, boolValue, arrayValue, kvlistValue, bytesValue
				var x any
				_ = json.Unmarshal(v, &x)
				out[kv.Key] = x
			}
		}
	}
	return out
}

// isOTLPMetricsPath matches OTLP/HTTP metrics endpoints (/v1/metrics, /otlp/v1/metrics).
func isOTLPMetricsPath(p string) bool {
	return strings.HasSuffix(strings.SplitN(p, "?", 2)[0], "/v1/metrics")
}
