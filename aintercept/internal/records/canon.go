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
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Stats is what a canon run did.
type Stats struct {
	Files, Rows, Bad, Records int64
	Cases                     map[string]int64
}

// CanonDirRecords turns a directory of raw observer files (…/instance=<pod>/<file>.ndjson.gz,
// as the observer used to upload them, and as the tests' datasets write them)
// into records, through the same assembler the live observer runs: each pod's
// files in order, one assembler per pod, handing the records each row
// completes to emit (canon -load inserts them into Postgres).
func CanonDirRecords(ctx context.Context, dir string, emit func([]Record) error) (Stats, error) {
	return canonDirSteps(ctx, dir, false, func(_ string, rs []Record, _ []Detail) error {
		if len(rs) == 0 {
			return nil
		}
		return emit(rs)
	})
}

// CanonDirRaw is CanonDirRecords writing v9's raw files instead (raw.go): each
// pod's records and completed calls' rows, as the live sink writes them, to
// raw(pod) (the writer's members stream for that pod).
func CanonDirRaw(ctx context.Context, dir string, raw func(pod string) RawWriter) (Stats, error) {
	return canonDirSteps(ctx, dir, true, func(pod string, rs []Record, ds []Detail) error {
		ms, err := RawMembers(rs, ds)
		if err != nil || len(ms) == 0 {
			return err
		}
		w := raw(pod)
		for _, m := range ms {
			if err := w.Write(m); err != nil {
				return err
			}
		}
		return nil
	})
}

// canonDirSteps runs each pod's files in order through one assembler, handing
// emit what each row completes (and, with details, the calls' rows).
func canonDirSteps(ctx context.Context, dir string, details bool, emit func(pod string, rs []Record, ds []Detail) error) (Stats, error) {
	pods, byPod, err := podFiles(dir)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Cases: map[string]int64{}}
	for _, pod := range pods {
		asm := NewAssembler("canon-" + pod)
		asm.KeepDetail = details
		var emitErr error
		out := func(rs []Record) {
			ds := asm.TakeDetails()
			if emitErr == nil && (len(rs) > 0 || len(ds) > 0) {
				emitErr = emit(pod, rs, ds)
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
