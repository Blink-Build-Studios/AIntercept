//go:build full

package stream

func init() {
	tieredSizes = append(tieredSizes, 100_000, 1_000_000)
	tieredOrderOps = append(tieredOrderOps, "u2")
}
