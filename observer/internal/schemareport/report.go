package schemareport

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/Blink-Build-Studios/aintercept/observer/internal/events"
)

// sampleRecord is one line from a schema-sample NDJSON object. It is the subset
// of the observer's Event we need for the report — the routing metadata plus the
// structure-only fingerprint. Content-bearing fields are absent from schema
// samples by construction, so there is nothing sensitive to read here.
type sampleRecord struct {
	TimestampNS       int64                     `json:"timestamp_ns"`
	Host              string                    `json:"host"`
	Method            string                    `json:"method"`
	Path              string                    `json:"path"`
	Status            int                       `json:"status"`
	Model             string                    `json:"model"`
	SchemaFingerprint *events.SchemaFingerprint `json:"schema_fingerprint"`
}

// EndpointKey identifies a distinct unrecognized endpoint shape. We group by the
// stable routing signature; path keeps its raw value (it may carry a model or an
// id, which is itself useful signal for recognizing the shape).
type EndpointKey struct {
	Host   string `json:"host"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// EndpointReport aggregates every sample seen for one EndpointKey.
type EndpointReport struct {
	EndpointKey
	Count           int               `json:"count"`
	Statuses        map[int]int       `json:"statuses"`         // status code -> count
	Models          []string          `json:"models,omitempty"` // distinct models seen (when any)
	SafeHeaders     map[string]string `json:"safe_headers,omitempty"`
	ReqHeaderNames  []string          `json:"request_header_names,omitempty"`
	RespHeaderNames []string          `json:"response_header_names,omitempty"`
	// RequestShapes / ResponseShapes are the distinct body skeletons seen for this
	// endpoint (folded — identical skeletons collapse to one with a count).
	RequestShapes  []ShapeVariant `json:"request_shapes,omitempty"`
	ResponseShapes []ShapeVariant `json:"response_shapes,omitempty"`
	FirstSeenNS    int64          `json:"first_seen_ns"`
	LastSeenNS     int64          `json:"last_seen_ns"`
}

// ShapeVariant is one distinct body skeleton and how often it was seen.
type ShapeVariant struct {
	Kind            string          `json:"kind"`
	ContentEncoding string          `json:"content_encoding,omitempty"`
	Count           int             `json:"count"`
	Skeleton        json.RawMessage `json:"skeleton,omitempty"`
	Truncated       bool            `json:"truncated,omitempty"`
}

// Report is the full redacted deliverable: every distinct unrecognized endpoint
// shape, with the watermark the next run should resume from.
type Report struct {
	GeneratedAt    time.Time        `json:"generated_at"`
	SinceNS        int64            `json:"since_ns,omitempty"` // cursor this run started from (0 = all)
	WatermarkNS    int64            `json:"watermark_ns"`       // pass as --since next run
	SamplesScanned int              `json:"samples_scanned"`    // raw sample lines read
	ObjectsScanned int              `json:"objects_scanned"`    // S3 objects read
	KeysRedacted   bool             `json:"keys_redacted"`      // true if skeleton keys were scrubbed
	Endpoints      []EndpointReport `json:"endpoints"`
}

// Aggregator folds sample records into per-endpoint reports. Build one, Add every
// decoded sample line, then call Finish.
type Aggregator struct {
	redactKeys bool
	byEndpoint map[EndpointKey]*endpointAcc
	scanned    int
	watermark  int64
	since      int64
}

type endpointAcc struct {
	rep        EndpointReport
	models     map[string]struct{}
	reqShapes  map[string]*ShapeVariant // keyed by skeleton+kind
	respShapes map[string]*ShapeVariant
}

// NewAggregator returns an Aggregator. redactKeys, when true, scrubs object keys
// in every skeleton to a type bucket as well (belt-and-suspenders beyond the
// value scrubbing the sampler already did) so even a key that an operator put
// sensitive data into does not reach the report.
func NewAggregator(redactKeys bool, since int64) *Aggregator {
	return &Aggregator{
		redactKeys: redactKeys,
		byEndpoint: map[EndpointKey]*endpointAcc{},
		since:      since,
	}
}

// Add folds one raw sample NDJSON line into the aggregate. A line older than the
// since cursor, or without a fingerprint, is skipped. Returns true if the line
// was counted. A malformed line is skipped (returns false) rather than failing
// the whole run.
func (a *Aggregator) Add(line []byte) bool {
	var rec sampleRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return false
	}
	if rec.SchemaFingerprint == nil {
		return false
	}
	if a.since > 0 && rec.TimestampNS <= a.since {
		return false
	}
	if rec.TimestampNS > a.watermark {
		a.watermark = rec.TimestampNS
	}
	a.scanned++

	key := EndpointKey{Host: rec.Host, Method: rec.Method, Path: rec.Path, Reason: rec.SchemaFingerprint.Reason}
	acc := a.byEndpoint[key]
	if acc == nil {
		acc = &endpointAcc{
			rep:        EndpointReport{EndpointKey: key, Statuses: map[int]int{}, FirstSeenNS: rec.TimestampNS},
			models:     map[string]struct{}{},
			reqShapes:  map[string]*ShapeVariant{},
			respShapes: map[string]*ShapeVariant{},
		}
		a.byEndpoint[key] = acc
	}

	acc.rep.Count++
	acc.rep.Statuses[rec.Status]++
	if rec.TimestampNS < acc.rep.FirstSeenNS {
		acc.rep.FirstSeenNS = rec.TimestampNS
	}
	if rec.TimestampNS > acc.rep.LastSeenNS {
		acc.rep.LastSeenNS = rec.TimestampNS
	}
	if rec.Model != "" {
		acc.models[rec.Model] = struct{}{}
	}
	// Headers: first writer wins for the representative sets (they're stable per
	// endpoint); union would be noisier without adding signal.
	fp := rec.SchemaFingerprint
	if acc.rep.SafeHeaders == nil && len(fp.SafeHeaders) > 0 {
		acc.rep.SafeHeaders = fp.SafeHeaders
	}
	if acc.rep.ReqHeaderNames == nil {
		acc.rep.ReqHeaderNames = fp.RequestHeaderNames
	}
	if acc.rep.RespHeaderNames == nil {
		acc.rep.RespHeaderNames = fp.ResponseHeaderNames
	}
	a.foldShape(acc.reqShapes, fp.Request)
	a.foldShape(acc.respShapes, fp.Response)
	return true
}

// foldShape merges one BodyShape into the distinct-shape set, optionally scrubbing
// keys first.
func (a *Aggregator) foldShape(dst map[string]*ShapeVariant, bs *events.BodyShape) {
	if bs == nil {
		return
	}
	skel := bs.Skeleton
	if a.redactKeys && len(skel) > 0 {
		skel = redactSkeletonKeys(skel)
	}
	// Dedup key: kind + encoding + skeleton bytes.
	dk := bs.Kind + "|" + bs.ContentEncoding + "|" + string(skel)
	if v := dst[dk]; v != nil {
		v.Count++
		return
	}
	dst[dk] = &ShapeVariant{
		Kind:            bs.Kind,
		ContentEncoding: bs.ContentEncoding,
		Count:           1,
		Skeleton:        skel,
		Truncated:       bs.Truncated,
	}
}

// Finish produces the sorted Report. Endpoints are ordered by descending count
// (busiest unknown shapes first), then by host/path for stability.
func (a *Aggregator) Finish() Report {
	eps := make([]EndpointReport, 0, len(a.byEndpoint))
	for _, acc := range a.byEndpoint {
		acc.rep.Models = sortedKeys(acc.models)
		acc.rep.RequestShapes = sortedVariants(acc.reqShapes)
		acc.rep.ResponseShapes = sortedVariants(acc.respShapes)
		eps = append(eps, acc.rep)
	}
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Count != eps[j].Count {
			return eps[i].Count > eps[j].Count
		}
		if eps[i].Host != eps[j].Host {
			return eps[i].Host < eps[j].Host
		}
		return eps[i].Path < eps[j].Path
	})
	return Report{
		GeneratedAt:    time.Now().UTC(),
		SinceNS:        a.since,
		WatermarkNS:    a.watermark,
		SamplesScanned: a.scanned,
		KeysRedacted:   a.redactKeys,
		Endpoints:      eps,
	}
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedVariants(m map[string]*ShapeVariant) []ShapeVariant {
	if len(m) == 0 {
		return nil
	}
	out := make([]ShapeVariant, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
