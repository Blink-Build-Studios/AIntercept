package stream

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// DefaultTTL is how long a lease (and a member's heartbeat) stays live
// without renewal, judged by the store's own write times, never pod clocks.
const DefaultTTL = 30 * time.Second

// Leaser holds this replica's bucket leases. A lease is the newest of a
// bucket's create-only epoch objects: claiming or renewing writes the next
// epoch, so of two replicas racing for one, exactly one wins. Each Tick
// heartbeats, renews what this replica owns, gives back what's above its share
// (ceil(buckets / live replicas)), and claims free buckets up to it.
type Leaser struct {
	S       Store
	L       Layout
	Replica string
	Buckets int
	TTL     time.Duration

	owned map[int]int64
	now   time.Time // the store's time at the last heartbeat
}

type leaseBody struct {
	Replica  string `json:"replica"`
	Released bool   `json:"released,omitempty"`
}

type leaseState struct {
	epoch    int64
	body     leaseBody
	modified time.Time
	old      []string // older epochs' keys, to collect
}

func (le *Leaser) ttl() time.Duration {
	if le.TTL == 0 {
		return DefaultTTL
	}
	return le.TTL
}

// Tick runs one round and returns the buckets owned, with their epochs.
func (le *Leaser) Tick(ctx context.Context) (map[int]int64, error) {
	if le.owned == nil {
		le.owned = map[int]int64{}
	}
	if err := le.heartbeat(ctx); err != nil {
		return le.copyOwned(), err
	}
	live, err := le.liveMembers(ctx)
	if err != nil {
		return le.copyOwned(), err
	}
	target := (le.Buckets + live - 1) / live

	states := make([]leaseState, le.Buckets)
	for b := 0; b < le.Buckets; b++ {
		if states[b], err = le.latest(ctx, b); err != nil {
			return le.copyOwned(), err
		}
	}
	// Renew what this replica holds, up to its share; give back the rest.
	kept := 0
	for b := 0; b < le.Buckets; b++ {
		s := states[b]
		if s.body.Replica != le.Replica || s.body.Released || !le.isLive(s) {
			delete(le.owned, b)
			continue
		}
		if kept >= target {
			_, _ = le.write(ctx, b, s.epoch+1, leaseBody{Replica: le.Replica, Released: true})
			delete(le.owned, b)
			continue
		}
		if ok, _ := le.write(ctx, b, s.epoch+1, leaseBody{Replica: le.Replica}); ok {
			le.owned[b] = s.epoch + 1
			kept++
		} else {
			delete(le.owned, b)
		}
	}
	// Claim free buckets, starting at a place of this replica's own so that
	// replicas don't all race for the same ones.
	start := int(hashString(le.Replica) % uint64(max(le.Buckets, 1)))
	for i := 0; i < le.Buckets && len(le.owned) < target; i++ {
		b := (start + i) % le.Buckets
		if _, mine := le.owned[b]; mine {
			continue
		}
		s := states[b]
		if s.epoch > 0 && !s.body.Released && le.isLive(s) {
			continue
		}
		if ok, _ := le.write(ctx, b, s.epoch+1, leaseBody{Replica: le.Replica}); ok {
			le.owned[b] = s.epoch + 1
		}
	}
	le.collect(ctx, states)
	return le.copyOwned(), nil
}

// Release gives a bucket back at once (a graceful shutdown, or rebalancing).
func (le *Leaser) Release(ctx context.Context, b int) error {
	s, err := le.latest(ctx, b)
	if err != nil {
		return err
	}
	delete(le.owned, b)
	if s.body.Replica != le.Replica || s.body.Released {
		return nil
	}
	_, err = le.write(ctx, b, s.epoch+1, leaseBody{Replica: le.Replica, Released: true})
	return err
}

// ReleaseAll gives back every bucket this replica holds.
func (le *Leaser) ReleaseAll(ctx context.Context) error {
	var errs []error
	for b := range le.copyOwned() {
		errs = append(errs, le.Release(ctx, b))
	}
	return errors.Join(errs...)
}

// Owner is who holds bucket b, at which epoch, and whether the lease is live.
func (le *Leaser) Owner(ctx context.Context, b int) (string, int64, bool, error) {
	if err := le.heartbeat(ctx); err != nil {
		return "", 0, false, err
	}
	s, err := le.latest(ctx, b)
	return s.body.Replica, s.epoch, s.epoch > 0 && !s.body.Released && le.isLive(s), err
}

func (le *Leaser) copyOwned() map[int]int64 {
	out := make(map[int]int64, len(le.owned))
	for b, e := range le.owned {
		out[b] = e
	}
	return out
}

func (le *Leaser) isLive(s leaseState) bool { return le.now.Sub(s.modified) <= le.ttl() }

// heartbeat writes this replica's member object; its store time is "now" for
// every age this round judges.
func (le *Leaser) heartbeat(ctx context.Context) error {
	key := le.L.MemberKey(le.Replica)
	if err := le.S.Put(ctx, key, []byte(le.Replica)); err != nil {
		return err
	}
	info, err := le.S.Stat(ctx, key)
	if err != nil {
		return err
	}
	le.now = info.Modified
	return nil
}

func (le *Leaser) liveMembers(ctx context.Context) (int, error) {
	ms, err := le.S.ListInfo(ctx, le.L.membersPrefix())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		if le.now.Sub(m.Modified) <= le.ttl() {
			n++
		}
	}
	return max(n, 1), nil
}

// latest is a bucket's newest lease epoch (epoch 0: never leased).
func (le *Leaser) latest(ctx context.Context, b int) (leaseState, error) {
	objs, err := le.S.ListInfo(ctx, le.L.LeasePrefix(b))
	if err != nil {
		return leaseState{}, err
	}
	for i := len(objs) - 1; i >= 0; i-- {
		ep, ok := leaseEpoch(objs[i].Key)
		if !ok {
			continue
		}
		raw, err := le.S.Get(ctx, objs[i].Key)
		if errors.Is(err, process.ErrNotFound) {
			continue
		}
		if err != nil {
			return leaseState{}, err
		}
		var body leaseBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return leaseState{}, err
		}
		st := leaseState{epoch: ep, body: body, modified: objs[i].Modified}
		for _, o := range objs[:max(i-3, 0)] { // keep the newest few
			st.old = append(st.old, o.Key)
		}
		return st, nil
	}
	return leaseState{}, nil
}

func (le *Leaser) write(ctx context.Context, b int, epoch int64, body leaseBody) (bool, error) {
	raw, _ := json.Marshal(body)
	return le.S.PutIfAbsent(ctx, le.L.LeaseKey(b, epoch), raw)
}

// collect deletes lease epochs well behind each bucket's newest.
func (le *Leaser) collect(ctx context.Context, states []leaseState) {
	for _, s := range states {
		for _, k := range s.old {
			_ = le.S.Delete(ctx, k)
		}
	}
}

func leaseEpoch(key string) (int64, bool) {
	name := key[strings.LastIndex(key, "/")+1:]
	name, ok := strings.CutPrefix(name, "epoch-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(name, 10, 64)
	return n, err == nil
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
