package schemareport

import (
	"encoding/json"
	"fmt"
	"sort"
)

// redactSkeletonKeys returns the skeleton with every OBJECT KEY replaced by a
// coarse length bucket (the same bucketing the sampler uses for string values),
// so that even if an operator placed sensitive data in key position it does not
// reach the report. Values in a skeleton are already type strings (the sampler
// scrubbed them), so only keys need touching.
//
// This is an OPT-IN belt-and-suspenders pass: the sampler's contract is that keys
// are kept, but when a report is handed to someone who must be sure NOTHING
// identifying leaves the environment, this strips keys too. The structure (object
// vs array vs scalar, nesting, value types) is fully preserved — just the key
// names become buckets like "key(<=8)".
//
// It is fail-safe: if the skeleton doesn't parse, it returns a conservative
// placeholder rather than the raw bytes, so a malformed skeleton can never pass
// unredacted keys through.
func redactSkeletonKeys(skeleton json.RawMessage) json.RawMessage {
	var v interface{}
	if err := json.Unmarshal(skeleton, &v); err != nil {
		return json.RawMessage(`"<unparseable-skeleton>"`)
	}
	red := redactValueKeys(v)
	out, err := json.Marshal(red)
	if err != nil {
		return json.RawMessage(`"<unredactable-skeleton>"`)
	}
	return out
}

// redactValueKeys walks a decoded skeleton and buckets object keys. Values are
// left as-is (they are already type strings). Arrays recurse into elements.
func redactValueKeys(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		// Preserve how many keys existed at each size by bucketing, and keep the
		// output deterministic. Collisions (two keys bucketing the same) are
		// disambiguated with an index suffix so no value is silently dropped.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		used := map[string]int{}
		for _, k := range keys {
			bucket := "key(" + keyLenBucket(len(k)) + ")"
			name := bucket
			if n := used[bucket]; n > 0 {
				name = fmt.Sprintf("%s#%d", bucket, n)
			}
			used[bucket]++
			out[name] = redactValueKeys(t[k])
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, el := range t {
			out[i] = redactValueKeys(el)
		}
		return out
	default:
		return v
	}
}

// keyLenBucket mirrors the sampler's value length buckets so redacted keys read
// consistently with scrubbed values.
func keyLenBucket(n int) string {
	switch {
	case n == 0:
		return "0"
	case n <= 8:
		return "<=8"
	case n <= 64:
		return "<=64"
	case n <= 512:
		return "<=512"
	default:
		return ">512"
	}
}
