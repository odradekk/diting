package v0baseline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/search"
)

// stubModule is a fake search.Module that returns canned results
// (or an error) so we can test v0-baseline without touching the network.
type stubModule struct {
	results []search.SearchResult
	err     error
	calls   int
}

func (s *stubModule) Manifest() search.Manifest {
	return search.Manifest{Name: "stub-bing", SourceType: search.SourceTypeGeneralWeb}
}
func (s *stubModule) Search(_ context.Context, _ string) ([]search.SearchResult, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	// Return a fresh copy so callers can mutate without affecting
	// subsequent calls.
	out := make([]search.SearchResult, len(s.results))
	copy(out, s.results)
	return out, nil
}
func (s *stubModule) Close() error { return nil }

func mkResult(url string) search.SearchResult {
	return search.SearchResult{URL: url, Title: "t", Snippet: "s"}
}

// --- Run: happy path -------------------------------------------------------

func TestRun_TopThreeCitations(t *testing.T) {
	stub := &stubModule{
		results: []search.SearchResult{
			mkResult("https://go.dev/doc"),
			mkResult("https://www.python.org/doc/"),
			mkResult("https://stackoverflow.com/q/1"),
			mkResult("https://fourth.example.com"),
			mkResult("https://fifth.example.com"),
		},
	}
	v := &variant{module: stub}

	out, err := v.Run(context.Background(), bench.RunInput{
		ID:    "test_001",
		Query: "how do go channels work",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if out.QueryID != "test_001" {
		t.Errorf("QueryID = %q", out.QueryID)
	}
	// Only top 3 citations even though 5 results were returned.
	if len(out.Citations) != 3 {
		t.Fatalf("len(Citations) = %d, want 3", len(out.Citations))
	}
	// Verify ranks are 1,2,3 in that order.
	for i, c := range out.Citations {
		if c.Rank != i+1 {
			t.Errorf("Citations[%d].Rank = %d, want %d", i, c.Rank, i+1)
		}
	}
	// All bing results are general-web.
	for _, c := range out.Citations {
		if c.SourceType != bench.SourceGeneralWeb {
			t.Errorf("SourceType = %q, want general_web", c.SourceType)
		}
	}
	// Domain extraction: www. stripped, lowercase.
	if out.Citations[0].Domain != "go.dev" {
		t.Errorf("c0.Domain = %q", out.Citations[0].Domain)
	}
	if out.Citations[1].Domain != "python.org" {
		t.Errorf("c1.Domain = %q (www. should be stripped)", out.Citations[1].Domain)
	}
	// No LLM → no Answer, no Cost.
	if out.Answer != "" {
		t.Errorf("Answer = %q, want empty", out.Answer)
	}
	if out.Cost != 0 {
		t.Errorf("Cost = %v, want 0", out.Cost)
	}
	// Latency is populated.
	if out.Latency <= 0 {
		t.Errorf("Latency = %v, want > 0", out.Latency)
	}
	// Metadata carries the raw result count.
	if out.Metadata["raw_results"] != 3 {
		t.Errorf("raw_results = %v, want 3", out.Metadata["raw_results"])
	}
}

func TestRun_FewerThanTopK(t *testing.T) {
	stub := &stubModule{
		results: []search.SearchResult{
			mkResult("https://only.example.com"),
		},
	}
	v := &variant{module: stub}
	out, err := v.Run(context.Background(), bench.RunInput{ID: "x", Query: "q"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Citations) != 1 {
		t.Errorf("len(Citations) = %d, want 1", len(out.Citations))
	}
}

// --- Run: error path ------------------------------------------------------

func TestRun_ModuleErrorCapturedInMetadata(t *testing.T) {
	stub := &stubModule{err: errors.New("bing blocked")}
	v := &variant{module: stub}

	out, err := v.Run(context.Background(), bench.RunInput{ID: "blocked", Query: "q"})
	// Error should NOT propagate — bench runner expects partial results.
	if err != nil {
		t.Fatalf("Run should not propagate module errors: %v", err)
	}
	if len(out.Citations) != 0 {
		t.Errorf("len(Citations) = %d, want 0 on error", len(out.Citations))
	}
	msg, ok := out.Metadata["error"].(string)
	if !ok {
		t.Fatalf("error metadata missing or wrong type: %v", out.Metadata)
	}
	if !strings.Contains(msg, "bing blocked") {
		t.Errorf("error metadata = %q", msg)
	}
}

// --- Name ------------------------------------------------------------------

func TestName(t *testing.T) {
	v := &variant{}
	if v.Name() != "v0-baseline" {
		t.Errorf("Name() = %q", v.Name())
	}
	if Name != "v0-baseline" {
		t.Errorf("package Name constant drifted")
	}
}

// --- extractDomain --------------------------------------------------------

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"https://go.dev/doc", "go.dev"},
		{"https://www.python.org", "python.org"},
		{"http://WWW.EXAMPLE.COM/foo", "example.com"},
		{"not a url", "not a url"},
	}
	for _, tt := range tests {
		got := extractDomain(tt.in)
		if got != tt.want {
			t.Errorf("extractDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- Registry self-registration -------------------------------------------

// We don't call variants.Register directly here because the package's
// own init() already registers v0-baseline at import time. Exercising
// it would require a reset helper and would tangle this test with
// the registry package's internals. The bench CLI test
// (cmd/diting/bench_test.go::TestRunBenchRun_EndToEnd) verifies the
// registry flow end-to-end with a separate fake variant.
