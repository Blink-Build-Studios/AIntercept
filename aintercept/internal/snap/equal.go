package snap

import (
	"fmt"
	"reflect"
	"sort"
	"unsafe"
)

// Equal is nil when a and b hold equal graphs: every field equal, exported or
// not, and the same sharing (where a reaches one object twice, b reaches one
// object twice). Funcs are equal when they're the same registered func. Else
// it's the first difference found, with its path.
func Equal(a, b any) error {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() {
		return fmt.Errorf("types %v and %v", va.Type(), vb.Type())
	}
	q := &eq{seen: map[ptrKey]unsafe.Pointer{}, back: map[ptrKey]unsafe.Pointer{}}
	return q.equal("", va, vb)
}

type eq struct {
	seen map[ptrKey]unsafe.Pointer // a's objects → b's
	back map[ptrKey]unsafe.Pointer // b's → a's
}

// same records that a's object pa is b's pb; it reports whether they were
// already paired (so they're compared already) and errors when either was
// paired with another.
func (q *eq) same(path string, t reflect.Type, pa, pb unsafe.Pointer) (bool, error) {
	ka, kb := ptrKey{pa, t}, ptrKey{pb, t}
	x, okA := q.seen[ka]
	y, okB := q.back[kb]
	switch {
	case okA && okB && x == pb && y == pa:
		return true, nil
	case okA || okB:
		return false, fmt.Errorf("%s: shared differently", path)
	}
	q.seen[ka], q.back[kb] = pb, pa
	return false, nil
}

func (q *eq) equal(path string, a, b reflect.Value) error {
	if !a.IsValid() || !b.IsValid() {
		if a.IsValid() != b.IsValid() {
			return fmt.Errorf("%s: one is invalid", path)
		}
		return nil
	}
	t := a.Type()
	if t != b.Type() {
		return fmt.Errorf("%s: types %v and %v", path, t, b.Type())
	}
	switch t.Kind() {
	case reflect.Bool:
		if a.Bool() != b.Bool() {
			return fmt.Errorf("%s: %v ≠ %v", path, a.Bool(), b.Bool())
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if a.Int() != b.Int() {
			return fmt.Errorf("%s: %d ≠ %d", path, a.Int(), b.Int())
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if a.Uint() != b.Uint() {
			return fmt.Errorf("%s: %d ≠ %d", path, a.Uint(), b.Uint())
		}
	case reflect.Float32, reflect.Float64:
		x, y := a.Float(), b.Float()
		if x != y && !(x != x && y != y) {
			return fmt.Errorf("%s: %v ≠ %v", path, x, y)
		}
	case reflect.String:
		if a.String() != b.String() {
			return fmt.Errorf("%s: %q ≠ %q", path, short(a.String()), short(b.String()))
		}
	case reflect.Array:
		for i := 0; i < a.Len(); i++ {
			if err := q.equal(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return fmt.Errorf("%s: slices of %d (nil %v) and %d (nil %v)", path, a.Len(), a.IsNil(), b.Len(), b.IsNil())
		}
		for i := 0; i < a.Len(); i++ {
			if err := q.equal(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return fmt.Errorf("%s: one map is nil", path)
			}
			return nil
		}
		done, err := q.same(path, t, a.UnsafePointer(), b.UnsafePointer())
		if done || err != nil {
			return err
		}
		if a.Len() != b.Len() {
			return fmt.Errorf("%s: maps of %d and %d", path, a.Len(), b.Len())
		}
		keys := a.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			vb := b.MapIndex(k)
			if !vb.IsValid() {
				return fmt.Errorf("%s: key %v only in one", path, k)
			}
			if err := q.equal(fmt.Sprintf("%s[%v]", path, k), a.MapIndex(k), vb); err != nil {
				return err
			}
		}
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return fmt.Errorf("%s: one pointer is nil", path)
			}
			return nil
		}
		done, err := q.same(path, t, a.UnsafePointer(), b.UnsafePointer())
		if done || err != nil {
			return err
		}
		return q.equal(path, a.Elem(), b.Elem())
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return fmt.Errorf("%s: one interface is nil", path)
			}
			return nil
		}
		return q.equal(path, a.Elem(), b.Elem())
	case reflect.Struct:
		// Unexported fields read through an addressable copy.
		ca, cb := addressable(a), addressable(b)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fa := reflect.NewAt(f.Type, unsafe.Add(ca.Addr().UnsafePointer(), f.Offset)).Elem()
			fb := reflect.NewAt(f.Type, unsafe.Add(cb.Addr().UnsafePointer(), f.Offset)).Elem()
			if err := q.equal(path+"."+f.Name, fa, fb); err != nil {
				return err
			}
		}
	case reflect.Func:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return fmt.Errorf("%s: one func is nil", path)
			}
			return nil
		}
		na, okA := reg.funcName(a.Pointer())
		nb, okB := reg.funcName(b.Pointer())
		if !okA || !okB || na != nb {
			return fmt.Errorf("%s: funcs %q and %q", path, na, nb)
		}
	default:
		return fmt.Errorf("%s: can't compare a %v", path, t)
	}
	return nil
}

// addressable is v, or an addressable copy of it. Every value the walk
// reaches came through NewAt, so none is read-only and Set copies it.
func addressable(v reflect.Value) reflect.Value {
	if v.CanAddr() {
		return v
	}
	c := reflect.New(v.Type()).Elem()
	c.Set(v)
	return c
}

func short(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
