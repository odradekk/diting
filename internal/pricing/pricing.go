// Package pricing implements the LLM cost-estimation logic used by the
// --max-cost guard and the --debug cost report.
//
// The price table is a static snapshot of published per-million-token
// rates. It is deliberately incomplete — enough models to cover the
// providers diting supports, plus a conservative default fallback for
// unknown models. Updating the table is a two-line code change; no user
// configuration is required.
//
// Phase 4.8 ships only the upfront estimate + post-hoc computation.
// Mid-run budget enforcement (abort between plan and answer if
// already over budget) is deferred — it would add complexity for
// limited benefit since the plan phase is the smaller of the two.
package pricing

import (
	"fmt"
	"strings"
)

// ModelPrice is the per-million-token price in USD for one model.
type ModelPrice struct {
	// InputPerMTok is the cost per 1,000,000 input (prompt) tokens.
	InputPerMTok float64
	// OutputPerMTok is the cost per 1,000,000 output (completion) tokens.
	OutputPerMTok float64
}

// priceTable maps model name prefixes to their prices. The lookup is a
// case-insensitive prefix match so naming variants like
// "claude-sonnet-4-20250514" resolve to the "claude-sonnet" entry.
//
// The table is a slice (not a map) so we preserve the order of
// prefix-match attempts: more specific prefixes must come first.
var priceTable = []struct {
	prefix string
	price  ModelPrice
}{
	// --- Anthropic Claude 4.6 series ---
	// Opus is the most expensive — must be checked before sonnet/haiku.
	{"claude-opus", ModelPrice{InputPerMTok: 15.00, OutputPerMTok: 75.00}},
	{"claude-sonnet", ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}},
	{"claude-haiku", ModelPrice{InputPerMTok: 0.80, OutputPerMTok: 4.00}},
	{"claude-", ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}}, // fallback: Sonnet-like

	// --- OpenAI GPT series ---
	// "gpt-5-mini" must come before "gpt-5".
	{"gpt-5-mini", ModelPrice{InputPerMTok: 0.40, OutputPerMTok: 1.60}},
	{"gpt-5", ModelPrice{InputPerMTok: 2.50, OutputPerMTok: 10.00}},
	{"gpt-4.1-mini", ModelPrice{InputPerMTok: 0.15, OutputPerMTok: 0.60}},
	{"gpt-4.1", ModelPrice{InputPerMTok: 2.00, OutputPerMTok: 8.00}},
	{"gpt-4o-mini", ModelPrice{InputPerMTok: 0.15, OutputPerMTok: 0.60}},
	{"gpt-4o", ModelPrice{InputPerMTok: 2.50, OutputPerMTok: 10.00}},
	{"gpt-", ModelPrice{InputPerMTok: 2.00, OutputPerMTok: 8.00}}, // fallback

	// --- MiniMax ---
	{"minimax-m2.7", ModelPrice{InputPerMTok: 0.30, OutputPerMTok: 0.60}},
	{"minimax-", ModelPrice{InputPerMTok: 0.30, OutputPerMTok: 0.60}},

	// --- Generic OpenAI-compatible fallbacks ---
	{"deepseek-", ModelPrice{InputPerMTok: 0.27, OutputPerMTok: 1.10}},
	{"llama-", ModelPrice{InputPerMTok: 0.20, OutputPerMTok: 0.60}},
	{"qwen", ModelPrice{InputPerMTok: 0.40, OutputPerMTok: 1.20}},
}

// DefaultPrice is the conservative fallback for models not found in the
// price table. It is deliberately on the higher end of the spectrum so
// budget checks err on the side of caution: an unknown model reports a
// higher estimate than it probably is, triggering abort sooner rather
// than later. Better to reject a cheap search you could afford than to
// accept an expensive one by accident.
var DefaultPrice = ModelPrice{
	InputPerMTok:  3.00,
	OutputPerMTok: 15.00,
}

// Lookup resolves a model name to its ModelPrice. Matching is
// case-insensitive prefix-based.
//
// Returns (price, true) if the model is in the price table, or
// (DefaultPrice, false) for an unknown model. Callers that need to
// warn the user about an unknown model should check the boolean.
func Lookup(model string) (ModelPrice, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return DefaultPrice, false
	}
	for _, entry := range priceTable {
		if strings.HasPrefix(normalized, entry.prefix) {
			return entry.price, true
		}
	}
	return DefaultPrice, false
}

// ComputeCost returns the USD cost of a request given token counts.
//
// Cache read tokens are billed at a discount (Anthropic charges 10%
// of the input price for cache reads; OpenAI charges 50%). We use
// 10% as the conservative default — underestimating the discount
// means overestimating the cost, which is the safe direction for a
// budget guard.
func ComputeCost(price ModelPrice, inputTokens, outputTokens, cacheReadTokens int) float64 {
	regularInput := inputTokens - cacheReadTokens
	if regularInput < 0 {
		regularInput = 0
	}
	inputCost := float64(regularInput) * price.InputPerMTok / 1_000_000
	cacheCost := float64(cacheReadTokens) * price.InputPerMTok * 0.10 / 1_000_000
	outputCost := float64(outputTokens) * price.OutputPerMTok / 1_000_000
	return inputCost + cacheCost + outputCost
}

// --- upfront estimation ----------------------------------------------------

// HeuristicTokens is the default token budget used by EstimateSearchCost
// for a search that hasn't run yet. The numbers reflect typical plan +
// answer phase usage observed in Phase 3 testing:
//
//   - Plan phase: system prompt (~2K) + user question (~100) +
//     JSON schema instructions (~500) ≈ 2.6K input; structured
//     plan output ≈ 600 tokens.
//
//   - Answer phase: system (~2K) + plan (~500) + formatted fetched
//     sources (~12K for 8 sources × 1.5K each) ≈ 14.5K input; the
//     answer itself with citations ≈ 1.5K output.
//
// Reasoning models (MiniMax M2.7, DeepSeek-R1) generate far more
// output due to <think> blocks — the numbers here are for
// non-reasoning models. The estimate is deliberately pessimistic
// for reasoning models, biasing toward early abort.
type HeuristicTokens struct {
	PlanInputTokens    int
	PlanOutputTokens   int
	AnswerInputTokens  int
	AnswerOutputTokens int
}

// DefaultHeuristics holds the token estimates used by EstimateSearchCost.
var DefaultHeuristics = HeuristicTokens{
	PlanInputTokens:    2600,
	PlanOutputTokens:   600,
	AnswerInputTokens:  14500,
	AnswerOutputTokens: 1500,
}

// Estimate is the structured result of a cost calculation.
type Estimate struct {
	Model            string
	Price            ModelPrice
	Known            bool
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	PlanCostUSD      float64
	AnswerCostUSD    float64
	TotalCostUSD     float64
}

// EstimateSearch runs the default heuristics against the price table
// and returns a full cost estimate for a search that has not yet run.
// `model` is matched case-insensitively; unknown models use DefaultPrice
// and set Known=false so the caller can warn the user.
func EstimateSearch(model string) *Estimate {
	return EstimateSearchWithHeuristics(model, DefaultHeuristics)
}

// EstimateSearchWithHeuristics is the test entry point for EstimateSearch.
// Production callers use EstimateSearch with the default heuristics.
func EstimateSearchWithHeuristics(model string, h HeuristicTokens) *Estimate {
	price, known := Lookup(model)
	planCost := ComputeCost(price, h.PlanInputTokens, h.PlanOutputTokens, 0)
	answerCost := ComputeCost(price, h.AnswerInputTokens, h.AnswerOutputTokens, 0)
	return &Estimate{
		Model:         model,
		Price:         price,
		Known:         known,
		InputTokens:   h.PlanInputTokens + h.AnswerInputTokens,
		OutputTokens:  h.PlanOutputTokens + h.AnswerOutputTokens,
		PlanCostUSD:   planCost,
		AnswerCostUSD: answerCost,
		TotalCostUSD:  planCost + answerCost,
	}
}

// --- formatting -------------------------------------------------------------

// FormatUSD renders a cost as "$0.0042" with 4 decimal places of
// precision — enough to see fractional-cent costs for cheap models.
func FormatUSD(usd float64) string {
	return fmt.Sprintf("$%.4f", usd)
}
