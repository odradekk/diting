package bench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errNoFixture is a test-only sentinel returned by fixtureVariant when a
// query has no canned Result registered. The runner captures it in
// Result.Metadata["error"].
var errNoFixture = errors.New("fixture: no canned result for query")

// fixtureVariant is a Variant that returns pre-recorded Results loaded from
// test/bench/testdata/fixtures. Queries without a matching fixture produce
// errNoFixture.
type fixtureVariant struct {
	fixtures map[string]Result
}

func (f *fixtureVariant) Name() string { return "fixture" }

func (f *fixtureVariant) Run(_ context.Context, in RunInput) (Result, error) {
	if r, ok := f.fixtures[in.ID]; ok {
		return r, nil
	}
	return Result{}, errNoFixture
}

func loadFixtureResult(t *testing.T, path string) Result {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q: %v", path, err)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parse fixture %q: %v", path, err)
	}
	return r
}

func newFixtureVariant(t *testing.T) *fixtureVariant {
	t.Helper()
	fixturesDir := filepath.Join(testDataDir(t), "test", "bench", "testdata", "fixtures")
	return &fixtureVariant{
		fixtures: map[string]Result{
			"et_001": loadFixtureResult(t, filepath.Join(fixturesDir, "perfect_answer.json")),
			"et_003": loadFixtureResult(t, filepath.Join(fixturesDir, "partial_answer.json")),
			"et_005": loadFixtureResult(t, filepath.Join(fixturesDir, "polluted_answer.json")),
		},
	}
}

func TestHarness_EndToEnd_WithFixtureVariant(t *testing.T) {
	qset, err := LoadAndValidate(realQueriesPath(t))
	if err != nil {
		t.Fatalf("LoadAndValidate: %v", err)
	}
	if len(qset.Batches) != 7 {
		t.Errorf("batches = %d, want 7", len(qset.Batches))
	}
	if qset.TotalQueries() != 50 {
		t.Errorf("total queries = %d, want 50", qset.TotalQueries())
	}

	v := newFixtureVariant(t)
	runner := NewRunner(v, WithConcurrency(4))
	report, err := runner.Run(context.Background(), qset)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Results) != 50 {
		t.Fatalf("len(Results) = %d, want 50", len(report.Results))
	}

	scorer := NewScorer()
	report.PopulateScores(scorer, qset)

	if len(report.Scores) != 50 {
		t.Fatalf("len(Scores) = %d, want 50", len(report.Scores))
	}

	// Index scores by ID for assertions.
	byID := map[string]Score{}
	for _, s := range report.Scores {
		byID[s.QueryID] = s
	}
	perfect, ok := byID["et_001"]
	if !ok {
		t.Fatal("et_001 score missing")
	}
	partial, ok := byID["et_003"]
	if !ok {
		t.Fatal("et_003 score missing")
	}
	polluted, ok := byID["et_005"]
	if !ok {
		t.Fatal("et_005 score missing")
	}

	if perfect.Composite < 90 {
		t.Errorf("perfect composite = %.2f, want >= 90", perfect.Composite)
	}
	if partial.Composite < 65 || partial.Composite > 85 {
		t.Errorf("partial composite = %.2f, want in [65, 85]", partial.Composite)
	}
	if polluted.Composite <= 0 {
		t.Errorf("polluted composite = %.2f, want > 0", polluted.Composite)
	}
	if partial.Composite-polluted.Composite < 10 {
		t.Errorf("partial %.2f - polluted %.2f = %.2f, want >= 10",
			partial.Composite, polluted.Composite, partial.Composite-polluted.Composite)
	}

	// Queries without a fixture should have an error in metadata on the
	// Result; the scorer runs on the Result as-is (zero citations / answer).
	var errored int
	for _, r := range report.Results {
		if r.QueryID == "et_001" || r.QueryID == "et_003" || r.QueryID == "et_005" {
			if _, bad := r.Metadata["error"]; bad {
				t.Errorf("%s should not have error metadata: %v", r.QueryID, r.Metadata)
			}
			continue
		}
		msg, hasErr := r.Metadata["error"]
		if !hasErr {
			t.Errorf("%s missing error metadata", r.QueryID)
			continue
		}
		if s, _ := msg.(string); !strings.Contains(s, "fixture") {
			t.Errorf("%s error = %v, want 'fixture'", r.QueryID, msg)
		}
		errored++
	}
	if errored != 47 {
		t.Errorf("errored queries = %d, want 47", errored)
	}

	// Render the Markdown report and assert substrings.
	rp := &Reporter{}
	md, err := rp.Markdown(report)
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	out := string(md)
	if len(out) == 0 {
		t.Fatal("markdown report empty")
	}
	if !strings.Contains(out, "fixture") {
		t.Errorf("markdown does not contain 'fixture': %s", out)
	}
	if !strings.Contains(out, "et_001") && !strings.Contains(out, "et_003") && !strings.Contains(out, "et_005") {
		t.Errorf("markdown does not mention any fixture query ID")
	}
	// Overall composite must be populated.
	if report.Overall.SampleSize != 50 {
		t.Errorf("Overall.SampleSize = %d, want 50", report.Overall.SampleSize)
	}
	if len(report.CategoryAgg) == 0 {
		t.Error("CategoryAgg empty")
	}
}
