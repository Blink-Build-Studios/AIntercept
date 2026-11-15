package snap

import (
	"math"
	"strings"
	"testing"
	"unsafe"
)

type leaf struct {
	name string
	n    int64
}

type node struct {
	id       string
	kids     []*node
	parent   *node
	byName   map[string]*leaf
	alias    map[string]*leaf // the same map as byName, or another
	any      any
	anys     []any
	less     func(a, b int64) bool
	arr      [3]int32
	f        float64
	b        bool
	u8       uint8
	bs       []byte
	empty    []int
	nilSlice []int
	keyed    map[[2]string]leaf
	nested   map[string]map[string]bool
	_        struct{}
}

func lessInt(a, b int64) bool { return a < b }

type boxed struct{ V int }

func init() {
	Register(leaf{}, &leaf{}, boxed{}, "", int64(0))
	RegisterFunc("snap.lessInt", lessInt)
}

func sample() *node {
	shared := &leaf{name: "shared", n: 7}
	m := map[string]*leaf{"a": shared, "b": shared, "c": {name: "own", n: -3}}
	root := &node{id: "root", byName: m, alias: m, any: shared, anys: []any{boxed{4}, "s", int64(9), nil, shared},
		less: lessInt, arr: [3]int32{1, -2, 3}, f: math.Pi, b: true, u8: 200, bs: []byte("bytes"), empty: []int{},
		keyed: map[[2]string]leaf{{"x", "y"}: {name: "k", n: 1}}, nested: map[string]map[string]bool{"o": {"i": true}}}
	kid := &node{id: "kid", parent: root, byName: map[string]*leaf{"s": shared}, less: lessInt}
	root.kids = []*node{kid, kid, {id: "other", parent: kid}}
	return root
}

func roundTrip[T any](t *testing.T, v *T) *T {
	t.Helper()
	b, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := Decode(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestRoundTripKeepsEveryFieldAndTheSharing(t *testing.T) {
	in := sample()
	out := roundTrip(t, in)
	if err := Equal(in, out); err != nil {
		t.Fatal(err)
	}
	// The sharing is the decoded graph's own.
	if out.byName["a"] != out.byName["b"] || out.byName["a"] != out.any.(*leaf) || out.kids[0] != out.kids[1] ||
		out.kids[0].parent != out || out.kids[2].parent != out.kids[0] || out.anys[4].(*leaf) != out.byName["a"] {
		t.Fatal("a shared pointer decoded as copies")
	}
	out.alias["new"] = nil
	if _, ok := out.byName["new"]; !ok {
		t.Fatal("a shared map decoded as copies")
	}
	if out.empty == nil || out.nilSlice != nil {
		t.Fatal("an empty slice and a nil one must stay apart")
	}
	if !out.less(1, 2) || out.less(2, 1) {
		t.Fatal("the registered func didn't come back")
	}
}

func TestEqualFindsEachDifference(t *testing.T) {
	cases := map[string]func(n *node){
		"a field":       func(n *node) { n.kids[2].id = "x" },
		"an unexported": func(n *node) { n.byName["c"].n = 4 },
		"a map key":     func(n *node) { n.keyed[[2]string{"x", "z"}] = leaf{} },
		"the sharing":   func(n *node) { n.byName["b"] = &leaf{name: "shared", n: 7} },
		"a map alias":   func(n *node) { n.alias = map[string]*leaf{"a": n.byName["a"], "b": n.byName["b"], "c": n.byName["c"]} },
		"nil vs empty":  func(n *node) { n.empty = nil },
		"an interface":  func(n *node) { n.anys[0] = boxed{5} },
		"a func":        func(n *node) { n.less = nil },
		"a float":       func(n *node) { n.f = 3 },
	}
	for name, change := range cases {
		a, b := sample(), sample()
		if err := Equal(a, b); err != nil {
			t.Fatalf("two samples differ: %v", err)
		}
		change(b)
		if err := Equal(a, b); err == nil {
			t.Fatalf("%s: no difference found", name)
		}
	}
}

// A string held twice is stored once: a short one by its content, a long one
// (an event's JSON) when it's the same string (its bytes shared), as the
// state's interning and a decoded snapshot make every repeated one.
func TestStringsAreStoredOnce(t *testing.T) {
	long := strings.Repeat("metadata ", 1000)
	short := "session-id-0001"
	type four struct{ a, b, c, d, e string }
	v := &four{long, long, long[:9], short, strings.Clone(short)}
	b, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > len(long)+64 {
		t.Fatalf("%d bytes for one long string of %d held twice", len(b), len(long))
	}
	out := roundTrip(t, v)
	if unsafe.StringData(out.a) != unsafe.StringData(out.b) || unsafe.StringData(out.d) != unsafe.StringData(out.e) {
		t.Fatal("a string held twice decodes as copies")
	}
	if err := Equal(v, out); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesWhatItCantEncode(t *testing.T) {
	type withChan struct{ c chan int }
	if _, err := Encode(&withChan{c: make(chan int)}); err == nil {
		t.Fatal("a channel was encoded")
	}
	type unregistered struct{ X int }
	type withAny struct{ v any }
	if _, err := Encode(&withAny{v: unregistered{1}}); err == nil {
		t.Fatal("an unregistered type was encoded")
	}
	type withFunc struct{ f func() }
	if _, err := Encode(&withFunc{f: func() {}}); err == nil {
		t.Fatal("an unregistered func was encoded")
	}
	if _, err := Encode(&withFunc{}); err != nil {
		t.Fatalf("a nil func: %v", err)
	}
}

func TestDecodeRefusesTruncatedAndTrailingBytes(t *testing.T) {
	b, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{1, len(b) / 3, len(b) - 1} {
		var out node
		if err := Decode(b[:cut], &out); err == nil {
			t.Fatalf("decoded %d of %d bytes", cut, len(b))
		}
	}
	var out node
	if err := Decode(append(b, 0), &out); err == nil {
		t.Fatal("decoded with a byte after the value")
	}
}

// A long chain (a list's next pointers) decodes without a stack problem.
func TestLongChains(t *testing.T) {
	type link struct {
		v    int
		next *link
	}
	var head *link
	for i := 0; i < 200_000; i++ {
		head = &link{i, head}
	}
	out := roundTrip(t, &head)
	n := 0
	for l := *out; l != nil; l = l.next {
		n++
	}
	if n != 200_000 {
		t.Fatalf("%d links", n)
	}
}

// A snapshot's reader refuses one whose types differ: a field added, renamed
// or retyped anywhere in the graph changes the fingerprint.
func TestFingerprintFollowsEveryType(t *testing.T) {
	type a struct {
		X int
		m map[string]*leaf
	}
	type b struct {
		X int
		m map[string]*node
	}
	type c struct {
		Y int
		m map[string]*leaf
	}
	fa, fb, fc := Fingerprint(&a{}), Fingerprint(&b{}), Fingerprint(&c{})
	if fa == fb || fa == fc || fb == fc {
		t.Fatalf("fingerprints %x %x %x", fa, fb, fc)
	}
	if Fingerprint(&a{}) != fa {
		t.Fatal("a fingerprint isn't stable")
	}
}

// A slice held in several places (the same array, length and capacity)
// decodes as one again: a cached parse's list shared by many rows stays one.
func TestSharedSlicesStayShared(t *testing.T) {
	list := []string{"a", "b"}
	type two struct {
		a, b []string
		c    []byte
		d    []byte
	}
	bs := []byte("bytes")
	out := roundTrip(t, &two{list, list, bs, bs})
	if &out.a[0] != &out.b[0] || &out.c[0] != &out.d[0] {
		t.Fatal("a shared slice decoded as copies")
	}
	out.a[0] = "x"
	if out.b[0] != "x" {
		t.Fatal("not shared")
	}
}
