package state

import "strconv"

// Presence is a session's presence segments (process presenceRows), stored by
// position key: Group 0 is the span's focused and away segments, Group 1 its
// sleep windows, each in FromNS order. The query numbers them (seq) in that
// order.
type Presence struct {
	SessionID string `parquet:"session_id"`
	Group     int32  `parquet:"grp"`
	FromNS    int64  `parquet:"from_ns"`
	ToNS      int64  `parquet:"to_ns"`
	State     string `parquet:"state"`
}

// Span is a half-open window [A, B).
type Span struct{ A, B int64 }

// spans is a set of windows kept merged: overlapping or touching windows join
// (claude_code.py _merge). By start.
type spans struct{ m *omap[int64, int64] }

func newSpans() *spans {
	return &spans{m: newOmap[int64, int64](func(a, b int64) bool { return a < b })}
}

// add merges w in: the merged window it made or grew, or false when w was
// already covered.
func (s *spans) add(w Span) (Span, bool) {
	_, merged := s.addTrack(w)
	if merged == nil {
		return Span{}, false
	}
	return *merged, true
}

// addTrack merges w in: the windows it absorbed (gone, as they were) and the
// merged window that replaces them.
func (s *spans) addTrack(w Span) (gone []Span, merged *Span) {
	a, b := w.A, w.B
	if pa, pb, ok := s.m.Floor(a); ok && pb >= a { // a window starting before w that reaches it
		a, b = pa, max(b, pb)
	}
	var absorbed []Span
	s.m.Ascend(a, func(xa, xb int64) bool {
		if xa > b {
			return false
		}
		absorbed = append(absorbed, Span{xa, xb})
		b = max(b, xb)
		return true
	})
	for _, x := range absorbed {
		s.m.Delete(x.A)
	}
	s.m.Set(a, b)
	if len(absorbed) == 1 && absorbed[0] == (Span{a, b}) {
		return nil, nil // already covered: nothing changed
	}
	return absorbed, &Span{a, b}
}

// before is the last window ending before t, if any.
func (s *spans) before(t int64) (Span, bool) {
	a, b, ok := s.m.Floor(t)
	if ok && b >= t { // it reaches t: the one before it
		if a == minInt64 {
			return Span{}, false
		}
		a, b, ok = s.m.Floor(a - 1)
	}
	return Span{a, b}, ok && b < t
}

// after is the first window starting after t, if any.
func (s *spans) after(t int64) (Span, bool) {
	var w Span
	found := false
	s.m.Ascend(t+1, func(a, b int64) bool { w, found = Span{a, b}, true; return false })
	return w, found
}

func (s *spans) list() []Span {
	var out []Span
	s.m.All(func(a, b int64) bool { out = append(out, Span{a, b}); return true })
	return out
}

const minInt64 = -1 << 63

func idOf(prefix string, ns int64) string { return prefix + strconv.FormatInt(ns, 10) }

// presence brings the key's presence rows up to date with what one record
// changed: only the stretch of the span that changed is cut again.
func (s *KeyState) presence(d *Delta, ch changed) {
	ses, sid := s.ses, sessionOf(s.Key)
	now, was := ses.spanNow(), ses.before
	ses.before = now

	// Sleep rows: each merged window is a row.
	if ch.sleep != nil {
		d.Cases = append(d.Cases, CasePresenceSleepRow)
		for _, g := range ch.sleepGone {
			s.rows.drop(d, TablePresence, idOf("z:", g.A))
		}
		s.rows.put(d, TablePresence, idOf("z:", ch.sleep.A), Presence{sid, 1, ch.sleep.A, ch.sleep.B, "asleep"})
	}

	// The stretch to cut again: what the record moved of the span, and the merged
	// blur windows it made or grew.
	lo, hi, any := int64(0), int64(0), false
	grow := func(a, b int64) {
		if !any || a < lo {
			lo = a
		}
		if !any || b > hi {
			hi = b
		}
		any = true
	}
	switch {
	case now.ok && !was.ok:
		grow(now.first, now.last)
	case now.ok:
		if now.first != was.first {
			grow(min(now.first, was.first), max(now.first, was.first))
		}
		if now.last != was.last {
			grow(min(now.last, was.last), max(now.last, was.last))
		}
	}
	for _, w := range ch.blur {
		grow(w.A, w.B)
	}
	if !any || !now.ok {
		return
	}
	d.Cases = append(d.Cases, CasePresenceRecut)
	first, last := now.first, now.last
	if was.ok {
		first, last = min(first, was.first), max(last, was.last)
	}
	// From the end of the last blur window before the stretch to the start of the
	// first after it: those edges are segment boundaries before and after.
	from, to := first, last
	if w, ok := ses.blur.before(lo); ok {
		from = max(from, w.B)
	}
	if w, ok := ses.blur.after(hi); ok {
		to = min(to, w.A)
	}
	s.recut(d, sid, from, to, now)
}

// recut writes the span's focused and away segments between from and to (both
// boundaries), replacing what was there.
func (s *KeyState) recut(d *Delta, sid string, from, to int64, now spanOf) {
	old := map[int64]bool{}
	s.segs.Ascend(from, func(a int64, _ Presence) bool {
		if a >= to {
			return false
		}
		old[a] = true
		return true
	})
	add := func(a, b int64, st string) {
		row := Presence{sid, 0, a, b, st}
		delete(old, a)
		s.segs.Set(a, row)
		s.rows.put(d, TablePresence, idOf("f:", a), row)
	}
	lo, hi := max(from, now.first), min(to, now.last)
	at, start := lo, lo
	if a, _, ok := s.ses.blur.m.Floor(lo); ok {
		start = a
	}
	s.ses.blur.m.Ascend(start, func(wa, wb int64) bool {
		if wa >= hi {
			return false
		}
		a, b := max(wa, now.first, lo), min(wb, now.last, hi)
		if a >= b {
			return true
		}
		if a > at {
			add(at, a, "focused")
		}
		add(a, b, "away")
		at = b
		return true
	})
	if at < hi {
		add(at, hi, "focused")
	}
	for a := range old {
		s.segs.Delete(a)
		s.rows.drop(d, TablePresence, idOf("f:", a))
	}
}

// sessionOf is a session key's session id.
func sessionOf(key string) string {
	if len(key) > 2 && key[:2] == "s:" {
		return key[2:]
	}
	return ""
}
