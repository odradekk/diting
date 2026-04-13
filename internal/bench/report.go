package bench

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"
)

// Reporter renders a RunReport as Markdown.
type Reporter struct {
	// Now is injected for testability. Defaults to time.Now.
	Now func() time.Time
	// CommitHash is optional; left empty if the caller does not inject a
	// git rev. The library layer never shells out to git — Phase 4.x CLI
	// injects.
	CommitHash string
}

// reportView is the template's input model. Building it explicitly keeps
// sort order deterministic and shields the template from map iteration.
type reportView struct {
	Variant       string
	GeneratedAt   string
	StartedAt     string
	Duration      string
	CommitHash    string
	OverallFmt    string
	SampleSize    int
	Categories    []categoryRow
	Metrics       []metricRow
	BestQueries   []worstRow
	WorstQueries  []worstRow
	Failures      []failureRow
	FailureCount  int
	HasCommitHash bool
	HasBest       bool
	HasWorst      bool
	HasCategories bool
	HasFailures   bool
}

type categoryRow struct {
	Name            string
	N               int
	Composite       string
	DomainHit       string
	TermCoverage    string
	Pollution       string
	SourceDiversity string
}

type metricRow struct {
	Name string
	Mean string
}

type worstRow struct {
	ID           string
	Category     string
	Composite    string
	QueryExcerpt string
	DomainHits   string
	TermHits     string
}

// failureRow holds one row of the "Failed queries" section. Errors are
// captured by the runner into Result.Metadata["error"] and surfaced here
// so a reader of the markdown can see WHY queries scored at the floor
// without grepping the sibling JSON dump.
type failureRow struct {
	ID    string
	Error string
}

// Markdown renders the report as a Markdown document. Format follows
// architecture §12.5: header, composite summary, per-category breakdown,
// per-metric drill-down, top-10 worst queries.
func (rp *Reporter) Markdown(report *RunReport) ([]byte, error) {
	if report == nil {
		return nil, fmt.Errorf("bench reporter: nil RunReport")
	}
	now := time.Now
	if rp.Now != nil {
		now = rp.Now
	}

	view := rp.buildView(report, now())

	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("bench reporter: execute template: %w", err)
	}
	return buf.Bytes(), nil
}

func (rp *Reporter) buildView(report *RunReport, now time.Time) reportView {
	v := reportView{
		Variant:       report.Variant,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		StartedAt:     report.StartedAt.UTC().Format(time.RFC3339),
		Duration:      report.Duration.Round(time.Millisecond).String(),
		CommitHash:    rp.CommitHash,
		HasCommitHash: rp.CommitHash != "",
		SampleSize:    report.Overall.SampleSize,
		OverallFmt:    fmt.Sprintf("%.1f", report.Overall.Mean),
	}

	// Per-category rows in the canonical order from AllCategories().
	if len(report.CategoryAgg) > 0 {
		v.HasCategories = true
		for _, cat := range AllCategories() {
			agg, ok := report.CategoryAgg[cat]
			if !ok {
				continue
			}
			v.Categories = append(v.Categories, categoryRow{
				Name:            escapeTableCell(string(cat)),
				N:               agg.SampleSize,
				Composite:       fmt.Sprintf("%.1f", agg.Mean),
				DomainHit:       fmt.Sprintf("%.2f", agg.PerMetric[MetricDomainHit]),
				TermCoverage:    fmt.Sprintf("%.2f", agg.PerMetric[MetricTermCoverage]),
				Pollution:       fmt.Sprintf("%.2f", agg.PerMetric[MetricPollution]),
				SourceDiversity: fmt.Sprintf("%.2f", agg.PerMetric[MetricSourceDiversity]),
			})
		}
	}

	// Per-metric drill-down in fixed order.
	metricOrder := []struct {
		key, label string
	}{
		{MetricDomainHit, "Domain hit rate"},
		{MetricTermCoverage, "Term coverage"},
		{MetricPollution, "Pollution suppression"},
		{MetricSourceDiversity, "Source-type diversity"},
		{MetricLatency, "Latency"},
		{MetricCost, "Cost"},
	}
	for _, m := range metricOrder {
		v.Metrics = append(v.Metrics, metricRow{
			Name: m.label,
			Mean: fmt.Sprintf("%.2f", report.Overall.PerMetric[m.key]),
		})
	}

	// Top-10 worst queries (lowest composite). Deterministic: sort by
	// composite ascending, then by QueryID ascending as tiebreaker.
	type scoreWithMeta struct {
		score Score
		res   Result
	}
	resByID := make(map[string]Result, len(report.Results))
	for _, r := range report.Results {
		resByID[r.QueryID] = r
	}
	enriched := make([]scoreWithMeta, 0, len(report.Scores))
	for _, s := range report.Scores {
		enriched = append(enriched, scoreWithMeta{score: s, res: resByID[s.QueryID]})
	}
	sort.SliceStable(enriched, func(i, j int) bool {
		if enriched[i].score.Composite != enriched[j].score.Composite {
			return enriched[i].score.Composite < enriched[j].score.Composite
		}
		return enriched[i].score.QueryID < enriched[j].score.QueryID
	})

	limit := 10
	if limit > len(enriched) {
		limit = len(enriched)
	}
	if limit > 0 {
		v.HasWorst = true
		v.HasBest = true
	}
	for i := 0; i < limit; i++ {
		v.WorstQueries = append(v.WorstQueries, makeWorstRow(enriched[i].score, enriched[i].res))
	}

	// Best list: sort a copy by Composite desc, QueryID asc so ties have a
	// canonical order instead of reverse-walking the worst sort.
	best := make([]scoreWithMeta, len(enriched))
	copy(best, enriched)
	sort.SliceStable(best, func(i, j int) bool {
		if best[i].score.Composite != best[j].score.Composite {
			return best[i].score.Composite > best[j].score.Composite
		}
		return best[i].score.QueryID < best[j].score.QueryID
	})
	for i := 0; i < limit; i++ {
		v.BestQueries = append(v.BestQueries, makeWorstRow(best[i].score, best[i].res))
	}

	// Failures: every Result whose Metadata carries an "error" string is
	// rendered into a dedicated section so the reader of the markdown can
	// see WHY a query scored at the floor. Sorted by QueryID for stability.
	failures := collectFailures(report.Results)
	if len(failures) > 0 {
		v.HasFailures = true
		v.FailureCount = len(failures)
		v.Failures = failures
	}

	return v
}

// collectFailures scans Results for queries whose runner-captured error
// metadata is set and returns them in a deterministic (QueryID-sorted)
// order. The error message is truncated to keep the markdown table
// readable; the full text always lives in the sibling .json dump.
func collectFailures(results []Result) []failureRow {
	out := make([]failureRow, 0)
	for _, r := range results {
		if r.Metadata == nil {
			continue
		}
		raw, ok := r.Metadata["error"]
		if !ok {
			continue
		}
		msg, ok := raw.(string)
		if !ok || msg == "" {
			continue
		}
		out = append(out, failureRow{
			ID:    escapeTableCell(r.QueryID),
			Error: escapeTableCell(shorten(msg, 140)),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func makeWorstRow(s Score, r Result) worstRow {
	excerpt := shorten(r.Answer, 80)
	if excerpt == "" {
		excerpt = "(no answer)"
	}
	return worstRow{
		ID:           escapeTableCell(s.QueryID),
		Composite:    fmt.Sprintf("%.1f", s.Composite),
		QueryExcerpt: escapeTableCell(excerpt),
		DomainHits:   fmt.Sprintf("%.2f", s.DomainHitRate),
		TermHits:     fmt.Sprintf("%.2f", s.TermCoverage),
	}
}

// escapeTableCell replaces characters that break Markdown table layout.
// Pipes become \| and stray newlines become spaces. Callers should pre-trim
// the text; this function is a last-resort defense.
func escapeTableCell(s string) string {
	if s == "" {
		return s
	}
	replacer := strings.NewReplacer(
		"|", `\|`,
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(s)
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

var reportTmpl = template.Must(template.New("bench-report").Parse(`# Bench Report — {{.Variant}}

- Generated: {{.GeneratedAt}}
- Started: {{.StartedAt}}
- Duration: {{.Duration}}
{{- if .HasCommitHash}}
- Commit: {{.CommitHash}}
{{- end}}

## Composite

Composite: {{.OverallFmt}}/100 across {{.SampleSize}} queries
{{if .HasCategories}}
## Per-category

| Category | N | Composite | Domain hit | Term cov | Pollution | Source div |
|---|---|---|---|---|---|---|
{{- range .Categories}}
| {{.Name}} | {{.N}} | {{.Composite}} | {{.DomainHit}} | {{.TermCoverage}} | {{.Pollution}} | {{.SourceDiversity}} |
{{- end}}
{{end}}
## Per-metric drill-down

| Metric | Mean |
|---|---|
{{- range .Metrics}}
| {{.Name}} | {{.Mean}} |
{{- end}}
{{if .HasBest}}
## Top-10 best queries

| ID | Composite | Domain hit | Term cov | Excerpt |
|---|---|---|---|---|
{{- range .BestQueries}}
| {{.ID}} | {{.Composite}} | {{.DomainHits}} | {{.TermHits}} | {{.QueryExcerpt}} |
{{- end}}
{{end}}
{{if .HasWorst}}
## Top-10 worst queries

| ID | Composite | Domain hit | Term cov | Excerpt |
|---|---|---|---|---|
{{- range .WorstQueries}}
| {{.ID}} | {{.Composite}} | {{.DomainHits}} | {{.TermHits}} | {{.QueryExcerpt}} |
{{- end}}
{{end}}
{{if .HasFailures}}
## Failed queries

{{.FailureCount}} of {{.SampleSize}} queries reported an error during the
run. The full error metadata + per-query Result is in the sibling ` + "`.json`" + ` dump.

| ID | Error |
|---|---|
{{- range .Failures}}
| {{.ID}} | {{.Error}} |
{{- end}}
{{end}}`))
