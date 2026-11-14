// Package rollup is the per-key layout under <prefix>/v1/keys/ that ingest and
// the workers (internal/worker) share: the ingest files' names (InName,
// InTime), each key's folder and its state chunks. v5's rollup loop (Redis
// locks, a create-only commit per run, the out/ tables) is gone
// (docs/PIPELINE.md v7, Deleted). The commit objects (Commit, WriteOnePass,
// ReadNewest) and the merge stay until the final gate: the oracle's layout
// (`aintercept v1layout`) still writes them.
//
// Layout (all under <prefix>/v1/), as v5 wrote it:
//
//	keys/<esc key>/in/<s3 time>-<incarnation>-<seq>.parquet   ingest files (step 1)
//	keys/<esc key>/out/<n 12 digits>-<nonce>/<table>.parquet  a run's changed rows, one file per table
//	keys/<esc key>/state/<n 12 digits>-<nonce>                a run's state checkpoint chunk
//	keys/<esc key>/commit/<n 12 digits>.json                  create-only; the key's current files
//	recent/<s3 minute>/<incarnation>-<seq>.json               per pod per minute: JSON list of keys flushed
//
// <esc key> is url.PathEscape(routing key). <s3 time> is InTime (UTC, to the
// second, S3's clock); <s3 minute> is RecentMinute. Table files use the stream
// table codec: key, id, tomb, then the table's typed columns (state.TableTypes).
// A commit's out lists every current output directory oldest first, each with
// the tables it holds: the newest
// row per (table, key, id) wins and tombstones drop it.
package rollup

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TimeFormat is <s3 time> in an ingest file's name; MinuteFormat is <s3 minute>.
const (
	TimeFormat   = "20060102T150405Z"
	MinuteFormat = "20060102T1504Z"
)

// Commit is keys/<key>/commit/<n>.json. Paths are relative to keys/<esc key>/.
type Commit struct {
	N           int64    `json:"n"`
	Key         string   `json:"key"`                    // the routing key (unescaped)
	Out         []Out    `json:"out"`                    // every current output directory, oldest first
	State       []string `json:"state"`                  // "state/<n>-<nonce>", oldest first
	StateLevels []int    `json:"state_levels,omitempty"` // each State entry's compaction tier
	LastApplied string   `json:"last_applied"`           // the newest in/ name applied
	Grace       []string `json:"grace"`                  // in/ names applied within the grace window
	UpdatedNS   int64    `json:"updated_ns"`
	// NewestRowNS is the newest raw row time of every ingest file applied (the
	// pods' clock): today's "newest data", including rows that complete no record.
	NewestRowNS int64 `json:"newest_row_ns,omitempty"`
	// ExpiresNS is the data time the key's first call still waiting for its
	// ending expires after (0: none waits). The merge keeps it in merge_keys and
	// flags the key once its newest data passes it, so a quiet key's calls expire.
	ExpiresNS int64 `json:"expires_ns,omitempty"`
	// Garbage are files superseded by compaction, deleted once DeleteAfterNS passes.
	Garbage []Garbage `json:"garbage,omitempty"`
}

// Out is one output directory (relative to the key folder, ending in "/") and
// exactly the tables it holds a <table>.parquet for, so readers never LIST. Level
// is its compaction tier (0: a run's own output).
type Out struct {
	Dir    string   `json:"dir"`
	Tables []string `json:"tables"`
	Level  int      `json:"level,omitempty"`
}

// Garbage is a superseded file or directory (relative path) and when it may go.
type Garbage struct {
	Path          string `json:"path"`
	DeleteAfterNS int64  `json:"delete_after_ns"`
}

// KeyDir is the key's folder: <prefix>/v1/keys/<esc key>/.
func KeyDir(prefix, key string) string {
	return join(prefix, "v1/keys/"+url.PathEscape(key)) + "/"
}

// InPrefix is where the key's ingest files are.
func InPrefix(prefix, key string) string { return KeyDir(prefix, key) + "in/" }

// InName is an ingest file's name (relative to in/).
func InName(t time.Time, incarnation string, seq int64) string {
	return fmt.Sprintf("%s-%s-%012d.parquet", t.UTC().Format(TimeFormat), incarnation, seq)
}

// InTime is the S3 time an ingest file's name carries.
func InTime(name string) (time.Time, error) {
	i := strings.IndexByte(name, '-')
	if i < 0 {
		return time.Time{}, fmt.Errorf("rollup: ingest name %q has no time", name)
	}
	return time.Parse(TimeFormat, name[:i])
}

// CommitKey is commit n's object key.
func CommitKey(prefix, key string, n int64) string {
	return fmt.Sprintf("%scommit/%012d.json", KeyDir(prefix, key), n)
}

// CommitN parses a commit object's sequence from its name.
func CommitN(name string) (int64, bool) {
	base := name[strings.LastIndexByte(name, '/')+1:]
	if !strings.HasSuffix(base, ".json") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(base, ".json"), 10, 64)
	return n, err == nil
}

// OutDir is run n's output directory (relative to the key folder).
func OutDir(n int64, nonce string) string { return fmt.Sprintf("out/%012d-%s/", n, nonce) }

// StatePath is run n's state chunk (relative to the key folder).
func StatePath(n int64, nonce string) string { return fmt.Sprintf("state/%012d-%s", n, nonce) }

// TableFile is a table's file in an output directory (relative to the key folder).
func TableFile(outDir, table string) string { return outDir + table + ".parquet" }

// RecentPrefix is the recent index for one S3 minute.
func RecentPrefix(prefix string, minute time.Time) string {
	return join(prefix, "v1/recent/"+minute.UTC().Format(MinuteFormat)) + "/"
}

// RecentName is a pod's recent-index object for one minute.
func RecentName(prefix string, minute time.Time, incarnation string, seq int64) string {
	return fmt.Sprintf("%s%s-%012d.json", RecentPrefix(prefix, minute), incarnation, seq)
}

func join(prefix, rest string) string {
	if p := strings.Trim(prefix, "/"); p != "" {
		return p + "/" + rest
	}
	return rest
}
