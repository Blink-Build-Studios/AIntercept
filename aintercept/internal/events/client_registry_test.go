package events

import (
	"reflect"
	"testing"
)

func testRegistry() *clientRegistry {
	return loadClientRegistry([]byte(`{"common":["session_id","env.version"],
		"events":{"tengu_api_success":["additional_metadata.costUSD","event_name"]}}`))
}

func TestClientEventPaths(t *testing.T) {
	got := clientEventPaths(map[string]any{
		"event_name": "x",
		"env":        map[string]any{"version": "2.1.308", "is_ci": false},
		"additional_metadata": map[string]any{
			"attachment_types": []any{"environment", "queued_command"},
			"empty":            map[string]any{},
		},
	})
	want := []string{"additional_metadata.attachment_types[]", "env.is_ci", "env.version", "event_name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRegistryCheck(t *testing.T) {
	r := testRegistry()
	known := ClientEvent{Name: "tengu_api_success", Data: map[string]any{
		"event_name": "tengu_api_success", "session_id": "s", "env": map[string]any{"version": "v"},
		"additional_metadata": map[string]any{"costUSD": 0.1},
	}}
	if n, f := r.check(known); n || f != nil {
		t.Fatalf("known shape flagged: new=%v fields=%v", n, f)
	}
	grown := known
	grown.Data = map[string]any{"event_name": "tengu_api_success", "additional_metadata": map[string]any{"costUSD": 0.1, "newMetric": 3}}
	if n, f := r.check(grown); n || !reflect.DeepEqual(f, []string{"additional_metadata.newMetric"}) {
		t.Fatalf("new field: new=%v fields=%v", n, f)
	}
	if n, _ := r.check(ClientEvent{Name: "tengu_brand_new"}); !n {
		t.Fatal("unregistered event name must be new")
	}
}

func TestEmbeddedRegistriesLoad(t *testing.T) {
	cc := registries["claude_code"]
	if cc == nil || len(cc.events) == 0 || len(cc.common) == 0 {
		t.Fatal("embedded client_events/claude_code.json failed to load or is empty")
	}
	if registries["codex"] == nil {
		t.Fatal("embedded client_events/codex.json failed to load")
	}
}

func TestProcessor_SamplesNewClientEventOncePerShape(t *testing.T) {
	saved := registries["claude_code"]
	registries["claude_code"] = testRegistry()
	defer func() { registries["claude_code"] = saved }()

	main := &recordingSink{}
	schema := &recordingSink{}
	p := NewProcessor(main, ProcessorConfig{BufferSize: 16, SchemaSink: schema})
	batch := []byte(`{"events":[
	  {"event_type":"T","event_data":{"event_name":"tengu_brand_new","session_id":"s"}},
	  {"event_type":"T","event_data":{"event_name":"tengu_brand_new","session_id":"s"}},
	  {"event_type":"T","event_data":{"event_name":"tengu_api_success","event_id":"x"}}]}`)
	for i := 0; i < 2; i++ {
		e := Event{Phase: PhaseComplete, Method: "POST", Host: "api.anthropic.com", Path: "/api/event_logging/v2/batch"}
		e.AttachBodies(batch, []byte(`{}`), "", nil)
		p.Offer(e)
	}
	p.Close()
	got := schema.events()
	if len(got) != 2 {
		t.Fatalf("want 2 samples (new event once, new field once), got %d", len(got))
	}
	byReason := map[string]Event{}
	for _, s := range got {
		byReason[s.SchemaFingerprint.Reason] = s
	}
	if s := byReason["new_client_event"]; s.Path != "/api/event_logging/v2/batch#tengu_brand_new" {
		t.Fatalf("new event sample path: %q", s.Path)
	}
	if s := byReason["new_client_event_fields"]; !reflect.DeepEqual(s.SchemaFingerprint.NewFields, []string{"event_id"}) {
		t.Fatalf("new fields sample: %+v", s.SchemaFingerprint)
	}
	rows := 0
	for _, r := range main.events() {
		if r.Phase == PhaseClientEvent {
			rows++
			if r.ClientEvent.Name == "tengu_brand_new" && !r.ClientEvent.NewEvent {
				t.Fatal("row must be marked new_event")
			}
		}
	}
	if rows != 6 {
		t.Fatalf("every client event is still recorded: want 6 rows, got %d", rows)
	}
}
