package main

import (
	"context"
	"testing"

	pb "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
)

func text(id, s string) *pb.Part {
	return &pb.Part{Id: id, Kind: &pb.Part_Text{Text: &pb.Text{Text: s}}}
}

func TestReplacesTheRabbitInUserMessages(t *testing.T) {
	v, err := server{}.OnRequest(context.Background(), &pb.RequestMessages{Messages: []*pb.ConvMessage{
		{Id: "a", Role: pb.Role_ROLE_USER, Parts: []*pb.Part{text("a/0", "I’m a silly rabbit!"), {Id: "a/1", Kind: &pb.Part_Media{Media: &pb.Media{MediaType: "image/png"}}}}},
		{Id: "b", Role: pb.Role_ROLE_TOOL, Parts: []*pb.Part{{Id: "b/0", Kind: &pb.Part_ToolResult{ToolResult: &pb.ToolResult{Text: "I'm a silly rabbit"}}}}},
		{Id: "c", Role: pb.Role_ROLE_USER, Parts: []*pb.Part{text("c/0", "hello")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.GetMessages()) != 1 || v.GetMessages()[0].GetMessageId() != "a" {
		t.Fatalf("verdict %v: want message a replaced, the tool result and c accepted", v)
	}
	m := v.GetMessages()[0].GetReplace().GetMessage()
	if got := m.GetParts()[0].GetText().GetText(); got != tricks+"!" || len(m.GetParts()) != 2 {
		t.Fatalf("replaced message %v", m)
	}
}
