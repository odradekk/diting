package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/odradekk/diting/internal/llm"
)

func TestConversation_BasicFlow(t *testing.T) {
	conv := NewConversation("You are helpful.")
	conv.AddUser("What is Go?")

	req := conv.AsRequest(nil, 1024, 0.7)

	if req.System != "You are helpful." {
		t.Errorf("System = %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != "user" || req.Messages[0].Content != "What is Go?" {
		t.Errorf("Messages[0] = %+v", req.Messages[0])
	}
	if req.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d", req.MaxTokens)
	}
	if req.Temperature != 0.7 {
		t.Errorf("Temperature = %f", req.Temperature)
	}
	if req.JSONSchema != nil {
		t.Error("JSONSchema should be nil")
	}
}

func TestConversation_TwoTurnFlow(t *testing.T) {
	conv := NewConversation("system")
	conv.AddUser("question")
	conv.AddAssistant(`{"plan": "search golang"}`)
	conv.AddUser("Here are the results: ...")

	if conv.Len() != 3 {
		t.Errorf("Len = %d, want 3", conv.Len())
	}

	req := conv.AsRequest(nil, 0, 0)
	msgs := req.Messages
	if len(msgs) != 3 {
		t.Fatalf("Messages len = %d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msgs[0].Role = %q", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msgs[1].Role = %q", msgs[1].Role)
	}
	if msgs[2].Role != "user" {
		t.Errorf("msgs[2].Role = %q", msgs[2].Role)
	}
}

func TestConversation_WithJSONSchema(t *testing.T) {
	conv := NewConversation("sys")
	conv.AddUser("q")

	schema := &llm.JSONSchema{
		Name:   "plan",
		Schema: json.RawMessage(`{"type":"object"}`),
	}

	req := conv.AsRequest(schema, 2048, 0)
	if req.JSONSchema == nil {
		t.Fatal("JSONSchema is nil")
	}
	if req.JSONSchema.Name != "plan" {
		t.Errorf("JSONSchema.Name = %q", req.JSONSchema.Name)
	}
}

func TestConversation_MessagesIsolation(t *testing.T) {
	conv := NewConversation("sys")
	conv.AddUser("q1")

	msgs := conv.Messages()
	msgs[0].Content = "MUTATED"

	// Original should not be affected.
	if conv.Messages()[0].Content != "q1" {
		t.Error("Messages() did not return a copy")
	}
}

func TestConversation_EmptySystem(t *testing.T) {
	conv := NewConversation("")
	conv.AddUser("q")

	req := conv.AsRequest(nil, 0, 0)
	if req.System != "" {
		t.Errorf("System = %q, want empty", req.System)
	}
}
