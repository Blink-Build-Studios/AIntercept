// Package snap encodes a warm key's object graph, for its snapshot checkpoint
// (docs/PIPELINE.md §2, v8: a cold load decodes the warm state and never
// replays or renders it).
//
// It encodes every field, exported or not, so the state's indexes need no
// encoding of their own, and it keeps what the graph shares:
//   - a pointer or a map reached twice is one object again after a decode;
//   - every string is stored once by content, so a decoded state shares what
//     the original held in several copies (an event's metadata in its fact
//     row and its event row).
//
// What it can't encode it refuses: channels, unsafe pointers, complex
// numbers, a func that isn't registered (RegisterFunc), and an interface
// holding a type that isn't (Register). A pointer into the middle of
// something else (a slice's element, a struct's field) decodes as an object of
// its own: the warm state holds none.
package snap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"sync"
	"unsafe"
)

// Register makes the concrete types of vs encodable where an interface holds
// them.
func Register(vs ...any) {
	reg.Lock()
	defer reg.Unlock()
	for _, v := range vs {
		t := reflect.TypeOf(v)
		name := typeName(t)
		if old, ok := reg.byName[name]; ok && old != t {
			panic("snap: two types named " + name)
		}
		reg.byName[name], reg.names[t] = t, name
	}
}

// RegisterFunc names a top-level func a field may hold (an ordered map's
// order, say), so a snapshot can hold it by name.
func RegisterFunc(name string, fn any) {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.IsNil() {
		panic("snap: RegisterFunc of a non-func")
	}
	reg.Lock()
	defer reg.Unlock()
	reg.funcs[name] = v
	reg.funcNames[v.Pointer()] = name
}

type registry struct {
	sync.Mutex
	byName    map[string]reflect.Type
	names     map[reflect.Type]string
	funcs     map[string]reflect.Value
	funcNames map[uintptr]string
	codecs    map[reflect.Type]*codec
}

var reg = &registry{byName: map[string]reflect.Type{}, names: map[reflect.Type]string{}, funcs: map[string]reflect.Value{},
	funcNames: map[uintptr]string{}, codecs: map[reflect.Type]*codec{}}

func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		return "*" + typeName(t.Elem())
	}
	if t.Name() != "" && t.PkgPath() != "" {
		return t.PkgPath() + "." + t.Name()
	}
	return t.String()
}

// Encode is v's graph: v is a non-nil pointer.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := EncodeTo(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// EncodeTo writes v's graph to w (v a non-nil pointer), a buffer at a time,
// so a large graph never sits in memory whole as bytes.
func EncodeTo(w io.Writer, v any) (err error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("snap: Encode needs a non-nil pointer")
	}
	c := codecOf(rv.Type().Elem())
	e := &encoder{w: w, b: make([]byte, 0, flushAt+4096), strs: map[string]uint32{}, long: map[strKey]uint32{},
		slices: map[sliceKey]uint32{}, ptrs: map[ptrKey]uint32{}, types: map[reflect.Type]uint32{}}
	defer catch(&err)
	// The root is object 0: a pointer back to it decodes as the root.
	e.ptrs[ptrKey{rv.UnsafePointer(), rv.Type()}], e.nptr = 0, 1
	c.enc(e, rv.UnsafePointer())
	e.flush()
	return nil
}

// flushAt is how much an encoder buffers before writing.
const flushAt = 1 << 16

// Decode fills v, a non-nil pointer to the type Encode was given, from b.
func Decode(b []byte, v any) (err error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("snap: Decode needs a non-nil pointer")
	}
	c := codecOf(rv.Type().Elem())
	d := &decoder{b: b, ptrs: []any{rv.UnsafePointer()}}
	defer catch(&err)
	c.dec(d, rv.UnsafePointer())
	if d.off != len(d.b) {
		return fmt.Errorf("snap: %d bytes after the value", len(d.b)-d.off)
	}
	return nil
}

// failure is a panic Encode and Decode turn into their error.
type failure struct{ err error }

func fail(format string, args ...any) { panic(failure{fmt.Errorf("snap: "+format, args...)}) }

func catch(err *error) {
	if r := recover(); r != nil {
		f, ok := r.(failure)
		if !ok {
			panic(r)
		}
		*err = f.err
	}
}

type ptrKey struct {
	p unsafe.Pointer
	t reflect.Type
}

type encoder struct {
	w      io.Writer
	b      []byte
	strs   map[string]uint32 // short strings, by content
	long   map[strKey]uint32 // long ones, by identity: hashing every event's JSON costs more than it saves
	slices map[sliceKey]uint32
	nstr   uint32
	ptrs   map[ptrKey]uint32 // pointers and maps written, by identity
	types  map[reflect.Type]uint32
	nptr   uint32
}

type decoder struct {
	b      []byte
	off    int
	strs   []string
	slices []reflect.Value
	ptrs   []any // unsafe.Pointer for a pointer, reflect.Value for a map
	types  []reflect.Type
}

// flush writes the buffered bytes.
func (e *encoder) flush() {
	if len(e.b) == 0 {
		return
	}
	if _, err := e.w.Write(e.b); err != nil {
		panic(failure{err})
	}
	e.b = e.b[:0]
}

func (e *encoder) uvarint(x uint64) {
	if len(e.b) >= flushAt {
		e.flush()
	}
	e.b = binary.AppendUvarint(e.b, x)
}
func (e *encoder) varint(x int64) { e.b = binary.AppendVarint(e.b, x) }

func (d *decoder) uvarint() uint64 {
	x, n := binary.Uvarint(d.b[d.off:])
	if n <= 0 {
		fail("truncated")
	}
	d.off += n
	return x
}

func (d *decoder) varint() int64 {
	x, n := binary.Varint(d.b[d.off:])
	if n <= 0 {
		fail("truncated")
	}
	d.off += n
	return x
}

func (d *decoder) bytes(n int) []byte {
	if n < 0 || d.off+n > len(d.b) {
		fail("truncated")
	}
	out := d.b[d.off : d.off+n]
	d.off += n
	return out
}

type strKey struct {
	p *byte
	n int
}

// longString is the length from which a string is shared by identity, not
// content.
const longString = 64

// A string: 0 is "", 2k+1 the k-th string written, 2n a new one of n bytes.
func (e *encoder) str(s string) {
	if s == "" {
		e.uvarint(0)
		return
	}
	if len(s) < longString {
		if i, ok := e.strs[s]; ok {
			e.uvarint(uint64(i)*2 + 1)
			return
		}
		e.strs[s] = e.nstr
	} else {
		k := strKey{unsafe.StringData(s), len(s)}
		if i, ok := e.long[k]; ok {
			e.uvarint(uint64(i)*2 + 1)
			return
		}
		e.long[k] = e.nstr
	}
	e.nstr++
	e.uvarint(uint64(len(s)) * 2)
	if len(s) >= flushAt {
		e.flush()
		if _, err := io.WriteString(e.w, s); err != nil {
			panic(failure{err})
		}
		return
	}
	e.b = append(e.b, s...)
}

func (d *decoder) str() string {
	x := d.uvarint()
	switch {
	case x == 0:
		return ""
	case x%2 == 1:
		if x/2 >= uint64(len(d.strs)) {
			fail("a string reference out of range")
		}
		return d.strs[x/2]
	}
	s := string(d.bytes(int(x / 2)))
	d.strs = append(d.strs, s)
	return s
}

// ref writes an identity: 0 nil, 2k+1 the k-th object written, 2 a new one
// (written next). It reports whether the object follows.
func (e *encoder) ref(p unsafe.Pointer, t reflect.Type) bool {
	if p == nil {
		e.uvarint(0)
		return false
	}
	k := ptrKey{p, t}
	if i, ok := e.ptrs[k]; ok {
		e.uvarint(uint64(i)*2 + 1)
		return false
	}
	e.ptrs[k] = e.nptr
	e.nptr++
	e.uvarint(2)
	return true
}

// A type, where an interface holds one: 0 is nil, 2k+1 the k-th written, 2
// a new one (its name follows).
func (e *encoder) typ(t reflect.Type) {
	if i, ok := e.types[t]; ok {
		e.uvarint(uint64(i)*2 + 1)
		return
	}
	reg.Lock()
	name, ok := reg.names[t]
	reg.Unlock()
	if !ok {
		fail("an interface holds %v, which isn't registered", t)
	}
	e.types[t] = uint32(len(e.types))
	e.uvarint(2)
	e.str(name)
}

func (d *decoder) typ() reflect.Type {
	x := d.uvarint()
	if x%2 == 1 {
		if x/2 >= uint64(len(d.types)) {
			fail("a type reference out of range")
		}
		return d.types[x/2]
	}
	name := d.str()
	reg.Lock()
	t, ok := reg.byName[name]
	reg.Unlock()
	if !ok {
		fail("type %s isn't registered", name)
	}
	d.types = append(d.types, t)
	return t
}

// codec is a type's encoder and decoder, over a pointer to a value of it.
type codec struct {
	enc func(e *encoder, p unsafe.Pointer)
	dec func(d *decoder, p unsafe.Pointer)
}

func codecOf(t reflect.Type) *codec {
	reg.Lock()
	defer reg.Unlock()
	return build(t)
}

// build is t's codec (reg held). A recursive type's codec is in the cache
// before its parts are built, so they call it through the pointer.
func build(t reflect.Type) *codec {
	if c, ok := reg.codecs[t]; ok {
		return c
	}
	c := &codec{}
	reg.codecs[t] = c
	switch t.Kind() {
	case reflect.Bool:
		c.enc = func(e *encoder, p unsafe.Pointer) {
			if *(*bool)(p) {
				e.b = append(e.b, 1)
			} else {
				e.b = append(e.b, 0)
			}
		}
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*bool)(p) = d.bytes(1)[0] != 0 }
	case reflect.Int:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.varint(int64(*(*int)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*int)(p) = int(d.varint()) }
	case reflect.Int8:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.varint(int64(*(*int8)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*int8)(p) = int8(d.varint()) }
	case reflect.Int16:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.varint(int64(*(*int16)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*int16)(p) = int16(d.varint()) }
	case reflect.Int32:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.varint(int64(*(*int32)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*int32)(p) = int32(d.varint()) }
	case reflect.Int64:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.varint(*(*int64)(p)) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*int64)(p) = d.varint() }
	case reflect.Uint:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.uvarint(uint64(*(*uint)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*uint)(p) = uint(d.uvarint()) }
	case reflect.Uint8:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.b = append(e.b, *(*uint8)(p)) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*uint8)(p) = d.bytes(1)[0] }
	case reflect.Uint16:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.uvarint(uint64(*(*uint16)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*uint16)(p) = uint16(d.uvarint()) }
	case reflect.Uint32:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.uvarint(uint64(*(*uint32)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*uint32)(p) = uint32(d.uvarint()) }
	case reflect.Uint64:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.uvarint(*(*uint64)(p)) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*uint64)(p) = d.uvarint() }
	case reflect.Uintptr:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.uvarint(uint64(*(*uintptr)(p))) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*uintptr)(p) = uintptr(d.uvarint()) }
	case reflect.Float32:
		c.enc = func(e *encoder, p unsafe.Pointer) {
			e.b = binary.LittleEndian.AppendUint32(e.b, math.Float32bits(*(*float32)(p)))
		}
		c.dec = func(d *decoder, p unsafe.Pointer) {
			*(*float32)(p) = math.Float32frombits(binary.LittleEndian.Uint32(d.bytes(4)))
		}
	case reflect.Float64:
		c.enc = func(e *encoder, p unsafe.Pointer) {
			e.b = binary.LittleEndian.AppendUint64(e.b, math.Float64bits(*(*float64)(p)))
		}
		c.dec = func(d *decoder, p unsafe.Pointer) {
			*(*float64)(p) = math.Float64frombits(binary.LittleEndian.Uint64(d.bytes(8)))
		}
	case reflect.String:
		c.enc = func(e *encoder, p unsafe.Pointer) { e.str(*(*string)(p)) }
		c.dec = func(d *decoder, p unsafe.Pointer) { *(*string)(p) = d.str() }
	case reflect.Array:
		el, n, size := build(t.Elem()), t.Len(), t.Elem().Size()
		c.enc = func(e *encoder, p unsafe.Pointer) {
			for i := 0; i < n; i++ {
				el.enc(e, unsafe.Add(p, uintptr(i)*size))
			}
		}
		c.dec = func(d *decoder, p unsafe.Pointer) {
			for i := 0; i < n; i++ {
				el.dec(d, unsafe.Add(p, uintptr(i)*size))
			}
		}
	case reflect.Slice:
		buildSlice(c, t)
	case reflect.Map:
		buildMap(c, t)
	case reflect.Pointer:
		buildPointer(c, t)
	case reflect.Interface:
		buildInterface(c, t)
	case reflect.Struct:
		buildStruct(c, t)
	case reflect.Func:
		c.enc = func(e *encoder, p unsafe.Pointer) {
			v := reflect.NewAt(t, p).Elem()
			if v.IsNil() {
				e.str("")
				return
			}
			name, ok := reg.funcName(v.Pointer())
			if !ok {
				fail("a func of type %v that isn't registered", t)
			}
			e.str(name)
		}
		c.dec = func(d *decoder, p unsafe.Pointer) {
			name := d.str()
			v := reflect.NewAt(t, p).Elem()
			if name == "" {
				v.SetZero()
				return
			}
			fn, ok := reg.fn(name)
			if !ok || fn.Type() != t {
				fail("func %s isn't registered as a %v", name, t)
			}
			v.Set(fn)
		}
	default:
		c.enc = func(*encoder, unsafe.Pointer) { fail("can't encode a %v", t) }
		c.dec = func(*decoder, unsafe.Pointer) { fail("can't decode a %v", t) }
	}
	return c
}

func (r *registry) funcName(pc uintptr) (string, bool) {
	r.Lock()
	defer r.Unlock()
	n, ok := r.funcNames[pc]
	return n, ok
}

func (r *registry) fn(name string) (reflect.Value, bool) {
	r.Lock()
	defer r.Unlock()
	v, ok := r.funcs[name]
	return v, ok
}

type sliceHeader struct {
	data     unsafe.Pointer
	len, cap int
}

// sliceKey is a slice's identity: one held in several places (a cached
// parse's list) is written once and decodes as one again.
type sliceKey struct {
	data     unsafe.Pointer
	len, cap int
	t        reflect.Type
}

// A slice: 0 is nil, 2k+1 the k-th slice written, 2(n+1) a new one of n
// elements (an empty slice isn't nil).
func buildSlice(c *codec, t reflect.Type) {
	el, size := build(t.Elem()), t.Elem().Size()
	bytes := t.Elem().Kind() == reflect.Uint8
	c.enc = func(e *encoder, p unsafe.Pointer) {
		h := (*sliceHeader)(p)
		if h.data == nil {
			e.uvarint(0)
			return
		}
		if h.len > 0 {
			k := sliceKey{h.data, h.len, h.cap, t}
			if i, ok := e.slices[k]; ok {
				e.uvarint(uint64(i)*2 + 1)
				return
			}
			e.slices[k] = uint32(len(e.slices))
		}
		e.uvarint(uint64(h.len+1) * 2)
		if bytes {
			e.flush()
			if _, err := e.w.Write(unsafe.Slice((*byte)(h.data), h.len)); err != nil {
				panic(failure{err})
			}
			return
		}
		for i := 0; i < h.len; i++ {
			el.enc(e, unsafe.Add(h.data, uintptr(i)*size))
		}
	}
	c.dec = func(d *decoder, p unsafe.Pointer) {
		x := d.uvarint()
		v := reflect.NewAt(t, p).Elem()
		switch {
		case x == 0:
			v.SetZero()
			return
		case x%2 == 1:
			if x/2 >= uint64(len(d.slices)) {
				fail("a slice reference out of range")
			}
			s := d.slices[x/2]
			if s.Type() != t {
				fail("a slice reference to another type")
			}
			v.Set(s)
			return
		}
		n := x/2 - 1
		if n > uint64(len(d.b)-d.off) && size > 0 {
			fail("a slice longer than the snapshot")
		}
		s := reflect.MakeSlice(t, int(n), int(n))
		v.Set(s)
		if n > 0 {
			d.slices = append(d.slices, s)
		}
		data := s.UnsafePointer()
		if bytes {
			copy(unsafe.Slice((*byte)(data), int(n)), d.bytes(int(n)))
			return
		}
		for i := 0; i < int(n); i++ {
			el.dec(d, unsafe.Add(data, uintptr(i)*size))
		}
	}
}

// A map: its identity (ref), then its length and its entries.
func buildMap(c *codec, t reflect.Type) {
	kc, vc := build(t.Key()), build(t.Elem())
	c.enc = func(e *encoder, p unsafe.Pointer) {
		m := reflect.NewAt(t, p).Elem()
		if !e.ref(m.UnsafePointer(), t) {
			return
		}
		e.uvarint(uint64(m.Len()))
		k, v := reflect.New(t.Key()).Elem(), reflect.New(t.Elem()).Elem()
		kp, vp := k.Addr().UnsafePointer(), v.Addr().UnsafePointer()
		it := m.MapRange()
		for it.Next() {
			k.SetIterKey(it)
			v.SetIterValue(it)
			kc.enc(e, kp)
			vc.enc(e, vp)
		}
	}
	c.dec = func(d *decoder, p unsafe.Pointer) {
		x := d.uvarint()
		dst := reflect.NewAt(t, p).Elem()
		switch {
		case x == 0:
			dst.SetZero()
			return
		case x%2 == 1:
			if x/2 >= uint64(len(d.ptrs)) {
				fail("a map reference out of range")
			}
			m, ok := d.ptrs[x/2].(reflect.Value)
			if !ok || m.Type() != t {
				fail("a map reference to another type")
			}
			dst.Set(m)
			return
		}
		n := d.uvarint()
		if n > uint64(len(d.b)-d.off) {
			fail("a map longer than the snapshot")
		}
		m := reflect.MakeMapWithSize(t, int(n))
		d.ptrs = append(d.ptrs, m)
		dst.Set(m)
		k, v := reflect.New(t.Key()).Elem(), reflect.New(t.Elem()).Elem()
		kp, vp := k.Addr().UnsafePointer(), v.Addr().UnsafePointer()
		for i := uint64(0); i < n; i++ {
			kc.dec(d, kp)
			vc.dec(d, vp)
			m.SetMapIndex(k, v)
		}
	}
}

func buildPointer(c *codec, t reflect.Type) {
	el := build(t.Elem())
	c.enc = func(e *encoder, p unsafe.Pointer) {
		q := *(*unsafe.Pointer)(p)
		if e.ref(q, t) {
			el.enc(e, q)
		}
	}
	c.dec = func(d *decoder, p unsafe.Pointer) {
		x := d.uvarint()
		switch {
		case x == 0:
			*(*unsafe.Pointer)(p) = nil
			return
		case x%2 == 1:
			if x/2 >= uint64(len(d.ptrs)) {
				fail("a pointer reference out of range")
			}
			q, ok := d.ptrs[x/2].(unsafe.Pointer)
			if !ok {
				fail("a pointer reference to a map")
			}
			*(*unsafe.Pointer)(p) = q
			return
		}
		q := reflect.New(t.Elem()).UnsafePointer()
		d.ptrs = append(d.ptrs, q)
		*(*unsafe.Pointer)(p) = q
		el.dec(d, q)
	}
}

// eface is an empty interface's words: its dynamic type, and its value (a
// pointer-shaped value itself, else a pointer to it).
type eface struct {
	typ, data unsafe.Pointer
}

// direct is whether a type's values are stored in an interface's data word
// itself (Go's pointer-shaped types), not pointed to by it.
func direct(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return true
	case reflect.Struct:
		return t.NumField() == 1 && direct(t.Field(0).Type)
	case reflect.Array:
		return t.Len() == 1 && direct(t.Elem())
	}
	return false
}

// typeWord is a type's word in an empty interface.
var typeWords sync.Map // reflect.Type → unsafe.Pointer

func typeWord(t reflect.Type) unsafe.Pointer {
	if w, ok := typeWords.Load(t); ok {
		return w.(unsafe.Pointer)
	}
	v := reflect.New(t).Elem().Interface()
	w := (*eface)(unsafe.Pointer(&v)).typ
	typeWords.Store(t, w)
	return w
}

// An interface: 0 is nil, else its dynamic type (typ) and its value. An
// empty interface's value is read and written in place (no copy), any other
// interface's through reflect.
func buildInterface(c *codec, t reflect.Type) {
	empty := t.NumMethod() == 0
	codecFor := func(ct reflect.Type) *codec {
		reg.Lock()
		defer reg.Unlock()
		return build(ct)
	}
	c.enc = func(e *encoder, p unsafe.Pointer) {
		if empty {
			ef := (*eface)(p)
			if ef.typ == nil {
				e.uvarint(0)
				return
			}
			ct := reflect.ValueOf(*(*any)(p)).Type()
			e.typ(ct)
			if direct(ct) {
				codecFor(ct).enc(e, unsafe.Pointer(&ef.data))
			} else {
				codecFor(ct).enc(e, ef.data)
			}
			return
		}
		v := reflect.NewAt(t, p).Elem()
		if v.IsNil() {
			e.uvarint(0)
			return
		}
		x := v.Elem()
		ct := x.Type()
		e.typ(ct)
		tmp := reflect.New(ct).Elem()
		tmp.Set(x)
		codecFor(ct).enc(e, tmp.Addr().UnsafePointer())
	}
	c.dec = func(d *decoder, p unsafe.Pointer) {
		if d.bytes(1)[0] == 0 {
			if empty {
				*(*eface)(p) = eface{}
			} else {
				reflect.NewAt(t, p).Elem().SetZero()
			}
			return
		}
		d.off--
		ct := d.typ()
		if !ct.Implements(t) {
			fail("%v doesn't implement %v", ct, t)
		}
		tmp := reflect.New(ct)
		codecFor(ct).dec(d, tmp.UnsafePointer())
		if !empty {
			reflect.NewAt(t, p).Elem().Set(tmp.Elem())
			return
		}
		if direct(ct) {
			*(*eface)(p) = eface{typeWord(ct), *(*unsafe.Pointer)(tmp.UnsafePointer())}
		} else {
			*(*eface)(p) = eface{typeWord(ct), tmp.UnsafePointer()} // the new value is the interface's box
		}
	}
}

func buildStruct(c *codec, t reflect.Type) {
	type field struct {
		off uintptr
		c   *codec
	}
	fields := make([]field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Size() == 0 {
			continue
		}
		fields = append(fields, field{f.Offset, build(f.Type)})
	}
	c.enc = func(e *encoder, p unsafe.Pointer) {
		for _, f := range fields {
			f.c.enc(e, unsafe.Add(p, f.off))
		}
	}
	c.dec = func(d *decoder, p unsafe.Pointer) {
		for _, f := range fields {
			f.c.dec(d, unsafe.Add(p, f.off))
		}
	}
}

// Fingerprint is a hash of v's type graph: every type it can reach, with its
// fields' names and types, and every registered type and func. A snapshot
// written by a build whose types differ has another fingerprint, so a reader
// refuses it rather than misread it.
func Fingerprint(v any) uint64 {
	reg.Lock()
	defer reg.Unlock()
	h := fnv64{h: 14695981039346656037}
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		h.str(typeName(t))
		h.str(t.Kind().String())
		if seen[t] {
			return
		}
		seen[t] = true
		switch t.Kind() {
		case reflect.Struct:
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				h.str(f.Name)
				walk(f.Type)
			}
		case reflect.Map:
			walk(t.Key())
			walk(t.Elem())
		case reflect.Slice, reflect.Pointer:
			walk(t.Elem())
		case reflect.Array:
			h.str(fmt.Sprint(t.Len()))
			walk(t.Elem())
		}
	}
	walk(reflect.TypeOf(v))
	names := make([]string, 0, len(reg.byName)+len(reg.funcs))
	for n := range reg.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		walk(reg.byName[n])
	}
	names = names[:0]
	for n := range reg.funcs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h.str("func " + n)
	}
	return h.h
}

type fnv64 struct{ h uint64 }

func (f *fnv64) str(s string) {
	for i := 0; i < len(s); i++ {
		f.h ^= uint64(s[i])
		f.h *= 1099511628211
	}
	f.h ^= 0xff
	f.h *= 1099511628211
}
