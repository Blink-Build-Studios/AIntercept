package state

import "sort"

// omap is an ordered map: sorted chunks of at most chunkMax entries, so a lookup
// or an insert costs a binary search plus one chunk's shift, however many
// entries it holds. The per-key indexes are these, which keeps an update's work
// flat as a session grows.
type omap[K, V any] struct {
	less   func(a, b K) bool
	chunks []*chunk[K, V]
	n      int
}

const chunkMax = 128

type entry[K, V any] struct {
	k K
	v V
}

type chunk[K, V any] struct{ e []entry[K, V] }

func newOmap[K, V any](less func(a, b K) bool) *omap[K, V] { return &omap[K, V]{less: less} }

func (m *omap[K, V]) Len() int { return m.n }

// chunkFor is the index of the chunk k belongs in: the last whose first entry is
// at or before k (the first chunk when k is before them all).
func (m *omap[K, V]) chunkFor(k K) int {
	i := sort.Search(len(m.chunks), func(i int) bool { return m.less(k, m.chunks[i].e[0].k) })
	if i > 0 {
		i--
	}
	return i
}

// find is k's chunk and position, and whether it's there.
func (m *omap[K, V]) find(k K) (int, int, bool) {
	if len(m.chunks) == 0 {
		return 0, 0, false
	}
	ci := m.chunkFor(k)
	e := m.chunks[ci].e
	j := sort.Search(len(e), func(j int) bool { return !m.less(e[j].k, k) })
	return ci, j, j < len(e) && !m.less(k, e[j].k)
}

func (m *omap[K, V]) Set(k K, v V) {
	if len(m.chunks) == 0 {
		m.chunks = []*chunk[K, V]{{e: []entry[K, V]{{k, v}}}}
		m.n = 1
		return
	}
	ci, j, ok := m.find(k)
	c := m.chunks[ci]
	if ok {
		c.e[j].v = v
		return
	}
	c.e = append(c.e, entry[K, V]{})
	copy(c.e[j+1:], c.e[j:])
	c.e[j] = entry[K, V]{k, v}
	m.n++
	if len(c.e) > chunkMax {
		half := len(c.e) / 2
		right := &chunk[K, V]{e: append([]entry[K, V](nil), c.e[half:]...)}
		c.e = c.e[:half:half]
		m.chunks = append(m.chunks, nil)
		copy(m.chunks[ci+2:], m.chunks[ci+1:])
		m.chunks[ci+1] = right
	}
}

func (m *omap[K, V]) Delete(k K) {
	ci, j, ok := m.find(k)
	if !ok {
		return
	}
	c := m.chunks[ci]
	c.e = append(c.e[:j], c.e[j+1:]...)
	m.n--
	if len(c.e) == 0 {
		m.chunks = append(m.chunks[:ci], m.chunks[ci+1:]...)
	}
}

// Floor is the greatest entry at or before k.
func (m *omap[K, V]) Floor(k K) (K, V, bool) {
	var zk K
	var zv V
	if len(m.chunks) == 0 {
		return zk, zv, false
	}
	ci, j, ok := m.find(k)
	if ok {
		e := m.chunks[ci].e[j]
		return e.k, e.v, true
	}
	if j > 0 { // chunkFor picked the last chunk starting at or before k, so j is 0 only when k is before every entry
		e := m.chunks[ci].e[j-1]
		return e.k, e.v, true
	}
	return zk, zv, false
}

// Ascend calls fn on the entries from k on, in order, until fn returns false.
func (m *omap[K, V]) Ascend(k K, fn func(K, V) bool) {
	if len(m.chunks) == 0 {
		return
	}
	ci, j, _ := m.find(k)
	for ; ci < len(m.chunks); ci, j = ci+1, 0 {
		for e := m.chunks[ci].e; j < len(e); j++ {
			if !fn(e[j].k, e[j].v) {
				return
			}
		}
	}
}

// All calls fn on every entry, in order.
func (m *omap[K, V]) All(fn func(K, V) bool) {
	for _, c := range m.chunks {
		for _, e := range c.e {
			if !fn(e.k, e.v) {
				return
			}
		}
	}
}

// Lower is the greatest entry strictly before k.
func (m *omap[K, V]) Lower(k K) (K, V, bool) {
	var zk K
	var zv V
	if len(m.chunks) == 0 {
		return zk, zv, false
	}
	ci, j, _ := m.find(k)
	if j > 0 {
		e := m.chunks[ci].e[j-1]
		return e.k, e.v, true
	}
	if ci > 0 {
		c := m.chunks[ci-1].e
		e := c[len(c)-1]
		return e.k, e.v, true
	}
	return zk, zv, false
}

// First is the least entry.
func (m *omap[K, V]) First() (K, V, bool) {
	var zk K
	var zv V
	if len(m.chunks) == 0 {
		return zk, zv, false
	}
	e := m.chunks[0].e[0]
	return e.k, e.v, true
}

// Last is the greatest entry.
func (m *omap[K, V]) Last() (K, V, bool) {
	var zk K
	var zv V
	if len(m.chunks) == 0 {
		return zk, zv, false
	}
	c := m.chunks[len(m.chunks)-1].e
	e := c[len(c)-1]
	return e.k, e.v, true
}
