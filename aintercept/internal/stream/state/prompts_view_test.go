package state

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The query's side, as the API's SQL does it (api/src/aintercept_api/sql/prompts.sql):
// the stored rows numbered and joined back into today's prompt rows, steps and
// marks.

func lessOrd(aTS int64, aType string, aG int32, aCorr string, aSeq, aRow int64, aName, aMD, aKey string,
	bTS int64, bType string, bG int32, bCorr string, bSeq, bRow int64, bName, bMD, bKey string) bool {
	switch {
	case aTS != bTS:
		return aTS < bTS
	case aType != bType:
		return aType < bType
	case aG != bG:
		return aG < bG
	case aCorr != bCorr:
		return aCorr < bCorr
	case aSeq != bSeq:
		return aSeq < bSeq
	case aRow != bRow:
		return aRow < bRow
	case aName != bName:
		return aName < bName
	case aMD != bMD:
		return aMD < bMD
	}
	return aKey < bKey
}

// promptViewOf is every session's prompt rows, steps and marks rebuilt from the
// stored rows.
func promptViewOf(o *streamOut) ([]process.PromptRow, []process.PromptStep, []process.SessionMark) {
	sess := map[string]PromptSession{}
	for _, r := range o.rows[TablePromptSession] {
		p := r.(PromptSession)
		sess[p.SessionID] = p
	}
	items := map[string][]*PromptItem{}
	for _, r := range o.rows[TablePromptItems] {
		p := r.(PromptItem)
		items[p.SessionID] = append(items[p.SessionID], &p)
	}
	steps := map[string][]*StepRow{}
	for _, r := range o.rows[TablePromptSteps] {
		s := r.(StepRow)
		steps[s.SessionID] = append(steps[s.SessionID], &s)
	}
	marks := map[string][]*MarkRow{}
	for _, r := range o.rows[TableMarks] {
		m := r.(MarkRow)
		marks[m.SessionID] = append(marks[m.SessionID], &m)
	}
	index := map[[2]string]int64{}
	for _, r := range o.rows[TableMarkPromptIndex] {
		p := r.(MarkPromptIndex)
		index[[2]string{p.SessionID, p.PromptID}] = p.Index
	}
	ids := map[string]bool{}
	for s := range items {
		ids[s] = true
	}
	for s := range marks {
		ids[s] = true
	}
	var sids []string
	for s := range ids {
		sids = append(sids, s)
	}
	sort.Strings(sids)

	var rows []process.PromptRow
	var stepRows []process.PromptStep
	var markRows []process.SessionMark
	for _, sid := range sids {
		ps := sess[sid]
		codex := ps.Product == canon.ProductCodex
		its := items[sid]
		sort.Slice(its, func(i, j int) bool {
			a, b := its[i], its[j]
			return lessOrd(a.TNS, a.Type, a.Group, a.OrdCorr, a.OrdSeq, a.OrdRowNS, a.OrdName, a.OrdMD, a.OrdKey,
				b.TNS, b.Type, b.Group, b.OrdCorr, b.OrdSeq, b.OrdRowNS, b.OrdName, b.OrdMD, b.OrdKey)
		})
		// The prompts in their own order (start, id): Codex's numbers and the open tail.
		var prompts []*PromptItem
		for _, it := range its {
			if it.Type == "prompt" {
				prompts = append(prompts, it)
			}
		}
		sort.Slice(prompts, func(i, j int) bool {
			return prompts[i].TNS < prompts[j].TNS || prompts[i].TNS == prompts[j].TNS && prompts[i].OrdKey < prompts[j].OrdKey
		})
		rank := map[*PromptItem]int{}
		for n, p := range prompts {
			rank[p] = n
		}
		seqOf := map[string]int64{}
		for i, it := range its {
			r := it.PromptRow
			r.Seq, r.SessionLastNS = int64(i+1), ps.LastNS
			if it.Type == "prompt" {
				seqOf[*r.PromptID] = r.Seq
				r.Typed = it.HasInput || codex
				if !it.HasInput {
					r.PromptIndex = nil
					if codex {
						n := int64(rank[it] + 1)
						r.PromptIndex = &n
					}
				}
				r.OpenTail = rank[it] == len(prompts)-1 && !it.HasTurnEnd && !(codex && it.ReportStatus)
				if !codex {
					r.DurationReportMS, r.TurnReportJSON = nil, nil
				}
			}
			rows = append(rows, r)
		}
		sts := steps[sid]
		sort.Slice(sts, func(i, j int) bool {
			a, b := sts[i], sts[j]
			if seqOf[a.PromptID] != seqOf[b.PromptID] {
				return seqOf[a.PromptID] < seqOf[b.PromptID]
			}
			return a.TNS < b.TNS || a.TNS == b.TNS && a.RequestID < b.RequestID
		})
		n, last := int64(0), ""
		for _, s := range sts {
			if s.PromptID != last {
				n, last = 0, s.PromptID
			}
			n++
			st := s.PromptStep
			st.Seq, st.Step = seqOf[s.PromptID], n
			st.EventsPendingIfLive = !s.HasSuccess && (!ps.HasPromptEvent || ps.LastPromptEventNS <= s.PendEnd)
			stepRows = append(stepRows, st)
		}

		ms := marks[sid]
		sort.Slice(ms, func(i, j int) bool {
			a, b := ms[i], ms[j]
			if a.Group != b.Group {
				return a.Group < b.Group
			}
			if a.TNS != b.TNS {
				return a.TNS < b.TNS
			}
			if lessOrd(0, "", 0, a.OrdCorr, a.OrdSeq, a.OrdRowNS, a.OrdName, a.OrdMD, a.OrdKey,
				0, "", 0, b.OrdCorr, b.OrdSeq, b.OrdRowNS, b.OrdName, b.OrdMD, b.OrdKey) {
				return true
			}
			if lessOrd(0, "", 0, b.OrdCorr, b.OrdSeq, b.OrdRowNS, b.OrdName, b.OrdMD, b.OrdKey,
				0, "", 0, a.OrdCorr, a.OrdSeq, a.OrdRowNS, a.OrdName, a.OrdMD, a.OrdKey) {
				return false
			}
			return a.Sub < b.Sub
		})
		var out []process.SessionMark
		add := func(m process.SessionMark) {
			m.SessionID, m.Seq = sid, int64(len(out))
			out = append(out, m)
		}
		var calls []*MarkRow
		var seg *process.SessionMark
		flush := func() {
			if seg != nil {
				add(*seg)
			}
		}
		for _, m := range ms {
			x := m.SessionMark
			if m.PromptRef != "" {
				if n, ok := index[[2]string{sid, m.PromptRef}]; ok {
					x.PromptIndex = &n
				}
			}
			switch m.Group {
			case 0:
				calls = append(calls, m)
				add(x)
			case 4:
				if seg != nil && *seg.Mode == *x.Mode {
					continue
				}
				var prev *string
				if seg != nil {
					ts := x.TNS
					seg.ToNS, prev = &ts, seg.Mode
					flush()
				}
				mode := *x.Mode
				seg = &process.SessionMark{Kind: "permission", TNS: x.TNS, Mode: &mode, PrevMode: prev}
			default:
				add(x)
			}
		}
		flush()
		seg = nil
		for _, c := range calls {
			if c.PointIP == "" || seg != nil && *seg.IP == c.PointIP {
				continue
			}
			if seg != nil {
				ts := c.TNS
				seg.ToNS = &ts
				flush()
			}
			ip := c.PointIP
			v6 := strings.Contains(ip, ":") && !strings.HasPrefix(strings.ToLower(ip), "::ffff:")
			seg = &process.SessionMark{Kind: "network", TNS: c.TNS, IP: &ip, IPv6: v6}
		}
		flush()
		markRows = append(markRows, out...)
	}
	return rows, stepRows, markRows
}

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
