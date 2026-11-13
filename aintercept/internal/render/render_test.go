package render

import (
	"math"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// The render's pieces, unit by unit. Its layers (every state case, every order
// of short sequences, generated sequences through a cold load) run in
// internal/stream/state's rendered_layers_test.go, on the state harness.

// BatchOf is the person summary's regexp_extract(event_id, '^(.*):[0-9]+$', 1):
// RE2 both, so Go's regexp is DuckDB's.
func TestBatchOfIsTheSummarysRegexp(t *testing.T) {
	re := regexp.MustCompile(`^(.*):[0-9]+$`)
	ids := []string{"b:1", "b:12", "a:b:3", "b:", ":1", "b", "", "b:1a", "b:a1", "b:1:", "x\n:1", "x:\n1", "x:1\n",
		"b::2", "é:9", "b:٣", "b: 1", "sha256:ff:0", "b:00"}
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", ":", "1", "9", "\n", "é", " ", "0"}
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		for n := rng.Intn(7); n > 0; n-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		ids = append(ids, b.String())
	}
	for _, id := range ids {
		want := ""
		if m := re.FindStringSubmatch(id); m != nil {
			want = m[1]
		}
		if got := BatchOf(id); got != want {
			t.Fatalf("BatchOf(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestDayNumberAndHour(t *testing.T) {
	for _, ns := range []int64{0, 1, 86_400e9 - 1, 86_400e9, 1_793_779_200_000_000_000, -1, -86_400e9, math.MaxInt64} {
		if got, want := DayNumber(ns), int64(math.Floor(float64(ns)/1e9/86400)); got != want {
			t.Errorf("DayNumber(%d) = %d, want %d", ns, got, want)
		}
	}
	for ns, want := range map[int64]int64{0: 0, 3600e9 - 1: 0, 3600e9: 3600e9, 3600e9 + 5: 3600e9, -1: -3600e9, -3600e9: -3600e9} {
		if got := HourNS(ns); got != want {
			t.Errorf("HourNS(%d) = %d, want %d", ns, got, want)
		}
	}
}

// A column's value is today's: a nil pointer and an optional field's zero are
// NULL, a required field's zero isn't, and a list is never NULL.
func TestCellsAreTodaysNulls(t *testing.T) {
	e := encOf("event_rows")
	pi := int64(0)
	row := process.EventRow{SessionID: "s", Marker: "", HeadSHA: "abc", PromptIndex: &pi, Name: ""}
	v := reflect.ValueOf(row)
	got := map[string]any{}
	for _, c := range e.cols {
		if c.path != nil {
			got[c.name] = fieldCell(v, &c).value(c.kind)
		}
	}
	for col, want := range map[string]any{"marker": nil, "head_sha": "abc", "prompt_index": int64(0), "pr": nil,
		"name": "", "internal": false, "seq": int64(0)} {
		if !reflect.DeepEqual(got[col], want) {
			t.Errorf("%s = %#v, want %#v", col, got[col], want)
		}
	}
	steps := encOf("prompt_steps")
	for _, c := range steps.cols {
		if c.name == "commands" {
			if v := fieldCell(reflect.ValueOf(process.PromptStep{}), &c).value(c.kind); !reflect.DeepEqual(v, []string{}) {
				t.Errorf("a nil list is %#v, want an empty one", v)
			}
		}
	}
	sr := encOf("session_request_rows")
	for _, c := range sr.cols {
		if c.name == "tool_calls" && (len(c.path) != 2 || c.kind != kTextList) {
			t.Errorf("session_request_rows.tool_calls: path %v kind %d", c.path, c.kind)
		}
	}
}

func TestPrimaryKeysRoundTrip(t *testing.T) {
	cells := []cell{{s: "s\x00:é"}, {i: -5}, {s: ""}, {i: math.MaxInt64}}
	kinds := []kind{kText, kInt, kText, kInt}
	if got := keyValues(pkKey(cells, kinds)); !reflect.DeepEqual(got, []any{"s\x00:é", int64(-5), "", int64(math.MaxInt64)}) {
		t.Fatalf("keyValues = %#v", got)
	}
}

func TestArrayLiterals(t *testing.T) {
	for v, want := range map[*any]string{
		ptr[any]([]string{}):                    "{}",
		ptr[any]([]string{"a", `b"c`, `d\e`}):   `{"a","b\"c","d\\e"}`,
		ptr[any]([]int64{1, -2, math.MinInt64}): "{1,-2,-9223372036854775808}",
	} {
		if got := arrayLiteral(*v); got != want {
			t.Errorf("arrayLiteral(%v) = %s, want %s", *v, got, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// Every table a render writes is keyed and owned as the schema says.
func TestTablesAndOwners(t *testing.T) {
	want := map[string]string{"session_rows": "session_id", "event_rows": "session_id", "session_request_rows": "scope_session_id",
		"session_call_days": "session_id", "agent_calls": "key", "agent_call_hours": "key", "batch_parts": "key"}
	for name, owner := range want {
		if got := OwnerColumn(name); got != owner {
			t.Errorf("%s's owner %s, want %s", name, got, owner)
		}
	}
	if len(Tables) != 13 {
		t.Errorf("%d tables", len(Tables))
	}
}
