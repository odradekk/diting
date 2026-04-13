package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

	stub := &stubModule{
		name:       "bing",
		sourceType: search.SourceTypeGeneralWeb,
		results: []search.SearchResult{
			// Populate results so if execute ran we'd see them flow through.
			makeResult("Some Result", "https://example.com", "A snippet"),
		},
	}
	modules := []search.Module{stub}

	llm := &sequenceLLM{planJSON: planJSON, answerJSON: "should never be used"}
	p := New(modules, nil, llm, nil, Config{PlanMode: PlanModeShow}, nil)

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
	// stubModule.Search() must NOT have been called — the execute phase
	// should be skipped entirely.
	if stub.searchCalls != 0 {
		t.Errorf("stubModule.Search() called %d times, want 0 in plan-only mode", stub.searchCalls)
	}
	// LLM must only have been called once (for the plan turn).
	if llm.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 in plan-only mode", llm.calls)
	}
	// Answer debug fields must be zero.
	if result.Debug.AnswerInputTokens != 0 || result.Debug.AnswerOutputTokens != 0 {
		t.Errorf("answer token counts should be 0 in plan-only mode, got in=%d out=%d",
			result.Debug.AnswerInputTokens, result.Debug.AnswerOutputTokens)
	}
}

func TestPipeline_Run_Raw(t *testing.T) {
	// Raw mode should run plan + search + fetch, then stop before the answer
	// LLM call. The sequenceLLM's second call would return an empty answer —
	// if the pipeline fell through to it, the test's assertions on Sources
	// would still pass but we'd have wasted an LLM call. We detect that by
	// counting calls.
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"go channels"},
	})

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{
				makeResult("Go Docs", "https://go.dev/doc", "Go concurrency documentation with goroutines and channels"),
				makeResult("Tutorial", "https://gobyexample.com", "Go by Example is a hands-on introduction"),
			},
		},
	}

	llm := &sequenceLLM{planJSON: planJSON, answerJSON: "should never be used"}
	p := New(modules, &stubFetcher{}, llm, nil, Config{PlanMode: PlanModeRaw}, nil)

	result, err := p.Run(context.Background(), "How do Go channels work?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// LLM should only have been called once (plan phase).
	if llm.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (answer phase should be skipped)", llm.calls)
	}

	// Plan should be populated.
	if result.Plan.TotalQueries() == 0 {
		t.Error("plan has zero queries")
	}

	// Sources should be populated (search + fetch both ran).
	if len(result.Sources) == 0 {
		t.Error("no sources in raw mode")
	}
	fetchedCount := 0
	for _, s := range result.Sources {
		if s.Fetched != nil {
			fetchedCount++
		}
	}
	if fetchedCount == 0 {
		t.Error("no fetched sources in raw mode")
	}

	// Answer MUST be empty.
	if result.Answer.Text != "" {
		t.Errorf("answer should be empty in raw mode, got %q", result.Answer.Text)
	}
	if len(result.Answer.Citations) != 0 {
		t.Errorf("citations should be empty in raw mode, got %d", len(result.Answer.Citations))
	}

	// Plan-phase debug fields should be populated; answer-phase fields should not.
	if result.Debug.PlanInputTokens == 0 {
		t.Error("PlanInputTokens = 0")
	}
	if result.Debug.AnswerInputTokens != 0 {
		t.Errorf("AnswerInputTokens = %d, want 0 (answer phase skipped)", result.Debug.AnswerInputTokens)
	}
	if result.Debug.AnswerOutputTokens != 0 {
		t.Errorf("AnswerOutputTokens = %d, want 0 (answer phase skipped)", result.Debug.AnswerOutputTokens)
	}
	if result.Debug.FetchedSources == 0 {
		t.Error("FetchedSources = 0")
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

// TestPipeline_Run_DebugLogging verifies that --debug level logging from
// pipeline.Run emits structured JSON events for plan phase completion,
// plan raw response preview, and answer raw response preview — the
// Phase 4.4 deliverable.
func TestPipeline_Run_DebugLogging(t *testing.T) {
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"go concurrency"},
	})
	answerJSON := makeAnswerJSON("Go uses goroutines [1].", "high", 1)

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{
				makeResult("Go Docs", "https://go.dev/doc", "Go concurrency documentation"),
			},
		},
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	p := New(modules, &stubFetcher{}, &sequenceLLM{planJSON: planJSON, answerJSON: answerJSON}, nil, Config{}, logger)

	_, err := p.Run(context.Background(), "How does Go concurrency work?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := parseJSONLines(t, buf.String())
	if len(events) == 0 {
		t.Fatal("no log events captured")
	}

	// Expected event messages.
	wantMessages := map[string]bool{
		"plan phase complete":     false,
		"plan: raw response":      false,
		"execute phase complete":  false,
		"answer phase complete":   false,
		"answer: raw response":    false,
		"fetch phase complete":    false,
	}
	for _, e := range events {
		msg, _ := e["msg"].(string)
		if _, want := wantMessages[msg]; want {
			wantMessages[msg] = true
		}
	}
	for msg, seen := range wantMessages {
		if !seen {
			t.Errorf("missing log event: %q", msg)
		}
	}

	// Assert the plan raw-response event has a content_preview field.
	for _, e := range events {
		if e["msg"] == "plan: raw response" {
			if _, ok := e["content_preview"]; !ok {
				t.Error("plan: raw response event missing content_preview field")
			}
			if _, ok := e["content_length"]; !ok {
				t.Error("plan: raw response event missing content_length field")
			}
		}
	}
}

// TestPreview covers the preview helper used for debug content snippets.
func TestPreview(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"", 10, ""},
		{"hi", 10, "hi"},
		{"hello world", 5, "hello…"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven char", 10, "eleven cha…"},
	}
	for _, tt := range tests {
		if got := preview(tt.in, tt.n); got != tt.want {
			t.Errorf("preview(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
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

// --- PipelineError partial debug extraction --------------------------------
//
// Guards the Phase 5.7 Round 1 Patch 5 fix: when a phase fails, the
// returned error must be a *PipelineError carrying a snapshot of every
// DebugInfo field populated before the failure. This lets the bench
// runner surface plan-phase token counts on answer-phase failures
// (etc.) without re-running the query.

// errLLM always returns the same error from Complete. Used to simulate
// provider failures cleanly.
type errLLM struct {
	err error
}

func (e *errLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return nil, e.err
}

// planOkThenAnswerErrLLM returns a valid plan on the first call and a
// specified error on subsequent calls. Use to trigger an answer-phase
// failure while the plan phase has already populated DebugInfo.
type planOkThenAnswerErrLLM struct {
	calls    int
	planJSON string
	err      error
}

func (p *planOkThenAnswerErrLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		return &llm.Response{Content: p.planJSON, InputTokens: 111, OutputTokens: 222}, nil
	}
	return nil, p.err
}

func TestPipelineError_PlanPhaseFailureHasEmptyDebug(t *testing.T) {
	// A plan-phase failure has no populated debug fields — the plan
	// LLM call errored before we could record its tokens.
	modules := []search.Module{&stubModule{name: "bing", sourceType: search.SourceTypeGeneralWeb}}
	p := New(modules, &stubFetcher{}, &errLLM{err: errForTest("rate limited")}, nil, Config{}, nil)

	_, err := p.Run(context.Background(), "any")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *PipelineError
	if !asPipelineError(err, &pe) {
		t.Fatalf("error is not *PipelineError: %v", err)
	}
	if pe.Phase != "plan" {
		t.Errorf("Phase = %q, want 'plan'", pe.Phase)
	}
	if pe.Debug.PlanInputTokens != 0 {
		t.Errorf("PlanInputTokens = %d, want 0 (plan never returned)", pe.Debug.PlanInputTokens)
	}
}

func TestPipelineError_AnswerPhaseFailurePreservesPlanDebug(t *testing.T) {
	// An answer-phase failure must preserve plan-phase tokens and
	// search/fetch counts from the earlier successful phases.
	planJSON := makePlanJSON(map[string][]string{
		"general_web": {"test query"},
	})
	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{
				makeResult("A", "https://example.com/a", "snippet about test"),
				makeResult("B", "https://example.com/b", "more snippet about test"),
			},
		},
	}
	llm := &planOkThenAnswerErrLLM{planJSON: planJSON, err: errForTest("answer failed")}
	p := New(modules, &stubFetcher{}, llm, nil, Config{}, nil)

	_, err := p.Run(context.Background(), "test query")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *PipelineError
	if !asPipelineError(err, &pe) {
		t.Fatalf("error is not *PipelineError: %v", err)
	}
	if pe.Phase != "answer" {
		t.Errorf("Phase = %q, want 'answer'", pe.Phase)
	}
	if pe.Debug.PlanInputTokens != 111 {
		t.Errorf("PlanInputTokens = %d, want 111 (from planOkLLM)", pe.Debug.PlanInputTokens)
	}
	if pe.Debug.PlanOutputTokens != 222 {
		t.Errorf("PlanOutputTokens = %d, want 222", pe.Debug.PlanOutputTokens)
	}
	if pe.Debug.SelectedSources == 0 {
		t.Error("SelectedSources = 0, want > 0 (execute phase completed)")
	}
	if pe.Debug.FetchedSources == 0 {
		t.Error("FetchedSources = 0, want > 0 (fetch phase completed)")
	}
	// Answer tokens must remain zero since the answer LLM errored.
	if pe.Debug.AnswerInputTokens != 0 {
		t.Errorf("AnswerInputTokens = %d, want 0", pe.Debug.AnswerInputTokens)
	}
}

func TestPipelineError_ErrorFormatIncludesPhasePrefix(t *testing.T) {
	pe := &PipelineError{Phase: "plan", Err: errForTest("boom")}
	got := pe.Error()
	want := "pipeline: plan: boom"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestPipelineError_UnwrapsToInner(t *testing.T) {
	inner := errForTest("inner")
	pe := &PipelineError{Phase: "plan", Err: inner}
	if pe.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", pe.Unwrap(), inner)
	}
}

// --- Config.PlanClient split-LLM contract (Round 3.1) -----------------------
//
// Guards Phase 5.7 Round 3.1: when Config.PlanClient is set, the plan
// phase MUST call PlanClient (not the main answer client), and the
// answer phase MUST call the main client. When PlanClient is nil, both
// phases use the same main client (backward compat).

// labelLLM is a mock that records its label every time Complete is
// called. Used to verify which client served which phase.
type labelLLM struct {
	label    string
	calls    int
	response string
}

func (l *labelLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	l.calls++
	return &llm.Response{Content: l.response, InputTokens: 10, OutputTokens: 5}, nil
}

func TestPipeline_Run_SeparatePlanClient(t *testing.T) {
	planJSON := makePlanJSON(map[string][]string{"general_web": {"q"}})
	answerJSON := makeAnswerJSON("answer text", "high", 1)

	planClient := &labelLLM{label: "plan", response: planJSON}
	answerClient := &labelLLM{label: "answer", response: answerJSON}

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{makeResult("A", "https://example.com/a", "snippet")},
		},
	}

	p := New(modules, &stubFetcher{}, answerClient, nil, Config{
		PlanClient: planClient,
	}, nil)

	_, err := p.Run(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Plan client must have been called exactly once (no retry, plan parsed).
	if planClient.calls != 1 {
		t.Errorf("planClient.calls = %d, want 1", planClient.calls)
	}
	// Answer client must have been called exactly once (the answer phase).
	if answerClient.calls != 1 {
		t.Errorf("answerClient.calls = %d, want 1", answerClient.calls)
	}
}

func TestPipeline_Run_NoPlanClientFallsBackToMain(t *testing.T) {
	// Backward-compat: when Config.PlanClient is nil, the main client
	// handles BOTH phases. The sequenceLLM mock returns plan on the
	// first call and answer on the second, so total calls = 2.
	planJSON := makePlanJSON(map[string][]string{"general_web": {"q"}})
	answerJSON := makeAnswerJSON("answer", "high", 1)

	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{makeResult("A", "https://example.com/a", "snippet")},
		},
	}

	main := &sequenceLLM{planJSON: planJSON, answerJSON: answerJSON}
	p := New(modules, &stubFetcher{}, main, nil, Config{}, nil)

	_, err := p.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if main.calls != 2 {
		t.Errorf("main.calls = %d, want 2 (plan + answer on same client)", main.calls)
	}
}

// --- test helpers ----------------------------------------------------------

// errForTest is a small shim so tests don't each import "errors" for
// a single error value.
func errForTest(msg string) error { return &testError{msg: msg} }

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// asPipelineError wraps errors.As in a tiny helper so test bodies read
// the way they describe themselves — we use this in two tests and
// inlining errors.As + *PipelineError clutters the intent.
func asPipelineError(err error, target **PipelineError) bool {
	for err != nil {
		if pe, ok := err.(*PipelineError); ok {
			*target = pe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
