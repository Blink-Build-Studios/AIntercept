package main

import (
	"context"
	"testing"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

func text(id, s string) *pb.Part {
	return &pb.Part{Id: id, Kind: &pb.Part_Text{Text: &pb.Text{Text: s}}}
}

func user(id string, parts ...*pb.Part) *pb.ConvMessage {
	return &pb.ConvMessage{Id: id, Role: pb.Role_ROLE_USER, Parts: parts}
}

func respondText(t *testing.T, v *pb.RequestVerdict) string {
	t.Helper()
	if len(v.GetMessages()) != 0 || v.GetFetch() != nil {
		t.Fatalf("verdict %v: want only a Respond, or nothing", v)
	}
	return v.GetRespond().GetText()
}

func TestRespondsToTheNewestUserMessage(t *testing.T) {
	cases := []struct {
		name string
		msgs []*pb.ConvMessage
		want string
	}{
		{"the rabbit", []*pb.ConvMessage{user("a", text("a/0", "I’m a silly rabbit!"), &pb.Part{Id: "a/1", Kind: &pb.Part_Media{Media: &pb.Media{MediaType: "image/png"}}})}, "Well, tricks are for kids."},
		{"the bing", []*pb.ConvMessage{user("a", text("a/0", "<reminder>"), text("a/1", "First we bing"))}, "then we bong"},
		{"a tool result saying it", []*pb.ConvMessage{{Id: "b", Role: pb.Role_ROLE_TOOL, Parts: []*pb.Part{{Id: "b/0", Kind: &pb.Part_ToolResult{ToolResult: &pb.ToolResult{Text: "I'm a silly rabbit"}}}}}}, ""},
		{"only an earlier message says it", []*pb.ConvMessage{user("a", text("a/0", "first we bing")), user("c", text("c/0", "hello"))}, ""},
		{"nothing", []*pb.ConvMessage{user("c", text("c/0", "hello"))}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := server{}.OnRequest(context.Background(), &pb.RequestMessages{Messages: c.msgs})
			if err != nil {
				t.Fatal(err)
			}
			if got := respondText(t, v); got != c.want {
				t.Fatalf("responded %q, want %q", got, c.want)
			}
		})
	}
}

func toolCall(args string) *pb.ResponseMessage {
	return &pb.ResponseMessage{Message: &pb.ConvMessage{Id: "toolu_1", Role: pb.Role_ROLE_ASSISTANT, Parts: []*pb.Part{
		{Id: "toolu_1/0", Kind: &pb.Part_ToolCall{ToolCall: &pb.ToolCall{CallId: "toolu_1", Name: "Bash", Arguments: args}}},
	}}}
}

func TestToolCalls(t *testing.T) {
	v, _ := server{}.OnToolCall(context.Background(), toolCall(`{"command":"echo first we bing"}`))
	if v.GetRespond().GetText() != "then we bong" {
		t.Fatalf("bing: %v", v)
	}
	v, _ = server{}.OnToolCall(context.Background(), toolCall(`{"command":"echo I'm a silly rabbit"}`))
	if got := v.GetReplace().GetMessage().GetParts()[0].GetToolCall().GetArguments(); got != `{"command":"echo Well, tricks are for kids."}` || v.GetMessageId() != "toolu_1" {
		t.Fatalf("rabbit: %v", v)
	}
	v, _ = server{}.OnToolCall(context.Background(), toolCall(`{"command":"ls"}`))
	if v.GetAction() != nil {
		t.Fatalf("ls: %v", v)
	}
}
