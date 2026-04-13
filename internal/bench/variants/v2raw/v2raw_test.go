package v2raw

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

// --- test stubs (minimal copies — see v2single_test.go for why) ----------

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

// planOnlyLLM returns a plan on the first call and panics on the
// second — v2-raw should NEVER reach a second LLM call because it
// uses PlanModeRaw.
type planOnlyLLM struct {
	calls    int
	planJSON string
}

func (s *planOnlyLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	s.calls++
	if s.calls > 1 {
		return nil, errors.New("v2-raw leaked an answer-phase LLM call")
	}
	return &llm.Response{Content: s.planJSON, InputTokens: 800, OutputTokens: 300}, nil
}

type stubModule struct{}

func (m *stubModule) Manifest() search.Manifest {
	return search.Manifest{Name: "stub", SourceType: search.SourceTypeGeneralWeb}
}
func (m *stubModule) Search(_ context.Context, _ string) ([]search.SearchResult, error) {
	return []search.SearchResult{
		{Title: "Docs", URL: "https://go.dev/doc", Snippet: "go docs"},
		{Title: "Example", URL: "https://gobyexample.com/channels", Snippet: "go by example channels"},
	}, nil
}
func (m *stubModule) Close() error { return nil }

func makePlanJSON(t *testing.T) string {
	t.Helper()
	p := map[string]any{
		"plan": map[string]any{
			"rationale": "raw-mode test",
			"queries_by_source_type": map[string][]string{
				"general_web": {"go concurrency"},
				"academic":    {},
				"code":        {},
				"community":   {},
				"docs":        {},
			},
			"expected_answer_shape": "sources-only",
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func buildTestPipeline(t *testing.T) (*pipeline.Pipeline, *planOnlyLLM) {
	t.Helper()
	llmStub := &planOnlyLLM{planJSON: makePlanJSON(t)}
	p := pipeline.New(
		[]search.Module{&stubModule{}},
		&stubFetcher{},
		llmStub,
		nil,
		pipeline.Config{PlanMode: pipeline.PlanModeRaw},
		v2shared.SilentLogger(),
	)
	return p, llmStub
}

// --- Run: raw-mode happy path ---------------------------------------------

func TestRun_RawModeNoAnswerLLMCall(t *testing.T) {
	p, llmStub := buildTestPipeline(t)
	v := newWithPipeline(p, "claude-sonnet-4")

	out, err := v.Run(context.Background(), bench.RunInput{
		ID:    "et_001",
		Query: "how do go channels work",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// LLM must only have been called once (plan phase).
	if llmStub.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (raw mode skips answer phase)", llmStub.calls)
	}

	// Answer MUST be empty — raw mode never synthesizes.
	if out.Answer != "" {
		t.Errorf("Answer = %q, want empty (raw mode)", out.Answer)
	}

	// Citations come from fetched sources.
	if len(out.Citations) == 0 {
		t.Error("Citations should be populated from Sources in raw mode")
	}
	// Rank comes from FetchedSource.ID (1-based).
	for i, c := range out.Citations {
		if c.Rank != i+1 {
			t.Errorf("Citations[%d].Rank = %d, want %d", i, c.Rank, i+1)
		}
	}

	// Metadata: plan token counts present, answer token counts absent.
	if out.Metadata["plan_input_tokens"] != 800 {
		t.Errorf("plan_input_tokens = %v", out.Metadata["plan_input_tokens"])
	}
	if _, has := out.Metadata["answer_input_tokens"]; has {
		t.Errorf("answer_input_tokens should be absent in raw mode: %v", out.Metadata)
	}
	// Confidence should also be absent (no Answer.Confidence set).
	if _, has := out.Metadata["confidence"]; has {
		t.Errorf("confidence should be absent in raw mode")
	}

	// Cost reflects ONLY the plan phase (answer tokens are 0).
	if out.Cost <= 0 {
		t.Errorf("Cost = %v, want > 0 (plan phase still costs tokens)", out.Cost)
	}
}

func TestRun_PipelineErrorCapturedInMetadata(t *testing.T) {
	p := pipeline.New(
		[]search.Module{&stubModule{}},
		&stubFetcher{},
		&brokenLLM{err: errors.New("rate limit hit")},
		nil,
		pipeline.Config{PlanMode: pipeline.PlanModeRaw},
		v2shared.SilentLogger(),
	)
	v := newWithPipeline(p, "gpt-4.1-mini")

	out, err := v.Run(context.Background(), bench.RunInput{ID: "q1", Query: "test"})
	if err != nil {
		t.Fatalf("Run should not propagate: %v", err)
	}
	msg, ok := out.Metadata["error"].(string)
	if !ok {
		t.Fatalf("missing error metadata: %v", out.Metadata)
	}
	if !strings.Contains(msg, "rate limit hit") {
		t.Errorf("error = %q", msg)
	}
}

// --- Name -----------------------------------------------------------------

func TestName(t *testing.T) {
	v := &variant{}
	if v.Name() != "v2-raw" {
		t.Errorf("Name() = %q", v.Name())
	}
	if Name != "v2-raw" {
		t.Error("package Name constant drifted")
	}
}

// --- brokenLLM ------------------------------------------------------------

type brokenLLM struct{ err error }

func (b *brokenLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return nil, b.err
}
