package stream_test

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// fill sets every field of v (a struct, addressable) to a non-zero value, and
// every pointer to a pointer to the zero value: the case parquet-go's optional
// pointers inside embedded structs used to write as null.
func fill(v reflect.Value, zeroPtr bool, rng *rand.Rand) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), zeroPtr, rng)
			}
		}
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		if !zeroPtr {
			fill(p.Elem(), zeroPtr, rng)
		}
		v.Set(p)
	case reflect.String:
		v.SetString("s")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(rng.Int63n(1 << 40))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(rng.Int63n(1 << 30)))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), zeroPtr, rng)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k, zeroPtr, rng)
		fill(e, zeroPtr, rng)
		m.SetMapIndex(k, e)
		v.Set(m)
	}
}

// Every stored row type survives the table file format exactly: the zero row,
// a full row, and a row whose pointers point at zero values.
func TestEveryRowTypeRoundTripsThroughItsTableFile(t *testing.T) {
	stream.RegisterStateTables()
	if len(state.TableTypes) == 0 {
		t.Fatal("no state tables")
	}
	rng := rand.New(rand.NewSource(1))
	for name, zero := range state.TableTypes {
		tbl, err := stream.TableFor(name)
		if err != nil {
			t.Fatal(err)
		}
		typ := reflect.TypeOf(zero)
		full, zeroPtrs := reflect.New(typ).Elem(), reflect.New(typ).Elem()
		fill(full, false, rng)
		fill(zeroPtrs, true, rng)
		in := []stream.Row{
			{Key: "s:a", ID: "1", Value: reflect.Zero(typ).Interface()},
			{Key: "s:a", ID: "2", Value: full.Interface()},
			{Key: "s:a", ID: "3", Value: zeroPtrs.Interface()},
			{Key: "s:a", ID: "4", Tomb: true},
		}
		b, err := tbl.Encode(in)
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		out, err := tbl.Decode(b)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if !reflect.DeepEqual(out, in) {
			for i := range in {
				if i < len(out) && !reflect.DeepEqual(out[i], in[i]) {
					t.Errorf("%s row %d: got %+v\nwant %+v", name, i, out[i].Value, in[i].Value)
				}
			}
			if len(out) != len(in) {
				t.Errorf("%s: %d rows back, want %d", name, len(out), len(in))
			}
		}
	}
}
