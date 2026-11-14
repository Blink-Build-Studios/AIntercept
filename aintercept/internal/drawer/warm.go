package drawer

import (
	"sort"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
)

// The warm scope (docs/PIPELINE.md §3.7): a render keeps its scope, and the
// next applies the run's changed rows to it, so a run costs what changed, not
// the session's size. Every list keeps the order a whole build gives it
// (newScope, build); the tests compare each run's rows with a whole render's.

// changes is what a run's updates moved, for the rows that read it.
type changes struct {
	order  map[string]*span // session → its steps (their places: "delivered in step N")
	lists  map[string]*span // session \x00 promptKey → its steps of that prompt
	exists map[string]bool  // sessions touched → whether they existed before
	moved  map[*ev]string   // events whose drawer id changed → their id before
	gone   []*ev            // events removed
	chain  []string         // chain keys whose entry changed
	mids   []string         // message ids whose chain step can have changed
	prevs  []string         // previous message ids whose chain step can have changed
}

// span is a list's first place a run changed, and its length before.
type span struct{ k, n int }

func newChanges() *changes {
	return &changes{order: map[string]*span{}, lists: map[string]*span{}, exists: map[string]bool{},
		moved: map[*ev]string{}}
}

func (c *changes) touch(m map[string]*span, key string, k, n int) {
	if c == nil {
		return
	}
	if sp := m[key]; sp != nil {
		sp.k = min(sp.k, k)
		return
	}
	m[key] = &span{k, n}
}

func (c *changes) touchExists(x *sess) {
	if c == nil {
		return
	}
	if _, ok := c.exists[x.id]; !ok {
		c.exists[x.id] = x.exists
	}
}

// insertSorted puts x after the elements not after it.
func insertSorted[T any](xs []T, x T, less func(a, b T) bool) ([]T, int) {
	i := sort.Search(len(xs), func(i int) bool { return less(x, xs[i]) })
	var zero T
	xs = append(xs, zero)
	copy(xs[i+1:], xs[i:])
	xs[i] = x
	return xs, i
}

// removeSorted takes x (by identity) out of xs, sorted by less.
func removeSorted[T comparable](xs []T, x T, less func(a, b T) bool) ([]T, int) {
	i := sort.Search(len(xs), func(i int) bool { return !less(xs[i], x) })
	for j := i; j < len(xs); j++ {
		if xs[j] == x {
			return append(xs[:j], xs[j+1:]...), j
		}
	}
	for j := range xs {
		if xs[j] == x {
			return append(xs[:j], xs[j+1:]...), j
		}
	}
	return xs, -1
}

func insEv(l []*ev, e *ev) []*ev { l, _ = insertSorted(l, e, evOrder); return l }
func remEv(l []*ev, e *ev) []*ev { l, _ = removeSorted(l, e, evOrder); return l }

// --- a session's steps and events ----------------------------------------------------

func (x *sess) listKey(st *step) string { return x.id + "\x00" + promptKey(st.prompt) }

func (x *sess) addStep(st *step) {
	c := x.s.ch
	c.touchExists(x)
	var i int
	n := len(x.steps)
	x.steps, i = insertSorted(x.steps, st, lessStep)
	c.touch(c.order, x.id, i, n)
	if !st.prompt.IsNone() {
		l := x.byPrompt[st.prompt.S]
		n = len(l)
		x.byPrompt[st.prompt.S], i = insertSorted(l, st, lessStep)
	} else {
		n = len(x.noPrompt)
		x.noPrompt, i = insertSorted(x.noPrompt, st, lessStep)
	}
	c.touch(c.lists, x.listKey(st), i, n)
	x.stepIDs[st.id]++
	x.s.stepOwner[st.id] = x.id
	x.chainKey(st.id)
	x.exists = true
}

func (x *sess) removeStep(st *step) {
	c := x.s.ch
	c.touchExists(x)
	var i int
	n := len(x.steps)
	x.steps, i = removeSorted(x.steps, st, lessStep)
	c.touch(c.order, x.id, i, n)
	if !st.prompt.IsNone() {
		l := x.byPrompt[st.prompt.S]
		n = len(l)
		if l, i = removeSorted(l, st, lessStep); len(l) == 0 {
			delete(x.byPrompt, st.prompt.S)
		} else {
			x.byPrompt[st.prompt.S] = l
		}
	} else {
		n = len(x.noPrompt)
		x.noPrompt, i = removeSorted(x.noPrompt, st, lessStep)
	}
	c.touch(c.lists, x.listKey(st), i, n)
	if x.stepIDs[st.id]--; x.stepIDs[st.id] <= 0 {
		delete(x.stepIDs, st.id)
		if x.s.stepOwner[st.id] == x.id {
			delete(x.s.stepOwner, st.id)
		}
	}
	x.chainKey(st.id)
	x.exists = len(x.steps) > 0 || len(x.events) > 0
}

func (x *sess) addEv(e *ev) {
	x.s.ch.touchExists(x)
	x.events = insEv(x.events, e)
	x.lists(e, insEv)
	x.refresh(e)
	x.exists = true
}

func (x *sess) removeEv(e *ev) {
	x.s.ch.touchExists(x)
	x.events = remEv(x.events, e)
	x.lists(e, remEv)
	x.refresh(e)
	x.exists = len(x.steps) > 0 || len(x.events) > 0
}

// refresh recomputes what an event added or removed changes of the views
// derived from the lists: the sandbox settings from it on, a prompt's index.
func (x *sess) refresh(e *ev) {
	switch e.name {
	case "tengu_bash_tool_command_executed":
		x.resandbox(sort.Search(len(x.bash), func(i int) bool { return !evOrder(x.bash[i], e) }))
	case "tengu_input_prompt":
		if pid, _, ok := promptIndexOf(e); ok {
			x.reindex(pid)
		}
	}
}

// --- the scope's rows ------------------------------------------------------------

// sessionFor is sid's view, built from the scope's rows when it's new.
func (s *scope) sessionFor(sid string) (*sess, bool) {
	if x := s.sessions[sid]; x != nil {
		return x, false
	}
	x := s.build(sid)
	if c := s.ch; c != nil { // a new session: every list of it is new
		if _, ok := c.exists[sid]; !ok {
			c.exists[sid] = false
		}
		c.touch(c.order, sid, 0, 0)
		for _, st := range x.steps {
			c.touch(c.lists, x.listKey(st), 0, 0)
		}
	}
	return x, true
}

// rechain keeps the chain entry a correlation id keys (when it's a step's id).
func (s *scope) rechain(corr string) {
	if sid, ok := s.stepOwner[corr]; ok {
		s.sessions[sid].chainKey(corr)
	}
}

// putCall applies a request row's change: c nil removes it.
func (s *scope) putCall(id string, c *canon.Request) {
	if old := s.calls[id]; old != nil {
		delete(s.calls, id)
		s.drawer[old.id], _ = removeSorted(s.drawer[old.id], old, byCallID)
		s.byCorr[old.c.CorrelationID], _ = removeSorted(s.byCorr[old.c.CorrelationID], old, byCallID)
		s.callsOf[old.c.SessionID], _ = removeSorted(s.callsOf[old.c.SessionID], old, byCallID)
		if old.st != nil {
			s.sessions[old.c.SessionID].removeStep(old.st)
		}
		s.rechain(old.c.CorrelationID)
	}
	if c == nil {
		return
	}
	r := newCallRow(c)
	s.calls[id] = r
	s.drawer[r.id], _ = insertSorted(s.drawer[r.id], r, byCallID)
	s.byCorr[c.CorrelationID], _ = insertSorted(s.byCorr[c.CorrelationID], r, byCallID)
	s.callsOf[c.SessionID], _ = insertSorted(s.callsOf[c.SessionID], r, byCallID)
	if x, built := s.sessionFor(c.SessionID); !built && r.ccCall() {
		r.st = newStep(r)
		x.addStep(r.st)
	}
	s.rechain(c.CorrelationID)
}

// putEvent applies an analytics row's change: e nil removes it.
func (s *scope) putEvent(id string, e *canon.Event) {
	var touched []*batch
	var added *ev
	if old, ok := s.rowCorr[id]; ok {
		b := s.batches[old]
		delete(b.analytic, id)
		delete(s.rowCorr, id)
		if x := s.evs[id]; x != nil {
			delete(s.evs, id)
			s.evsOf[x.session], _ = removeSorted(s.evsOf[x.session], x, byRowKey)
			b.evs, _ = removeSorted(b.evs, x, byRowKey)
			s.unindex(x)
			s.sessions[x.session].removeEv(x)
			if s.ch != nil {
				s.ch.gone = append(s.ch.gone, x)
			}
		}
		touched = append(touched, b)
	}
	if e != nil {
		b := s.batch(e.CorrelationID)
		row := *e
		b.analytic[id] = &row
		s.rowCorr[id] = e.CorrelationID
		if x := newEv(&row, id); x != nil {
			s.evs[id] = x
			s.evsOf[x.session], _ = insertSorted(s.evsOf[x.session], x, byRowKey)
			b.evs, _ = insertSorted(b.evs, x, byRowKey)
			added = x
		}
		touched = append(touched, b)
	}
	for _, b := range touched {
		s.renumber(b, added)
		b.session = earliestSession(b)
	}
	if added != nil {
		s.index(added)
		if x, built := s.sessionFor(added.session); !built {
			x.addEv(added)
		}
	}
}

// renumber gives a batch's events their ids again; an event whose id moves is
// taken out of its session's lists and put back in its new place (the lists
// are in the ids' rank order). added isn't in them yet.
func (s *scope) renumber(b *batch, added *ev) {
	ids := s.ids(b)
	for _, e := range b.evs {
		k := ids[e]
		if e == added {
			e.n, e.id = k.n, k.id
			continue
		}
		if e.id == k.id && e.n == k.n {
			continue
		}
		if s.ch != nil {
			if _, ok := s.ch.moved[e]; !ok {
				s.ch.moved[e] = e.id
			}
		}
		x := s.sessions[e.session]
		x.removeEv(e)
		s.unindex(e)
		e.n, e.id = k.n, k.id
		s.index(e)
		x.addEv(e)
	}
}

// index and unindex keep an event in the scope's indexes by drawer id and by
// find_report's keys.
func (s *scope) index(e *ev) {
	s.evByID[e.id], _ = insertSorted(s.evByID[e.id], e, byRowKey)
	for _, k := range reportKeys(e) {
		s.reports[k], _ = insertSorted(s.reports[k], e, byRowKey)
	}
}

func (s *scope) unindex(e *ev) {
	if l, _ := removeSorted(s.evByID[e.id], e, byRowKey); len(l) == 0 {
		delete(s.evByID, e.id)
	} else {
		s.evByID[e.id] = l
	}
	for _, k := range reportKeys(e) {
		if l, _ := removeSorted(s.reports[k], e, byRowKey); len(l) == 0 {
			delete(s.reports, k)
		} else {
			s.reports[k] = l
		}
	}
}
