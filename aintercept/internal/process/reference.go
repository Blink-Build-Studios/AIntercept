package process

import "github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"

// RawRow is one raw row as read from an observer's file: the row, its line and
// the file it came from.
type RawRow struct {
	Event events.Event
	Line  []byte
	File  string
}

// Shaper applies raw rows in order and keeps the request and event rows they
// complete, before any expiry: the single-pass reference the observer's
// canonical records are tested against.
type Shaper struct {
	s   *state
	out batch
}

// NewShaper starts an empty shaper.
func NewShaper() *Shaper { return &Shaper{s: newState()} }

// Apply folds one raw row in.
func (x *Shaper) Apply(e *events.Event, line []byte, file string) { x.s.apply(e, line, file, &x.out) }

// Rows is the request and event rows so far.
func (x *Shaper) Rows() ([]Request, []Event) { return x.out.requests, x.out.events }

// Shape is the request and event rows shaped from rows applied in order.
func Shape(rows []RawRow) ([]Request, []Event) {
	x := NewShaper()
	for i := range rows {
		x.Apply(&rows[i].Event, rows[i].Line, rows[i].File)
	}
	return x.Rows()
}

// EventNumbers is each event's number in its batch, in the order given, as the
// event rows number Claude Code events (their event id): the reference ranks
// the observer's records are tested against. 0 for an event that gets none
// (another source, or no session).
func EventNumbers(evs []Event) []int32 {
	part := newEventAgg()
	listed := make([]*listedEvent, len(evs))
	for i, e := range evs {
		before := len(part.ccAll)
		part.add(eventCols{CorrelationID: e.CorrelationID, RowNS: e.RowNS, NS: e.NS, Seq: e.Seq, Source: e.Source, Name: e.Name,
			SessionID: e.SessionID, PromptID: e.PromptID, PersonID: e.PersonID, MetadataJSON: e.MetadataJSON,
			DeviceID: e.DeviceID, EnvJSON: e.EnvJSON, HeadSHA: e.HeadSHA, OrgID: e.OrgID, EventKey: e.EventKey})
		if len(part.ccAll) > before {
			listed[i] = part.ccAll[len(part.ccAll)-1]
		}
	}
	part.numberEvents()
	out := make([]int32, len(evs))
	for i, ce := range listed {
		if ce != nil {
			out[i] = ce.n
		}
	}
	return out
}
