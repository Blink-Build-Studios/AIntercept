package records

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// v9's raw files (docs/PIPELINE.md §2.1): one stream per observer pod, at
//
//	<prefix>/raw/<YYYY-MM-DD>/<HH>/<pod>/<ns>-<seq>.ndjson.gz
//
// (the date and hour it was uploaded in, the time its first member was
// written). A file is gzip members back to back: one per completed call (its
// call records, then its rows as the raw log has them) and one per batch of
// other records (call starts, analytics events, account sightings). Any gzip
// reader reads the whole file; a call's member is also readable alone, by its
// byte range, which the workers store on its calls row.
//
// A record's line is {"record":<the record>}; every other line is a row.

// RawWriter takes one whole gzip member (the writer's members stream).
type RawWriter interface {
	Write(member []byte) error
}

// RawPrefix is where raw files are, under prefix.
func RawPrefix(prefix string) string {
	return withSlash(strings.Trim(prefix, "/")) + "raw/"
}

// RawKey names a pod's raw files: a key template the writer fills in with the
// hour it uploads the file in (events.UploadHour).
func RawKey(prefix, pod string) func(seq uint64, start time.Time) string {
	return func(seq uint64, start time.Time) string {
		return fmt.Sprintf("%s%s/%s/%019d-%06d.ndjson.gz", RawPrefix(prefix), events.UploadHour, pod, start.UnixNano(), seq)
	}
}

// RawPod is the pod a raw file's key names (…/raw/<date>/<hour>/<pod>/<file>).
func RawPod(key string) (string, bool) {
	i := strings.LastIndex(key, "raw/")
	if i < 0 {
		return "", false
	}
	parts := strings.Split(key[i+len("raw/"):], "/")
	if len(parts) != 4 || parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

var rawRecordPrefix = []byte(`{"record":`)

// IsRawRecordLine reports whether a raw file's line is a record (not a row).
func IsRawRecordLine(line []byte) bool { return bytes.HasPrefix(line, rawRecordPrefix) }

type rawRecord struct {
	Record *Record `json:"record"`
}

var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// rawMember is lines as one gzip member.
func rawMember(lines [][]byte) []byte {
	var b bytes.Buffer
	zw := gzipWriters.Get().(*gzip.Writer)
	zw.Reset(&b)
	for _, l := range lines {
		_, _ = zw.Write(l)
		_, _ = zw.Write([]byte{'\n'})
	}
	_ = zw.Close()
	gzipWriters.Put(zw)
	return b.Bytes()
}

// RawMembers is what the records and call details one step of the assembler
// completed add to a raw file: a member per detail, holding the records of its
// call and then its rows, and one member for the other records.
func RawMembers(rs []Record, ds []Detail) ([][]byte, error) {
	if len(rs) == 0 && len(ds) == 0 {
		return nil, nil
	}
	of := make(map[string]int, len(ds))
	groups := make([][][]byte, len(ds)+1) // the last: the other records
	for i, d := range ds {
		of[d.CorrelationID] = i
	}
	for i := range rs {
		line, err := json.Marshal(rawRecord{Record: &rs[i]})
		if err != nil {
			return nil, err
		}
		g, ok := of[rs[i].CallID]
		if !ok || rs[i].CallID == "" {
			g = len(ds)
		}
		groups[g] = append(groups[g], line)
	}
	out := make([][]byte, 0, len(groups))
	for i, d := range ds {
		out = append(out, rawMember(append(groups[i], d.Lines...)))
	}
	if loose := groups[len(ds)]; len(loose) > 0 {
		out = append(out, rawMember(loose))
	}
	return out, nil
}

// RawRange is where a completed call's member is in its raw file.
type RawRange struct {
	CallID      string
	Offset, Len int64
}

// ReadRawFile reads a raw file: its records, in order, and the byte range of
// each completed call's member (one with rows: its call_end and
// ws_connection records name the call).
func ReadRawFile(b []byte) ([]Record, []RawRange, error) {
	var (
		recs   []Record
		ranges []RawRange
		zr     gzip.Reader
	)
	br := bytes.NewReader(b) // an io.ByteReader: gzip reads no further than each member
	for br.Len() > 0 {
		off := br.Size() - int64(br.Len())
		if err := zr.Reset(br); err != nil {
			return nil, nil, fmt.Errorf("raw: member at %d: %w", off, err)
		}
		zr.Multistream(false)
		var ends []string
		rows := 0
		sc := bufio.NewScanner(&zr)
		sc.Buffer(make([]byte, 64<<10), 256<<20)
		for sc.Scan() {
			l := sc.Bytes()
			if !IsRawRecordLine(l) {
				rows++
				continue
			}
			var rl rawRecord
			if err := json.Unmarshal(l, &rl); err != nil || rl.Record == nil {
				return nil, nil, fmt.Errorf("raw: a record at %d: %v", off, err)
			}
			if k := rl.Record.Kind; k == KindCallEnd || k == KindWSConnection {
				ends = append(ends, rl.Record.CallID)
			}
			recs = append(recs, *rl.Record)
		}
		if err := sc.Err(); err != nil {
			return nil, nil, fmt.Errorf("raw: member at %d: %w", off, err)
		}
		n := br.Size() - int64(br.Len()) - off
		if rows > 0 {
			seen := map[string]bool{}
			for _, id := range ends {
				if !seen[id] {
					seen[id] = true
					ranges = append(ranges, RawRange{CallID: id, Offset: off, Len: n})
				}
			}
		}
	}
	return recs, ranges, nil
}
