//go:build full

package stream

func init() {
	historySizes = []int{1, 2, 4, 8}
	tierSizes = []int{10, 100, 1_000, 10_000, 30_000} // 100,000 is quadratic in table compaction (whole-base rewrites)
	burstSteps = 400
	simRecords, simSteps = 3000, 400
}
