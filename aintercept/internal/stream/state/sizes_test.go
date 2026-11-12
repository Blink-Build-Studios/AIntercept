//go:build !full

package state

// make check's sizes: flat cost compared at 10 and 1,000 records, a third of
// the generated seeds. make test-full (-tags full) runs 100,000 and all of them.
const (
	flatBig  = 1_000
	seedFrac = 3
)
