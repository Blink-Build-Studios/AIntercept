//go:build full

package state

// make test-full's sizes: flat cost up to 100,000 records, every generated seed.
const (
	flatBig  = 100_000
	seedFrac = 1
)
