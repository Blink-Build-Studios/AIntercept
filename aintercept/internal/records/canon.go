package records

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Stats is what a canon run did.
type Stats struct {
	Files, Rows, Bad, Records int64
	Cases                     map[string]int64
}

// CheckCoverage fails when real data hit a case with no atomic test: one this
// code doesn't declare, or the unknown-phase case.
func (s Stats) CheckCoverage() error {
	known := map[string]bool{}
	for _, c := range AllCases {
		known[c] = true
	}
	var bad []string
	for c, n := range s.Cases {
		if n > 0 && (!known[c] || c == CaseUnknownPhase) {
			bad = append(bad, fmt.Sprintf("%s (%d)", c, n))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return fmt.Errorf("cases seen in the data with no atomic test: %s", strings.Join(bad, ", "))
	}
	return nil
}

// CanonDir turns a directory of raw observer files (…/instance=<pod>/<file>.ndjson.gz,
// as the observer uploads them) into records, through the same assembler the
// live observer runs: each pod's files in order, one assembler per pod.
// Records are written with put under prefix, each bucket's per file, named by
// the raw file's upload time.
func CanonDir(ctx context.Context, dir string, put Putter, prefix string) (Stats, error) {
	return CanonDirObserved(ctx, dir, put, prefix, nil)
}

// CanonDirObserved is CanonDir, calling observe with each row as it's read (a
// verification run feeds the same rows to other code to compare).
func CanonDirObserved(ctx context.Context, dir string, put Putter, prefix string, observe func(pod string, e *events.Event, line []byte)) (Stats, error) {
	byPod := map[string][]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ndjson.gz") {
			return err
		}
		pod := ""
		for _, part := range strings.Split(filepath.ToSlash(p), "/") {
			if v, ok := strings.CutPrefix(part, "instance="); ok {
				pod = v
			}
		}
		byPod[pod] = append(byPod[pod], p)
		return nil
	})
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Cases: map[string]int64{}}
	pods := make([]string, 0, len(byPod))
	for p := range byPod {
		pods = append(pods, p)
	}
	sort.Strings(pods)
	for _, pod := range pods {
		files := byPod[pod]
		// Upload order: a file is named by its batch start time and sequence.
		sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
		if err := canonPod(ctx, pod, files, put, prefix, &st, observe); err != nil {
			return st, err
		}
	}
	return st, nil
}

func canonPod(ctx context.Context, pod string, files []string, put Putter, prefix string, st *Stats,
	observe func(string, *events.Event, []byte)) error {
	var at time.Time
	s := NewSink(SinkConfig{Putter: put, Prefix: prefix, Incarnation: "canon-" + pod, MaxRecords: 50000,
		MaxAge: 365 * 24 * time.Hour, Now: func() time.Time { return at }})
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		at = fileTime(f)
		n, bad, err := readRaw(f, func(e *events.Event, line []byte) {
			if observe != nil {
				observe(pod, e, line)
			}
			s.mu.Lock()
			s.add(s.asm.Apply(e, line, ""))
			s.mu.Unlock()
		})
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		st.Files++
		st.Rows += n
		st.Bad += bad
	}
	s.mu.Lock()
	s.add(s.asm.Flush())
	s.mu.Unlock()
	for c, n := range s.Cases() {
		st.Cases[c] += n
	}
	s.mu.Lock()
	st.Records += s.asm.seq
	s.mu.Unlock()
	return s.Flush()
}

// fileTime is when a raw file's batch began, from its name (<unix nanos>-<seq>.ndjson.gz).
func fileTime(p string) time.Time {
	base := filepath.Base(p)
	head, _, _ := strings.Cut(base, "-")
	if n, err := strconv.ParseInt(head, 10, 64); err == nil && n > 1e15 {
		return time.Unix(0, n).UTC()
	}
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Time{}
}

// readRaw calls f for each row of a raw file, with its line as written.
func readRaw(p string, f func(*events.Event, []byte)) (rows, bad int64, err error) {
	fh, err := os.Open(p)
	if err != nil {
		return 0, 0, err
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return 0, 0, err
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var e events.Event
		if json.Unmarshal(line, &e) != nil || e.Phase == "" || e.CorrelationID == "" {
			bad++
			continue
		}
		rows++
		f(&e, append([]byte(nil), line...))
	}
	return rows, bad, sc.Err()
}
