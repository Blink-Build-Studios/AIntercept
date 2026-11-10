package events

import (
	"embed"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
)

// The client-event registries are what we already understand about each
// client's analytics events: their names and field paths, one file per client
// (client_events/<source>.json). An event whose name isn't registered, or that
// carries fields the registry doesn't list, is new (marked on its row and
// sampled to schema-samples/), so client changes surface in the same backlog
// as unrecognized request shapes. Regenerate with `make client-event-registry`
// after reviewing the samples.
//
//go:embed client_events/*.json
var clientRegistryFiles embed.FS

type clientRegistry struct {
	// Common lists field paths every event may carry (env, auth, …).
	Common []string            `json:"common"`
	Events map[string][]string `json:"events"`

	common map[string]bool
	events map[string]map[string]bool
}

// registries maps a client source ("claude_code", "codex") to its registry.
var registries = loadClientRegistries()

func loadClientRegistries() map[string]*clientRegistry {
	out := map[string]*clientRegistry{}
	entries, _ := fs.ReadDir(clientRegistryFiles, "client_events")
	for _, e := range entries {
		b, err := clientRegistryFiles.ReadFile("client_events/" + e.Name())
		if err == nil {
			out[strings.TrimSuffix(e.Name(), ".json")] = loadClientRegistry(b)
		}
	}
	return out
}

// checkClientEvent checks ce against its source's registry; a source with no
// registry treats every event as new.
func checkClientEvent(ce ClientEvent) (newEvent bool, newFields []string) {
	r, ok := registries[ce.Source]
	if !ok {
		return true, nil
	}
	return r.check(ce)
}

func loadClientRegistry(b []byte) *clientRegistry {
	r := &clientRegistry{}
	if json.Unmarshal(b, r) != nil {
		r = &clientRegistry{}
	}
	r.common = toSet(r.Common)
	r.events = make(map[string]map[string]bool, len(r.Events))
	for name, paths := range r.Events {
		r.events[name] = toSet(paths)
	}
	return r
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// check reports whether ce is an unregistered event, and which of its field
// paths are new for its name.
func (r *clientRegistry) check(ce ClientEvent) (newEvent bool, newFields []string) {
	known, ok := r.events[ce.Name]
	if !ok {
		return true, nil
	}
	for _, p := range clientEventPaths(ce.Data) {
		if !known[p] && !r.common[p] {
			newFields = append(newFields, p)
		}
	}
	return false, newFields
}

// maxClientEventPaths bounds the paths walked per event.
const maxClientEventPaths = 500

// clientEventPaths lists an event's leaf field paths: nested keys joined with
// ".", array elements as "[]" (so "attachment_types[]"), sorted and unique.
func clientEventPaths(data map[string]any) []string {
	seen := map[string]bool{}
	var walk func(v any, p string)
	walk = func(v any, p string) {
		if len(seen) >= maxClientEventPaths {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if p == "" {
					walk(c, k)
				} else {
					walk(c, p+"."+k)
				}
			}
		case []any:
			for _, c := range x {
				walk(c, p+"[]")
			}
		default:
			if p != "" {
				seen[p] = true
			}
		}
	}
	walk(data, "")
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// clientEventSample is the structure-only schema sample for a new client
// event shape. Its Path carries the event name ("…/batch#tengu_x") so the
// schema report lists each event separately.
func clientEventSample(row Event) Event {
	ce := row.ClientEvent
	reason := "new_client_event_fields"
	if ce.NewEvent {
		reason = "new_client_event"
	}
	n := 0
	sk, _ := json.Marshal(skeleton(map[string]any(ce.Data), 0, &n))
	return Event{
		CorrelationID:    row.CorrelationID,
		TimestampNS:      row.TimestampNS,
		Phase:            PhaseComplete,
		Host:             row.Host,
		Method:           row.Method,
		Path:             row.Path + "#" + ce.Name,
		Status:           row.Status,
		KeyHash:          row.KeyHash,
		KeyHint:          row.KeyHint,
		ObserverInstance: row.ObserverInstance,
		SchemaFingerprint: &SchemaFingerprint{
			Reason:    reason,
			NewFields: ce.NewFields,
			Request: &BodyShape{
				Kind:      "json",
				Skeleton:  sk,
				Truncated: n >= maxFingerprintNodes,
			},
		},
	}
}

// clientShapeKey identifies a new client-event shape for deduplication.
func clientShapeKey(ce *ClientEvent) string {
	return ce.Source + "|" + ce.Name + "|" + strings.Join(ce.NewFields, ",")
}
