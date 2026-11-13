package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// mixed is n records of one session touching every table: typed prompts, steps
// and their reports and tools, hidden calls, compaction and query events, turn
// ends, blur and sleep windows. tag keeps ids apart between calls.
func mixed(sess, tag string, n int, base int64) []records.Record {
	out := make([]records.Record, 0, n)
	for i := 0; i < n; i++ {
		pid := fmt.Sprintf("%sp%d", tag, i/10)
		at := base + int64(i)*1000
		rid := fmt.Sprintf("%sr%d", tag, i)
		switch i % 10 {
		case 0:
			out = append(out, typedPrompt(sess, pid, "1", at))
		case 1, 2:
			out = append(out, callEnd(withIDs(step(fmt.Sprintf("%sc%d", tag, i), sess, pid, at), rid, "")))
		case 3:
			out = append(out, success(sess, at, fmt.Sprintf("%sr%d", tag, i-2)))
		case 4:
			out = append(out, pev(sess, pid, "tengu_tool_use_success", at, fmt.Sprintf(`{"requestId":"%sr%d","toolName":"Read"}`, tag, i-3)))
		case 5:
			out = append(out, callEnd(hidden(step(fmt.Sprintf("%sh%d", tag, i), sess, "", at))))
		case 6:
			out = append(out, ccEvent(sess, "tengu_api_query", at, `{"permissionMode":"plan"}`))
		case 7:
			out = append(out, blur(sess, at, 300))
		case 8:
			out = append(out, sleep(sess, at, 200))
		default:
			out = append(out, pev(sess, pid, "tengu_turn_end", at, `{}`))
		}
	}
	return out
}

// A checkpoint is what changed since the last one, never the key's history: the
// next 200 records' checkpoint is the same size, and takes the same time to
// write, whether the key already holds 10, 1,000 or 100,000 records.
func TestCheckpointCostIsFlat(t *testing.T) {
	heavy(t)
	const next = 200
	measure := func(held int) (int, time.Duration) {
		s := New("s:big")
		for _, r := range mixed("big", "h", held, 0) {
			s.Apply(r)
		}
		if _, err := Encode(s); err != nil { // the checkpoint the history ends with
			t.Fatal(err)
		}
		for _, r := range mixed("big", "n", next, int64(held+1)*1000) {
			s.Apply(r)
		}
		start := time.Now()
		b, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		return len(b), time.Since(start)
	}
	b10, t10 := measure(10)
	b1k, t1k := measure(1_000)
	bBig, tBig := measure(flatBig)
	t.Logf("checkpoint of the next %d records: holding 10: %d bytes %v; 1,000: %d bytes %v; %d: %d bytes %v",
		next, b10, t10, b1k, t1k, flatBig, bBig, tBig)
	if float64(bBig) > float64(b10)*1.2+512 {
		t.Errorf("checkpoint size grows with the key's history: %d bytes holding 10, %d holding %d", b10, bBig, flatBig)
	}
	if tBig > 8*t10+5*time.Millisecond {
		t.Errorf("checkpoint time grows with the key's history: %v holding 10, %v holding %d", t10, tBig, flatBig)
	}
}

// Loading a key back reads every checkpoint it wrote, and costs the same per
// record held whether it holds 1,000 or 100,000.
func TestLoadCostPerRecordIsFlat(t *testing.T) {
	heavy(t)
	measure := func(held int) time.Duration {
		s := New("s:big")
		var chunks [][]byte
		for i, r := range mixed("big", "h", held, 0) {
			s.Apply(r)
			if i%500 == 499 {
				b, err := Encode(s)
				if err != nil {
					t.Fatal(err)
				}
				chunks = append(chunks, b)
			}
		}
		b, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, b)
		start := time.Now()
		s2, err := Decode(chunks...)
		if err != nil {
			t.Fatal(err)
		}
		el := time.Since(start)
		if !reflect.DeepEqual(s2.Rows(), s.Rows()) {
			t.Fatalf("holding %d: rows differ after loading", held)
		}
		return el / time.Duration(held)
	}
	small, big := measure(1_000), measure(flatBig)
	t.Logf("load per record held: 1,000 %v; %d %v", small, flatBig, big)
	if big > 3*small+2*time.Microsecond {
		t.Errorf("loading costs more per record as the key grows: %v at 1,000, %v at %d", small, big, flatBig)
	}
}

// A key checkpointed at any points and loaded back from its checkpoints (as
// written, or merged in any grouping) carries on exactly as the key that was
// never checkpointed: the same rows, and the same changes for every record after.
func TestCheckpointChainsResume(t *testing.T) {
	heavy(t)
	for seed := int64(1); seed <= 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		rs := mixed("s1", fmt.Sprintf("x%d", seed), 300, 0)
		rs = append(rs, shuffled(rng, rs[:120], 10)[:20]...) // late copies: no-ops
		rng.Shuffle(len(rs), func(i, j int) { rs[i], rs[j] = rs[j], rs[i] })
		cut := 100 + rng.Intn(150)

		plain, chained := New("s:s1"), New("s:s1")
		var chunks [][]byte
		for i, r := range rs[:cut] {
			plain.Apply(r)
			chained.Apply(r)
			if rng.Intn(40) == 0 || i == cut-1 {
				b, err := Encode(chained)
				if err != nil {
					t.Fatal(err)
				}
				chunks = append(chunks, b)
			}
		}
		// Merge some neighbours, as compaction does.
		for len(chunks) > 1 && rng.Intn(3) > 0 {
			i := rng.Intn(len(chunks) - 1)
			m, err := Merge(chunks[i], chunks[i+1])
			if err != nil {
				t.Fatal(err)
			}
			chunks = append(append(chunks[:i:i], m), chunks[i+2:]...)
		}
		back, err := Decode(chunks...)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back.Rows(), plain.Rows()) {
			t.Fatalf("seed %d: rows differ after loading %d checkpoints", seed, len(chunks))
		}
		for _, r := range rs[cut:] {
			a, b := plain.Apply(r), back.Apply(r)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("seed %d: record %s changes differ after loading", seed, r.RecordID)
			}
		}
		if !reflect.DeepEqual(back.Rows(), plain.Rows()) {
			t.Fatalf("seed %d: rows differ at the end", seed)
		}
	}
}

// Keys applied at the same time on different goroutines (the processor's
// buckets) still parse each record's metadata once.
func TestMetadataParsedOnceUnderConcurrency(t *testing.T) {
	const keys, per = 8, 400
	sets := make([][]records.Record, keys)
	n := 0
	for k := range sets {
		sess := fmt.Sprintf("c%d", k)
		for i := 0; i < per; i++ {
			md := fmt.Sprintf(`{"requestId":"r%d-%d","clientRequestId":"q%d","cc_prompt_id":"p%d","prompt_index":%d,"toolName":"Read","permissionMode":"plan","n":%d}`, k, i, i, i/5, i/5+1, i)
			name := []string{"tengu_api_success", "tengu_tool_use_success", "tengu_api_query", "tengu_input_prompt"}[i%4]
			sets[k] = append(sets[k], ccEvent(sess, name, int64(i)*1000, md))
			n++
		}
	}
	before := process.FieldParses()
	var wg sync.WaitGroup
	for k := range sets {
		wg.Add(1)
		go func(rs []records.Record) {
			defer wg.Done()
			s := New(rs[0].RoutingKey)
			for _, r := range rs {
				s.Apply(r)
			}
		}(sets[k])
	}
	wg.Wait()
	if parsed := process.FieldParses() - before; parsed > int64(n) {
		t.Errorf("%d records' metadata parsed %d times on %d goroutines; want at most once each", n, parsed, keys)
	}
}

// One more tool or progress event on a request id costs the same whether 1, 100
// or 2,000 steps carry that id: it changes the id's tools, not every step.
func TestSharedRequestIDToolsFlatCost(t *testing.T) {
	heavy(t)
	measure := func(shared int) (float64, time.Duration) {
		s := New("s:rid")
		for i := 0; i < shared; i++ {
			s.Apply(callEnd(withReq(ccCall(fmt.Sprintf("c%d", i), "rid", int64(i)*1000), "rx")))
		}
		base := int64(shared) * 1000
		pre := make([]records.Record, 600)
		for k := range pre {
			at := base + int64(k)*10
			name := "tengu_tool_use_success"
			if k%2 == 1 {
				name = "tengu_tool_use_progress"
			}
			pre[k] = ccEvent("rid", name, at, fmt.Sprintf(`{"requestId":"rx","toolUseID":"tu%d","toolName":"Read"}`, k))
		}
		k := 0
		allocs := testing.AllocsPerRun(200, func() { s.Apply(pre[k]); k++ })
		start := time.Now()
		for ; k < len(pre); k++ {
			s.Apply(pre[k])
		}
		return allocs, time.Since(start) / time.Duration(len(pre)-200)
	}
	a1, t1 := measure(1)
	a100, t100 := measure(100)
	a2k, t2k := measure(2000)
	t.Logf("per tool event, steps sharing the id: 1 %.1f allocs %v; 100 %.1f allocs %v; 2,000 %.1f allocs %v", a1, t1, a100, t100, a2k, t2k)
	if a2k > a1*1.5+4 {
		t.Errorf("allocations grow with the steps sharing a request id: %.1f at 1, %.1f at 2,000", a1, a2k)
	}
	if t2k > 4*t1+20*time.Microsecond {
		t.Errorf("time grows with the steps sharing a request id: %v at 1, %v at 2,000", t1, t2k)
	}
}
