package canon

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// No NUL and no oversized ids reach Postgres (docs/PIPELINE.md §3.1): its text
// can't hold NUL, and a btree entry over about 2.7 KB fails the insert, so
// either would wedge a key. Records are built with these (records.Clean), so
// the state, the oracle and the API all see the same values. Only pathological
// input changes.

// MaxIDBytes is the longest client-controlled id kept as it is; a longer one
// becomes sha256:<hex> of it.
const MaxIDBytes = 512

// CleanText is s with each NUL replaced by U+FFFD.
func CleanText(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "\uFFFD")
}

// CleanJSON is CleanText for a JSON document, which also replaces each \u0000
// escape with \ufffd: a reader that decodes it gets no NUL either. Other
// escapes, and the document's key order, are kept.
func CleanJSON(s string) string {
	s = CleanText(s)
	if !strings.Contains(s, `\u0000`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		if strings.HasPrefix(s[i:], `\u0000`) {
			b.WriteString(`\ufffd`)
			i += 5
			continue
		}
		b.WriteString(s[i : i+2]) // an escape: its character isn't the start of another
		i++
	}
	return b.String()
}

// CleanID is CleanText for a client-controlled id (a session, request, call,
// event or prompt id); one over MaxIDBytes then becomes sha256:<hex> of it.
func CleanID(s string) string {
	s = CleanText(s)
	if len(s) <= MaxIDBytes {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
