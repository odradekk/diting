package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/pricing"
)

// --- reorderSearchArgs ------------------------------------------------------

func TestReorderSearchArgs_PreservesFlagValuePairs(t *testing.T) {
	// Regression: in Phase 4.8 the naive reorder split `--max-cost 1.00`
	// into `[--max-cost, ..., 1.00]`, causing flag.Parse to treat the
	// next token as --max-cost's value. The pairwise reorder must keep
	// `--flag value` tuples adjacent.
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flag=value and positional",
			in:   []string{"--format=json", "my question"},
			want: []string{"--format=json", "my question"},
		},
		{
			name: "flag value pair before positional",
			in:   []string{"--max-cost", "1.00", "my question"},
			want: []string{"--max-cost", "1.00", "my question"},
		},
		{
			name: "positional then flag value pair",
			in:   []string{"my question", "--max-cost", "1.00"},
			want: []string{"--max-cost", "1.00", "my question"},
		},
		{
			name: "bool flag between positional and value flag",
			in:   []string{"my question", "--plan-only", "--max-cost", "1.00"},
			want: []string{"--plan-only", "--max-cost", "1.00", "my question"},
		},
		{
			name: "bool flag stays standalone",
			in:   []string{"query", "--json"},
			want: []string{"--json", "query"},
		},
		{
			name: "interleaved value flags and positional",
			in:   []string{"--max-cost", "0.5", "query", "--provider", "openai"},
			want: []string{"--max-cost", "0.5", "--provider", "openai", "query"},
		},
		{
			name: "multi-word positional",
			in:   []string{"--debug", "how does Go channels work"},
			want: []string{"--debug", "how does Go channels work"},
		},
		{
			name: "value flag at end with no value — passed through for flag.Parse to error",
			in:   []string{"query", "--max-cost"},
			want: []string{"--max-cost", "query"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderSearchArgs(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d\ngot:  %v\nwant: %v",
					len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] got %q, want %q\nfull: %v vs %v",
						i, got[i], tt.want[i], got, tt.want)
				}
			}
		})
	}
}

func TestReorderFetchArgs_PreservesFlagValuePairs(t *testing.T) {
	// Same regression check for the fetch subcommand.
	in := []string{"https://example.com", "--timeout", "30s", "--json"}
	got := reorderFetchArgs(in)
	// Expect: [--timeout, 30s, --json, https://example.com]
	want := []string{"--timeout", "30s", "--json", "https://example.com"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

// --- enforceMaxCost ---------------------------------------------------------

func TestEnforceMaxCost_UnderBudget(t *testing.T) {
	// Cheap model, generous budget → no error, prints info note.
	var buf bytes.Buffer
	err := enforceMaxCost(&buf, "gpt-4.1-mini", 1.00)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "estimated cost") {
		t.Errorf("missing estimate in info note:\n%s", out)
	}
	if !strings.Contains(out, "gpt-4.1-mini") {
		t.Errorf("missing model label:\n%s", out)
	}
}

func TestEnforceMaxCost_OverBudget_Expensive(t *testing.T) {
	// Claude Opus, tiny budget → should abort with a detailed message.
	var buf bytes.Buffer
	err := enforceMaxCost(&buf, "claude-opus-4", 0.01)
	if err == nil {
		t.Fatal("expected error: opus should exceed a penny budget")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "exceeds --max-cost") {
		t.Errorf("error should say 'exceeds --max-cost': %v", err)
	}
	if !strings.Contains(errMsg, "$0.0100") {
		t.Errorf("error should show the budget: %v", err)
	}
	// Breakdown lines present.
	if !strings.Contains(errMsg, "plan") {
		t.Errorf("error missing plan breakdown: %v", err)
	}
	if !strings.Contains(errMsg, "answer") {
		t.Errorf("error missing answer breakdown: %v", err)
	}
	// Hint mentions a cheaper alternative.
	if !strings.Contains(errMsg, "gpt-4.1-mini") {
		t.Errorf("error should suggest cheaper model: %v", err)
	}
}

func TestEnforceMaxCost_UnknownModelWarning(t *testing.T) {
	// Unknown model uses DefaultPrice (pessimistic) — if the budget is
	// too low the error message should call out that the model is
	// unknown so the user understands the estimate is conservative.
	var buf bytes.Buffer
	err := enforceMaxCost(&buf, "some-random-model", 0.001)
	if err == nil {
		t.Fatal("expected over-budget error")
	}
	if !strings.Contains(err.Error(), "not in price table") {
		t.Errorf("error should warn about unknown model: %v", err)
	}
}

func TestEnforceMaxCost_EmptyModelBudgetUnderRun(t *testing.T) {
	// Empty model = provider default. EstimateSearch falls through to
	// DefaultPrice but marks Known=false — with a generous budget it
	// should still pass.
	var buf bytes.Buffer
	err := enforceMaxCost(&buf, "", 10.00)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "<provider default>") {
		t.Errorf("empty model should render as '<provider default>':\n%s", buf.String())
	}
}

func TestEnforceMaxCost_EstimateMatchesPricingPackage(t *testing.T) {
	// Sanity: the estimate enforceMaxCost uses must be identical to
	// what pricing.EstimateSearch returns. Protects against some
	// future refactor accidentally passing different heuristics.
	want := pricing.EstimateSearch("claude-sonnet-4")
	// Budget is exactly at the estimated total — must NOT error.
	var buf bytes.Buffer
	if err := enforceMaxCost(&buf, "claude-sonnet-4", want.TotalCostUSD+0.0001); err != nil {
		t.Errorf("at-estimate budget should pass: %v", err)
	}
	// Budget 1 cent below the estimate → must error.
	if err := enforceMaxCost(&buf, "claude-sonnet-4", want.TotalCostUSD-0.01); err == nil {
		t.Error("below-estimate budget should fail")
	}
}

// --- modelLabel -------------------------------------------------------------

func TestModelLabel(t *testing.T) {
	tests := []struct {
		model string
		known bool
		want  string
	}{
		{"", false, "<provider default>"},
		{"", true, "<provider default>"},
		{"claude-sonnet-4", true, "claude-sonnet-4"},
		{"weird-model-xyz", false, "weird-model-xyz (unknown, using default price)"},
	}
	for _, tt := range tests {
		if got := modelLabel(tt.model, tt.known); got != tt.want {
			t.Errorf("modelLabel(%q, %v) = %q, want %q", tt.model, tt.known, got, tt.want)
		}
	}
}

// --- actualCost -------------------------------------------------------------

func TestActualCost_SplitsPlanAndAnswer(t *testing.T) {
	debug := pipeline.DebugInfo{
		PlanInputTokens:    1000,
		PlanOutputTokens:   500,
		AnswerInputTokens:  8000,
		AnswerOutputTokens: 1000,
	}

	planUSD, answerUSD, totalUSD := actualCost("claude-sonnet-4", debug)

	if planUSD <= 0 {
		t.Errorf("plan cost = %v, want > 0", planUSD)
	}
	if answerUSD <= 0 {
		t.Errorf("answer cost = %v, want > 0", answerUSD)
	}
	// Answer must be more expensive than plan (more tokens).
	if answerUSD <= planUSD {
		t.Errorf("answer cost %v should exceed plan cost %v", answerUSD, planUSD)
	}
	// Sum matches total.
	if delta := totalUSD - (planUSD + answerUSD); delta > 0.00001 || delta < -0.00001 {
		t.Errorf("sum mismatch: plan+answer=%v total=%v", planUSD+answerUSD, totalUSD)
	}
}

func TestActualCost_Zero(t *testing.T) {
	debug := pipeline.DebugInfo{} // all zeros
	_, _, totalUSD := actualCost("claude-sonnet-4", debug)
	if totalUSD != 0 {
		t.Errorf("zero tokens should give zero cost, got %v", totalUSD)
	}
}

func TestActualCost_UnknownModelFallsBack(t *testing.T) {
	// Unknown model should not panic — it should use DefaultPrice.
	debug := pipeline.DebugInfo{
		PlanInputTokens:  1000,
		PlanOutputTokens: 100,
	}
	_, _, totalUSD := actualCost("unknown-xyz", debug)
	if totalUSD <= 0 {
		t.Errorf("unknown model should still compute a positive cost, got %v", totalUSD)
	}
}

// --- integration: debug output includes cost line --------------------------

func TestPrintSearchText_Debug_IncludesCostLine(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchText(&buf, fullResult(), true, renderOptions{Model: "claude-sonnet-4"}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Cost:") {
		t.Errorf("debug output missing Cost line:\n%s", out)
	}
	if !strings.Contains(out, "claude-sonnet-4") {
		t.Errorf("cost line should mention the model:\n%s", out)
	}
}

func TestPrintSearchText_Debug_NoCostLineWhenModelEmpty(t *testing.T) {
	// No model → no cost line (can't compute meaningfully).
	var buf bytes.Buffer
	if err := printSearchText(&buf, fullResult(), true, renderOptions{Model: ""}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	if strings.Contains(buf.String(), "Cost:") {
		t.Errorf("cost line should be hidden when model is empty:\n%s", buf.String())
	}
}

func TestPrintSearchMarkdown_Debug_IncludesCostLine(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchMarkdown(&buf, fullResult(), true, renderOptions{Model: "gpt-4.1-mini"}); err != nil {
		t.Fatalf("printSearchMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "**Cost**:") {
		t.Errorf("debug markdown missing bold Cost bullet:\n%s", out)
	}
	if !strings.Contains(out, "`gpt-4.1-mini`") {
		t.Errorf("cost line should mention the model in code spans:\n%s", out)
	}
}

func TestPrintSearchText_Debug_NoCostLineForZeroTokens(t *testing.T) {
	// A plan-only result has zero answer tokens but may also have
	// zero plan tokens in fixtures where we didn't populate them —
	// the cost line should still be suppressed in that case.
	r := planOnlyResult()
	r.Debug.PlanInputTokens = 0
	r.Debug.PlanOutputTokens = 0
	r.Debug.PlanCacheReadTokens = 0

	var buf bytes.Buffer
	if err := printSearchText(&buf, r, true, renderOptions{Model: "claude-sonnet-4"}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	if strings.Contains(buf.String(), "Cost:") {
		t.Errorf("zero-token run should not print Cost line:\n%s", buf.String())
	}
}
