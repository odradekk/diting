package pricing

import (
	"math"
	"strings"
	"testing"
)

// --- Lookup -----------------------------------------------------------------

func TestLookup_KnownModels(t *testing.T) {
	tests := []struct {
		model         string
		wantInput     float64
		wantOutput    float64
		wantKnown     bool
	}{
		// Anthropic
		{"claude-sonnet-4-20250514", 3.00, 15.00, true},
		{"claude-opus-4-20250514", 15.00, 75.00, true},
		{"claude-haiku-4-5", 0.80, 4.00, true},
		{"claude-3-5-sonnet", 3.00, 15.00, true}, // hits generic "claude-" fallback

		// OpenAI
		{"gpt-4.1-mini", 0.15, 0.60, true},
		{"gpt-4.1", 2.00, 8.00, true},
		{"gpt-5-mini-2025", 0.40, 1.60, true},
		{"gpt-5", 2.50, 10.00, true},
		{"gpt-4o", 2.50, 10.00, true},
		{"gpt-4o-mini", 0.15, 0.60, true},

		// MiniMax (OpenAI-compatible)
		{"MiniMax-M2.7-highspeed", 0.30, 0.60, true},
		{"minimax-m2.7-highspeed", 0.30, 0.60, true}, // case-insensitive
		{"MiniMax-M2.7", 0.30, 0.60, true},

		// Community
		{"deepseek-chat", 0.27, 1.10, true},
		{"qwen2.5-72b", 0.40, 1.20, true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			price, known := Lookup(tt.model)
			if known != tt.wantKnown {
				t.Errorf("known = %v, want %v", known, tt.wantKnown)
			}
			if price.InputPerMTok != tt.wantInput {
				t.Errorf("input = %v, want %v", price.InputPerMTok, tt.wantInput)
			}
			if price.OutputPerMTok != tt.wantOutput {
				t.Errorf("output = %v, want %v", price.OutputPerMTok, tt.wantOutput)
			}
		})
	}
}

func TestLookup_UnknownModels(t *testing.T) {
	tests := []string{
		"",
		"some-random-model",
		"bert-base",
		"llm-2099",
	}
	for _, model := range tests {
		price, known := Lookup(model)
		if known {
			t.Errorf("%q should be unknown, got known=true", model)
		}
		if price != DefaultPrice {
			t.Errorf("%q should return DefaultPrice, got %+v", model, price)
		}
	}
}

func TestLookup_PrefixSpecificityOrder(t *testing.T) {
	// Critical: "claude-opus" must win over "claude-" for models starting
	// with "claude-opus-...". If we accidentally reordered the table,
	// opus would fall through to sonnet pricing (which is cheaper) and
	// the budget guard would underestimate — the dangerous direction.
	price, known := Lookup("claude-opus-4-20250514")
	if !known {
		t.Fatal("claude-opus should be known")
	}
	if price.InputPerMTok != 15.00 {
		t.Errorf("claude-opus resolved to wrong tier: got %v, want 15.00", price.InputPerMTok)
	}

	// Same for gpt-5-mini vs gpt-5.
	p1, _ := Lookup("gpt-5-mini")
	p2, _ := Lookup("gpt-5-medium-something")
	if p1.InputPerMTok != 0.40 {
		t.Errorf("gpt-5-mini wrong: %v", p1.InputPerMTok)
	}
	if p2.InputPerMTok != 2.50 {
		t.Errorf("gpt-5-medium-something should fall through to gpt-5: %v", p2.InputPerMTok)
	}
}

func TestLookup_WhitespaceTolerance(t *testing.T) {
	price, known := Lookup("  claude-sonnet-4  ")
	if !known {
		t.Error("trimmed should still match")
	}
	if price.InputPerMTok != 3.00 {
		t.Errorf("trim failed: %v", price.InputPerMTok)
	}
}

// --- ComputeCost ------------------------------------------------------------

func TestComputeCost_Simple(t *testing.T) {
	// 1M input @ $3 + 1M output @ $15 = $18.00
	p := ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}
	got := ComputeCost(p, 1_000_000, 1_000_000, 0)
	if math.Abs(got-18.00) > 0.0001 {
		t.Errorf("got %v, want 18.00", got)
	}
}

func TestComputeCost_Small(t *testing.T) {
	// 1000 input @ $3/MTok = $0.003
	// 1000 output @ $15/MTok = $0.015
	// total = $0.018
	p := ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}
	got := ComputeCost(p, 1000, 1000, 0)
	if math.Abs(got-0.018) > 0.00001 {
		t.Errorf("got %v, want 0.018", got)
	}
}

func TestComputeCost_WithCacheDiscount(t *testing.T) {
	// 10,000 total input, 8000 cache read, 2000 fresh
	// fresh input: 2000 * $3/MTok = $0.006
	// cache read: 8000 * $3 * 0.10 / MTok = $0.0024
	// output: 1000 * $15/MTok = $0.015
	// total = $0.0234
	p := ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}
	got := ComputeCost(p, 10000, 1000, 8000)
	want := 0.006 + 0.0024 + 0.015
	if math.Abs(got-want) > 0.0001 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeCost_CacheReadExceedsInput(t *testing.T) {
	// Defensive: if cache read > total input (shouldn't happen in
	// practice), don't go negative.
	p := ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}
	got := ComputeCost(p, 100, 0, 1000)
	// fresh input clamps to 0, cache cost = 1000 * 3 * 0.10 / 1M = 0.0003
	if got < 0 {
		t.Errorf("negative cost: %v", got)
	}
}

func TestComputeCost_Zero(t *testing.T) {
	p := ModelPrice{InputPerMTok: 3.00, OutputPerMTok: 15.00}
	if got := ComputeCost(p, 0, 0, 0); got != 0 {
		t.Errorf("zero tokens should cost 0, got %v", got)
	}
}

// --- EstimateSearch ---------------------------------------------------------

func TestEstimateSearch_Sonnet(t *testing.T) {
	est := EstimateSearch("claude-sonnet-4")

	if !est.Known {
		t.Error("claude-sonnet-4 should be known")
	}
	if est.InputTokens != DefaultHeuristics.PlanInputTokens+DefaultHeuristics.AnswerInputTokens {
		t.Errorf("input tokens = %d, want %d", est.InputTokens,
			DefaultHeuristics.PlanInputTokens+DefaultHeuristics.AnswerInputTokens)
	}
	// Total should be non-zero and roughly the sum of plan + answer.
	if est.PlanCostUSD <= 0 || est.AnswerCostUSD <= 0 {
		t.Errorf("plan=%v answer=%v", est.PlanCostUSD, est.AnswerCostUSD)
	}
	sum := est.PlanCostUSD + est.AnswerCostUSD
	if math.Abs(sum-est.TotalCostUSD) > 0.0001 {
		t.Errorf("sum mismatch: plan+answer=%v total=%v", sum, est.TotalCostUSD)
	}
	// Sanity: claude-sonnet search should cost somewhere in the cents range.
	if est.TotalCostUSD < 0.001 || est.TotalCostUSD > 1.00 {
		t.Errorf("sonnet estimate out of plausible range: %v", est.TotalCostUSD)
	}
}

func TestEstimateSearch_Cheap(t *testing.T) {
	// gpt-4.1-mini is very cheap — estimate should be under 1 cent.
	est := EstimateSearch("gpt-4.1-mini")
	if est.TotalCostUSD > 0.01 {
		t.Errorf("gpt-4.1-mini estimate unreasonably high: %v", est.TotalCostUSD)
	}
}

func TestEstimateSearch_Unknown(t *testing.T) {
	est := EstimateSearch("unknown-model-xyz")
	if est.Known {
		t.Error("should be Known=false")
	}
	if est.Price != DefaultPrice {
		t.Error("should use DefaultPrice")
	}
	// Still produces a non-zero estimate.
	if est.TotalCostUSD <= 0 {
		t.Errorf("unknown-model estimate = %v, want > 0", est.TotalCostUSD)
	}
}

func TestEstimateSearch_UnknownMorePessimistic(t *testing.T) {
	// The unknown-model fallback MUST be at least as expensive as the
	// cheapest known model, so the guard errs on the side of caution.
	unknown := EstimateSearch("some-random-model")
	cheap := EstimateSearch("gpt-4.1-mini")
	if unknown.TotalCostUSD <= cheap.TotalCostUSD {
		t.Errorf("unknown (%v) should be at least as pessimistic as cheapest known (%v)",
			unknown.TotalCostUSD, cheap.TotalCostUSD)
	}
}

func TestEstimateSearch_OpusMostExpensive(t *testing.T) {
	// Opus should be the most expensive model in the table.
	opus := EstimateSearch("claude-opus-4")
	for _, cheaper := range []string{"claude-sonnet-4", "gpt-4.1", "minimax-m2.7"} {
		est := EstimateSearch(cheaper)
		if est.TotalCostUSD >= opus.TotalCostUSD {
			t.Errorf("%s (%v) should be cheaper than opus (%v)",
				cheaper, est.TotalCostUSD, opus.TotalCostUSD)
		}
	}
}

func TestEstimateSearchWithHeuristics_Custom(t *testing.T) {
	h := HeuristicTokens{
		PlanInputTokens:    1000,
		PlanOutputTokens:   100,
		AnswerInputTokens:  5000,
		AnswerOutputTokens: 500,
	}
	est := EstimateSearchWithHeuristics("claude-sonnet-4", h)
	if est.InputTokens != 6000 {
		t.Errorf("input tokens = %d, want 6000", est.InputTokens)
	}
	if est.OutputTokens != 600 {
		t.Errorf("output tokens = %d, want 600", est.OutputTokens)
	}
}

// --- FormatUSD --------------------------------------------------------------

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "$0.0000"},
		{0.0042, "$0.0042"},
		{0.1234, "$0.1234"},
		{1.5, "$1.5000"},
		{1234.5678, "$1234.5678"},
	}
	for _, tt := range tests {
		if got := FormatUSD(tt.in); got != tt.want {
			t.Errorf("FormatUSD(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- regression: price-table sanity ----------------------------------------

// TestPriceTableSanity verifies the price-table invariants: no negative
// prices, every input price <= its output price (a truism for every
// current LLM billing model), and no duplicate prefixes.
func TestPriceTableSanity(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range priceTable {
		if entry.price.InputPerMTok < 0 || entry.price.OutputPerMTok < 0 {
			t.Errorf("negative price for %q: %+v", entry.prefix, entry.price)
		}
		if entry.price.InputPerMTok > entry.price.OutputPerMTok {
			t.Errorf("%q: input price (%v) > output price (%v) — suspicious",
				entry.prefix, entry.price.InputPerMTok, entry.price.OutputPerMTok)
		}
		if seen[entry.prefix] {
			t.Errorf("duplicate prefix in price table: %q", entry.prefix)
		}
		seen[entry.prefix] = true
	}
	if DefaultPrice.InputPerMTok <= 0 || DefaultPrice.OutputPerMTok <= 0 {
		t.Error("DefaultPrice must be positive")
	}
}

func TestPriceTable_OrderingProtection(t *testing.T) {
	// More-specific prefixes must appear before their less-specific
	// parents so prefix-match resolution hits the right entry.
	// Example: "claude-opus" must appear before "claude-".
	idx := func(prefix string) int {
		for i, e := range priceTable {
			if e.prefix == prefix {
				return i
			}
		}
		return -1
	}
	pairs := [][2]string{
		{"claude-opus", "claude-"},
		{"claude-sonnet", "claude-"},
		{"claude-haiku", "claude-"},
		{"gpt-5-mini", "gpt-5"},
		{"gpt-4.1-mini", "gpt-4.1"},
		{"gpt-4o-mini", "gpt-4o"},
	}
	for _, p := range pairs {
		specific, general := idx(p[0]), idx(p[1])
		if specific == -1 {
			t.Errorf("missing specific prefix %q", p[0])
			continue
		}
		if general == -1 {
			t.Errorf("missing general prefix %q", p[1])
			continue
		}
		if specific >= general {
			t.Errorf("%q (at %d) must come before %q (at %d) in price table",
				p[0], specific, p[1], general)
		}
	}
}

// TestLookupCaseInsensitivity guards against the trimmed/normalized
// lookup regressing to case-sensitive behaviour.
func TestLookupCaseInsensitivity(t *testing.T) {
	variants := []string{
		"CLAUDE-SONNET-4",
		"Claude-Sonnet-4",
		"cLaUdE-sOnNeT-4",
	}
	for _, v := range variants {
		p, known := Lookup(v)
		if !known {
			t.Errorf("%q should be known", v)
		}
		if p.InputPerMTok != 3.00 {
			t.Errorf("%q resolved wrong: %v", v, p.InputPerMTok)
		}
	}
}

func TestEstimate_SanityPrintable(t *testing.T) {
	// Integration: estimate + format should produce a printable string.
	est := EstimateSearch("claude-sonnet-4")
	s := FormatUSD(est.TotalCostUSD)
	if !strings.HasPrefix(s, "$") {
		t.Errorf("formatted cost should start with $, got %q", s)
	}
}
