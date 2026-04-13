package main

import (
	"fmt"
	"io"

	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/pricing"
)

// enforceMaxCost runs the upfront cost estimate and returns a non-nil
// error if the estimate exceeds the user-supplied budget. The error
// message is formatted for direct printing to stderr.
//
// `w` receives informational output about the estimate (the breakdown
// line) on the success path. The function itself never exits — the
// caller decides whether to abort.
func enforceMaxCost(w io.Writer, model string, budget float64) error {
	est := pricing.EstimateSearch(model)

	if est.TotalCostUSD > budget {
		warn := ""
		if !est.Known {
			warn = fmt.Sprintf(" (model %q not in price table — using conservative default; set --model or fund higher to proceed)", model)
		}
		return fmt.Errorf(
			"error: estimated cost %s exceeds --max-cost %s%s\n"+
				"  plan  : %s (~%d in / %d out)\n"+
				"  answer: %s (~%d in / %d out)\n"+
				"  hint  : use a cheaper model (e.g. --model gpt-4.1-mini) or raise --max-cost",
			pricing.FormatUSD(est.TotalCostUSD),
			pricing.FormatUSD(budget),
			warn,
			pricing.FormatUSD(est.PlanCostUSD),
			pricing.DefaultHeuristics.PlanInputTokens,
			pricing.DefaultHeuristics.PlanOutputTokens,
			pricing.FormatUSD(est.AnswerCostUSD),
			pricing.DefaultHeuristics.AnswerInputTokens,
			pricing.DefaultHeuristics.AnswerOutputTokens,
		)
	}

	// Under budget — print a one-line info note so the user sees what
	// the guard decided.
	fmt.Fprintf(w, "diting: estimated cost %s (budget %s, model %s)\n",
		pricing.FormatUSD(est.TotalCostUSD),
		pricing.FormatUSD(budget),
		modelLabel(model, est.Known),
	)
	return nil
}

// modelLabel is a small helper for --max-cost messages: it shows the
// model name or "<provider default>" plus a question mark if the model
// is not in the price table.
func modelLabel(model string, known bool) string {
	if model == "" {
		return "<provider default>"
	}
	if !known {
		return model + " (unknown, using default price)"
	}
	return model
}

// actualCost computes the post-hoc cost of a completed pipeline run
// using the same pricing table. Returns (plan, answer, total) in USD.
//
// Used by the --debug output to show what the search actually cost
// versus what was estimated.
func actualCost(model string, debug pipeline.DebugInfo) (planUSD, answerUSD, totalUSD float64) {
	price, _ := pricing.Lookup(model)
	planUSD = pricing.ComputeCost(price,
		debug.PlanInputTokens,
		debug.PlanOutputTokens,
		debug.PlanCacheReadTokens,
	)
	answerUSD = pricing.ComputeCost(price,
		debug.AnswerInputTokens,
		debug.AnswerOutputTokens,
		debug.AnswerCacheReadTokens,
	)
	return planUSD, answerUSD, planUSD + answerUSD
}
