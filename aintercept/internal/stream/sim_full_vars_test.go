//go:build full

package stream

func init() {
	historySizes = []int{1, 2, 4, 8}
	burstSteps = 400
	simRecords, simSteps = 3000, 400
}
