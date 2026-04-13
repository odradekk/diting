package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// --- ParsePlan tests ---------------------------------------------------------

const samplePlanJSON = `{
  "plan": {
    "rationale": "Go concurrency is well-covered in docs and community",
    "queries_by_source_type": {
      "general_web": ["Go concurrency patterns", "golang goroutine best practices"],
      "academic": [],
      "code": ["golang concurrency examples"],
      "community": ["goroutine leak stackoverflow"],
      "docs": ["go.dev concurrency tutorial"]
    },
    "expected_answer_shape": "A tutorial-style answer covering goroutines, channels, and select"
  }
}`

func TestParsePlan_Envelope(t *testing.T) {
	plan, err := ParsePlan(samplePlanJSON)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Rationale == "" {
		t.Error("Rationale is empty")
	}
	if plan.TotalQueries() != 5 {
		t.Errorf("TotalQueries = %d, want 5", plan.TotalQueries())
	}
	if len(plan.QueriesBySourceType[search.SourceTypeGeneralWeb]) != 2 {
		t.Errorf("general_web queries = %d, want 2", len(plan.QueriesBySourceType[search.SourceTypeGeneralWeb]))
	}
	if len(plan.QueriesBySourceType[search.SourceTypeAcademic]) != 0 {
		t.Errorf("academic queries = %d, want 0", len(plan.QueriesBySourceType[search.SourceTypeAcademic]))
	}
	if plan.ExpectedAnswerShape == "" {
		t.Error("ExpectedAnswerShape is empty")
	}
}

func TestParsePlan_FlatForm(t *testing.T) {
	flat := `{
    "rationale": "direct",
    "queries_by_source_type": {
      "general_web": ["test query"],
      "academic": [], "code": [], "community": [], "docs": []
    },
    "expected_answer_shape": "short"
  }`
	plan, err := ParsePlan(flat)
	if err != nil {
		t.Fatalf("ParsePlan flat: %v", err)
	}
	if plan.Rationale != "direct" {
		t.Errorf("Rationale = %q", plan.Rationale)
	}
	if plan.TotalQueries() != 1 {
		t.Errorf("TotalQueries = %d, want 1", plan.TotalQueries())
	}
}

func TestParsePlan_WithCodeFences(t *testing.T) {
	fenced := "```json\n" + samplePlanJSON + "\n```"
	plan, err := ParsePlan(fenced)
	if err != nil {
		t.Fatalf("ParsePlan fenced: %v", err)
	}
	if plan.TotalQueries() != 5 {
		t.Errorf("TotalQueries = %d, want 5", plan.TotalQueries())
	}
}

func TestParsePlan_InvalidJSON(t *testing.T) {
	_, err := ParsePlan("not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParsePlan_MissingQueries(t *testing.T) {
	_, err := ParsePlan(`{"rationale":"test","expected_answer_shape":"x"}`)
	if err == nil {
		t.Fatal("expected error for missing queries")
	}
}

func TestParsePlan_ZeroQueries(t *testing.T) {
	_, err := ParsePlan(`{
    "rationale": "nothing to search",
    "queries_by_source_type": {
      "general_web": [], "academic": [], "code": [], "community": [], "docs": []
    },
    "expected_answer_shape": "empty"
  }`)
	if err == nil {
		t.Fatal("expected error for zero queries")
	}
	if !strings.Contains(err.Error(), "zero queries") {
		t.Errorf("error = %v", err)
	}
}

// --- trimJSONFences tests ----------------------------------------------------

func TestTrimJSONFences(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		{"  \n```json\n{\"a\":1}\n```\n  ", `{"a":1}`},
		// <think> blocks from reasoning models (MiniMax, DeepSeek).
		{"<think>\nLet me think...\n</think>\n{\"a\":1}", `{"a":1}`},
		{"<think>reasoning</think>\n```json\n{\"a\":1}\n```", `{"a":1}`},
	}
	for _, tt := range tests {
		got := trimJSONFences(tt.in)
		if got != tt.want {
			t.Errorf("trimJSONFences(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- PlanSchema validity -----------------------------------------------------

func TestPlanSchema_ValidJSON(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal(PlanSchema.Schema, &parsed); err != nil {
		t.Fatalf("PlanSchema is not valid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("schema type = %v, want object", parsed["type"])
	}
}

// --- RunPlanPhase with mock LLM ----------------------------------------------

type mockLLM struct {
	fn func(context.Context, llm.Request) (*llm.Response, error)
}

func (m *mockLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return m.fn(ctx, req)
}

func TestRunPlanPhase_Success(t *testing.T) {
	var gotSchema *llm.JSONSchema
	client := &mockLLM{fn: func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		gotSchema = req.JSONSchema
		return &llm.Response{
			Content:     samplePlanJSON,
			InputTokens: 100,
			OutputTokens: 50,
		}, nil
	}}

	system, _ := RenderSystem(SystemPromptData{SourceTypes: "general_web", Modules: "bing"})
	conv := NewConversation(system)

	result, err := RunPlanPhase(context.Background(), client, conv, "How does Go concurrency work?", 2048)
	if err != nil {
		t.Fatalf("RunPlanPhase: %v", err)
	}

	if result.Plan.TotalQueries() != 5 {
		t.Errorf("TotalQueries = %d, want 5", result.Plan.TotalQueries())
	}
	if result.InputTokens != 100 {
		t.Errorf("InputTokens = %d", result.InputTokens)
	}
	if result.RawContent != samplePlanJSON {
		t.Error("RawContent mismatch")
	}

	// Schema should be the PlanSchema.
	if gotSchema == nil || gotSchema.Name != "search_plan" {
		t.Errorf("schema = %+v, want search_plan", gotSchema)
	}

	// Conversation should have 1 user message after plan phase.
	if conv.Len() != 1 {
		t.Errorf("conv.Len = %d, want 1", conv.Len())
	}
}

func TestRunPlanPhase_LLMError(t *testing.T) {
	client := &mockLLM{fn: func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		return nil, fmt.Errorf("rate limited")
	}}

	conv := NewConversation("sys")
	_, err := RunPlanPhase(context.Background(), client, conv, "test", 1024)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "llm") {
		t.Errorf("error = %v", err)
	}
}

func TestRunPlanPhase_ParseError(t *testing.T) {
	client := &mockLLM{fn: func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		return &llm.Response{Content: "not valid json at all"}, nil
	}}

	conv := NewConversation("sys")
	_, err := RunPlanPhase(context.Background(), client, conv, "test", 1024)
	if err == nil {
		t.Fatal("expected error for invalid plan JSON")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %v", err)
	}
}
