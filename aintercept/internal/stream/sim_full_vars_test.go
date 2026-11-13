//go:build full

package stream

func init() {
	historySizes = []int{1, 2, 4, 8}
	tierSizes = []int{10, 100, 1_000, 10_000, 100_000}
	burstSteps = 400
	simRecords, simSteps = 3000, 400
}
