package records

import (
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// The record schema is the processor's input contract: a column can't change
// without this list (and Version) changing with it.
func TestColumnsPinned(t *testing.T) {
	want := []string{
		"record_version", "record_id", "kind", "instance", "call_id", "routing_key", "bucket",
		"batch_id", "batch_n", "batch_cc_n", "batch_session", "event_key",
		"call.call_id", "call.correlation_id", "call.source_file", "call.first_file", "call.ws_turn", "call.transport",
		"call.start_ns", "call.response_start_ns", "call.end_ns", "call.host", "call.method", "call.path", "call.status",
		"call.synthetic", "call.ip", "call.key_hash", "call.key_hint", "call.error",
		"call.session_id", "call.prompt_id", "call.person_id", "call.device_id", "call.org_id", "call.request_class",
		"call.client_request_id", "call.request_id", "call.agent_id", "call.agent_type", "call.user_agent",
		"call.model", "call.stop_reason", "call.message_id", "call.previous_message_id", "call.thread_type", "call.stream",
		"call.service_tier", "call.input_tokens", "call.output_tokens", "call.cache_read_tokens", "call.cache_write_tokens",
		"call.reasoning_tokens", "call.effort", "call.detail_json", "call.request_bytes", "call.response_bytes",
		"call.content_encoding", "call.client_json", "call.provider_json", "call.tool_uses_json",
		"call.product", "call.client", "call.client_version", "call.hidden_kind", "call.ext_json",
		"event.event_key", "event.correlation_id", "event.source_file", "event.row_ns", "event.ns", "event.seq",
		"event.source", "event.name", "event.type", "event.client_event_id", "event.session_id", "event.prompt_id",
		"event.person_id", "event.org_id", "event.device_id", "event.agent_id", "event.agent_type", "event.model",
		"event.request_id", "event.client_request_id", "event.tool_use_id", "event.prompt_index", "event.permission_mode",
		"event.head_sha", "event.metadata_json", "event.env_json", "event.batch_n",
		"account.account_id", "account.email", "account.name", "account.org", "account.ns",
	}
	var got []string
	for _, path := range parquet.SchemaOf(Record{}).Columns() {
		got = append(got, strings.Join(path, "."))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("record columns changed (bump Version and update this list):\n got %v\nwant %v", got, want)
	}
	if Version != 1 {
		t.Fatalf("Version = %d", Version)
	}
}

func TestEncodeDecodeRoundTrips(t *testing.T) {
	in := []Record{
		{Version: Version, RecordID: "pod-1-ab-000000000001", Kind: KindCallEnd, Instance: "pod", CallID: "c1", RoutingKey: "s:x", Bucket: 3},
		{Version: Version, RecordID: "pod-1-ab-000000000002", Kind: KindClientEvent, Instance: "pod", RoutingKey: "s:x", Bucket: 3,
			BatchID: "c2", BatchN: 1, EventKey: "pod-1-ab-000000000002"},
	}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].CallID != "c1" || out[1].BatchN != 1 || out[1].EventKey != in[1].EventKey {
		t.Fatalf("round trip: %+v", out)
	}
}

func TestIncarnationIsUniquePerStart(t *testing.T) {
	a, b := NewIncarnation("envoy-7b9c-2vbpd"), NewIncarnation("envoy-7b9c-2vbpd")
	if a == b || !strings.HasSuffix(a, "-envoy-7b9c-2vbpd") {
		t.Fatalf("incarnations %q %q", a, b)
	}
}
