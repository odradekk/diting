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

// ParsePlan extracts a Plan from the LLM's JSON response. It tries three
// strategies in order of strictness:
//
//  1. Envelope form `{"plan": {"queries_by_source_type": {...}, ...}}`
//     — the canonical shape declared by PlanSchema.
//  2. Flat form `{"queries_by_source_type": {...}, ...}` — the same
//     inner object without the "plan" wrapper.
//  3. Lenient recovery — walks the generic JSON tree and harvests any
//     string arrays it finds, bucketing them by parent key name (falling
//     back to general_web for unknown keys). This handles reasoning
//     providers like MiniMax that silently ignore response_format and
//     return semantically-correct content in a slightly different shape.
//
// Strategies 1 and 2 are the fast path. Strategy 3 only kicks in when the
// JSON parsed successfully but neither canonical shape matched — i.e. the
// model produced valid JSON with a different schema. This was triggered
// on 3 queries in the Phase 5.7 first run (et_005, et_013, fr_002) where
// MiniMax returned recognizable plan content under non-canonical keys.
func ParsePlan(content string) (Plan, error) {
	content = trimJSONFences(content)

	// Strategy 1: canonical envelope.
	var envelope planEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Plan.QueriesBySourceType != nil {
		return validatePlan(envelope.Plan)
	}

	// Strategy 2: flat form.
	var plan Plan
	strictErr := json.Unmarshal([]byte(content), &plan)
	if strictErr == nil && plan.QueriesBySourceType != nil {
		return validatePlan(plan)
	}

	// Strategy 3: lenient recovery from a generic walk, but only when the
	// content was parseable as JSON. If the JSON itself is malformed
	// (truncation, unbalanced braces, etc.) we surface the parse error
	// instead — there's nothing to recover from.
	if strictErr == nil {
		if recovered, ok := recoverPlanFromGeneric([]byte(content)); ok {
			return validatePlan(recovered)
		}
	}

	// Final error: prefer the strict JSON parse error if we have one,
	// otherwise the "missing queries_by_source_type" error from the
	// validator.
	if strictErr != nil {
		return Plan{}, fmt.Errorf("invalid plan JSON: %w", strictErr)
	}
	return Plan{}, fmt.Errorf("plan missing queries_by_source_type")
}

// recoverPlanFromGeneric attempts to harvest plan content from a generic
// JSON tree when neither canonical shape matched. It walks the tree
// looking for: rationale/expected_answer_shape strings, and any array of
// strings which becomes a query list bucketed by the parent map key.
//
// Returns (recovered, true) only when the recovered plan has at least
// one query. Unknown parent keys fall through to general_web on the
// assumption that any search is better than no search for the benchmark
// reliability use case.
func recoverPlanFromGeneric(raw []byte) (Plan, bool) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Plan{}, false
	}

	out := Plan{
		QueriesBySourceType: map[search.SourceType][]string{},
	}
	walkPlanValues(root, "", &out)

	if out.TotalQueries() == 0 {
		return Plan{}, false
	}
	if out.Rationale == "" {
		out.Rationale = "(recovered from alternate plan shape)"
	}
	return out, true
}

// walkPlanValues walks a generic JSON value and harvests plan content
// into out. It's called recursively — maps are traversed key-by-key;
// string arrays are bucketed by the parentKey (the map key that contained
// them); known scalar keys (rationale, expected_answer_shape) populate
// the corresponding Plan field directly.
func walkPlanValues(node any, parentKey string, out *Plan) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			switch k {
			case "rationale":
				if s, ok := child.(string); ok && out.Rationale == "" {
					out.Rationale = s
				}
			case "expected_answer_shape":
				if s, ok := child.(string); ok && out.ExpectedAnswerShape == "" {
					out.ExpectedAnswerShape = s
				}
			default:
				walkPlanValues(child, k, out)
			}
		}
	case []any:
		strs := stringArray(v)
		if len(strs) == 0 {
			return
		}
		srcType := inferSourceType(parentKey)
		out.QueriesBySourceType[srcType] = append(out.QueriesBySourceType[srcType], strs...)
	}
}

// stringArray extracts the string elements of a generic slice, skipping
// any non-string elements silently. Used by the plan recovery walk.
func stringArray(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// inferSourceType maps a JSON key name to the closest SourceType. Used
// by the lenient plan recovery walker when the LLM returns queries under
// a non-canonical key. Unknown keys fall through to general_web so a
// bare "queries" list still produces a runnable plan.
func inferSourceType(key string) search.SourceType {
	switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
	case "general_web", "web", "general", "web_search", "websearch", "search":
		return search.SourceTypeGeneralWeb
	case "academic", "arxiv", "papers", "scholar", "research":
		return search.SourceTypeAcademic
	case "code", "github", "source", "source_code":
		return search.SourceTypeCode
	case "community", "stackoverflow", "forum", "q_and_a", "qa":
		return search.SourceTypeCommunity
	case "docs", "documentation", "doc", "official_docs", "reference":
		return search.SourceTypeDocs
	}
	return search.SourceTypeGeneralWeb
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
