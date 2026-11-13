package state

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// refPrompts is the reference's rows in the view's order (session, then seq).
func refPrompts(d *process.Derived) ([]process.PromptRow, []process.PromptStep, []process.SessionMark) {
	rows := append([]process.PromptRow(nil), d.PromptRows...)
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].SessionID < rows[j].SessionID || rows[i].SessionID == rows[j].SessionID && rows[i].Seq < rows[j].Seq
	})
	steps := append([]process.PromptStep(nil), d.PromptSteps...)
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i], steps[j]
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		return a.Seq < b.Seq || a.Seq == b.Seq && a.Step < b.Step
	})
	marks := append([]process.SessionMark(nil), d.SessionMarks...)
	sort.SliceStable(marks, func(i, j int) bool {
		return marks[i].SessionID < marks[j].SessionID || marks[i].SessionID == marks[j].SessionID && marks[i].Seq < marks[j].Seq
	})
	return rows, steps, marks
}

// checkPrompts requires the view's prompt rows, steps and marks to equal the
// reference's over the same records.
func checkPrompts(t *testing.T, label string, rs []records.Record, o *streamOut) {
	t.Helper()
	wr, ws, wm := refPrompts(reference(rs))
	gr, gs, gm := promptViewOf(o)
	if !reflect.DeepEqual(nonNil(gr), nonNil(wr)) {
		t.Fatalf("%s: prompt rows differ (%d, want %d): %s", label, len(gr), len(wr), fieldDiff(gr, wr))
	}
	if !reflect.DeepEqual(nonNil(gs), nonNil(ws)) {
		t.Fatalf("%s: prompt steps differ (%d, want %d): %s", label, len(gs), len(ws), fieldDiff(gs, ws))
	}
	if !reflect.DeepEqual(nonNil(gm), nonNil(wm)) {
		t.Fatalf("%s: marks differ (%d, want %d): %s", label, len(gm), len(wm), fieldDiff(gm, wm))
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// fieldDiff names the first row and its fields that differ.
func fieldDiff[T any](got, want []T) string {
	for i := range min(len(got), len(want)) {
		if reflect.DeepEqual(got[i], want[i]) {
			continue
		}
		gv, wv := reflect.ValueOf(got[i]), reflect.ValueOf(want[i])
		var b strings.Builder
		b.WriteString("row " + itoa(i) + ":")
		for f := 0; f < gv.NumField(); f++ {
			if !reflect.DeepEqual(gv.Field(f).Interface(), wv.Field(f).Interface()) {
				b.WriteString(" " + gv.Type().Field(f).Name + " got " + sprint(deep(gv.Field(f))) + " want " + sprint(deep(wv.Field(f))))
			}
		}
		b.WriteString("\n got " + sprint(got[i]) + "\nwant " + sprint(want[i]))
		return b.String()
	}
	if len(got) > len(want) {
		return "extra " + sprint(got[len(want)])
	}
	if len(want) > len(got) {
		return "missing " + sprint(want[len(got)])
	}
	return "?"
}

func deep(v reflect.Value) any {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}
