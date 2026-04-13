package bench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func fixedTimeReporter() *Reporter {
	return &Reporter{
		Now: func() time.Time {
			return time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
		},
	}
}

func makeReportForMarkdown() *RunReport {
	report := &RunReport{
		Variant:   "fixture",
		StartedAt: time.Date(2026, 4, 11, 11, 58, 0, 0, time.UTC),
		Duration:  90 * time.Second,
		Results: []Result{
			{QueryID: "a", Answer: "perfect"},
			{QueryID: "b", Answer: "partial"},
			{QueryID: "c", Answer: "weak"},
		},
		Scores: []Score{
			{QueryID: "a", DomainHitRate: 1, TermCoverage: 1, PollutionSuppression: 1, SourceTypeDiversity: 1, LatencyScore: 1, CostScore: 1, Composite: 95},
			{QueryID: "b", DomainHitRate: 0.5, TermCoverage: 0.5, PollutionSuppression: 1, SourceTypeDiversity: 0.5, LatencyScore: 1, CostScore: 1, Composite: 70},
			{QueryID: "c", DomainHitRate: 0, TermCoverage: 0, PollutionSuppression: 0.5, SourceTypeDiversity: 0, LatencyScore: 0.5, CostScore: 0.5, Composite: 15},
		},
		CategoryAgg: map[Category]AggScore{
			CategoryErrorTroubleshooting: {
				Mean:       60,
				SampleSize: 3,
				PerMetric: map[string]float64{
					MetricDomainHit:       0.5,
					MetricTermCoverage:    0.5,
					MetricPollution:       0.83,
					MetricSourceDiversity: 0.5,
					MetricLatency:         0.83,
					MetricCost:            0.83,
				},
			},
		},
		Overall: AggScore{
			Mean:       60,
			SampleSize: 3,
			PerMetric: map[string]float64{
				MetricDomainHit:       0.5,
				MetricTermCoverage:    0.5,
				MetricPollution:       0.83,
				MetricSourceDiversity: 0.5,
				MetricLatency:         0.83,
				MetricCost:            0.83,
			},
		},
	}
	return report
}

func TestReporter_MarkdownContainsVariantName(t *testing.T) {
	rp := fixedTimeReporter()
	out, err := rp.Markdown(makeReportForMarkdown())
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "# Bench Report — fixture") {
		t.Errorf("missing variant header; got:\n%s", s)
	}
	if !strings.Contains(s, "60.0/100") {
		t.Errorf("missing composite: %s", s)
	}
	if !strings.Contains(s, "3 queries") {
		t.Errorf("missing sample size: %s", s)
	}
}

func TestReporter_MarkdownContainsPerCategoryTable(t *testing.T) {
	rp := fixedTimeReporter()
	out, err := rp.Markdown(makeReportForMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "## Per-category") {
		t.Errorf("missing per-category section")
	}
	if !strings.Contains(s, "error_troubleshooting") {
		t.Errorf("missing category row")
	}
	if !strings.Contains(s, "## Per-metric drill-down") {
		t.Errorf("missing per-metric section")
	}
	if !strings.Contains(s, "Domain hit rate") {
		t.Errorf("missing metric row")
	}
	if !strings.Contains(s, "## Top-10 worst queries") {
		t.Errorf("missing worst-queries section")
	}
	// Narrow the ordering assertion to the worst-queries section so the
	// separate best-queries section does not confuse it.
	worstIdx := strings.Index(s, "## Top-10 worst queries")
	if worstIdx < 0 {
		t.Fatal("worst section not found")
	}
	worstSection := s[worstIdx:]
	ic := strings.Index(worstSection, "| c |")
	ib := strings.Index(worstSection, "| b |")
	ia := strings.Index(worstSection, "| a |")
	if ic < 0 || ib < 0 || ia < 0 {
		t.Fatalf("missing worst rows; out:\n%s", worstSection)
	}
	if !(ic < ib && ib < ia) {
		t.Errorf("worst-queries order wrong: c=%d b=%d a=%d", ic, ib, ia)
	}
}

func TestReporter_IsDeterministic(t *testing.T) {
	rp := fixedTimeReporter()
	a, err := rp.Markdown(makeReportForMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	b, err := rp.Markdown(makeReportForMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("reports not byte-equal:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

func TestReporter_NilReport(t *testing.T) {
	rp := &Reporter{}
	if _, err := rp.Markdown(nil); err == nil {
		t.Error("expected error on nil report")
	}
}

func TestReporter_CommitHashRendered(t *testing.T) {
	rp := fixedTimeReporter()
	rp.CommitHash = "abc1234"
	out, err := rp.Markdown(makeReportForMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "abc1234") {
		t.Errorf("commit hash not rendered")
	}
}

// TestReporter_RendersFailureMetadata guards Phase 5.7's report-blindness
// bug: queries whose runner captured an error in Metadata["error"] used to
// be invisible in the markdown report (they showed up as "(no answer)" in
// the worst-queries table). The new "Failed queries" section surfaces the
// error string so a reader can diagnose without grepping the JSON dump.
func TestReporter_RendersFailureMetadata(t *testing.T) {
	report := makeReportForMarkdown()
	// Mark query "c" as a runtime failure with an error string. Query "a"
	// stays clean to confirm the failure section only renders rows that
	// actually have an error.
	for i := range report.Results {
		if report.Results[i].QueryID == "c" {
			report.Results[i].Metadata = map[string]any{
				"error": "openai: rate limit exceeded (429)",
			}
		}
	}

	rp := fixedTimeReporter()
	out, err := rp.Markdown(report)
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "## Failed queries") {
		t.Errorf("missing Failed queries section:\n%s", s)
	}
	if !strings.Contains(s, "1 of 3 queries reported an error") {
		t.Errorf("missing failure count line:\n%s", s)
	}
	if !strings.Contains(s, "rate limit exceeded") {
		t.Errorf("missing error message:\n%s", s)
	}
	// Negative: query "a" must NOT appear in the failure table.
	failIdx := strings.Index(s, "## Failed queries")
	if failIdx >= 0 {
		failSection := s[failIdx:]
		if strings.Contains(failSection, "| a |") {
			t.Errorf("clean query 'a' should not appear in failure section:\n%s", failSection)
		}
	}
}

// TestReporter_NoFailureSectionWhenAllClean confirms the Failed queries
// section is omitted entirely when no result has an error — a clean run
// shouldn't carry an empty error table.
func TestReporter_NoFailureSectionWhenAllClean(t *testing.T) {
	rp := fixedTimeReporter()
	out, err := rp.Markdown(makeReportForMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "## Failed queries") {
		t.Errorf("Failed queries section should be absent for clean run:\n%s", out)
	}
}

// TestCollectFailures_IgnoresNonStringError defends against malformed
// metadata: only string-typed "error" entries are surfaced. Anything
// else (nil, number, bool) is silently skipped.
func TestCollectFailures_IgnoresNonStringError(t *testing.T) {
	results := []Result{
		{QueryID: "ok", Metadata: nil},
		{QueryID: "empty", Metadata: map[string]any{"error": ""}},
		{QueryID: "wrongtype", Metadata: map[string]any{"error": 42}},
		{QueryID: "good", Metadata: map[string]any{"error": "boom"}},
	}
	got := collectFailures(results)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got %+v", len(got), got)
	}
	if got[0].ID != "good" || !strings.Contains(got[0].Error, "boom") {
		t.Errorf("got = %+v", got[0])
	}
}
