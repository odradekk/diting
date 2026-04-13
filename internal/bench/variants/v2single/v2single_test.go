package v2single

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/bench/variants/v2shared"
	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/search"
)

// --- test stubs ------------------------------------------------------------
//
// These are minimal copies of the stubs in internal/pipeline/*_test.go.
// We can't import those (test-only) so a small amount of duplication is
// the price of isolating this variant's tests from the real network.

type stubFetcher struct{}

func (f *stubFetcher) Fetch(_ context.Context, url string) (*fetch.Result, error) {
	return &fetch.Result{URL: url, Content: "content for " + url, ContentType: "text/plain"}, nil
}
func (f *stubFetcher) FetchMany(ctx context.Context, urls []string) ([]*fetch.Result, error) {
	out := make([]*fetch.Result, len(urls))
	for i, u := range urls {
		out[i], _ = f.Fetch(ctx, u)
	}
	return out, nil
}
func (f *stubFetcher) Close() error { return nil }

type sequenceLLM struct {
	calls      int
	planJSON   string
	answerJSON string
}

func (s *sequenceLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &llm.Response{Content: s.planJSON, InputTokens: 1000, OutputTokens: 500}, nil
	}
	return &llm.Response{Content: s.answerJSON, InputTokens: 5000, OutputTokens: 800}, nil
}

type stubModule struct{}

func (m *stubModule) Manifest() search.Manifest {
	return search.Manifest{Name: "stub", SourceType: search.SourceTypeGeneralWeb}
}
func (m *stubModule) Search(_ context.Context, _ string) ([]search.SearchResult, error) {
	return []search.SearchResult{
		{Title: "Result 1", URL: "https://go.dev/doc", Snippet: "go concurrency docs"},
		{Title: "Result 2", URL: "https://gobyexample.com", Snippet: "go by example snippet"},
	}, nil
}
func (m *stubModule) Close() error { return nil }

func makePlanJSON(t *testing.T) string {
	t.Helper()
	p := map[string]any{
		"plan": map[string]any{
			"rationale": "test plan",
			"queries_by_source_type": map[string][]string{
				"general_web": {"go concurrency"},
				"academic":    {},
				"code":        {},
				"community":   {},
				"docs":        {},
			},
			"expected_answer_shape": "short explanation",
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func makeAnswerJSON(t *testing.T) string {
	t.Helper()
	a := map[string]any{
		"answer":     "Go uses goroutines and channels [1].",
		"confidence": "high",
		"citations": []map[string]any{
			{"id": 1, "url": "https://go.dev/doc", "title": "Go Docs", "source_type": "general_web"},
		},
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// buildTestPipeline constructs a pipeline.Pipeline wired to stub
// dependencies so we can exercise v2single.Run() without touching
// the real LLM, search registry, or fetch chain.
func buildTestPipeline(t *testing.T) *pipeline.Pipeline {
	t.Helper()
	return pipeline.New(
		[]search.Module{&stubModule{}},
		&stubFetcher{},
		&sequenceLLM{planJSON: makePlanJSON(t), answerJSON: makeAnswerJSON(t)},
		nil, // default scorer
		pipeline.Config{PlanMode: pipeline.PlanModeAuto},
		v2shared.SilentLogger(),
	)
}

// --- v2single.Run ---------------------------------------------------------

func TestRun_HappyPath(t *testing.T) {
	v := newWithPipeline(buildTestPipeline(t), "claude-sonnet-4")

	out, err := v.Run(context.Background(), bench.RunInput{
		ID:    "et_001",
		Query: "how do go channels work",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if out.QueryID != "et_001" {
		t.Errorf("QueryID = %q", out.QueryID)
	}
	if out.Answer == "" {
		t.Error("Answer should be populated")
	}
	// After R2.2, Citations are the union of LLM-cited (1 in the
	// makeAnswerJSON fixture) + fetched-but-uncited sources (1 from
	// the stub module's second result that the LLM didn't reference).
	// The fixture intentionally exercises the merge path.
	if len(out.Citations) != 2 {
		t.Errorf("len(Citations) = %d, want 2 (1 LLM-cited + 1 merged from sources)", len(out.Citations))
	}
	// llm_cited_count tracks the LLM-only contribution separately.
	if out.Metadata["llm_cited_count"] != 1 {
		t.Errorf("llm_cited_count = %v, want 1", out.Metadata["llm_cited_count"])
	}
	if out.Latency <= 0 {
		t.Errorf("Latency = %v, want > 0", out.Latency)
	}
	if out.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0 (sonnet pricing)", out.Cost)
	}
	// Metadata should include confidence and token counts.
	if out.Metadata["confidence"] != "high" {
		t.Errorf("confidence = %v", out.Metadata["confidence"])
	}
	if out.Metadata["plan_input_tokens"] != 1000 {
		t.Errorf("plan_input_tokens = %v", out.Metadata["plan_input_tokens"])
	}
	if out.Metadata["answer_input_tokens"] != 5000 {
		t.Errorf("answer_input_tokens = %v", out.Metadata["answer_input_tokens"])
	}
}

func TestRun_PipelineErrorCapturedInMetadata(t *testing.T) {
	// Pipeline built with a broken LLM → Run should capture the error
	// in Metadata["error"] rather than propagating it.
	p := pipeline.New(
		[]search.Module{&stubModule{}},
		&stubFetcher{},
		&brokenLLM{err: errors.New("quota exhausted")},
		nil,
		pipeline.Config{},
		v2shared.SilentLogger(),
	)
	v := newWithPipeline(p, "gpt-4.1-mini")

	out, err := v.Run(context.Background(), bench.RunInput{ID: "q1", Query: "test"})
	if err != nil {
		t.Fatalf("Run should not propagate pipeline errors: %v", err)
	}
	if out.QueryID != "q1" {
		t.Errorf("QueryID = %q", out.QueryID)
	}
	msg, ok := out.Metadata["error"].(string)
	if !ok {
		t.Fatalf("missing error metadata: %v", out.Metadata)
	}
	if !strings.Contains(msg, "quota exhausted") {
		t.Errorf("error metadata = %q", msg)
	}
}

// --- Name ------------------------------------------------------------------

func TestName(t *testing.T) {
	v := &variant{}
	if v.Name() != "v2-single" {
		t.Errorf("Name() = %q", v.Name())
	}
	if Name != "v2-single" {
		t.Errorf("package Name constant drifted")
	}
}

// --- brokenLLM -------------------------------------------------------------

type brokenLLM struct{ err error }

func (b *brokenLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return nil, b.err
}
