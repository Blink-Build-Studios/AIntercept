package rollup

import (
	"context"
	"net/url"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// A reported call nothing captured is shown under the id reported:<id> (its
// request id, client request id or time). Its row lives with the key of the
// session that reported it, so whoever writes that row marks it:
// v1/reported/<escaped id>/<escaped key>, an empty object. The request drawer
// finds the row's key from the id alone. A mark written by a run that then
// loses its commit is harmless: the winner stores the same row.

// ReportedMark is the object key of the mark saying key stores the missed
// report shown as reported:<id>.
func ReportedMark(prefix, id, key string) string {
	return join(prefix, "v1/reported") + "/" + url.PathEscape(id) + "/" + url.PathEscape(key)
}

// markReported writes a mark for each missed report among rows.
func markReported(ctx context.Context, s stream.Store, prefix, key string, rows []stream.Row) error {
	done := map[string]bool{}
	for _, r := range rows {
		if r.Tomb {
			continue
		}
		m, ok := r.Value.(state.MissedRow)
		if !ok {
			continue
		}
		id := strings.TrimPrefix(m.Row.RequestID, "reported:")
		if id == "" || done[id] {
			continue
		}
		done[id] = true
		if err := s.Put(ctx, ReportedMark(prefix, id, key), nil); err != nil {
			return err
		}
	}
	return nil
}
