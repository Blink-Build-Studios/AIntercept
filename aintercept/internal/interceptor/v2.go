package interceptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v1"
	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Contract v2 (interceptor/v2, docs/INTERCEPTOR_V2.md): OnRequest carries a
// call's new messages, parsed by convo, and its answers come back per
// message; OnToolCall each tool call in a reply. Call, CallStart and
// CallRecord are v1's messages field for field, so they're built as v1 and
// converted on the wire format.
//
// A Respond at call start or OnRequest is delivered by the observer
// (synthesized, or the request rewritten), which then records it with
// Responded; one at a tool call is always delivered in the stream.

const (
	// fetchMax bounds a fetched part's content; a larger one stays a reference.
	fetchMax = 32 << 20
	// messageMax keeps OnRequest under gRPC's default 4 MiB receive limit:
	// past it, the largest inline parts go by reference.
	messageMax   = 3 << 20
	previewBytes = 1 << 10
)

// errInvalid marks an answer that can't be applied (as opposed to a failed
// call): recorded as an error in both modes.
var errInvalid = errors.New("invalid answer")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errInvalid, fmt.Sprintf(format, a...))
}

// convert copies a v1 message into its v2 twin through the wire format.
func convert[T proto.Message](from proto.Message, to T) T {
	if b, err := proto.Marshal(from); err == nil {
		_ = proto.Unmarshal(b, to)
	}
	return to
}

// StartV2 is OnCallStart's message in contract v2.
func StartV2(s *pb.CallStart) *pb2.CallStart { return convert(s, &pb2.CallStart{}) }

func recordedV2(vs []events.Verdict) []*pb2.RecordedVerdict {
	var out []*pb2.RecordedVerdict
	for _, v := range vs {
		rv := &pb2.RecordedVerdict{Callback: v.Callback, Action: v.Action, Applied: v.Applied, Reason: v.Reason, MessageId: v.MessageID, RespondMode: v.RespondMode}
		for k, val := range v.Annotations {
			rv.Annotations = append(rv.Annotations, &pb2.Annotation{Key: k, Value: val})
		}
		out = append(out, rv)
	}
	return out
}

// respondMode is how the call's latest applied Respond was delivered.
func respondMode(vs []events.Verdict) string {
	for i := len(vs) - 1; i >= 0; i-- {
		if vs[i].Action == "respond" && vs[i].Applied {
			return vs[i].RespondMode
		}
	}
	return ""
}

// Responded records a Respond decision's row: delivered as mode
// ("synthesized" or "rewritten"), or, with mode "", not delivered (recorded,
// not applied).
func (c *Client) Responded(callID string, d Decision, mode string) {
	if d.pending == nil {
		return
	}
	v := *d.pending
	v.Applied, v.RespondMode = mode != "", mode
	c.store(callID, v)
}

// checkRespond checks a Respond's text.
func checkRespond(r *pb2.Respond) error {
	if r.GetText() == "" {
		return invalid("a Respond with no text")
	}
	return nil
}

func annotations(as []*pb2.Annotation) map[string]string {
	if len(as) == 0 {
		return nil
	}
	m := make(map[string]string, len(as))
	for _, a := range as {
		m[a.GetKey()] = a.GetValue()
	}
	return m
}

// ── OnCallStart ──────────────────────────────────────────────────────────────

func (c *Client) decideStart2(ctx context.Context, s *pb.CallStart) Decision {
	callID := s.GetCall().GetCallId()
	ctx, cancel := c.ctx(ctx, c.cfg.Deadline)
	defer cancel()
	t := time.Now()
	v, err := c.api2.OnCallStart(ctx, StartV2(s))
	took := time.Since(t)
	if err == nil && v.GetRespond() != nil {
		err = checkRespond(v.GetRespond())
	}
	if err != nil {
		return c.fail(callID, "call_start", "", err, took)
	}
	c.decided.Add(1)
	if r := v.GetRespond(); r != nil {
		return Decision{Action: Respond, Text: r.GetText(), pending: &events.Verdict{Callback: "call_start", Action: "respond",
			Mode: "enforce", LatencyMS: took.Milliseconds(), Annotations: annotations(v.GetAnnotations())}}
	}
	c.noteStart2(callID, "enforce", v, true, took)
	return Decision{Action: Allow}
}

// noteStart2 records OnCallStart's answer: an annotated Accept, or (observe)
// a Respond, recorded and not applied.
func (c *Client) noteStart2(callID, mode string, v *pb2.CallVerdict, enforce bool, took time.Duration) {
	ev := events.Verdict{Callback: "call_start", Action: "accept", Applied: enforce, Mode: mode,
		LatencyMS: took.Milliseconds(), Annotations: annotations(v.GetAnnotations())}
	if v.GetRespond() != nil {
		ev.Action, ev.Applied = "respond", false
	}
	if ev.Action == "accept" && len(ev.Annotations) == 0 {
		return
	}
	c.store(callID, ev)
}

// ── OnRequest ────────────────────────────────────────────────────────────────

// Request is one call's request as OnRequest sees it: its new messages, with
// their ids. It keeps the body and those messages (not the history's
// content), to apply a Replace.
type Request struct {
	call   *pb2.Call
	parsed convo.Request // Messages: only the new ones are set
	msgs   []convo.Message
	ids    []string
	at     map[string]int    // message id → position in msgs
	tools  map[string]string // tool call id → its tool, from the whole request
}

// NewRequest prepares a parsed request for OnRequest. Ids chain from the
// call's session (convo.Request.IDs).
func NewRequest(call *pb.Call, r convo.Request) *Request {
	ids := r.IDs(call.GetIdentity().GetSessionId())
	q := &Request{call: convert(call, &pb2.Call{}), parsed: r, at: map[string]int{}, tools: map[string]string{}}
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == convo.KindToolCall {
				q.tools[p.CallID] = p.Name
			}
		}
	}
	q.parsed.Messages = make([]convo.Message, len(r.Messages))
	for _, i := range r.New {
		q.parsed.Messages[i] = r.Messages[i]
		q.at[ids[i]] = len(q.msgs)
		q.msgs = append(q.msgs, r.Messages[i])
		q.ids = append(q.ids, ids[i])
	}
	return q
}

func partID(messageID string, i int) string { return messageID + "/" + strconv.Itoa(i) }

// message builds OnRequest's message; fetched names the parts to send with
// their content (the second round).
func (q *Request) message(fetched map[string]bool) *pb2.RequestMessages {
	out := &pb2.RequestMessages{
		Call: q.call, Format: q.parsed.Format, Chained: q.parsed.Chained,
		TotalMessages: int32(len(q.parsed.Messages)), Fetched: fetched != nil,
	}
	type inline struct {
		p   *pb2.Part
		src convo.Part
	}
	var inlined []inline
	for i, m := range q.msgs {
		cm := &pb2.ConvMessage{Id: q.ids[i], Role: role(m.Role), Index: int32(m.Index)}
		for j, p := range m.Parts {
			id := partID(q.ids[i], j)
			pp := part(p, id, fetched[id])
			if r := pp.GetToolResult(); r != nil {
				r.Name = q.tools[p.CallID]
			}
			if !pp.GetRef() && !fetched[id] && p.Size > 0 {
				inlined = append(inlined, inline{pp, p})
			}
			cm.Parts = append(cm.Parts, pp)
		}
		out.Messages = append(out.Messages, cm)
	}
	// Many large parts at once: the largest go by reference until it fits.
	if n := proto.Size(out); n > messageMax {
		slices.SortFunc(inlined, func(a, b inline) int { return b.src.Size - a.src.Size })
		for _, in := range inlined {
			if n <= messageMax {
				break
			}
			before := proto.Size(in.p)
			byReference(in.p, in.src)
			n -= before - proto.Size(in.p)
		}
	}
	return out
}

func role(r convo.Role) pb2.Role {
	switch r {
	case convo.RoleUser:
		return pb2.Role_ROLE_USER
	case convo.RoleAssistant:
		return pb2.Role_ROLE_ASSISTANT
	case convo.RoleTool:
		return pb2.Role_ROLE_TOOL
	}
	return pb2.Role_ROLE_UNSPECIFIED
}

// part maps one part; fetch fills a by-reference part's content.
func part(p convo.Part, id string, fetch bool) *pb2.Part {
	pp := &pb2.Part{Id: id, Size: int64(p.Size), Ref: p.Ref, Preview: p.Preview}
	text, args := p.Text, p.Arguments
	var data []byte
	switch {
	case p.Ref && fetch && p.Size <= fetchMax:
		pp.Ref, pp.Preview = false, ""
		data = p.Content()
		text, args = string(data), string(data)
	case !p.Ref && p.Kind == convo.KindMedia:
		data = p.Content()
	}
	switch p.Kind {
	case convo.KindText:
		pp.Kind = &pb2.Part_Text{Text: &pb2.Text{Text: text}}
	case convo.KindMedia:
		pp.Kind = &pb2.Part_Media{Media: &pb2.Media{MediaType: p.MediaType, Url: p.URL, CallId: p.CallID, Data: data}}
	case convo.KindToolCall:
		pp.Kind = &pb2.Part_ToolCall{ToolCall: &pb2.ToolCall{CallId: p.CallID, Name: p.Name, Arguments: args}}
	case convo.KindToolResult:
		pp.Kind = &pb2.Part_ToolResult{ToolResult: &pb2.ToolResult{CallId: p.CallID, Text: text, IsError: p.IsError}}
	default:
		pp.Kind = &pb2.Part_Opaque{Opaque: &pb2.Opaque{}}
	}
	return pp
}

// byReference drops an inline part's content, leaving its preview.
func byReference(pp *pb2.Part, src convo.Part) {
	pp.Ref = true
	switch k := pp.GetKind().(type) {
	case *pb2.Part_Text:
		k.Text.Text = ""
	case *pb2.Part_ToolCall:
		k.ToolCall.Arguments = ""
	case *pb2.Part_ToolResult:
		k.ToolResult.Text = ""
	case *pb2.Part_Media:
		k.Media.Data = nil
		if !strings.HasPrefix(src.MediaType, "text/") {
			return
		}
	}
	pp.Preview = preview(string(src.Content()))
}

// preview is the first 1 KiB of s, cut on a rune.
func preview(s string) string {
	if len(s) <= previewBytes {
		return s
	}
	i := previewBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// fetch checks a Fetch's part ids.
func (q *Request) fetch(ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		msg, n, ok := strings.Cut(id, "/")
		i, known := q.at[msg]
		j, err := strconv.Atoi(n)
		if !ok || !known || err != nil || j < 0 || j >= len(q.msgs[i].Parts) {
			return nil, invalid("Fetch of an unknown part %q", id)
		}
		out[id] = true
	}
	return out, nil
}

// answer turns OnRequest's answer into edits and the verdict rows to record
// (callback, action, message id, annotations). A Respond is the call's
// answer: its row is the only one, and the per-message answers are ignored.
func (q *Request) answer(v *pb2.RequestVerdict) ([]convo.Edit, []events.Verdict, error) {
	var edits []convo.Edit
	var rows []events.Verdict
	row := func(action, messageID string, as []*pb2.Annotation) {
		rows = append(rows, events.Verdict{Callback: "request", Action: action, MessageID: messageID, Annotations: annotations(as)})
	}
	if r := respondOf(v); r != nil {
		if err := checkRespond(r); err != nil {
			return nil, nil, err
		}
		row("respond", "", v.GetAnnotations())
		return nil, rows, nil
	}
	if len(v.GetAnnotations()) > 0 {
		row("accept", "", v.GetAnnotations())
	}
	seen := map[string]bool{}
	for _, mv := range v.GetMessages() {
		id := mv.GetMessageId()
		i, ok := q.at[id]
		if !ok {
			return nil, nil, invalid("an answer for an unknown message %q", id)
		}
		if seen[id] {
			return nil, nil, invalid("two answers for message %q", id)
		}
		seen[id] = true
		switch a := mv.GetAction().(type) {
		case *pb2.MessageVerdict_Replace:
			e, err := q.replace(i, a.Replace.GetMessage())
			if err != nil {
				return nil, nil, fmt.Errorf("message %q: %w", id, err)
			}
			edits = append(edits, e...)
			row("replace", id, mv.GetAnnotations())
		default:
			if len(mv.GetAnnotations()) > 0 {
				row("accept", id, mv.GetAnnotations())
			}
		}
	}
	return edits, rows, nil
}

// replace checks a Replace against the message it replaces and returns the
// edits for the parts it changes. Every part comes back, by id, in order, of
// the same kind; only text, tool results' text and tool calls' arguments
// change; media and opaque parts are as they were.
func (q *Request) replace(i int, rm *pb2.ConvMessage) ([]convo.Edit, error) {
	m := q.msgs[i]
	if len(rm.GetParts()) != len(m.Parts) {
		return nil, invalid("Replace has %d parts, the message %d: every part comes back, by id", len(rm.GetParts()), len(m.Parts))
	}
	var edits []convo.Edit
	for j, rp := range rm.GetParts() {
		p := m.Parts[j]
		if want := partID(q.ids[i], j); rp.GetId() != want {
			return nil, invalid("Replace part %d is %q, want %q: parts come back by id, in order", j, rp.GetId(), want)
		}
		var now string
		switch p.Kind {
		case convo.KindText:
			now = rp.GetText().GetText()
			if rp.GetText() == nil {
				return nil, invalid("part %q isn't text any more", rp.GetId())
			}
		case convo.KindToolResult:
			now = rp.GetToolResult().GetText()
			if r := rp.GetToolResult(); r == nil || r.GetCallId() != p.CallID || r.GetIsError() != p.IsError {
				return nil, invalid("part %q: only a tool result's text can change", rp.GetId())
			}
		case convo.KindToolCall:
			now = rp.GetToolCall().GetArguments()
			if tc := rp.GetToolCall(); tc == nil || tc.GetCallId() != p.CallID || tc.GetName() != p.Name {
				return nil, invalid("part %q: only a tool call's arguments can change", rp.GetId())
			}
		case convo.KindMedia:
			md := rp.GetMedia()
			if md == nil || md.GetMediaType() != p.MediaType || md.GetUrl() != p.URL || len(md.GetData()) > 0 && string(md.GetData()) != string(p.Content()) {
				return nil, invalid("part %q: media comes back unchanged", rp.GetId())
			}
			continue
		default:
			if rp.GetOpaque() == nil {
				return nil, invalid("part %q: opaque parts come back unchanged", rp.GetId())
			}
			continue
		}
		if rp.GetRef() {
			continue // still a reference: unchanged
		}
		if now != string(p.Content()) {
			edits = append(edits, convo.Edit{Message: m.Index, Part: j, Content: now})
		}
	}
	return edits, nil
}

// ask runs OnRequest, with its one Fetch round, and reads the answer: the
// edits, the rows, and a Respond's text.
func (c *Client) ask(ctx context.Context, q *Request) ([]convo.Edit, []events.Verdict, string, error) {
	v, err := c.api2.OnRequest(ctx, q.message(nil))
	if err != nil {
		return nil, nil, "", err
	}
	if ids := v.GetFetch().GetPartIds(); len(ids) > 0 {
		fetched, err := q.fetch(ids)
		if err != nil {
			return nil, nil, "", err
		}
		if v, err = c.api2.OnRequest(ctx, q.message(fetched)); err != nil {
			return nil, nil, "", err
		}
		if len(v.GetFetch().GetPartIds()) > 0 {
			return nil, nil, "", invalid("a second Fetch: there is one per call")
		}
	}
	edits, rows, err := q.answer(v)
	if err != nil {
		return nil, nil, "", err
	}
	return edits, rows, respondOf(v).GetText(), nil
}

// respondOf is OnRequest's Respond: the call's, or else the first message's
// (a message's Respond is the call's).
func respondOf(v *pb2.RequestVerdict) *pb2.Respond {
	if r := v.GetRespond(); r != nil {
		return r
	}
	for _, mv := range v.GetMessages() {
		if r := mv.GetRespond(); r != nil {
			return r
		}
	}
	return nil
}

// apply puts the edits on the wire: the body to forward.
func (q *Request) apply(edits []convo.Edit) ([]byte, error) {
	b, err := q.parsed.Apply(edits)
	if err != nil {
		return nil, invalid("Replace can't be applied: %v", err)
	}
	return b, nil
}

// ObserveRequest queues OnRequest (observe mode). No-op without content.
func (c *Client) ObserveRequest(call *pb.Call, r convo.Request) {
	if c.Content() && c.V2() {
		c.enqueue(job{callID: call.GetCallId(), req: NewRequest(call, r)})
	}
}

// onRequestObserved delivers a queued OnRequest: its answers are recorded,
// never applied. An answer that couldn't be applied is recorded as an error.
func (c *Client) onRequestObserved(ctx context.Context, q *Request) error {
	t := time.Now()
	edits, rows, _, err := c.ask(ctx, q)
	if err == nil && len(edits) > 0 {
		_, err = q.apply(edits)
	}
	took := time.Since(t)
	callID := q.call.GetCallId()
	if err != nil {
		if errors.Is(err, errInvalid) {
			c.store(callID, events.Verdict{Callback: "request", Action: "error", Mode: "observe", Reason: err.Error(), LatencyMS: took.Milliseconds()})
		}
		return err
	}
	for _, r := range rows {
		r.Mode, r.LatencyMS = "observe", took.Milliseconds()
		c.store(callID, r)
	}
	return nil
}

// DecideRequest calls OnRequest and waits for the answer (enforce mode),
// within the deadline, Fetch included. Replace: Payload is the body to
// forward. Respond: the caller delivers it and records it with Responded. A
// failure, a missed deadline or an answer that can't be applied is onError.
func (c *Client) DecideRequest(ctx context.Context, call *pb.Call, r convo.Request) Decision {
	if !c.Content() || !c.V2() {
		return Decision{Action: Allow}
	}
	q := NewRequest(call, r)
	ctx, cancel := c.ctx(ctx, c.cfg.Deadline)
	defer cancel()
	t := time.Now()
	edits, rows, text, err := c.ask(ctx, q)
	var body []byte
	if err == nil && len(edits) > 0 {
		body, err = q.apply(edits)
	}
	took := time.Since(t)
	if err != nil {
		return c.fail(call.GetCallId(), "request", "", err, took)
	}
	c.decided.Add(1)
	if text != "" {
		r := rows[0]
		r.Mode, r.LatencyMS = "enforce", took.Milliseconds()
		return Decision{Action: Respond, Text: text, pending: &r}
	}
	for _, r := range rows {
		r.Mode, r.Applied, r.LatencyMS = "enforce", true, took.Milliseconds()
		c.store(call.GetCallId(), r)
	}
	if body != nil {
		return Decision{Action: Replace, Payload: body}
	}
	return Decision{Action: Allow}
}

// ── OnToolCall ───────────────────────────────────────────────────────────────

// ToolCalls reports whether tool calls in replies go to OnToolCall (v2 with
// content, until the Interceptor turns out not to implement it).
func (c *Client) ToolCalls() bool { return c.Content() && c.V2() && !c.noToolCall.Load() }

// toolMessage is OnToolCall's message for a tool call.
func toolMessage(call *pb.Call, tc convo.ToolCall) *pb2.ResponseMessage {
	part := &pb2.Part{Id: partID(tc.ID, 0), Size: int64(len(tc.Arguments)),
		Kind: &pb2.Part_ToolCall{ToolCall: &pb2.ToolCall{CallId: tc.ID, Name: tc.Name, Arguments: tc.Arguments}}}
	if len(tc.Arguments) > messageMax {
		byReference(part, convo.Part{Kind: convo.KindToolCall})
		part.Preview = preview(tc.Arguments)
	}
	return &pb2.ResponseMessage{Call: convert(call, &pb2.Call{}),
		Message: &pb2.ConvMessage{Id: tc.ID, Role: pb2.Role_ROLE_ASSISTANT, Parts: []*pb2.Part{part}}}
}

// DecideToolCall calls OnToolCall for one complete tool call in a reply and
// waits for the answer (enforce mode), within the deadline. Replace: Payload
// is the new arguments. Respond: Text ends the reply (recorded here, as
// synthesized). A failure, a missed deadline or an answer that can't be
// applied is onError; an Interceptor without OnToolCall accepts.
func (c *Client) DecideToolCall(ctx context.Context, call *pb.Call, tc convo.ToolCall) Decision {
	if !c.ToolCalls() {
		return Decision{Action: Allow}
	}
	callID := call.GetCallId()
	msg := toolMessage(call, tc)
	ctx, cancel := c.ctx(ctx, c.cfg.Deadline)
	defer cancel()
	t := time.Now()
	v, err := c.api2.OnToolCall(ctx, msg)
	took := time.Since(t)
	if status.Code(err) == codes.Unimplemented {
		c.noToolCall.Store(true)
		return Decision{Action: Allow}
	}
	var d Decision
	if err == nil {
		d, err = toolAnswer(tc, v)
	}
	if err != nil {
		return c.fail(callID, "tool_call", tc.ID, err, took)
	}
	c.decided.Add(1)
	row := events.Verdict{Callback: "tool_call", Action: "accept", Applied: true, Mode: "enforce", LatencyMS: took.Milliseconds(),
		MessageID: tc.ID, Annotations: annotations(v.GetAnnotations())}
	switch d.Action {
	case Replace:
		row.Action = "replace"
	case Respond:
		row.Action, row.RespondMode = "respond", "synthesized"
	}
	if row.Action != "accept" || len(row.Annotations) > 0 {
		c.store(callID, row)
	}
	return d
}

// toolAnswer checks OnToolCall's answer against the call.
func toolAnswer(tc convo.ToolCall, v *pb2.MessageVerdict) (Decision, error) {
	switch a := v.GetAction().(type) {
	case *pb2.MessageVerdict_Respond:
		if err := checkRespond(a.Respond); err != nil {
			return Decision{}, err
		}
		return Decision{Action: Respond, Text: a.Respond.GetText()}, nil
	case *pb2.MessageVerdict_Replace:
		ps := a.Replace.GetMessage().GetParts()
		if len(ps) != 1 || ps[0].GetToolCall() == nil {
			return Decision{}, invalid("Replace of a tool call is one tool call part")
		}
		if ps[0].GetRef() {
			return Decision{Action: Allow}, nil // still a reference: unchanged
		}
		r := ps[0].GetToolCall()
		if r.GetCallId() != tc.ID || r.GetName() != tc.Name {
			return Decision{}, invalid("only a tool call's arguments can change")
		}
		args := r.GetArguments()
		var str string
		switch {
		case !json.Valid([]byte(args)):
			return Decision{}, invalid("tool call arguments must be JSON")
		case tc.Custom && json.Unmarshal([]byte(args), &str) != nil:
			return Decision{}, invalid("a custom tool's arguments are a JSON string")
		case args == tc.Arguments:
			return Decision{Action: Allow}, nil
		}
		return Decision{Action: Replace, Payload: []byte(args)}, nil
	}
	return Decision{Action: Allow}, nil
}

// ── OnReply ──────────────────────────────────────────────────────────────────

// Replies reports whether reply text is held for OnReply (v2 with content,
// until the Interceptor turns out not to implement it).
func (c *Client) Replies() bool { return c.Content() && c.V2() && !c.noReply.Load() }

// replyMessage is OnReply's message for a text block's pending text.
func replyMessage(call *pb.Call, p convo.TextPiece) *pb2.ResponseMessage {
	part := &pb2.Part{Id: partID(p.ID, 0), Size: int64(len(p.Pending)), Kind: &pb2.Part_Text{Text: &pb2.Text{Text: p.Pending}}}
	return &pb2.ResponseMessage{Call: convert(call, &pb2.Call{}), Released: p.Released, Done: p.Done,
		Message: &pb2.ConvMessage{Id: p.ID, Role: pb2.Role_ROLE_ASSISTANT, Parts: []*pb2.Part{part}}}
}

// opaqueMessage is OnReply's notice of an opaque part.
func opaqueMessage(call *pb.Call, id string) *pb2.ResponseMessage {
	return &pb2.ResponseMessage{Call: convert(call, &pb2.Call{}), Done: true,
		Message: &pb2.ConvMessage{Id: id, Role: pb2.Role_ROLE_ASSISTANT,
			Parts: []*pb2.Part{{Id: partID(id, 0), Kind: &pb2.Part_Opaque{Opaque: &pb2.Opaque{}}}}}}
}

// replyAnswer checks OnReply's answer against the pending text.
func replyAnswer(p convo.TextPiece, v *pb2.MessageVerdict) (Decision, error) {
	d := Decision{Action: Allow}
	switch a := v.GetAction().(type) {
	case *pb2.MessageVerdict_Respond:
		if err := checkRespond(a.Respond); err != nil {
			return Decision{}, err
		}
		return Decision{Action: Respond, Text: a.Respond.GetText()}, nil
	case *pb2.MessageVerdict_Hold:
		return Decision{Action: Hold}, nil
	case *pb2.MessageVerdict_Replace:
		ps := a.Replace.GetMessage().GetParts()
		if len(ps) != 1 || ps[0].GetText() == nil {
			return Decision{}, invalid("Replace of reply text is one text part")
		}
		d = Decision{Action: Replace, Text: ps[0].GetText().GetText()}
	}
	if k := int(v.GetKeep()); k != 0 && !p.Done {
		if k < 0 || k > len(p.Pending) || k < len(p.Pending) && !utf8.RuneStart(p.Pending[len(p.Pending)-k]) {
			return Decision{}, invalid("keep %d of %d bytes isn't the pending text's tail, cut on a character", k, len(p.Pending))
		}
		d.Keep = k
	}
	if d.Action == Replace && d.Keep == 0 && d.Text == p.Pending {
		d = Decision{Action: Allow}
	}
	return d, nil
}

// replyRow is the verdict row for an answer on reply text: every answer but
// a plain Accept.
func replyRow(id string, d Decision, v *pb2.MessageVerdict) (events.Verdict, bool) {
	row := events.Verdict{Callback: "reply", Action: "accept", MessageID: id, Annotations: annotations(v.GetAnnotations())}
	switch d.Action {
	case Replace, Hold:
		row.Action = string(d.Action)
	case Respond:
		row.Action, row.RespondMode = "respond", "synthesized"
	}
	return row, row.Action != "accept" || len(row.Annotations) > 0
}

// DecideReply calls OnReply for a text block's pending text and waits for the
// answer (enforce mode), within the deadline: Allow (with Keep), Replace
// (Text, with Keep), Hold or Respond. A failure, a missed deadline or an
// answer that can't be applied is onError; an Interceptor without OnReply
// accepts. A block over the message limit goes on undecided.
func (c *Client) DecideReply(ctx context.Context, call *pb.Call, p convo.TextPiece) Decision {
	if !c.Replies() || len(p.Released)+len(p.Pending) > messageMax {
		return Decision{Action: Allow}
	}
	callID := call.GetCallId()
	ctx, cancel := c.ctx(ctx, c.cfg.Deadline)
	defer cancel()
	t := time.Now()
	v, err := c.api2.OnReply(ctx, replyMessage(call, p))
	took := time.Since(t)
	if status.Code(err) == codes.Unimplemented {
		c.noReply.Store(true)
		return Decision{Action: Allow}
	}
	var d Decision
	if err == nil {
		d, err = replyAnswer(p, v)
	}
	if err != nil {
		return c.fail(callID, "reply", p.ID, err, took)
	}
	c.decided.Add(1)
	if row, ok := replyRow(p.ID, d, v); ok {
		row.Applied, row.Mode, row.LatencyMS = true, "enforce", took.Milliseconds()
		c.store(callID, row)
	}
	return d
}

// replyJob is an OnReply or OnToolCall delivered off the stream: observe
// mode, or an opaque part's notice.
type replyJob struct {
	msg    *pb2.ResponseMessage
	text   *convo.TextPiece // OnReply on text
	tool   *convo.ToolCall  // OnToolCall
	notice bool             // OnReply on an opaque part: nothing to record
}

// ObserveReply queues OnReply for reply text (observe mode).
func (c *Client) ObserveReply(call *pb.Call, p convo.TextPiece) {
	if c.Replies() && len(p.Released)+len(p.Pending) <= messageMax {
		c.enqueue(job{callID: call.GetCallId(), reply: &replyJob{msg: replyMessage(call, p), text: &p}})
	}
}

// ObserveToolCall queues OnToolCall (observe mode).
func (c *Client) ObserveToolCall(call *pb.Call, tc convo.ToolCall) {
	if c.ToolCalls() {
		c.enqueue(job{callID: call.GetCallId(), reply: &replyJob{msg: toolMessage(call, tc), tool: &tc}})
	}
}

// ObserveOpaque queues OnReply's notice of an opaque part of a reply
// (reasoning, encrypted content; observe mode).
func (c *Client) ObserveOpaque(call *pb.Call, id string) {
	if c.Replies() {
		c.enqueue(job{callID: call.GetCallId(), reply: &replyJob{msg: opaqueMessage(call, id), notice: true}})
	}
}

// Opaque tells OnReply of an opaque part of a reply (enforce mode), in the
// reply's path so the call's messages reach the Interceptor in order. It
// can't be changed: the answer, and any failure, are ignored.
func (c *Client) Opaque(ctx context.Context, call *pb.Call, id string) {
	if !c.Replies() {
		return
	}
	ctx, cancel := c.ctx(ctx, c.cfg.Deadline)
	defer cancel()
	if _, err := c.api2.OnReply(ctx, opaqueMessage(call, id)); status.Code(err) == codes.Unimplemented {
		c.noReply.Store(true)
	}
}

// onReplyObserved delivers a queued OnReply or OnToolCall: its answer is
// recorded, never applied.
func (c *Client) onReplyObserved(ctx context.Context, callID string, j *replyJob) error {
	t := time.Now()
	var v *pb2.MessageVerdict
	var err error
	if j.tool != nil {
		v, err = c.api2.OnToolCall(ctx, j.msg)
	} else {
		v, err = c.api2.OnReply(ctx, j.msg)
	}
	took := time.Since(t)
	if status.Code(err) == codes.Unimplemented {
		if j.tool != nil {
			c.noToolCall.Store(true)
		} else {
			c.noReply.Store(true)
		}
		return nil
	}
	if err != nil || j.notice {
		return err
	}
	callback, id := "reply", j.msg.GetMessage().GetId()
	var d Decision
	if j.tool != nil {
		callback = "tool_call"
		d, err = toolAnswer(*j.tool, v)
	} else {
		d, err = replyAnswer(*j.text, v)
	}
	if err != nil {
		c.store(callID, events.Verdict{Callback: callback, Action: "error", Mode: "observe", Reason: err.Error(), LatencyMS: took.Milliseconds(), MessageID: id})
		return nil
	}
	row, ok := replyRow(id, d, v)
	row.Callback = callback
	if ok {
		row.Mode, row.LatencyMS = "observe", took.Milliseconds()
		if row.Action == "respond" {
			row.RespondMode = ""
		}
		c.store(callID, row)
	}
	return nil
}
