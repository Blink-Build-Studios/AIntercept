package interceptor_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	v1 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	v2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// Identity.account_id (field 4) was removed: both versions reserve its name
// and number, so neither can come back with another meaning.
func TestIdentityAccountIDReserved(t *testing.T) {
	for _, d := range []protoreflect.MessageDescriptor{
		(&v1.Identity{}).ProtoReflect().Descriptor(),
		(&v2.Identity{}).ProtoReflect().Descriptor(),
	} {
		if f := d.Fields().ByName("account_id"); f != nil {
			t.Errorf("%s still has field account_id (%d)", d.FullName(), f.Number())
		}
		if !d.ReservedNames().Has("account_id") {
			t.Errorf("%s doesn't reserve the name account_id", d.FullName())
		}
		if !d.ReservedRanges().Has(4) {
			t.Errorf("%s doesn't reserve field number 4", d.FullName())
		}
	}
}
