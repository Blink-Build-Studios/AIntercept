package rollup

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// The merge loop (docs/PIPELINE.md §3): every MergeEvery, the replica holding
// lock:merge runs the merge over the keys in `committed`; the others skip.
const (
	mergeLock  = "lock:merge"
	mergeAlive = "merge:alive" // set by the merge: gone means Redis lost `committed` too
)

// redisMergeQueue is the merge's queue: the committed set.
type redisMergeQueue struct{ r *redis.Client }

func (q redisMergeQueue) Pop(ctx context.Context, n int) ([]string, error) {
	keys, err := q.r.SPopN(ctx, committedSet, int64(n)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return keys, err
}

func (q redisMergeQueue) Requeue(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	return q.r.SAdd(ctx, committedSet, toAny(keys)...).Err()
}

func (q redisMergeQueue) Alive(ctx context.Context) (bool, error) {
	n, err := q.r.Exists(ctx, mergeAlive).Result()
	return n > 0, err
}

func (q redisMergeQueue) MarkAlive(ctx context.Context) error {
	return q.r.Set(ctx, mergeAlive, "1", 0).Err()
}

// MergeTick is one merge period on this replica: take lock:merge (or renew it
// if this replica holds it) and run the merge once. ran is false when another
// replica holds the lock. A run that outlives the lock is still safe: the
// merge commit is create-only.
func (w *Worker) MergeTick(ctx context.Context) (res MergeResult, ran bool, err error) {
	ok, err := w.cfg.Redis.SetNX(ctx, mergeLock, w.cfg.Replica, w.cfg.LockTTL).Result()
	if err != nil {
		return res, false, err
	}
	if !ok {
		n, err := renewScript.Run(ctx, w.cfg.Redis, []string{mergeLock}, w.cfg.Replica, w.cfg.LockTTL.Milliseconds()).Int()
		if err != nil || n == 0 {
			return res, false, err
		}
	}
	m := &Merger{Store: w.cfg.Store, Prefix: w.cfg.Prefix, Queue: redisMergeQueue{w.cfg.Redis}, Clock: w.cfg.Clock,
		Recent: w.cfg.SweepSpan, Flag: func(ctx context.Context, keys []string) error {
			return w.cfg.Redis.SAdd(ctx, dirtySet, toAny(keys)...).Err()
		}}
	res, err = m.RunOnce(ctx)
	return res, true, err
}

// ReleaseMerge lets go of lock:merge if this replica holds it.
func (w *Worker) ReleaseMerge(ctx context.Context) {
	_ = releaseScript.Run(ctx, w.cfg.Redis, []string{mergeLock}, w.cfg.Replica).Err()
}

// MergeLoop runs MergeTick every MergeEvery until ctx ends, then releases the
// lock.
func (w *Worker) MergeLoop(ctx context.Context) {
	t := time.NewTicker(w.cfg.MergeEvery)
	defer t.Stop()
	for {
		if res, ran, err := w.MergeTick(ctx); err != nil && !errors.Is(err, ErrMergeFenced) {
			slog.Warn("rollup: merge failed; its keys wait for the next run", "err", err)
		} else if ran && res.N > 0 {
			slog.Info("rollup: merged", "n", res.N, "keys", res.Keys, "partitions", len(res.Rewritten), "recovered", res.Recovered,
				"flagged", res.Flagged)
		}
		select {
		case <-ctx.Done():
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.ReleaseMerge(rctx)
			cancel()
			return
		case <-t.C:
		}
	}
}
