package records

import "net/url"

// KeyMark says a call's records went to Key: the sink writes it as an empty
// object, calls/<id>.keys/<escaped key> (CallKeyMark), so the request drawer
// finds the keys that hold a call's rows from the call's id alone. A call is
// marked under a key when its first record goes there (its start, for a call
// in flight), so a call still open is findable too.
type KeyMark struct {
	CorrelationID string
	Key           string
}

// CallKeyMark is the object key of a call's mark for key.
func CallKeyMark(prefix, correlationID, key string) string {
	return withSlash(prefix) + "calls/" + correlationID + ".keys/" + url.PathEscape(key)
}

// corrOf is the call a record belongs to ("" for an account sighting).
func corrOf(r *Record) string {
	if r.Kind == KindClientEvent && r.Event != nil && r.Event.CorrelationID != "" {
		return r.Event.CorrelationID
	}
	if r.Call != nil {
		return r.Call.CorrelationID
	}
	return ""
}

// mark notes that r's call has a record under r's key, once per (call, key).
func (a *Assembler) mark(r *Record) {
	if !a.KeepDetail || r.RoutingKey == "" {
		return
	}
	corr := corrOf(r)
	if corr == "" {
		return
	}
	if a.marked == nil {
		a.marked = map[string]map[string]bool{}
	}
	ks := a.marked[corr]
	if ks == nil {
		ks = map[string]bool{}
		a.marked[corr] = ks
	}
	if ks[r.RoutingKey] {
		return
	}
	ks[r.RoutingKey] = true
	a.marks = append(a.marks, KeyMark{CorrelationID: corr, Key: r.RoutingKey})
}

// TakeKeyMarks returns the marks made since the last call.
func (a *Assembler) TakeKeyMarks() []KeyMark {
	m := a.marks
	a.marks = nil
	return m
}
