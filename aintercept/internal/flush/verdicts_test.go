package flush

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

func verdict(call string, seq int64, action string) records.Record {
	return records.Record{Kind: records.KindVerdict, CallID: call, Verdict: &records.Verdict{
		Seq: seq, NS: t0 + seq, Callback: "request", Action: action, Applied: true, Mode: "enforce",
		MessageID: "m-1", Reason: "", AnnotationsJSON: `{"rule":"rabbit"}`, LatencyMS: 3,
	}}
}

// Verdicts are keyed by their call and seq: a resent record is one row.
func TestCollapseVerdicts(t *testing.T) {
	rows := Collapse([]records.Record{verdict("c2", 7, "replace"), verdict("c1", 9, "respond"), verdict("c2", 7, "replace"), verdict("c1", 3, "error")})
	if len(rows.Verdicts) != 3 || rows.Verdicts[0].CallID != "c1" || rows.Verdicts[0].V.Seq != 3 || rows.Verdicts[2].CallID != "c2" {
		t.Fatalf("verdicts %+v", rows.Verdicts)
	}
	if rows.LastNS != t0+9 {
		t.Fatalf("last %d", rows.LastNS)
	}
}

// The verdicts table holds every field, written by the same flush as the
// call; loading the same records again changes nothing.
func TestVerdictsAreWritten(t *testing.T) {
	db := fresh(t)
	v := verdict("c1", 4, "respond")
	v.Verdict.RespondMode, v.Verdict.Mode, v.Verdict.Applied = "synthesized", "observe", false
	e := verdict("c1", 5, "error")
	e.Verdict.Reason, e.Verdict.AnnotationsJSON = "deadline exceeded", ""
	for range 2 {
		write(t, db, end("c1", "s1", t0, t0+5), v, e)
	}
	rows, err := db.Query(ctx(t), `SELECT call_id, seq, ts_ns, callback, action, applied, mode, coalesce(message_id, ''), coalesce(respond_mode, ''),
		coalesce(reason, ''), coalesce(annotations::text, ''), latency_ms FROM verdicts ORDER BY call_id, seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var call, cb, action, mode, msg, rm, reason, ann string
		var seq, ns, lat int64
		var applied bool
		if err := rows.Scan(&call, &seq, &ns, &cb, &action, &applied, &mode, &msg, &rm, &reason, &ann, &lat); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %d %d %s %s %v %s %s %s %q %s %d", call, seq, ns-t0, cb, action, applied, mode, msg, rm, reason, ann, lat))
	}
	want := []string{
		`c1 4 4 request respond false observe m-1 synthesized "" {"rule": "rabbit"} 3`,
		`c1 5 5 request error true enforce m-1  "deadline exceeded"  3`,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The bound per call holds in the table too, across flushes (the
// assembler's count is per observer, and only while it holds the call).
func TestVerdictsBoundedPerCall(t *testing.T) {
	db := fresh(t)
	var rs []records.Record
	for i := range records.MaxVerdictsPerCall {
		rs = append(rs, verdict("c1", int64(i+1), "replace"))
	}
	write(t, db, rs...)
	write(t, db, verdict("c1", int64(records.MaxVerdictsPerCall+1), "replace"), verdict("c2", 1, "replace"))
	var c1, c2 int
	if err := db.QueryRow(ctx(t), `SELECT count(*) FILTER (WHERE call_id = 'c1'), count(*) FILTER (WHERE call_id = 'c2') FROM verdicts`).Scan(&c1, &c2); err != nil {
		t.Fatal(err)
	}
	if c1 != records.MaxVerdictsPerCall || c2 != 1 {
		t.Fatalf("c1 %d c2 %d", c1, c2)
	}
}
