package records

import (
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"

	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

// DefaultBucketBits is how many hash bits name a bucket: 64 buckets. One more
// bit splits every bucket cleanly in two.
const DefaultBucketBits = 6

// RoutingKey is the key a record is grouped by: its session ("s:<id>"), or,
// for what names none, its credential and day ("c:<key hash>:<YYYY-MM-DD>"),
// or with no credential its source IP and hour ("ip:<ip>:<YYYY-MM-DDTHH>");
// an account sighting is its account ("a:<id>"). An analytics request's own
// call goes with the session its batch's earliest event names; an event, with
// its own session.
func RoutingKey(r *Record) string {
	switch {
	case r.Kind == KindAccount && r.Account != nil:
		return "a:" + r.Account.AccountID
	case r.Kind == KindClientEvent && r.Event != nil:
		if r.Event.SessionID != "" {
			return "s:" + r.Event.SessionID
		}
		var kh, ip string
		if r.Call != nil {
			kh, ip = r.Call.KeyHash, r.Call.IP
		}
		return credentialKey(kh, ip, r.Event.RowNS)
	case r.Call != nil:
		if r.BatchSession != "" {
			return "s:" + r.BatchSession
		}
		if r.Call.SessionID != "" {
			return "s:" + r.Call.SessionID
		}
		at := r.Call.StartNS
		if at == 0 {
			at = r.Call.EndNS
		}
		return credentialKey(r.Call.KeyHash, r.Call.IP, at)
	}
	return ""
}

// credentialKey is the key of what names no session: its credential and day,
// or with no credential its source IP and hour ("ip:<ip>:<YYYY-MM-DDTHH>").
func credentialKey(keyHash, ip string, ns int64) string {
	if keyHash == "" && ip != "" {
		return "ip:" + ip + ":" + time.Unix(0, ns).UTC().Format("2006-01-02T15")
	}
	return "c:" + keyHash + ":" + time.Unix(0, ns).UTC().Format("2006-01-02")
}

// BucketOf is a key's bucket: the top bits of its FNV-1a 64-bit hash. The API
// computes the same (its test pins these vectors too).
func BucketOf(key string, bits int) int {
	h := fnv.New64a()
	h.Write([]byte(key))
	return int(h.Sum64() >> (64 - bits))
}

// ObjectKeyPrefix is a bucket's records prefix.
func ObjectKeyPrefix(prefix string, bucket int) string {
	return fmt.Sprintf("%srecords/v%d/bucket=%02d/", withSlash(prefix), Version, bucket)
}

// ObjectKey names one records file: its bucket, the minute it was uploaded in,
// and the writer's incarnation and sequence.
func ObjectKey(prefix string, bucket int, uploaded time.Time, incarnation string, seq int64) string {
	return fmt.Sprintf("%sminute=%s/%s-%012d.parquet", ObjectKeyPrefix(prefix, bucket),
		uploaded.UTC().Format("2006-01-02T15:04"), incarnation, seq)
}

// ObjectKeyTemplate is ObjectKey with its minute left as events.UploadMinute,
// for the writer to fill in at each upload attempt.
func ObjectKeyTemplate(prefix string, bucket int, incarnation string, seq int64) string {
	return fmt.Sprintf("%sminute=%s/%s-%012d.parquet", ObjectKeyPrefix(prefix, bucket),
		events.UploadMinute, incarnation, seq)
}

func withSlash(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}
