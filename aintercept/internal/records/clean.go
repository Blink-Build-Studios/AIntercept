package records

import (
	"reflect"
	"strings"
	"sync"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
)

// idColumn is whether a column holds a client-controlled id that canon caps
// (docs/PIPELINE.md §3.1): session, request, call, event and prompt ids.
func idColumn(col string) bool {
	switch col {
	case "call_id", "correlation_id", "batch_id", // call ids (a batch is its request's call)
		"session_id", "batch_session", "batch_other_sessions",
		"prompt_id", "request_id", "client_request_id",
		"event_key", "client_event_id":
		return true
	}
	return false
}

// Clean sanitizes a record in place (canon.CleanText, CleanJSON and CleanID):
// no string it holds has a NUL, no *_json column a NUL escape, and no id is
// over canon.MaxIDBytes. Every string field is covered, nested ones included,
// so a field added later is too. Idempotent; clean records are unchanged.
func Clean(r *Record) {
	cleanValue(reflect.ValueOf(r).Elem())
}

// cleaner is how a field's strings are cleaned.
type cleaner func(string) string

type fieldPlan struct {
	index int
	clean cleaner // for a string or []string field
	sub   bool    // a struct, or a pointer to one: cleaned in turn
}

var plans sync.Map // reflect.Type -> []fieldPlan

func planOf(t reflect.Type) []fieldPlan {
	if p, ok := plans.Load(t); ok {
		return p.([]fieldPlan)
	}
	var out []fieldPlan
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col, _, _ := strings.Cut(f.Tag.Get("parquet"), ",")
		c := canon.CleanText
		switch {
		case idColumn(col):
			c = canon.CleanID
		case strings.HasSuffix(col, "_json"):
			c = canon.CleanJSON
		}
		ft := f.Type
		switch {
		case ft.Kind() == reflect.String, ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String:
			out = append(out, fieldPlan{index: i, clean: c})
		case ft.Kind() == reflect.Struct, ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct:
			out = append(out, fieldPlan{index: i, sub: true})
		}
	}
	plans.Store(t, out)
	return out
}

func cleanValue(v reflect.Value) {
	for _, p := range planOf(v.Type()) {
		f := v.Field(p.index)
		switch {
		case p.sub && f.Kind() == reflect.Pointer:
			if !f.IsNil() {
				cleanValue(f.Elem())
			}
		case p.sub:
			cleanValue(f)
		case f.Kind() == reflect.String:
			if s, c := f.String(), p.clean(f.String()); c != s {
				f.SetString(c)
			}
		default: // []string
			for i := 0; i < f.Len(); i++ {
				e := f.Index(i)
				if s, c := e.String(), p.clean(e.String()); c != s {
					e.SetString(c)
				}
			}
		}
	}
}
