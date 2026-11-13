package stream

import (
	"fmt"
	"strings"
)

// Layout names every object the processor writes, under Prefix (the same
// prefix ingest writes records/ under).
//
//	stream/v1/buckets/NN/commit-<seq>.json             the bucket's commit log
//	stream/v1/tables/<table>/bucket=NN/run-<epoch>-<seq>.parquet
//	stream/v1/tables/<table>/bucket=NN/base-<gen>-<epoch>-<seq>-<part>.parquet
//	stream/v1/state/bucket=NN/run-<epoch>-<seq>.parquet, base-<gen>-<epoch>-<seq>-<part>.parquet
//	stream/v1/manifests/bucket=NN/base-<gen>-<epoch>-<seq>.json  a base generation's parts
//	stream/v1/leases/bucket=NN/epoch-<n>                who owns the bucket
//	stream/v1/members/<replica>                         live replicas (heartbeats)
type Layout struct{ Prefix string }

func (l Layout) root() string {
	p := l.Prefix
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p + "stream/v1/"
}

func (l Layout) CommitPrefix(b int) string { return fmt.Sprintf("%sbuckets/%02d/", l.root(), b) }
func (l Layout) CommitKey(b int, seq int64) string {
	return fmt.Sprintf("%scommit-%012d.json", l.CommitPrefix(b), seq)
}
func (l Layout) tablePrefix(table string, b int) string {
	return fmt.Sprintf("%stables/%s/bucket=%02d/", l.root(), table, b)
}
func (l Layout) RunKey(table string, b int, epoch, seq int64) string {
	return fmt.Sprintf("%srun-%010d-%012d.parquet", l.tablePrefix(table, b), epoch, seq)
}

// Base and state-base names carry the writer's epoch and commit sequence as
// well as the generation: every object a writer creates has a name no other
// writer can produce, so a fenced owner can never overwrite a live file.
func (l Layout) BaseKey(table string, b int, gen, epoch, seq int64, part int) string {
	return fmt.Sprintf("%sbase-%012d-%010d-%012d-%04d.parquet", l.tablePrefix(table, b), gen, epoch, seq, part)
}
func (l Layout) statePrefix(b int) string { return fmt.Sprintf("%sstate/bucket=%02d/", l.root(), b) }
func (l Layout) StateKey(b int, epoch, seq int64) string {
	return fmt.Sprintf("%srun-%010d-%012d.parquet", l.statePrefix(b), epoch, seq)
}
func (l Layout) StateBaseKey(b int, gen, epoch, seq int64, part int) string {
	return fmt.Sprintf("%sbase-%012d-%010d-%012d-%04d.parquet", l.statePrefix(b), gen, epoch, seq, part)
}
func (l Layout) manifestPrefix(b int) string {
	return fmt.Sprintf("%smanifests/bucket=%02d/", l.root(), b)
}
func (l Layout) BaseManifestKey(b int, gen, epoch, seq int64) string {
	return fmt.Sprintf("%sbase-%012d-%010d-%012d.json", l.manifestPrefix(b), gen, epoch, seq)
}
func (l Layout) LeasePrefix(b int) string { return fmt.Sprintf("%sleases/bucket=%02d/", l.root(), b) }
func (l Layout) LeaseKey(b int, epoch int64) string {
	return fmt.Sprintf("%sepoch-%010d", l.LeasePrefix(b), epoch)
}
func (l Layout) membersPrefix() string           { return l.root() + "members/" }
func (l Layout) MemberKey(replica string) string { return l.membersPrefix() + replica }
