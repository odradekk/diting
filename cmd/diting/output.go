package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/pricing"
	"github.com/odradekk/diting/internal/search"
)

// outputFormat is the rendering mode for `diting search` results.
type outputFormat int

const (
	formatText outputFormat = iota
	formatJSON
	formatMarkdown
)

// parseOutputFormat converts a --format flag value to an outputFormat.
// Accepted values (case-insensitive): "text" / "txt" / "t",
// "json" / "j", "markdown" / "md" / "m".
func parseOutputFormat(s string) (outputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text", "txt", "t":
		return formatText, nil
	case "json", "j":
		return formatJSON, nil
	case "markdown", "md", "m":
		return formatMarkdown, nil
	default:
		return 0, fmt.Errorf("unknown format %q (want text|json|markdown)", s)
	}
}

// --- search output -----------------------------------------------------------

type searchJSONResult struct {
	Question string              `json:"question"`
	Plan     pipeline.Plan       `json:"plan"`
	Answer   pipeline.Answer     `json:"answer"`
	Sources  []searchJSONSource  `json:"sources"`
	Debug    *pipeline.DebugInfo `json:"debug,omitempty"`
}

type searchJSONSource struct {
	ID         int     `json:"id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	SourceType string  `json:"source_type"`
	Score      float64 `json:"score"`
	Fetched    bool    `json:"fetched"`
}

// renderOptions carries optional rendering context that's only used by
// a subset of paths. Keeping it in a struct avoids yet another parameter
// on printSearchText/Markdown as features accrete.
type renderOptions struct {
	// Model is the resolved LLM model name — used by the debug output
	// to compute actual cost via the pricing table. Empty string means
	// "unknown model, skip cost line".
	Model string
}

// renderSearch dispatches to the appropriate format renderer.
func renderSearch(w io.Writer, r *pipeline.Result, format outputFormat, showDebug bool, opts renderOptions) error {
	switch format {
	case formatJSON:
		return printSearchJSON(w, r)
	case formatMarkdown:
		return printSearchMarkdown(w, r, showDebug, opts)
	case formatText:
		return printSearchText(w, r, showDebug, opts)
	default:
		return fmt.Errorf("unknown format: %d", format)
	}
}

// resultMode classifies a Result for output routing:
//
//   - plan-only: no sources, no answer (the --plan-only short-circuit)
//   - raw:       sources present but no answer (the --raw short-circuit)
//   - full:      both sources and answer (the default pipeline path)
type resultMode int

const (
	modeFull resultMode = iota
	modePlanOnly
	modeRaw
)

func classifyResult(r *pipeline.Result) resultMode {
	if r.Answer.Text != "" {
		return modeFull
	}
	if len(r.Sources) > 0 {
		return modeRaw
	}
	return modePlanOnly
}

func printSearchJSON(w io.Writer, r *pipeline.Result) error {
	jr := searchJSONResult{
		Question: r.Question,
		Plan:     r.Plan,
		Answer:   r.Answer,
		Debug:    &r.Debug,
	}
	for _, s := range r.Sources {
		jr.Sources = append(jr.Sources, searchJSONSource{
			ID:         s.ID,
			Title:      s.Result.Title,
			URL:        s.Result.URL,
			SourceType: string(s.Result.SourceType),
			Score:      s.Result.Score,
			Fetched:    s.Fetched != nil,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jr)
}

func printSearchText(w io.Writer, r *pipeline.Result, showDebug bool, opts renderOptions) error {
	switch classifyResult(r) {
	case modePlanOnly:
		writePlanText(w, r)
	case modeRaw:
		writePlanText(w, r)
		writeSourcesText(w, r)
	case modeFull:
		writeAnswerText(w, r)
	}
	if showDebug {
		writeDebugText(w, r, opts)
	}
	return nil
}

func writePlanText(w io.Writer, r *pipeline.Result) {
	total := r.Plan.TotalQueries()
	nonEmptyTypes := countNonEmpty(r.Plan.QueriesBySourceType)

	fmt.Fprintln(w, "=== Plan ===")
	fmt.Fprintf(w, "Rationale: %s\n", r.Plan.Rationale)
	fmt.Fprintf(w, "Queries:   %d across %s\n", total, pluralSourceTypes(nonEmptyTypes))
	for _, st := range sortedSourceTypes(r.Plan.QueriesBySourceType) {
		qs := r.Plan.QueriesBySourceType[st]
		if len(qs) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n[%s]\n", st)
		for _, q := range qs {
			fmt.Fprintf(w, "  - %s\n", q)
		}
	}
	fmt.Fprintf(w, "\nExpected answer: %s\n", r.Plan.ExpectedAnswerShape)
}

func writeSourcesText(w io.Writer, r *pipeline.Result) {
	fmt.Fprintln(w, "\n=== Sources ===")
	for _, s := range r.Sources {
		fmt.Fprintf(w, "\n[%d] %s\n", s.ID, s.Result.Title)
		fmt.Fprintf(w, "    URL:     %s\n", s.Result.URL)
		fmt.Fprintf(w, "    Source:  %s · score %.2f", s.Result.SourceType, s.Result.Score)
		if s.Fetched != nil {
			fmt.Fprintf(w, " · fetched (%d chars)", len(s.Fetched.Content))
		} else {
			fmt.Fprintf(w, " · not fetched")
		}
		fmt.Fprintln(w)
		if snippet := strings.TrimSpace(s.Result.Snippet); snippet != "" {
			fmt.Fprintf(w, "    Snippet: %s\n", snippet)
		}
	}
}

func writeAnswerText(w io.Writer, r *pipeline.Result) {
	fmt.Fprintln(w, r.Answer.Text)

	if len(r.Answer.Citations) > 0 {
		fmt.Fprintln(w, "\nSources:")
		for _, c := range r.Answer.Citations {
			fmt.Fprintf(w, "  [%d] %s\n      %s\n", c.ID, c.Title, c.URL)
		}
	}

	fmt.Fprintf(w, "\nConfidence: %s\n", r.Answer.Confidence)
}

func writeDebugText(w io.Writer, r *pipeline.Result, opts renderOptions) {
	fmt.Fprintln(w, "\n--- Debug ---")
	fmt.Fprintf(w, "Plan tokens:   %d in / %d out (cache: %d)\n",
		r.Debug.PlanInputTokens, r.Debug.PlanOutputTokens, r.Debug.PlanCacheReadTokens)
	// Skip answer tokens when the answer phase didn't run (--plan-only / --raw).
	if r.Debug.AnswerInputTokens > 0 || r.Debug.AnswerOutputTokens > 0 {
		fmt.Fprintf(w, "Answer tokens: %d in / %d out (cache: %d)\n",
			r.Debug.AnswerInputTokens, r.Debug.AnswerOutputTokens, r.Debug.AnswerCacheReadTokens)
	}
	fmt.Fprintf(w, "Search:        %d results → %d selected → %d fetched\n",
		r.Debug.TotalSearchResults, r.Debug.SelectedSources, r.Debug.FetchedSources)
	writeCostLineText(w, r, opts)
}

// writeCostLineText prints a "Cost: $X.XXXX (...)" line below the debug
// block when a model name is available. Uses the same pricing table as
// the upfront --max-cost guard so the two values can be compared.
func writeCostLineText(w io.Writer, r *pipeline.Result, opts renderOptions) {
	if opts.Model == "" {
		return
	}
	planUSD, answerUSD, totalUSD := actualCost(opts.Model, r.Debug)
	// Don't print a zero-cost line — that just means no tokens flowed
	// (plan-only with stub, or a cached run).
	if totalUSD == 0 {
		return
	}
	fmt.Fprintf(w, "Cost:          %s (plan %s + answer %s, model %s)\n",
		pricing.FormatUSD(totalUSD),
		pricing.FormatUSD(planUSD),
		pricing.FormatUSD(answerUSD),
		opts.Model,
	)
}

// printSearchMarkdown renders a result as GitHub-flavoured markdown.
func printSearchMarkdown(w io.Writer, r *pipeline.Result, showDebug bool, opts renderOptions) error {
	switch classifyResult(r) {
	case modePlanOnly:
		fmt.Fprintf(w, "# Plan: %s\n\n", mdEscape(r.Question))
		writePlanMarkdown(w, r)
	case modeRaw:
		fmt.Fprintf(w, "# Raw Results: %s\n\n", mdEscape(r.Question))
		writePlanMarkdown(w, r)
		writeSourcesMarkdown(w, r)
	case modeFull:
		fmt.Fprintf(w, "# %s\n\n", mdEscape(r.Question))
		writeAnswerMarkdown(w, r)
	}
	if showDebug {
		writeDebugMarkdown(w, r, opts)
	}
	return nil
}

func writePlanMarkdown(w io.Writer, r *pipeline.Result) {
	if r.Plan.Rationale != "" {
		fmt.Fprintf(w, "**Rationale**: %s\n\n", mdEscape(r.Plan.Rationale))
	}
	total := r.Plan.TotalQueries()
	nonEmptyTypes := countNonEmpty(r.Plan.QueriesBySourceType)
	fmt.Fprintf(w, "**Queries**: %d across %s\n\n", total, pluralSourceTypes(nonEmptyTypes))
	fmt.Fprintln(w, "## Queries")
	for _, st := range sortedSourceTypes(r.Plan.QueriesBySourceType) {
		qs := r.Plan.QueriesBySourceType[st]
		if len(qs) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n### %s\n\n", st)
		for _, q := range qs {
			fmt.Fprintf(w, "- %s\n", mdEscape(q))
		}
	}
	if r.Plan.ExpectedAnswerShape != "" {
		fmt.Fprintf(w, "\n**Expected answer**: %s\n", mdEscape(r.Plan.ExpectedAnswerShape))
	}
}

func writeSourcesMarkdown(w io.Writer, r *pipeline.Result) {
	fmt.Fprintln(w, "\n## Sources")
	for _, s := range r.Sources {
		title := mdEscape(s.Result.Title)
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(w, "\n### %d. [%s](%s)\n\n", s.ID, title, s.Result.URL)
		fmt.Fprintf(w, "- **Source type**: `%s`\n", s.Result.SourceType)
		fmt.Fprintf(w, "- **Score**: %.2f\n", s.Result.Score)
		if s.Fetched != nil {
			fmt.Fprintf(w, "- **Fetched**: yes (%d chars)\n", len(s.Fetched.Content))
		} else {
			fmt.Fprintln(w, "- **Fetched**: no")
		}
		if snippet := strings.TrimSpace(s.Result.Snippet); snippet != "" {
			fmt.Fprintf(w, "- **Snippet**: %s\n", mdEscape(snippet))
		}
	}
}

func writeAnswerMarkdown(w io.Writer, r *pipeline.Result) {
	fmt.Fprintln(w, r.Answer.Text)

	if len(r.Answer.Citations) > 0 {
		fmt.Fprintln(w, "\n## Sources")
		fmt.Fprintln(w)
		for _, c := range r.Answer.Citations {
			title := mdEscape(c.Title)
			if title == "" {
				title = "(untitled)"
			}
			st := string(c.SourceType)
			if st == "" {
				fmt.Fprintf(w, "%d. [%s](%s)\n", c.ID, title, c.URL)
			} else {
				fmt.Fprintf(w, "%d. [%s](%s) — `%s`\n", c.ID, title, c.URL, st)
			}
		}
	}

	fmt.Fprintf(w, "\n**Confidence**: %s\n", r.Answer.Confidence)
}

func writeDebugMarkdown(w io.Writer, r *pipeline.Result, opts renderOptions) {
	fmt.Fprintln(w, "\n---\n\n## Debug")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "- **Plan tokens**: %d in / %d out (cache: %d)\n",
		r.Debug.PlanInputTokens, r.Debug.PlanOutputTokens, r.Debug.PlanCacheReadTokens)
	// Skip answer tokens when the answer phase didn't run (--plan-only / --raw).
	if r.Debug.AnswerInputTokens > 0 || r.Debug.AnswerOutputTokens > 0 {
		fmt.Fprintf(w, "- **Answer tokens**: %d in / %d out (cache: %d)\n",
			r.Debug.AnswerInputTokens, r.Debug.AnswerOutputTokens, r.Debug.AnswerCacheReadTokens)
	}
	fmt.Fprintf(w, "- **Search**: %d results → %d selected → %d fetched\n",
		r.Debug.TotalSearchResults, r.Debug.SelectedSources, r.Debug.FetchedSources)
	writeCostLineMarkdown(w, r, opts)
}

func writeCostLineMarkdown(w io.Writer, r *pipeline.Result, opts renderOptions) {
	if opts.Model == "" {
		return
	}
	planUSD, answerUSD, totalUSD := actualCost(opts.Model, r.Debug)
	if totalUSD == 0 {
		return
	}
	fmt.Fprintf(w, "- **Cost**: %s (plan %s + answer %s, model `%s`)\n",
		pricing.FormatUSD(totalUSD),
		pricing.FormatUSD(planUSD),
		pricing.FormatUSD(answerUSD),
		opts.Model,
	)
}

// sortedSourceTypes returns source-type keys in a deterministic order so
// output is stable across runs (maps iterate in random order).
func sortedSourceTypes(m map[search.SourceType][]string) []search.SourceType {
	keys := make([]search.SourceType, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// countNonEmpty counts map entries whose slice value has at least one element.
// Used to report "N queries across M source types" in plan output.
func countNonEmpty(m map[search.SourceType][]string) int {
	n := 0
	for _, v := range m {
		if len(v) > 0 {
			n++
		}
	}
	return n
}

// pluralSourceTypes renders "N source type" or "N source types" so the
// plan summary reads naturally for both singular and plural counts.
func pluralSourceTypes(n int) string {
	if n == 1 {
		return "1 source type"
	}
	return fmt.Sprintf("%d source types", n)
}

// mdEscape escapes markdown metacharacters in inline text. We deliberately
// keep this minimal — enough to stop a stray `*` or `[` from breaking a
// heading or list item, but not so aggressive that it mangles answers that
// already contain valid markdown (the LLM often emits markdown in the
// Answer.Text field, which we pass through unchanged).
func mdEscape(s string) string {
	// Collapse newlines to spaces for inline contexts (headings, list items).
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
