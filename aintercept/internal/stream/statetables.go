package stream

import (
	"reflect"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// RegisterStateTables registers every table the stream state writes, with its
// row type (state.TableTypes).
func RegisterStateTables() {
	for name, v := range state.TableTypes {
		RegisterTableType(name, reflect.TypeOf(v))
	}
}
