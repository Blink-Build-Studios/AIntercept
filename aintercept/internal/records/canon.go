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
	return CanonDirWith(ctx, dir, put, prefix, CanonOptions{})
}

// CanonOptions are a canon run's options.
type CanonOptions struct {
	// Details also writes each call's detail object.
	Details bool
	// Observe is called with each row as it's read (a verification run feeds
	// the same rows to other code to compare).
	Observe func(pod string, e *events.Event, line []byte)
}

// CanonDirWith is CanonDir with options.
func CanonDirWith(ctx context.Context, dir string, put Putter, prefix string, opt CanonOptions) (Stats, error) {
	pods, byPod, err := podFiles(dir)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Cases: map[string]int64{}}
	for _, pod := range pods {
		if err := canonPod(ctx, pod, byPod[pod], put, prefix, &st, opt); err != nil {
			return st, err
		}
	}
	return st, nil
}

// CanonDirRecords is CanonDir without a sink: each pod's files in order
// through one assembler, as CanonDir reads them, handing the records each row
// completes to emit (canon -load inserts them into Postgres).
func CanonDirRecords(ctx context.Context, dir string, emit func([]Record) error) (Stats, error) {
	pods, byPod, err := podFiles(dir)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Cases: map[string]int64{}}
	for _, pod := range pods {
		asm := NewAssembler("canon-" + pod)
		var emitErr error
		out := func(rs []Record) {
			if emitErr == nil && len(rs) > 0 {
				emitErr = emit(rs)
			}
		}
		for _, f := range byPod[pod] {
			if err := ctx.Err(); err != nil {
				return st, err
			}
			n, bad, err := readRaw(f, func(e *events.Event, line []byte) { out(asm.Apply(e, line, "")) })
			if err != nil {
				return st, fmt.Errorf("%s: %w", f, err)
			}
			if emitErr != nil {
				return st, emitErr
			}
			st.Files++
			st.Rows += n
			st.Bad += bad
		}
		out(asm.Flush())
		if emitErr != nil {
			return st, emitErr
		}
		for c, n := range asm.Cases() {
			st.Cases[c] += n
		}
		st.Records += asm.seq
	}
	return st, nil
}

// podFiles is a directory's raw files by pod (…/instance=<pod>/…), each pod's
// in upload order (a file is named by its batch start time and sequence), and
// the pods sorted.
func podFiles(dir string) ([]string, map[string][]string, error) {
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
		return nil, nil, err
	}
	pods := make([]string, 0, len(byPod))
	for p, files := range byPod {
		pods = append(pods, p)
		sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
	}
	sort.Strings(pods)
	return pods, byPod, nil
}

func canonPod(ctx context.Context, pod string, files []string, put Putter, prefix string, st *Stats, opt CanonOptions) error {
	var at time.Time
	keys, err := os.MkdirTemp("", "aintercept-canon-keys-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(keys)
	s := NewSink(SinkConfig{Putter: put, Prefix: prefix, Incarnation: "canon-" + pod, Details: opt.Details, KeySpoolDir: keys,
		MaxAge: 365 * 24 * time.Hour, Now: func() time.Time { return at }})
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		at = fileTime(f)
		n, bad, err := readRaw(f, func(e *events.Event, line []byte) {
			if opt.Observe != nil {
				opt.Observe(pod, e, line)
			}
			s.mu.Lock()
			s.sawRow(e)
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
	for _, d := range s.asm.FlushDetails() {
		s.putDetail(d)
	}
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
