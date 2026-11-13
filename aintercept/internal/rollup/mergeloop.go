package rollup

import (
	"context"
	"errors"
	"time"
)

// The merge loop's stub (implemented next).

// MergeTick is one merge period on this replica.
func (w *Worker) MergeTick(ctx context.Context) (MergeResult, bool, error) {
	return MergeResult{}, false, errors.New("not yet")
}

// ReleaseMerge lets go of lock:merge if this replica holds it.
func (w *Worker) ReleaseMerge(ctx context.Context) {}

var _ = time.Second
