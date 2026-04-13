package pipeline

import (
	"strings"
	"testing"
)

func TestRenderSystem(t *testing.T) {
	s, err := RenderSystem(SystemPromptData{
		SourceTypes: "general_web, academic, code, community",
		Modules:     "bing (general_web), arxiv (academic), github (code)",
	})
	if err != nil {
		t.Fatalf("RenderSystem: %v", err)
	}
	if !strings.Contains(s, "diting") {
		t.Errorf("system prompt missing 'diting': %s", s[:100])
	}
	if !strings.Contains(s, "general_web, academic, code, community") {
		t.Error("system prompt missing source types")
	}
	if !strings.Contains(s, "bing") {
		t.Error("system prompt missing module names")
	}
}

func TestRenderPlan(t *testing.T) {
	s, err := RenderPlan(PlanPromptData{})
	if err != nil {
		t.Fatalf("RenderPlan: %v", err)
	}
	if !strings.Contains(s, "JSON") {
		t.Errorf("plan prompt missing JSON instruction: %s", s)
	}
}

func TestRenderAnswer(t *testing.T) {
	s, err := RenderAnswer(AnswerPromptData{
		Sources: "SOURCE 1 [docs]\nURL: https://example.com\nContent: test content",
	})
	if err != nil {
		t.Fatalf("RenderAnswer: %v", err)
	}
	if !strings.Contains(s, "SOURCE 1") {
		t.Error("answer prompt missing sources")
	}
	if !strings.Contains(s, "citation") {
		t.Error("answer prompt missing citation instruction")
	}
}
