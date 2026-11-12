package state

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Real data, local only: AINTERCEPT_RECORDS names a records directory
// (`aintercept canon -out`, never committed). Every record applied per key, in a
// shuffled order, must give the reference's rows, and every case the data takes
// must have an atomic test. It prints the cases' histogram.
func TestRealData(t *testing.T) {
	dir := os.Getenv("AINTERCEPT_RECORDS")
	if dir == "" {
		t.Skip("AINTERCEPT_RECORDS isn't set")
	}
	var rs []records.Record
	err := filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || !strings.HasSuffix(p, ".parquet") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		x, err := records.Decode(b)
		rs = append(rs, x...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	in := shuffled(rng, rs, len(rs)/50)
	o := stream(in)
	t.Logf("%d records (%d applied, with duplicates), %d keys", len(rs), len(in), len(o.states))

	var names []string
	for c := range o.cases {
		names = append(names, c)
	}
	sort.Strings(names)
	covered := map[string]bool{CaseDuplicate: true}
	for _, cs := range [][]atomicCase{presenceCases, eventCases, dimCases, promptCases, requestCases} {
		for _, c := range cs {
			for _, x := range c.cases {
				covered[x] = true
			}
		}
	}
	for _, c := range names {
		t.Logf("case %-24s %9d", c, o.cases[c])
		if !covered[c] {
			t.Errorf("case %q occurs in the data and has no atomic test", c)
		}
	}

	ref := reference(rs)
	want, err := refPresence(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := o.presenceRows(); !reflect.DeepEqual(got, want) {
		t.Errorf("presence: %d rows, want %d; first difference %s", len(got), len(want), firstDiff(got, want))
	}
	gotEv, wantEv := eventRowsOf(o), ref.EventRows
	if !reflect.DeepEqual(gotEv, wantEv) {
		ids, other := 0, 0
		for i := range min(len(gotEv), len(wantEv)) {
			g, w := gotEv[i], wantEv[i]
			if g.EventID != w.EventID {
				ids++
			}
			g.EventID = w.EventID
			if !reflect.DeepEqual(g, w) {
				other++
				if other <= 3 {
					gv, wv := reflect.ValueOf(g), reflect.ValueOf(w)
					for f := 0; f < gv.NumField(); f++ {
						if !reflect.DeepEqual(gv.Field(f).Interface(), wv.Field(f).Interface()) {
							t.Logf("row %d field %s: got %v want %v", i, gv.Type().Field(f).Name, sprint(gv.Field(f).Interface()), sprint(wv.Field(f).Interface()))
						}
					}
				}
			}
		}
		if other > 0 || len(gotEv) != len(wantEv) {
			t.Errorf("event rows: %d rows, want %d; %d differ in more than event_id; first difference %s",
				len(gotEv), len(wantEv), other, firstDiff(gotEv, wantEv))
		}
		// event_id's number is the event's rank among its batch's Claude Code events,
		// which a batch spanning sessions spreads over several keys: only ingest, which
		// holds the whole batch, can number it (records' batch_n is the place among
		// all the batch's events). Until it does, event_id is the one known difference.
		t.Errorf("event rows: %d of %d differ in event_id only: needs ingest's batch rank", ids, len(wantEv))
	}
	wantTools := ref.SessionTools
	sortTools(wantTools)
	if got := sessionToolsOf(o); !reflect.DeepEqual(got, wantTools) {
		t.Errorf("session tools: %d, want %d", len(got), len(wantTools))
	}
	pm, ps, af, ad := personTables(o)
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"person_map", pm, ref.PersonMap}, {"person_sessions", ps, ref.PersonSess},
		{"account_facts", af, ref.AcctFacts}, {"account_devices", ad, ref.AcctDevices},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: %d rows, want %d", c.name, reflect.ValueOf(c.got).Len(), reflect.ValueOf(c.want).Len())
		} else {
			t.Logf("%s: %d rows, equal", c.name, reflect.ValueOf(c.want).Len())
		}
	}
	wr, wst, wm := refPrompts(ref)
	gr, gst, gm := promptViewOf(o)
	if !reflect.DeepEqual(nonNil(gr), nonNil(wr)) {
		t.Errorf("prompt rows: %d, want %d: %s", len(gr), len(wr), fieldDiff(gr, wr))
	}
	if !reflect.DeepEqual(nonNil(gst), nonNil(wst)) {
		t.Errorf("prompt steps: %d, want %d: %s", len(gst), len(wst), fieldDiff(gst, wst))
	}
	if !reflect.DeepEqual(nonNil(gm), nonNil(wm)) {
		t.Errorf("session marks: %d, want %d: %s", len(gm), len(wm), fieldDiff(gm, wm))
	}
	t.Logf("prompt rows %d, prompt steps %d, session marks %d: compared", len(wr), len(wst), len(wm))
	t.Logf("presence %d rows, event rows %d, session tools %d: compared", len(want), len(wantEv), len(wantTools))
}

func firstDiff[T any](got, want []T) string {
	for i := range min(len(got), len(want)) {
		if !reflect.DeepEqual(got[i], want[i]) {
			return "at " + itoa(i) + ": got " + sprint(got[i]) + " want " + sprint(want[i])
		}
	}
	return "in length"
}

func itoa(i int) string { return fmt.Sprint(i) }

func sprint(v any) string { return fmt.Sprintf("%+v", v) }
