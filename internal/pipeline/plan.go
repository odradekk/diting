package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// Plan is the parsed output of the plan phase (LLM turn 1).
type Plan struct {
	Rationale           string                          `json:"rationale"`
	QueriesBySourceType map[search.SourceType][]string  `json:"queries_by_source_type"`
	ExpectedAnswerShape string                          `json:"expected_answer_shape"`
}

// TotalQueries returns the total number of queries across all source types.
func (p *Plan) TotalQueries() int {
	n := 0
	for _, qs := range p.QueriesBySourceType {
		n += len(qs)
	}
	return n
}

// planEnvelope is the top-level JSON structure the LLM emits.
type planEnvelope struct {
	Plan Plan `json:"plan"`
}

// PlanSchema is the JSON schema enforced on the LLM during the plan phase.
// Providers map this to their native structured output mechanism (OpenAI
// response_format, Anthropic tool_choice).
var PlanSchema = &llm.JSONSchema{
	Name: "search_plan",
	Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "plan": {
      "type": "object",
      "properties": {
        "rationale": {
          "type": "string",
          "description": "Why these source types and queries were chosen"
        },
        "queries_by_source_type": {
          "type": "object",
          "properties": {
            "general_web": { "type": "array", "items": { "type": "string" } },
            "academic":    { "type": "array", "items": { "type": "string" } },
            "code":        { "type": "array", "items": { "type": "string" } },
            "community":   { "type": "array", "items": { "type": "string" } },
            "docs":        { "type": "array", "items": { "type": "string" } }
          },
          "required": ["general_web", "academic", "code", "community", "docs"],
          "additionalProperties": false
        },
        "expected_answer_shape": {
          "type": "string",
          "description": "What a good answer looks like"
        }
      },
      "required": ["rationale", "queries_by_source_type", "expected_answer_shape"],
      "additionalProperties": false
    }
  },
  "required": ["plan"],
  "additionalProperties": false
}`),
}

// PlanResult holds the plan phase output plus the raw LLM response for
// conversation continuation and cost tracking.
type PlanResult struct {
	Plan        Plan
	RawContent  string       // raw LLM response (JSON string), used as assistant message in turn 2
	InputTokens int
	OutputTokens int
	CacheReadTokens int
}

// RunPlanPhase executes the plan phase: builds the conversation, calls the
// LLM with schema enforcement, and parses the structured plan.
func RunPlanPhase(ctx context.Context, client llm.Client, conv *Conversation, question string, maxTokens int) (*PlanResult, error) {
	// Append user question + plan instructions.
	planInstructions, err := RenderPlan(PlanPromptData{})
	if err != nil {
		return nil, fmt.Errorf("plan: render instructions: %w", err)
	}
	conv.AddUser(question + "\n\n" + planInstructions)

	// Call LLM with plan schema.
	req := conv.AsRequest(PlanSchema, maxTokens, 0)
	resp, err := client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan: llm: %w", err)
	}

	// Parse the plan from the response.
	plan, err := ParsePlan(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("plan: parse: %w", err)
	}

	return &PlanResult{
		Plan:            plan,
		RawContent:      resp.Content,
		InputTokens:     resp.InputTokens,
		OutputTokens:    resp.OutputTokens,
		CacheReadTokens: resp.CacheReadTokens,
	}, nil
}

// ParsePlan extracts a Plan from the LLM's JSON response. It accepts both
// the envelope form {"plan": {...}} and the flat form {...}.
func ParsePlan(content string) (Plan, error) {
	content = trimJSONFences(content)

	// Try envelope form first.
	var envelope planEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Plan.Rationale != "" {
		return validatePlan(envelope.Plan)
	}

	// Fall back to flat form (no "plan" wrapper).
	var plan Plan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return Plan{}, fmt.Errorf("invalid plan JSON: %w", err)
	}

	return validatePlan(plan)
}

func validatePlan(p Plan) (Plan, error) {
	if p.QueriesBySourceType == nil {
		return Plan{}, fmt.Errorf("plan missing queries_by_source_type")
	}
	if p.TotalQueries() == 0 {
		return Plan{}, fmt.Errorf("plan has zero queries")
	}
	return p, nil
}

// trimJSONFences strips wrappers that LLMs commonly add around JSON output:
//   - ```json ... ``` markdown fences
//   - <think>...</think> reasoning blocks (MiniMax, DeepSeek, etc.)
func trimJSONFences(s string) string {
	// Strip <think>...</think> blocks (may appear before the JSON).
	if idx := strings.Index(s, "</think>"); idx >= 0 {
		s = s[idx+len("</think>"):]
	}

	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
