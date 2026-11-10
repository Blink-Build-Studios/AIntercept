package process

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Keeping start bounded by recent data, whatever the total (BUILD_PLAN §10):
//   - each fact part's data-time range is in the manifest, so start reads only
//     the request parts end markers need, and only their marker columns;
//   - compaction groups rows into one file per data day and rewrites only the
//     days new rows touch, so history settles into files start never opens;
//   - days more than sealAfter behind the newest data, with nothing unread,
//     are sealed: the full listing and the ledger load skip them.

// sealAfter: raw files land within minutes of their batch, so a day this far
// behind the newest row is complete.
const sealAfter = 72 * time.Hour

// PartInfo describes a fact part: its rows' data-time range, and for a day
// file, the day it holds (empty for one run's part).
type PartInfo struct {
	MinNS int64  `json:"min_ns"`
	MaxNS int64  `json:"max_ns"`
	Day   string `json:"day,omitempty"`
}

// fact is a row of a fact table: its primary key and data time.
type fact interface {
	Request | Event | Ledger
}

func pkOf(v any) string {
	switch r := v.(type) {
	case Request:
		return r.CallID
	case Event:
		return r.EventKey
	case Ledger:
		return r.RawKey
	}
	return ""
}

func timeOf(v any) int64 {
	switch r := v.(type) {
	case Request:
		return max(r.EndNS, r.StartNS)
	case Event:
		return r.RowNS
	case Ledger:
		return keyNS(r.RawKey)
	}
	return 0
}

var keyHour = regexp.MustCompile(`dt=(\d{4}-\d{2}-\d{2})/hour=(\d{2})/`)

// keyNS is the hour a raw file's key names, as Unix ns (0 when it names none).
func keyNS(key string) int64 {
	m := keyHour.FindStringSubmatch(key)
	if m == nil {
		return 0
	}
	t, err := time.Parse("2006-01-02T15", m[1]+"T"+m[2])
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

func dayOf(ns int64) string { return time.Unix(0, ns).UTC().Format("2006-01-02") }

func rangeOf[T fact](rows []T) PartInfo {
	var info PartInfo
	for i, r := range rows {
		t := timeOf(r)
		if i == 0 || t < info.MinNS {
			info.MinNS = t
		}
		info.MaxNS = max(info.MaxNS, t)
	}
	return info
}

// runParts is the table's parts that are one run's (not day files), in order.
func (m *Manifest) runParts(table string) []string {
	var out []string
	for _, k := range m.Facts[table] {
		if m.Parts[k].Day == "" {
			out = append(out, k)
		}
	}
	return out
}

// compactByDay merges the table's run parts into its day files: each day the
// rows touch is rewritten as one file (its old file's rows plus the new), in
// primary-key order; days the rows don't touch are left alone. It works a day at
// a time, streaming the run parts, so it holds one day's rows however much the
// run parts hold (a rebuild's parts carry the whole history).
func compactByDay[T fact](ctx context.Context, p *Processor, m *Manifest, table string, runID int64) error {
	runs := m.runParts(table)
	partDays := map[string]map[string]bool{}
	byDay := map[string]bool{}
	for _, k := range runs {
		days := map[string]bool{}
		if err := eachParquet(ctx, p.cfg.Out, k, func(r T) { days[dayOf(timeOf(r))] = true }); err != nil {
			return fmt.Errorf("compact %s: %w", k, err)
		}
		partDays[k] = days
		for d := range days {
			byDay[d] = true
		}
	}
	after := p.cfg.Wall().Add(p.cfg.Grace)
	replaced := map[string]bool{}
	for _, k := range runs {
		replaced[k] = true
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	added := map[string]PartInfo{}
	for _, d := range days {
		var rows []T
		for _, k := range runs {
			if !partDays[k][d] {
				continue
			}
			err := eachParquet(ctx, p.cfg.Out, k, func(r T) {
				if dayOf(timeOf(r)) == d {
					rows = append(rows, r)
				}
			})
			if err != nil {
				return fmt.Errorf("compact %s: %w", k, err)
			}
		}
		for _, k := range m.Facts[table] {
			if m.Parts[k].Day == d {
				old, err := readParquet[T](ctx, p.cfg.Out, k)
				if err != nil {
					return fmt.Errorf("compact %s: %w", k, err)
				}
				rows = append(old, rows...)
				replaced[k] = true
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return pkOf(rows[i]) < pkOf(rows[j]) })
		k := p.outKey(table, fmt.Sprintf("day-%s-%012d.parquet", d, runID))
		if err := writeParquet(ctx, p.cfg.Out, k, rows); err != nil {
			return fmt.Errorf("write %s: %w", k, err)
		}
		info := rangeOf(rows)
		info.Day = d
		added[k] = info
	}
	var keep []string
	for _, k := range m.Facts[table] {
		if replaced[k] {
			m.Garbage = append(m.Garbage, Garbage{Key: k, After: after})
			delete(m.Parts, k)
		} else {
			keep = append(keep, k)
		}
	}
	for _, d := range days {
		for k, info := range added {
			if info.Day == d {
				keep = append(keep, k)
				m.Parts[k] = info
			}
		}
	}
	m.Facts[table] = keep
	return nil
}

// sealThrough is the newest day the next commit may seal: more than sealAfter
// behind the newest row, and before any day with an unread file. Never earlier
// than what's already sealed.
func (p *Processor) sealThrough(m *Manifest) string {
	if m.NewestNS == 0 {
		return m.SealedThrough
	}
	cut := time.Unix(0, m.NewestNS).UTC().Add(-sealAfter).Truncate(24 * time.Hour).Add(-24 * time.Hour)
	d := cut.Format("2006-01-02")
	for k := range p.unread {
		if ns := keyNS(k); ns != 0 && dayOf(ns) <= d {
			d = time.Unix(0, ns).UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour).Format("2006-01-02")
		}
	}
	if d < m.SealedThrough {
		return m.SealedThrough
	}
	return d
}

// sealed reports whether the day a raw key names is sealed.
func (p *Processor) sealed(key string) bool {
	ns := keyNS(key)
	return !p.cfg.SweepAll && ns != 0 && p.man.SealedThrough != "" && dayOf(ns) <= p.man.SealedThrough
}

// markerRow is the columns end markers need: start reads only these.
type markerRow struct {
	CorrelationID string `parquet:"correlation_id"`
	StartNS       int64  `parquet:"start_ns,optional"`
	EndNS         int64  `parquet:"end_ns,optional"`
	WSTurn        int64  `parquet:"ws_turn,optional"`
}
