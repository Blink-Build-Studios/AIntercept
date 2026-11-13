package state

import (
	"math/rand"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The harness, exported to the layer tests of the rendered tables
// (rendered_layers_test.go, package state_test): they import internal/render,
// which imports this package, so they can't be in it.

// Stream is the per-key states after applying records one at a time.
type Stream = streamOut

// NewStream applies rs in order.
func NewStream(rs []records.Record) *Stream { return stream(rs) }

// Apply applies one record to its key's state.
func (o *streamOut) Apply(r records.Record) Delta { return o.apply(r) }

// States is each key's state.
func (o *streamOut) States() map[string]*KeyState { return o.states }

// AtomicCase is one record per case (every state.Cases branch), after its setup.
type AtomicCase struct {
	Name   string
	Before func() []records.Record // nil: none
	Rec    func() records.Record
}

// AtomicCases is every atomic case the render's layers run (TestRenderSessionAtomic's).
func AtomicCases() []AtomicCase {
	var out []AtomicCase
	for _, cs := range [][]atomicCase{presenceCases, eventCases, dimCases, promptCases, requestCases, sessionCases, callToolCases, factCases} {
		for _, c := range cs {
			out = append(out, AtomicCase{Name: c.name, Before: c.before, Rec: c.rec})
		}
	}
	return out
}

// ResetBatches restarts the builders' batch numbering (each test's records number their batches from 1).
func ResetBatches() { batchN = map[string]int64{} }

// CheckRendered requires got, each session's tables, to equal the reference's
// (process.DeriveRows) over rs, which o applied.
func CheckRendered(t *testing.T, label string, rs []records.Record, o *Stream, got map[string]*SessionTables) {
	t.Helper()
	checkRendered(t, label, rs, o, got)
}

// ShortSets are the short sequences the render runs in every order.
func ShortSets() map[string]func() []records.Record { return renderShortSets }

// GenerateHours is n sessions' records spread over hours and days, and a Codex thread each.
func GenerateHours(rng *rand.Rand, n, per int) []records.Record { return generateHours(rng, n, per) }

// Shuffled is rs in a random order, some duplicated.
func Shuffled(rng *rand.Rand, rs []records.Record, dupes int) []records.Record {
	return shuffled(rng, rs, dupes)
}

// Permute calls fn with every permutation of 0..n-1.
func Permute(n int, fn func([]int)) { permute(n, fn) }

// Heavy skips a slow single-goroutine test under -race (make check runs it without).
func Heavy(t *testing.T) { t.Helper(); heavy(t) }

// SeedFrac divides the generated tests' seed counts (1 with -tags full).
const SeedFrac = seedFrac

// The record builders (harness_test.go, prompts_test.go, events_test.go).
var (
	At          = at
	CCEvent     = ccEvent
	Pev         = pev
	TypedPrompt = typedPrompt
	Success     = success
	Step        = step
	WithIDs     = withIDs
	WithIP      = withIP
	CallStart   = callStart
	CallEnd     = callEnd
	CodexCall   = codexCall
	Blur        = blur
)

// WithTools gives a call's response tool uses.
func WithTools(c *canon.Request, js string) *canon.Request { return withTools(c, js) }

// HourMS is an hour in ms.
const HourMS = hourMS
