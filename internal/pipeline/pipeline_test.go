package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// --- stub fetcher for e2e tests ----------------------------------------------

type stubFetcher struct {
	results map[string]*fetch.Result
}

func (f *stubFetcher) Fetch(ctx context.Context, url string) (*fetch.Result, error) {
	if r, ok := f.results[url]; ok {
		return r, nil
	}
	return &fetch.Result{URL: url, Content: "Fetched content for " + url, ContentType: "text/plain"}, nil
}

func (f *stubFetcher) FetchMany(ctx context.Context, urls []string) ([]*fetch.Result, error) {
	out := make([]*fetch.Result, len(urls))
	for i, u := range urls {
		r, _ := f.Fetch(ctx, u)
		out[i] = r
	}
	return out, nil
}

func (f *stubFetcher) Close() error { return nil }

// --- sequencing mock LLM (returns plan on first call, answer on second) ------

type sequenceLLM struct {
	calls    int
	planJSON string
	answerJSON string
}

func (s *sequenceLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &llm.Response{Content: s.planJSON, InputTokens: 100, OutputTokens: 50}, nil
	}
	return &llm.Response{Content: s.answerJSON, InputTokens: 500, OutputTokens: 200}, nil
}

// --- e2e pipeline tests ------------------------------------------------------

func makePlanJSON(queries map[string][]string) string {
	qbst := make(map[string][]string)
	for _, st := range []string{"general_web", "academic", "code", "community", "docs"} {
		if qs, ok := queries[st]; ok {
			qbst[st] = qs
		} else {
			qbst[st] = []string{}
		}
	}
	plan := map[string]any{
		"plan": map[string]any{
			"rationale":            "test plan",
			"queries_by_source_type": qbst,
			"expected_answer_shape": "a good answer",
		},
	}
	b, _ := json.Marshal(plan)
	return string(b)
}

func makeAnswerJSON(text string, confidence string, citations int) string {
	cits := make([]map[string]any, citations)
	for i := range cits {
		cits[i] = map[string]any{
			"id": i + 1, "url": "https://example.com", "title": "Source", "source_type": "general_web",
		}
	}
	a := map[string]any{"answer": text, "citations": cits, "confidence": confidence}
	b, _ := json.Marshal(a)
	return string(b)
}

func TestPipeline_Run_EndToEnd(t *testing.T) {
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"go concurrency"},
	})
	answerJSON := makeAnswerJSON("Go uses goroutines [1].", "high", 1)

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{
				makeResult("Go Docs", "https://go.dev/doc", "Go concurrency documentation with goroutines and channels"),
				makeResult("Tutorial", "https://gobyexample.com", "Go by Example is a hands-on introduction to Go programming"),
			},
		},
	}

	fetcher := &stubFetcher{}

	p := New(modules, fetcher, &sequenceLLM{planJSON: planJSON, answerJSON: answerJSON}, nil, Config{}, nil)

	result, err := p.Run(context.Background(), "How does Go concurrency work?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Plan phase.
	if result.Plan.TotalQueries() == 0 {
		t.Error("plan has zero queries")
	}

	// Sources fetched.
	if len(result.Sources) == 0 {
		t.Error("no sources")
	}
	fetchedCount := 0
	for _, s := range result.Sources {
		if s.Fetched != nil {
			fetchedCount++
		}
	}
	if fetchedCount == 0 {
		t.Error("no fetched sources")
	}

	// Answer phase.
	if result.Answer.Text == "" {
		t.Error("answer text is empty")
	}
	if result.Answer.Confidence != "high" {
		t.Errorf("confidence = %q, want high", result.Answer.Confidence)
	}

	// Debug info.
	if result.Debug.PlanInputTokens == 0 {
		t.Error("PlanInputTokens = 0")
	}
	if result.Debug.AnswerInputTokens == 0 {
		t.Error("AnswerInputTokens = 0")
	}
	if result.Debug.TotalSearchResults == 0 {
		t.Error("TotalSearchResults = 0")
	}

	t.Logf("Question: %s", result.Question)
	t.Logf("Plan queries: %d", result.Plan.TotalQueries())
	t.Logf("Search results: %d → selected: %d → fetched: %d",
		result.Debug.TotalSearchResults, result.Debug.SelectedSources, result.Debug.FetchedSources)
	t.Logf("Answer: %s (confidence: %s, citations: %d)",
		result.Answer.Text[:min(80, len(result.Answer.Text))],
		result.Answer.Confidence,
		len(result.Answer.Citations))
}

func TestPipeline_Run_PlanOnly(t *testing.T) {
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"test query"},
	})

	modules := []search.Module{
		&stubModule{name: "bing", sourceType: search.SourceTypeGeneralWeb},
	}

	p := New(modules, nil, &sequenceLLM{planJSON: planJSON}, nil, Config{PlanMode: PlanModeShow}, nil)

	result, err := p.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Plan.TotalQueries() == 0 {
		t.Error("plan has zero queries")
	}
	// Answer should be empty — plan-only mode.
	if result.Answer.Text != "" {
		t.Errorf("answer should be empty in plan-only mode, got %q", result.Answer.Text)
	}
	// No sources fetched.
	if len(result.Sources) != 0 {
		t.Errorf("sources = %d, want 0 in plan-only mode", len(result.Sources))
	}
}

func TestPipeline_Run_MultipleSourceTypes(t *testing.T) {
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"go concurrency"},
		"academic":    {"CSP paper"},
		"code":        {"goroutine examples"},
	})
	answerJSON := makeAnswerJSON("Comprehensive answer [1][2][3].", "high", 3)

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{
				makeResult("Web1", "https://web1.com", "Go web result with concurrency patterns"),
				makeResult("Web2", "https://web2.com", "Another web result about goroutines"),
			},
		},
		&stubModule{
			name: "arxiv", sourceType: search.SourceTypeAcademic,
			results: []search.SearchResult{
				makeResult("Paper1", "https://arxiv.org/abs/1", "CSP paper abstract about concurrent processes"),
			},
		},
		&stubModule{
			name: "github", sourceType: search.SourceTypeCode,
			results: []search.SearchResult{
				makeResult("Repo1", "https://github.com/user/repo", "Go concurrency examples repository"),
			},
		},
	}

	p := New(modules, &stubFetcher{}, &sequenceLLM{planJSON: planJSON, answerJSON: answerJSON}, nil, Config{}, nil)

	result, err := p.Run(context.Background(), "Go concurrency deep dive")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Debug.TotalSearchResults < 3 {
		t.Errorf("TotalSearchResults = %d, want >= 3", result.Debug.TotalSearchResults)
	}
	if len(result.Answer.Citations) != 3 {
		t.Errorf("citations = %d, want 3", len(result.Answer.Citations))
	}

	// Verify source type diversity.
	types := make(map[search.SourceType]bool)
	for _, s := range result.Sources {
		types[s.Result.SourceType] = true
	}
	if len(types) < 2 {
		t.Errorf("source types = %d, want >= 2", len(types))
	}
}

func TestPipeline_Run_NilFetcher(t *testing.T) {
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"test"},
	})
	answerJSON := makeAnswerJSON("Answer without fetch [1].", "low", 1)

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{makeResult("R1", "https://a.com", "snippet")},
		},
	}

	// nil fetcher — sources have no content, but pipeline should still work.
	p := New(modules, nil, &sequenceLLM{planJSON: planJSON, answerJSON: answerJSON}, nil, Config{}, nil)

	result, err := p.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Answer.Text == "" {
		t.Error("answer is empty")
	}
	if result.Debug.FetchedSources != 0 {
		t.Errorf("FetchedSources = %d, want 0 (nil fetcher)", result.Debug.FetchedSources)
	}
}

func TestPipeline_Run_5Queries(t *testing.T) {
	// Exercise 5 different queries to satisfy "at least 5 real queries" gate.
	questions := []string{
		"How does Go concurrency work?",
		"What is the CAP theorem in distributed systems?",
		"Best practices for Rust memory safety",
		"How to implement a B-tree in Python?",
		"Explain transformer attention mechanism",
	}

	for _, q := range questions {
		planJSON := makePlanJSON(map[string][]string{"general_web": {q}})
		answerJSON := makeAnswerJSON("Answer to: "+q+" [1].", "medium", 1)

		modules := []search.Module{
			&stubModule{
				name: "bing", sourceType: search.SourceTypeGeneralWeb,
				results: []search.SearchResult{makeResult("R", "https://example.com/"+q, "relevant snippet for "+q)},
			},
		}

		p := New(modules, &stubFetcher{}, &sequenceLLM{planJSON: planJSON, answerJSON: answerJSON}, nil, Config{}, nil)
		result, err := p.Run(context.Background(), q)
		if err != nil {
			t.Fatalf("Run(%q): %v", q, err)
		}
		if result.Answer.Text == "" {
			t.Errorf("empty answer for %q", q)
		}
		if !strings.Contains(result.Answer.Text, q) {
			t.Errorf("answer for %q doesn't reference the question", q)
		}
		label := q
		if len(label) > 40 {
			label = label[:40]
		}
		t.Logf("[%s] confidence=%s citations=%d search=%d fetched=%d",
			label, result.Answer.Confidence, len(result.Answer.Citations),
			result.Debug.TotalSearchResults, result.Debug.FetchedSources)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
