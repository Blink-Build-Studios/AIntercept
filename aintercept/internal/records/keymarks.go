package records

// KeyMark says a call's records went to Key: the sink writes it as an empty
// object, calls/<id>.keys/<escaped key> (CallKeyMark), so the request drawer
// finds the keys that hold a call's rows from the call's id alone.
type KeyMark struct {
	CorrelationID string
	Key           string
}

// CallKeyMark is the object key of a call's mark for key.
func CallKeyMark(prefix, correlationID, key string) string { return "" }

// corrOf is the call a record belongs to ("" for an account sighting).
func corrOf(r *Record) string { return "" }

// TakeKeyMarks returns the marks made since the last call.
func (a *Assembler) TakeKeyMarks() []KeyMark { return nil }
