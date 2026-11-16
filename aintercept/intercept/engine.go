package intercept

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// Hooks give full control. Every hook is optional; a message no rule, hook
// or watch applies to is accepted without a round trip, and when nothing
// applies to the model's reply text (or its tool calls) the observer is told
// not to hold them at all.
//
// For each message, the most specific runs first, and the first answer that
// isn't Accept wins (the rest are skipped):
//
//	prompt           rules → watches → OnRequest → OnMessage
//	tool result      rules → watches → OnToolCallResponse → OnRequest → OnMessage
//	reply text       watches (rules on reply text among them) → rules → OnResponse → OnMessage
//	tool call        rules → OnToolCallRequest → OnResponse → OnMessage
//
// Per call, hooks run in order (start, the messages in order both ways,
// complete); calls run concurrently. ctx carries the deadline. In observe
// mode the same code runs; answers are recorded, never applied.
type Hooks struct {
	OnCallStart func(ctx context.Context, c *Call) Answer
	// OnMessage: every message, both directions, every kind.
	OnMessage func(ctx context.Context, m *Message) Answer
	// OnRequest: messages to the model (prompts, tool results, media).
	OnRequest func(ctx context.Context, m *Message) Answer
	// OnResponse: messages from the model: its reply text, each streamed
	// piece as it comes (Before is what's already released; Final marks the
	// last), and its tool calls.
	OnResponse func(ctx context.Context, m *Message) Answer
	// OnToolCallRequest: the model asks to run a tool (a tool call request is
	// in the model's response), before the agent sees it.
	OnToolCallRequest func(ctx context.Context, t *ToolCall) Answer
	// OnToolCallResponse: a tool's result going back to the model (a tool
	// call response is in the next request).
	OnToolCallResponse func(ctx context.Context, t *ToolResult) Answer
	OnCallComplete     func(ctx context.Context, r *Record)
	Watches            []Watch
}

func (Hooks) item() {}

// Item is what Run and New take: Rules, Hooks, Watches.
type Item interface{ item() }

// Server is an Interceptor built from rules, hooks and watches: a contract v2
// gRPC service (pb.InterceptorServer).
type Server struct {
	pb.UnimplementedInterceptorServer
	rules   []Rule
	hooks   Hooks
	watches []watcher // all of them, rules' among them
	textW   []watcher // those on reply text
	reqW    []watcher // those on messages to the model
	// replyText, toolCalls: something applies to them.
	replyText, toolCalls bool

	mu    sync.Mutex
	calls map[string]*Call
	log   *slog.Logger
	stats *stats
}

// New builds the Interceptor from rules, hooks and watches.
func New(items ...Item) *Server {
	s := &Server{calls: map[string]*Call{}, log: slog.Default(), stats: newStats()}
	var ws []Watch
	for _, it := range items {
		switch x := it.(type) {
		case Rule:
			s.rules = append(s.rules, x)
			if w, ok := x.watch(); ok {
				ws = append(ws, w)
			}
		case Hooks:
			s.hooks = merge(s.hooks, x)
			ws = append(ws, x.Watches...)
		case Watch:
			ws = append(ws, x)
		}
	}
	for i := range ws {
		w := newWatcher(&ws[i])
		s.watches = append(s.watches, w)
		if ws[i].In != ToModel {
			s.textW = append(s.textW, w)
		}
		if ws[i].In != FromModel {
			s.reqW = append(s.reqW, w)
		}
	}
	h := s.hooks
	s.replyText = len(s.textW) > 0 || h.OnResponse != nil || h.OnMessage != nil
	s.toolCalls = h.OnToolCallRequest != nil || h.OnResponse != nil || h.OnMessage != nil
	for _, r := range s.rules {
		switch r.on {
		case onResponse, onMessage:
			s.toolCalls = true
			if _, ok := r.watch(); !ok {
				s.replyText = true
			}
		case onToolCallRequest:
			s.toolCalls = true
		}
	}
	return s
}

// merge combines two Hooks: the later one's hooks fill the earlier one's gaps.
func merge(a, b Hooks) Hooks {
	if a.OnCallStart == nil {
		a.OnCallStart = b.OnCallStart
	}
	if a.OnMessage == nil {
		a.OnMessage = b.OnMessage
	}
	if a.OnRequest == nil {
		a.OnRequest = b.OnRequest
	}
	if a.OnResponse == nil {
		a.OnResponse = b.OnResponse
	}
	if a.OnToolCallRequest == nil {
		a.OnToolCallRequest = b.OnToolCallRequest
	}
	if a.OnToolCallResponse == nil {
		a.OnToolCallResponse = b.OnToolCallResponse
	}
	if a.OnCallComplete == nil {
		a.OnCallComplete = b.OnCallComplete
	}
	return a
}

// ── Calls ────────────────────────────────────────────────────────────────────

// callTTL frees a call whose OnCallComplete never came.
const callTTL = time.Hour

// call is the Call for c, the same object for the call's every hook.
func (s *Server) call(c *pb.Call) *Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	k := c.GetCallId()
	cl := s.calls[k]
	if cl == nil {
		if len(s.calls) > 0 && len(s.calls)%1024 == 0 {
			for id, old := range s.calls {
				if now.Sub(old.seen) > callTTL {
					delete(s.calls, id)
				}
			}
		}
		id := c.GetIdentity()
		cl = &Call{ID: k, Mode: Observe, Person: Person{Email: id.GetEmail(), Account: firstOf(id.GetAccountId(), id.GetOrgId())},
			Agent: Agent(c.GetClient().GetProduct()), Session: id.GetSessionId(), Transport: HTTP}
		if c.GetMode() == pb.Mode_MODE_ENFORCE {
			cl.Mode = Enforce
		}
		if c.GetTransport() == pb.Transport_TRANSPORT_WEBSOCKET {
			cl.Transport = WebSocket
		}
		s.calls[k] = cl
	}
	cl.seen = now
	return cl
}

func (s *Server) done(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.calls, id)
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ── Running hooks ────────────────────────────────────────────────────────────

// step is one rule, watch pass or hook in a message's order.
type step func(ctx context.Context, m *Message) Answer

// first runs steps in order until one answers other than Accept. Notes from
// every step are kept. A panic is a Fail.
func first(ctx context.Context, m *Message, steps ...step) (a *answer) {
	var notes [][2]string
	defer func() {
		if p := recover(); p != nil {
			a = &answer{verb: vFail, err: fmt.Errorf("panic: %v", p)}
		}
		a.notes = append(notes, a.notes...)
	}()
	for _, st := range steps {
		if st == nil {
			continue
		}
		x := get(st(ctx, m))
		if x.verb != vAccept {
			return x
		}
		notes = append(notes, x.notes...)
	}
	return &answer{}
}

// ruleSteps is the rules that apply to m, as steps; skip leaves out rules
// that became watches (for reply text).
func (s *Server) ruleSteps(m *Message, skipWatches bool) []step {
	var out []step
	for _, r := range s.rules {
		if !r.applies(m) {
			continue
		}
		if _, ok := r.watch(); ok && skipWatches {
			continue
		}
		out = append(out, func(_ context.Context, m *Message) Answer {
			if r.m != nil && !r.m.Match(m) {
				return nil
			}
			return r.do(m)
		})
	}
	return out
}

func msgHook(f func(context.Context, *Message) Answer) step {
	if f == nil {
		return nil
	}
	return f
}

// note logs a decision and counts it.
func (s *Server) note(callback string, m *Message, a *answer) {
	s.stats.add(callback, a.verb.String())
	if a.verb == vAccept && len(a.notes) == 0 {
		return
	}
	attrs := []any{"callback", callback, "answer", a.verb.String(), "message", m.ID, "kind", m.Kind.String()}
	if m.Call != nil {
		attrs = append(attrs, "call", m.Call.ID, "mode", map[Mode]string{Observe: "observe", Enforce: "enforce"}[m.Call.Mode])
	}
	if a.err != nil {
		attrs = append(attrs, "err", a.err.Error())
	}
	s.log.Info("intercept decision", attrs...)
}

func annotations(notes [][2]string) []*pb.Annotation {
	var out []*pb.Annotation
	for _, n := range notes {
		out = append(out, &pb.Annotation{Key: n[0], Value: n[1]})
	}
	return out
}

func failed(a *answer) error {
	err := a.err
	if err == nil {
		err = errors.New("failed")
	}
	return status.Error(codes.Aborted, "intercept: "+err.Error())
}

// ── OnCallStart ──────────────────────────────────────────────────────────────

func (s *Server) OnCallStart(ctx context.Context, cs *pb.CallStart) (*pb.CallVerdict, error) {
	c := s.call(cs.GetCall())
	c.Model = cs.GetRequest().GetModel()
	v := &pb.CallVerdict{}
	if s.hooks.OnCallStart == nil {
		return v, nil
	}
	m := &Message{Call: c}
	a := first(ctx, m, func(ctx context.Context, _ *Message) Answer { return s.hooks.OnCallStart(ctx, c) })
	s.note("call_start", m, a)
	v.Annotations = annotations(a.notes)
	switch a.verb {
	case vReply:
		v.Action = &pb.CallVerdict_Respond{Respond: &pb.Respond{Text: a.text}}
	case vFail:
		return nil, failed(a)
	}
	return v, nil
}

// ── OnRequest: messages to the model ─────────────────────────────────────────

var exitCode = regexp.MustCompile(`(?m)^(?:Exit code:?|exit code:?|Process exited with code) (-?\d+)`)

func (s *Server) OnRequest(ctx context.Context, r *pb.RequestMessages) (*pb.RequestVerdict, error) {
	c := s.call(r.GetCall())
	v := &pb.RequestVerdict{}
	// The newest prompt: the last user message with text.
	newest := -1
	for i, cm := range r.GetMessages() {
		for _, p := range cm.GetParts() {
			if cm.GetRole() == pb.Role_ROLE_USER && p.GetText() != nil {
				newest = i
			}
		}
	}
	var fetch []string
	for i, cm := range r.GetMessages() {
		var changed *pb.ConvMessage
		for j, p := range cm.GetParts() {
			m := &Message{Direction: ToModel, Call: c, ID: p.GetId(), Truncated: p.GetRef(), Size: int(p.GetSize())}
			var res *ToolResult
			switch k := p.GetKind().(type) {
			case *pb.Part_Text:
				m.Kind, m.Text, m.Newest = KindPrompt, k.Text.GetText(), i == newest
			case *pb.Part_ToolResult:
				m.Kind, m.Text, m.Tool, m.IsError = KindToolResult, k.ToolResult.GetText(), k.ToolResult.GetName(), k.ToolResult.GetIsError()
				res = &ToolResult{Name: m.Tool, Text: m.Text, IsError: m.IsError, Message: m, Call: c}
				if x := exitCode.FindStringSubmatch(m.Text); x != nil {
					n, _ := strconv.Atoi(x[1])
					res.ExitCode = &n
				}
			case *pb.Part_Media:
				m.Kind = KindMedia
			case *pb.Part_ToolCall:
				continue // a tool call in the request (history the provider resends): not this call's
			default:
				m.Kind = KindOpaque
			}
			if m.Truncated {
				m.Text = p.GetPreview()
			}
			steps := s.ruleSteps(m, false)
			if m.Kind == KindPrompt || m.Kind == KindToolResult {
				steps = append(steps, s.watchStep(s.reqW))
			}
			if res != nil && s.hooks.OnToolCallResponse != nil {
				steps = append(steps, func(ctx context.Context, _ *Message) Answer { return s.hooks.OnToolCallResponse(ctx, res) })
			}
			steps = append(steps, msgHook(s.hooks.OnRequest), msgHook(s.hooks.OnMessage))
			a := first(ctx, m, steps...)
			s.note("request", m, a)
			v.Annotations = append(v.Annotations, annotations(a.notes)...)
			switch a.verb {
			case vReply:
				v.Respond = &pb.Respond{Text: a.text}
				v.Messages, v.Fetch = nil, nil
				return v, nil
			case vFail:
				return nil, failed(a)
			case vNeedFull:
				if m.Truncated && !r.GetFetched() {
					fetch = append(fetch, p.GetId())
				}
			case vReplace, vReplaceArgs:
				if !m.Changeable() && c.Mode == Enforce {
					return nil, failed(&answer{err: fmt.Errorf("a %s can't be replaced", m.Kind)})
				}
				if changed == nil {
					changed = proto.Clone(cm).(*pb.ConvMessage)
				}
				cp := changed.GetParts()[j]
				cp.Ref, cp.Preview = false, ""
				switch k := cp.GetKind().(type) {
				case *pb.Part_Text:
					k.Text.Text = a.text
				case *pb.Part_ToolResult:
					k.ToolResult.Text = a.text
				}
			}
		}
		if changed != nil {
			v.Messages = append(v.Messages, &pb.MessageVerdict{MessageId: cm.GetId(), Action: &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: changed}}})
		}
	}
	if len(fetch) > 0 {
		return &pb.RequestVerdict{Fetch: &pb.Fetch{PartIds: fetch}}, nil
	}
	return v, nil
}

// watchStep runs the watches on a whole message: their replacements, or a
// Reply.
func (s *Server) watchStep(ws []watcher) step {
	if len(ws) == 0 {
		return nil
	}
	return func(ctx context.Context, m *Message) Answer {
		sc := scan(ctx, ws, m, m.Text, 0, true)
		var a *answer
		switch {
		case sc.err != nil:
			a = &answer{verb: vFail, err: sc.err}
		case sc.reply != nil:
			a = sc.reply
		case sc.changed:
			a = &answer{verb: vReplace, text: sc.out}
		default:
			a = &answer{}
		}
		a.notes = append(sc.notes, a.notes...)
		return a
	}
}

// ── OnReply: the model's reply text ──────────────────────────────────────────

func (s *Server) OnReply(ctx context.Context, r *pb.ResponseMessage) (*pb.MessageVerdict, error) {
	if !s.replyText {
		return nil, status.Error(codes.Unimplemented, "intercept: nothing applies to reply text")
	}
	c := s.call(r.GetCall())
	cm := r.GetMessage()
	v := &pb.MessageVerdict{MessageId: cm.GetId()}
	p := cm.GetParts()[0]
	if p.GetText() == nil { // an opaque part: told, can't change it
		m := &Message{Direction: FromModel, Kind: KindOpaque, Call: c, ID: cm.GetId(), Final: true, Size: int(p.GetSize())}
		first(ctx, m, msgHook(s.hooks.OnResponse), msgHook(s.hooks.OnMessage))
		return v, nil
	}
	pending := p.GetText().GetText()
	m := &Message{Direction: FromModel, Kind: KindText, Call: c, ID: cm.GetId(), Before: r.GetReleased(), Final: r.GetDone(), Text: pending, Size: len(pending)}
	// Enforce: what was released never ends in a match's start (it was held
	// back). Observe: nothing was held, so a match can start in it.
	ctxt := ""
	if c.Mode == Observe {
		ctxt = releasedTail(s.textW, r.GetReleased())
	}
	sc := scan(ctx, s.textW, m, ctxt+pending, len(ctxt), r.GetDone())
	var a *answer
	switch {
	case sc.err != nil:
		a = &answer{verb: vFail, err: sc.err}
	case sc.reply != nil:
		a = &answer{verb: vReply, text: sc.out + sc.reply.text, notes: sc.reply.notes}
	case sc.changed:
		a = &answer{verb: vReplace, text: sc.out[:len(sc.out)-sc.keep]}
	case len(pending)-sc.keep == 0:
		a = &answer{} // all of it could still become a match
	default:
		m.Text = pending[:len(pending)-sc.keep] // what can go now
		a = first(ctx, m, append(s.ruleSteps(m, true), msgHook(s.hooks.OnResponse), msgHook(s.hooks.OnMessage))...)
	}
	a.notes = append(sc.notes, a.notes...)
	s.note("reply", m, a)
	v.Annotations = annotations(a.notes)
	switch a.verb {
	case vFail:
		return nil, failed(a)
	case vReply:
		v.Action = &pb.MessageVerdict_Respond{Respond: &pb.Respond{Text: a.text}}
	case vHold:
		v.Action = &pb.MessageVerdict_Hold{Hold: &pb.Hold{}}
	case vReplace:
		cm.GetParts()[0].GetText().Text = a.text
		v.Action = &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: cm}}
		v.Keep = int32(sc.keep)
	default:
		v.Keep = int32(sc.keep)
	}
	return v, nil
}

// ── OnToolCall: the model asks to run a tool ─────────────────────────────────

func (s *Server) OnToolCall(ctx context.Context, r *pb.ResponseMessage) (*pb.MessageVerdict, error) {
	if !s.toolCalls {
		return nil, status.Error(codes.Unimplemented, "intercept: nothing applies to tool calls")
	}
	c := s.call(r.GetCall())
	cm := r.GetMessage()
	p := cm.GetParts()[0]
	tc := p.GetToolCall()
	m := &Message{Direction: FromModel, Kind: KindToolCall, Call: c, ID: cm.GetId(), Tool: tc.GetName(), Text: tc.GetArguments(),
		Truncated: p.GetRef(), Size: int(p.GetSize()), Final: true}
	if m.Truncated {
		m.Text = p.GetPreview()
	}
	call := &ToolCall{Name: tc.GetName(), Args: decodeArgs(m.Text), Message: m, Call: c}
	if rest, ok := strings.CutPrefix(call.Name, "mcp__"); ok {
		call.Server, _, _ = strings.Cut(rest, "__")
	}
	steps := s.ruleSteps(m, false)
	if s.hooks.OnToolCallRequest != nil {
		steps = append(steps, func(ctx context.Context, _ *Message) Answer { return s.hooks.OnToolCallRequest(ctx, call) })
	}
	a := first(ctx, m, append(steps, msgHook(s.hooks.OnResponse), msgHook(s.hooks.OnMessage))...)
	s.note("tool_call", m, a)
	v := &pb.MessageVerdict{MessageId: cm.GetId(), Annotations: annotations(a.notes)}
	switch a.verb {
	case vFail:
		return nil, failed(a)
	case vReply:
		v.Action = &pb.MessageVerdict_Respond{Respond: &pb.Respond{Text: a.text}}
	case vReplace, vReplaceArgs:
		tc.Arguments = a.text
		p.Ref, p.Preview = false, ""
		v.Action = &pb.MessageVerdict_Replace{Replace: &pb.Replace{Message: cm}}
	}
	return v, nil
}

// ── OnCallComplete ───────────────────────────────────────────────────────────

func (s *Server) OnCallComplete(ctx context.Context, r *pb.CallRecord) (*pb.Ack, error) {
	c := s.call(r.GetCall())
	if r.GetWsTurn() == 0 {
		defer s.done(c.ID)
	}
	if s.hooks.OnCallComplete == nil {
		return &pb.Ack{}, nil
	}
	resp := r.GetResponse()
	rec := &Record{Call: c, Status: int(resp.GetStatus()), Model: resp.GetModel(), StopReason: resp.GetStopReason(),
		TTFB: time.Duration(resp.GetTtfbMs()) * time.Millisecond, Duration: time.Duration(resp.GetDurationMs()) * time.Millisecond,
		Usage: Usage{Input: resp.GetInputTokens(), Output: resp.GetOutputTokens(), CacheRead: resp.GetCacheReadTokens(),
			CacheWrite: resp.GetCacheWriteTokens(), Reasoning: resp.GetReasoningTokens()},
		Replied: r.GetRespond(), WSTurn: int(r.GetWsTurn())}
	for _, d := range r.GetVerdicts() {
		dd := Decision{Callback: d.GetCallback(), Action: d.GetAction(), Applied: d.GetApplied(), MessageID: d.GetMessageId(),
			Reason: d.GetReason(), RespondMode: d.GetRespondMode()}
		for _, a := range d.GetAnnotations() {
			if dd.Notes == nil {
				dd.Notes = map[string]string{}
			}
			dd.Notes[a.GetKey()] = a.GetValue()
		}
		rec.Answers = append(rec.Answers, dd)
	}
	func() {
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("intercept: OnCallComplete panicked", "call", c.ID, "panic", fmt.Sprint(p))
			}
		}()
		s.hooks.OnCallComplete(ctx, rec)
	}()
	return &pb.Ack{}, nil
}
