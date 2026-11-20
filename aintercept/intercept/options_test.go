package intercept_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Blink-Build-Studios/aintercept/aintercept/intercept"
	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

// Per-person fast mode (issue #118): a call's options, read with Get, and an
// Error that refuses it as Anthropic does for an organization without it.
func fastOnlyFor(email string) intercept.Hooks {
	refuse := func(o intercept.Options, p intercept.Person) intercept.Answer {
		if o.Get(intercept.Speed) == "fast" && !(p.Verified && p.Email == email) {
			return intercept.Error(400, "invalid_request_error", "Fast mode is not enabled for your organization.")
		}
		return nil
	}
	return intercept.Hooks{
		OnCallStart: func(_ context.Context, c *intercept.Call) intercept.Answer { return refuse(c.Options, c.Person) },
		OnTurnStart: func(_ context.Context, t *intercept.Turn) intercept.Answer { return refuse(t.Options, t.Call.Person) },
	}
}

func start(callID, email string, opts ...*pb.OptionValue) *pb.CallStart {
	return &pb.CallStart{
		Call: &pb.Call{CallId: callID, Mode: pb.Mode_MODE_ENFORCE, Provider: pb.Provider_PROVIDER_ANTHROPIC, Api: pb.Api_API_ANTHROPIC_MESSAGES,
			Identity: &pb.Identity{Email: email, EmailSource: pb.IdentitySource_IDENTITY_SOURCE_CREDENTIAL}},
		Request: &pb.RequestInfo{Model: "m", Options: opts},
	}
}

var fast = &pb.OptionValue{Key: pb.Option_OPTION_SPEED, Value: "fast"}

func TestErrorAtCallStart(t *testing.T) {
	s := intercept.New(fastOnlyFor("ada@example.com"))
	ctx := context.Background()
	v, err := s.OnCallStart(ctx, start("1", "bob@example.com", fast))
	if err != nil || v.GetError().GetHttpStatus() != 400 || v.GetError().GetType() != "invalid_request_error" ||
		v.GetError().GetMessage() != "Fast mode is not enabled for your organization." {
		t.Fatalf("bob, fast: %v %v", v, err)
	}
	if v, err := s.OnCallStart(ctx, start("2", "ada@example.com", fast)); err != nil || v.GetAction() != nil {
		t.Fatalf("ada, fast: %v %v", v, err)
	}
	if v, err := s.OnCallStart(ctx, start("3", "bob@example.com")); err != nil || v.GetAction() != nil {
		t.Fatalf("bob, standard: %v %v", v, err)
	}
}

// A WebSocket's turns: each one's options in OnTurnStart, on the
// connection's Call.
func TestErrorAtTurnStart(t *testing.T) {
	s := intercept.New(fastOnlyFor("ada@example.com"))
	ctx := context.Background()
	up := start("ws", "bob@example.com")
	up.Call.Transport, up.Call.Api = pb.Transport_TRANSPORT_WEBSOCKET, pb.Api_API_CHATGPT_BACKEND
	if _, err := s.OnCallStart(ctx, up); err != nil {
		t.Fatal(err)
	}
	turn := func(n int32, opts ...*pb.OptionValue) *pb.CallVerdict {
		v, err := s.OnTurnStart(ctx, &pb.TurnStart{Call: up.Call, WsTurn: n, Request: &pb.RequestInfo{Options: opts}})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := turn(1); v.GetAction() != nil {
		t.Fatalf("turn 1: %v", v)
	}
	if v := turn(2, fast); v.GetError().GetHttpStatus() != 400 {
		t.Fatalf("turn 2: %v", v)
	}
}

// Without an OnTurnStart hook the Interceptor doesn't implement it: the
// observer stops asking.
func TestNoTurnStartHook(t *testing.T) {
	s := intercept.New(intercept.Hooks{})
	if _, err := s.OnTurnStart(context.Background(), &pb.TurnStart{Call: &pb.Call{CallId: "x"}}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("got %v, want Unimplemented", err)
	}
}

// Error answers a call or a request; on the model's reply it's a Fail.
func TestErrorOnRequestAndReply(t *testing.T) {
	no := func(context.Context, *intercept.Message) intercept.Answer { return intercept.Error(403, "", "No.") }
	s := intercept.New(intercept.Hooks{OnRequest: no, OnResponse: no})
	ctx := context.Background()
	call := &pb.Call{CallId: "c", Mode: pb.Mode_MODE_ENFORCE}
	v, err := s.OnRequest(ctx, &pb.RequestMessages{Call: call, Messages: []*pb.ConvMessage{{Id: "m1", Role: pb.Role_ROLE_USER,
		Parts: []*pb.Part{{Id: "m1/0", Kind: &pb.Part_Text{Text: &pb.Text{Text: "hi"}}}}}}})
	if err != nil || v.GetError().GetHttpStatus() != 403 || v.GetError().GetMessage() != "No." || len(v.GetMessages()) != 0 {
		t.Fatalf("OnRequest: %v %v", v, err)
	}
	_, err = s.OnReply(ctx, &pb.ResponseMessage{Call: call, Done: true, Message: &pb.ConvMessage{Id: "r:0", Role: pb.Role_ROLE_ASSISTANT,
		Parts: []*pb.Part{{Kind: &pb.Part_Text{Text: &pb.Text{Text: "hello"}}}}}})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("OnReply: %v, want a failure", err)
	}
}

// A record carries what was asked for and what served it.
func TestRecordOptions(t *testing.T) {
	var got *intercept.Record
	s := intercept.New(intercept.Hooks{OnCallComplete: func(_ context.Context, r *intercept.Record) { got = r }})
	_, _ = s.OnCallComplete(context.Background(), &pb.CallRecord{Call: &pb.Call{CallId: "c"},
		Request:  &pb.RequestInfo{Options: []*pb.OptionValue{fast}},
		Response: &pb.ResponseInfo{Options: []*pb.OptionValue{{Key: pb.Option_OPTION_SPEED, Value: "standard"}}}})
	if got.Asked.Get(intercept.Speed) != "fast" || got.Served.Get(intercept.Speed) != "standard" {
		t.Fatalf("asked %v served %v", got.Asked, got.Served)
	}
}
