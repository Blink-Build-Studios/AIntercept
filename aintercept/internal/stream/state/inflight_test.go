package state

import (
	"reflect"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// A call still in flight is in the request log as today's processor holds it:
// its pending row, built from every header ingest saw (the client headers,
// response headers, bytes so far), not only the Claude Code ones. A Codex call
// with no thread id in flight is a transaction: its originator header says so.

func inflight(c *canon.Request, clientJSON string) records.Record {
	x := *c
	x.EndNS, x.Status, x.HiddenKind, x.OutputTokens = 0, 0, "", 0
	x.ClientJSON = clientJSON
	return rec(records.Record{Kind: records.KindCallStart, CallID: c.CallID, Call: &x})
}

// todaysPending is the call as today's processor holds it in flight (pending):
// the start's own fields, every client header, what it had of the response.
func todaysPending(c *canon.Request, clientJSON string) canon.Pending {
	return canon.Pending{CorrelationID: c.CallID, SeenNS: c.StartNS, StartNS: c.StartNS, ResponseStartNS: c.ResponseStartNS,
		Host: c.Host, Method: c.Method, Path: c.Path, Synthetic: c.Synthetic, KeyHash: c.KeyHash, KeyHint: c.KeyHint,
		IP: c.IP, RequestID: c.RequestID, RequestBytes: c.RequestBytes, ResponseBytes: c.ResponseBytes,
		ContentEncoding: c.ContentEncoding, ProviderJSON: c.ProviderJSON, ClientJSON: clientJSON}
}

func TestInflightStartIsTodaysTransaction(t *testing.T) {
	cases := []struct {
		name       string
		call       *canon.Request
		clientJSON string
		isTx       bool
	}{
		{"a Codex call with no thread yet (its originator makes it Codex)",
			&canon.Request{CallID: "cx1", CorrelationID: "cx1", StartNS: 5 * ms, Host: "chatgpt.com", Method: "POST",
				Path: "/backend-api/codex/responses", KeyHash: "k2", IP: "10.0.0.9", RequestBytes: 2048},
			`{"originator":"codex_cli_rs","user-agent":"codex_cli_rs/0.40"}`, true},
		{"a Claude Code call with its client headers",
			&canon.Request{CallID: "cc1", CorrelationID: "cc1", StartNS: 7 * ms, Host: "api.anthropic.com", Method: "POST",
				Path: "/v1/messages", KeyHash: "k1", SessionID: "s1", IP: "10.0.0.8", ResponseStartNS: 8 * ms,
				ProviderJSON: `{"anthropic-ratelimit-unified-status":"allowed"}`},
			`{"x-claude-code-session-id":"s1","user-agent":"claude-cli/2.0.1 (external, cli)","x-app":"cli"}`, true},
		{"a Codex WebSocket connection's opening GET (never a transaction)",
			&canon.Request{CallID: "cx2", CorrelationID: "cx2", StartNS: 9 * ms, Host: "chatgpt.com", Method: "GET",
				Path: "/backend-api/codex/responses", KeyHash: "k2"},
			`{"originator":"codex_cli_rs"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := inflight(tc.call, tc.clientJSON)
			s := New(r.RoutingKey)
			d := s.Apply(r)
			var got *ReqTx
			for _, c := range d.Changes {
				if c.Table == TableRequestTxs && c.Row != nil {
					x := c.Row.(ReqTx)
					got = &x
				}
			}
			want := process.TxOfPending(todaysPending(tc.call, tc.clientJSON))
			if got == nil {
				t.Fatalf("no request_txs row for the call in flight")
			}
			if got.IsTx != tc.isTx || got.IsTx != want.IsTransaction() {
				t.Fatalf("is_tx %v, today's processor %v (want %v)", got.IsTx, want.IsTransaction(), tc.isTx)
			}
			if w := want.Row(nil, nil); !reflect.DeepEqual(got.Row, w) {
				t.Fatalf("row differs from today's in-flight row:\n got %+v\nwant %+v", got.Row, w)
			}
			// A duplicate is a no-op.
			if d2 := s.Apply(r); len(d2.Changes) != 0 {
				t.Fatalf("a duplicate changed %d rows", len(d2.Changes))
			}
		})
	}
}
