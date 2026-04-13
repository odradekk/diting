package v2shared

import (
	"errors"
	"testing"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/search"
)

// --- extractDomain ----------------------------------------------------------

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"https://go.dev/doc/effective_go", "go.dev"},
		{"https://www.python.org/doc", "python.org"},
		{"http://WWW.Example.COM/foo", "WWW.Example.COM/foo"}, // no parse failure, but no lowercasing
		{"not a url", "not a url"},
		{"https://stackoverflow.com/q/123", "stackoverflow.com"},
		{"https://docs.python.org/3/library/", "docs.python.org"},
	}
	for _, tt := range tests {
		got := extractDomain(tt.in)
		// For the weird "http://WWW.Example.COM" case, url.Parse will
		// succeed and give us a non-lowercased host — our function
		// doesn't lowercase (v0baseline.extractDomain does). Both are
		// valid behaviours given where they're used.
		if tt.in == "http://WWW.Example.COM/foo" {
			if got != "WWW.Example.COM" {
				t.Errorf("extractDomain(%q) = %q, want WWW.Example.COM", tt.in, got)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("extractDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- ConvertPipelineResult: full-answer mode -------------------------------

func TestConvertPipelineResult_FullAnswer(t *testing.T) {
	r := &pipeline.Result{
		Question: "How do Go channels work?",
		Plan: pipeline.Plan{
			QueriesBySourceType: map[search.SourceType][]string{
				"docs":        {"go channels"},
				"community":   {"buffered channel"},
				"general_web": {"go channel tutorial"},
			},
		},
		Answer: pipeline.Answer{
			Text:       "Go channels are typed conduits [1][2].",
			Confidence: "high",
			Citations: []pipeline.Citation{
				{ID: 1, URL: "https://go.dev/tour/concurrency/2", Title: "Tour", SourceType: "docs"},
				{ID: 2, URL: "https://gobyexample.com/channel-buffering", Title: "By Example", SourceType: "community"},
			},
		},
		Debug: pipeline.DebugInfo{
			PlanInputTokens:    1200,
			PlanOutputTokens:   400,
			AnswerInputTokens:  8000,
			AnswerOutputTokens: 500,
			TotalSearchResults: 40,
			SelectedSources:    12,
			FetchedSources:     10,
		},
	}

	out := ConvertPipelineResult("et_001", r, 1234*time.Millisecond, "claude-sonnet-4")

	if out.QueryID != "et_001" {
		t.Errorf("QueryID = %q", out.QueryID)
	}
	if out.Answer != "Go channels are typed conduits [1][2]." {
		t.Errorf("Answer = %q", out.Answer)
	}
	if out.Latency != 1234*time.Millisecond {
		t.Errorf("Latency = %v", out.Latency)
	}
	if out.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0 (sonnet has real pricing)", out.Cost)
	}

	// Citations: pulled from Answer.Citations, 2 entries.
	if len(out.Citations) != 2 {
		t.Fatalf("len(Citations) = %d, want 2", len(out.Citations))
	}
	c0 := out.Citations[0]
	if c0.URL != "https://go.dev/tour/concurrency/2" {
		t.Errorf("c0.URL = %q", c0.URL)
	}
	if c0.Domain != "go.dev" {
		t.Errorf("c0.Domain = %q, want go.dev", c0.Domain)
	}
	if c0.SourceType != bench.SourceDocs {
		t.Errorf("c0.SourceType = %q, want docs", c0.SourceType)
	}
	if c0.Rank != 1 {
		t.Errorf("c0.Rank = %d, want 1 (from Citation.ID)", c0.Rank)
	}
	c1 := out.Citations[1]
	if c1.SourceType != bench.SourceCommunity {
		t.Errorf("c1.SourceType = %q, want community", c1.SourceType)
	}
	if c1.Domain != "gobyexample.com" {
		t.Errorf("c1.Domain = %q", c1.Domain)
	}

	// Metadata: token counts, confidence, plan queries.
	if out.Metadata["plan_queries"] != 3 {
		t.Errorf("plan_queries = %v, want 3", out.Metadata["plan_queries"])
	}
	if out.Metadata["plan_input_tokens"] != 1200 {
		t.Errorf("plan_input_tokens = %v", out.Metadata["plan_input_tokens"])
	}
	if out.Metadata["answer_input_tokens"] != 8000 {
		t.Errorf("answer_input_tokens = %v", out.Metadata["answer_input_tokens"])
	}
	if out.Metadata["confidence"] != "high" {
		t.Errorf("confidence = %v", out.Metadata["confidence"])
	}
	if out.Metadata["fetched_sources"] != 10 {
		t.Errorf("fetched_sources = %v", out.Metadata["fetched_sources"])
	}
}

// --- ConvertPipelineResult: full-answer mode citation merge (R2.2) ---------
//
// Guards Phase 5.7 Round 2.2: full-answer mode now MERGES the LLM's
// chosen citations with the full fetched-sources list, so the scorer's
// domain_hit metric sees both. The LLM's citations keep ranks 1..N to
// preserve inline [N] references in the answer text; uncited fetched
// sources are appended at ranks N+1..N+M.

func TestConvertPipelineResult_FullAnswer_MergesFetchedSources(t *testing.T) {
	r := &pipeline.Result{
		Answer: pipeline.Answer{
			Text:       "Answer references [1] and [2].",
			Confidence: "high",
			Citations: []pipeline.Citation{
				{ID: 1, URL: "https://go.dev/doc", Title: "Go Docs", SourceType: "docs"},
				{ID: 2, URL: "https://gobyexample.com/x", Title: "Example", SourceType: "docs"},
			},
		},
		Sources: []pipeline.FetchedSource{
			// First source IS already cited by the LLM — should be deduped.
			{
				ID: 1,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						URL:        "https://go.dev/doc",
						SourceType: "docs",
					},
				},
			},
			// Second source NOT cited — should be appended.
			{
				ID: 2,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						URL:        "https://stackoverflow.com/q/1",
						SourceType: "community",
					},
				},
			},
			// Third source NOT cited — should be appended.
			{
				ID: 3,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						URL:        "https://github.com/golang/go",
						SourceType: "code",
					},
				},
			},
		},
		Debug: pipeline.DebugInfo{
			PlanInputTokens:    100,
			PlanOutputTokens:   50,
			AnswerInputTokens:  500,
			AnswerOutputTokens: 200,
		},
	}

	out := ConvertPipelineResult("test", r, time.Second, "claude-sonnet-4")

	// Expect 4 citations: 2 LLM-cited + 2 unique fetched (the duplicate
	// go.dev/doc was deduped).
	if len(out.Citations) != 4 {
		t.Fatalf("len(Citations) = %d, want 4 (2 LLM + 2 unique fetched)", len(out.Citations))
	}

	// Ranks 1, 2 are the LLM citations preserved.
	if out.Citations[0].URL != "https://go.dev/doc" || out.Citations[0].Rank != 1 {
		t.Errorf("Citations[0] = %+v, want go.dev/doc rank 1", out.Citations[0])
	}
	if out.Citations[1].URL != "https://gobyexample.com/x" || out.Citations[1].Rank != 2 {
		t.Errorf("Citations[1] = %+v, want gobyexample rank 2", out.Citations[1])
	}

	// Ranks 3, 4 are the appended uncited fetched sources.
	if out.Citations[2].Rank != 3 {
		t.Errorf("Citations[2].Rank = %d, want 3", out.Citations[2].Rank)
	}
	if out.Citations[2].Domain != "stackoverflow.com" {
		t.Errorf("Citations[2].Domain = %q, want stackoverflow.com", out.Citations[2].Domain)
	}
	if out.Citations[2].SourceType != bench.SourceCommunity {
		t.Errorf("Citations[2].SourceType = %q, want community", out.Citations[2].SourceType)
	}
	if out.Citations[3].Rank != 4 {
		t.Errorf("Citations[3].Rank = %d, want 4", out.Citations[3].Rank)
	}
	if out.Citations[3].SourceType != bench.SourceCode {
		t.Errorf("Citations[3].SourceType = %q, want code", out.Citations[3].SourceType)
	}

	// Metadata exposes both counts so reports can show LLM-cited vs total.
	if out.Metadata["llm_cited_count"] != 2 {
		t.Errorf("llm_cited_count = %v, want 2", out.Metadata["llm_cited_count"])
	}
	if out.Metadata["citation_count"] != 4 {
		t.Errorf("citation_count = %v, want 4", out.Metadata["citation_count"])
	}
}

func TestConvertPipelineResult_FullAnswer_NoFetchedSources(t *testing.T) {
	// When Sources is empty (e.g., test fixture with only Answer
	// populated), the merge is a no-op and we get just the LLM
	// citations. Guards backward compatibility with the original
	// pre-R2.2 behaviour.
	r := &pipeline.Result{
		Answer: pipeline.Answer{
			Text:       "x",
			Confidence: "high",
			Citations: []pipeline.Citation{
				{ID: 1, URL: "https://a.example", SourceType: "docs"},
				{ID: 2, URL: "https://b.example", SourceType: "docs"},
			},
		},
		Sources: nil,
	}

	out := ConvertPipelineResult("t", r, time.Second, "")
	if len(out.Citations) != 2 {
		t.Errorf("len(Citations) = %d, want 2 (LLM only)", len(out.Citations))
	}
	if out.Metadata["llm_cited_count"] != 2 {
		t.Errorf("llm_cited_count = %v", out.Metadata["llm_cited_count"])
	}
}

func TestMergeAnswerAndSourceCitations_PreservesNonContiguousRanks(t *testing.T) {
	// LLM citations may have non-1-based or non-contiguous ranks
	// (e.g., the model picked sources [1], [3], [5] from a 10-source
	// pool). The merge must use highestRank+1 as the starting rank
	// for appended fetched sources, NOT len(llmCited)+1.
	llmCited := []pipeline.Citation{
		{ID: 1, URL: "https://a.example", SourceType: "docs"},
		{ID: 5, URL: "https://e.example", SourceType: "docs"},
	}
	fetched := []pipeline.FetchedSource{
		{
			ID: 1,
			Result: pipeline.ScoredResult{
				SearchResult: search.SearchResult{URL: "https://x.example", SourceType: "code"},
			},
		},
	}

	merged := mergeAnswerAndSourceCitations(llmCited, fetched)
	if len(merged) != 3 {
		t.Fatalf("len = %d, want 3", len(merged))
	}
	// Appended source must take rank 6 (highestRank=5, next=6).
	if merged[2].Rank != 6 {
		t.Errorf("appended Rank = %d, want 6 (highestRank=5+1)", merged[2].Rank)
	}
}

func TestHighestRank(t *testing.T) {
	tests := []struct {
		name string
		in   []pipeline.Citation
		want int
	}{
		{"empty", nil, 0},
		{"single rank 1", []pipeline.Citation{{ID: 1}}, 1},
		{"contiguous 1..3", []pipeline.Citation{{ID: 1}, {ID: 2}, {ID: 3}}, 3},
		{"out of order", []pipeline.Citation{{ID: 5}, {ID: 2}, {ID: 8}, {ID: 1}}, 8},
		{"all zero", []pipeline.Citation{{ID: 0}, {ID: 0}}, 0},
	}
	for _, tt := range tests {
		if got := highestRank(tt.in); got != tt.want {
			t.Errorf("%s: highestRank = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// --- ConvertPipelineResult: raw mode ---------------------------------------

func TestConvertPipelineResult_RawMode(t *testing.T) {
	// Raw mode: no Answer.Text, no Answer.Citations, but Sources is
	// populated. Citations should come from Sources.
	r := &pipeline.Result{
		Question: "How do Go channels work?",
		Plan: pipeline.Plan{
			QueriesBySourceType: map[search.SourceType][]string{
				"docs": {"go channels"},
			},
		},
		Answer: pipeline.Answer{}, // empty
		Sources: []pipeline.FetchedSource{
			{
				ID: 1,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						URL:        "https://go.dev/ref/spec",
						SourceType: "docs",
					},
				},
			},
			{
				ID: 2,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						URL:        "https://stackoverflow.com/q/999",
						SourceType: "community",
					},
				},
			},
		},
		Debug: pipeline.DebugInfo{
			PlanInputTokens:  1000,
			PlanOutputTokens: 300,
			// No answer tokens — raw mode didn't call the answer phase.
			TotalSearchResults: 20,
			SelectedSources:    5,
			FetchedSources:     2,
		},
	}

	out := ConvertPipelineResult("et_001", r, 2*time.Second, "claude-sonnet-4")

	if out.Answer != "" {
		t.Errorf("Answer = %q, want empty (raw mode)", out.Answer)
	}
	if len(out.Citations) != 2 {
		t.Fatalf("len(Citations) = %d, want 2", len(out.Citations))
	}
	if out.Citations[0].URL != "https://go.dev/ref/spec" {
		t.Errorf("c0.URL = %q", out.Citations[0].URL)
	}
	if out.Citations[0].Domain != "go.dev" {
		t.Errorf("c0.Domain = %q", out.Citations[0].Domain)
	}
	if out.Citations[0].SourceType != bench.SourceDocs {
		t.Errorf("c0.SourceType = %q", out.Citations[0].SourceType)
	}
	if out.Citations[0].Rank != 1 {
		t.Errorf("c0.Rank = %d, want 1 (from FetchedSource.ID)", out.Citations[0].Rank)
	}
	if out.Citations[1].SourceType != bench.SourceCommunity {
		t.Errorf("c1.SourceType = %q", out.Citations[1].SourceType)
	}

	// Metadata must NOT include answer_input_tokens (they're zero).
	if _, ok := out.Metadata["answer_input_tokens"]; ok {
		t.Errorf("answer_input_tokens should be absent in raw mode")
	}
	// Confidence must NOT be set (empty Answer.Confidence).
	if _, ok := out.Metadata["confidence"]; ok {
		t.Errorf("confidence should be absent in raw mode")
	}
}

// --- ConvertPipelineResult: empty mode (plan-only) -------------------------

func TestConvertPipelineResult_PlanOnly(t *testing.T) {
	// Plan-only: no answer, no sources. Citations should be empty.
	r := &pipeline.Result{
		Plan: pipeline.Plan{
			QueriesBySourceType: map[search.SourceType][]string{"docs": {"x"}},
		},
		Debug: pipeline.DebugInfo{
			PlanInputTokens:  500,
			PlanOutputTokens: 100,
		},
	}

	out := ConvertPipelineResult("p1", r, time.Second, "gpt-4.1-mini")
	if len(out.Citations) != 0 {
		t.Errorf("Citations should be empty for plan-only, got %d", len(out.Citations))
	}
	if out.Answer != "" {
		t.Errorf("Answer should be empty, got %q", out.Answer)
	}
	// Cost should still be computed (plan phase did run).
	if out.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0", out.Cost)
	}
}

// --- computeCost -----------------------------------------------------------

func TestComputeCost_BothPhases(t *testing.T) {
	d := pipeline.DebugInfo{
		PlanInputTokens:    1000,
		PlanOutputTokens:   500,
		AnswerInputTokens:  8000,
		AnswerOutputTokens: 1000,
	}
	cost := computeCost("claude-sonnet-4", d)
	// Sonnet: $3/MTok input, $15/MTok output.
	// Plan:    1000 * 3 / 1M + 500 * 15 / 1M = 0.003 + 0.0075 = 0.0105
	// Answer:  8000 * 3 / 1M + 1000 * 15 / 1M = 0.024 + 0.015 = 0.039
	// Total:   0.0495
	want := 0.0495
	if diff := cost - want; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("cost = %v, want ~%v", cost, want)
	}
}

func TestComputeCost_UnknownModelFallback(t *testing.T) {
	d := pipeline.DebugInfo{PlanInputTokens: 1000, PlanOutputTokens: 100}
	cost := computeCost("some-random-model", d)
	if cost <= 0 {
		t.Errorf("unknown model should still produce positive cost, got %v", cost)
	}
}

func TestComputeCost_EmptyModel(t *testing.T) {
	d := pipeline.DebugInfo{PlanInputTokens: 1000, PlanOutputTokens: 100}
	cost := computeCost("", d)
	if cost <= 0 {
		t.Errorf("empty model should still produce positive cost, got %v", cost)
	}
}

// --- SilentLogger ----------------------------------------------------------

func TestSilentLogger_DropsEverything(t *testing.T) {
	logger := SilentLogger()
	if logger == nil {
		t.Fatal("nil logger")
	}
	// Should be usable without panicking.
	logger.Debug("should be dropped", "key", "value")
	logger.Info("also dropped")
	logger.Warn("dropped too")
	logger.Error("even errors are dropped")
}

// --- ErrorResult ------------------------------------------------------------
//
// Guards the Phase 5.7 Round 1 Patch 5 bench-side contract: when a
// variant surfaces a pipeline.PipelineError, v2shared.ErrorResult must
// copy the partial DebugInfo into the bench.Result metadata so the
// JSON dump carries per-failure diagnostic data.

func TestErrorResult_PreservesPhaseAndTokensFromPipelineError(t *testing.T) {
	pe := &pipeline.PipelineError{
		Phase: "answer",
		Err:   errors.New("answer: parse: invalid"),
		Debug: pipeline.DebugInfo{
			PlanInputTokens:    500,
			PlanOutputTokens:   1200,
			TotalSearchResults: 40,
			SelectedSources:    15,
			FetchedSources:     12,
		},
	}

	r := ErrorResult("et_002", pe, 45*time.Second, "claude-sonnet-4")

	if r.QueryID != "et_002" {
		t.Errorf("QueryID = %q", r.QueryID)
	}
	if r.Metadata["error_phase"] != "answer" {
		t.Errorf("error_phase = %v, want 'answer'", r.Metadata["error_phase"])
	}
	if r.Metadata["plan_input_tokens"] != 500 {
		t.Errorf("plan_input_tokens = %v, want 500", r.Metadata["plan_input_tokens"])
	}
	if r.Metadata["plan_output_tokens"] != 1200 {
		t.Errorf("plan_output_tokens = %v, want 1200", r.Metadata["plan_output_tokens"])
	}
	if r.Metadata["total_results"] != 40 {
		t.Errorf("total_results = %v, want 40", r.Metadata["total_results"])
	}
	if r.Metadata["selected_sources"] != 15 {
		t.Errorf("selected_sources = %v, want 15", r.Metadata["selected_sources"])
	}
	if r.Metadata["fetched_sources"] != 12 {
		t.Errorf("fetched_sources = %v, want 12", r.Metadata["fetched_sources"])
	}
	if r.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0 (plan phase still burned tokens)", r.Cost)
	}
	msg, ok := r.Metadata["error"].(string)
	if !ok || msg == "" {
		t.Errorf("error metadata missing or wrong type: %v", r.Metadata)
	}
}

func TestErrorResult_NonPipelineErrorHasOnlyRawMessage(t *testing.T) {
	// When the error isn't a *PipelineError (e.g. context timeout from
	// the runner itself), ErrorResult should still produce a bench.Result
	// with the message preserved, just without the phase/token metadata.
	r := ErrorResult("q1", errors.New("ctx deadline exceeded"), time.Second, "gpt-4.1-mini")

	if _, ok := r.Metadata["error_phase"]; ok {
		t.Errorf("error_phase should be absent for non-PipelineError: %v", r.Metadata)
	}
	if _, ok := r.Metadata["plan_input_tokens"]; ok {
		t.Errorf("plan_input_tokens should be absent: %v", r.Metadata)
	}
	if r.Metadata["error"] != "ctx deadline exceeded" {
		t.Errorf("error = %v", r.Metadata["error"])
	}
}
