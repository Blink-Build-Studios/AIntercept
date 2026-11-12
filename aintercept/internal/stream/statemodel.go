package stream

import "github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"

// StateModel is the derived tables (internal/stream/state) as the processor's
// model.
type StateModel struct{}

func (StateModel) Tables() []string                         { return nil }
func (StateModel) New(key string) any                       { return nil }
func (StateModel) Apply(st any, r *records.Record) Delta    { return nil }
func (StateModel) Encode(st any) ([]byte, error)            { return nil, nil }
func (StateModel) Decode(key string, b []byte) (any, error) { return nil, nil }
func (StateModel) Size(st any) int                          { return 0 }
