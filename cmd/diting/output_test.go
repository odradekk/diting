package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/search"
)

// --- parseOutputFormat -------------------------------------------------------

func TestParseOutputFormat_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  outputFormat
	}{
		{"", formatText},
		{"text", formatText},
		{"TEXT", formatText},
		{"txt", formatText},
		{"t", formatText},
		{"json", formatJSON},
		{"JSON", formatJSON},
		{"j", formatJSON},
		{"markdown", formatMarkdown},
		{"MARKDOWN", formatMarkdown},
		{"md", formatMarkdown},
		{"m", formatMarkdown},
		{"  markdown  ", formatMarkdown},
	}
	for _, tt := range tests {
		got, err := parseOutputFormat(tt.input)
		if err != nil {
			t.Errorf("parseOutputFormat(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseOutputFormat(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseOutputFormat_Invalid(t *testing.T) {
	tests := []string{"yaml", "xml", "html", "txjson", "foobar"}
	for _, in := range tests {
		_, err := parseOutputFormat(in)
		if err == nil {
			t.Errorf("parseOutputFormat(%q) expected error", in)
		}
	}
}

// --- fixtures ----------------------------------------------------------------

func fullResult() *pipeline.Result {
	return &pipeline.Result{
		Question: "How do Go channels work?",
		Plan: pipeline.Plan{
			Rationale: "Need official docs plus community patterns.",
			QueriesBySourceType: map[search.SourceType][]string{
				"docs":          {"go channels", "goroutine synchronization"},
				"community":     {"buffered vs unbuffered channel"},
				"general_web":   {"go channel tutorial"},
			},
			ExpectedAnswerShape: "Explanation with code samples and citations.",
		},
		Answer: pipeline.Answer{
			Text: "Go channels are typed conduits for goroutines [1]. " +
				"Buffered channels have capacity; unbuffered channels block until a receiver is ready [2].",
			Citations: []pipeline.Citation{
				{ID: 1, URL: "https://go.dev/tour/concurrency/2", Title: "A Tour of Go — Channels", SourceType: "docs"},
				{ID: 2, URL: "https://gobyexample.com/channel-buffering", Title: "Go by Example: Channel Buffering", SourceType: "community"},
			},
			Confidence: "high",
		},
		Sources: []pipeline.FetchedSource{
			{
				ID: 1,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						Title:      "A Tour of Go — Channels",
						URL:        "https://go.dev/tour/concurrency/2",
						SourceType: "docs",
						Snippet:    "Channels are typed conduits...",
					},
					Score: 0.92,
				},
			},
			{
				ID: 2,
				Result: pipeline.ScoredResult{
					SearchResult: search.SearchResult{
						Title:      "Go by Example: Channel Buffering",
						URL:        "https://gobyexample.com/channel-buffering",
						SourceType: "community",
						Snippet:    "Buffered channels accept a limited number...",
					},
					Score: 0.75,
				},
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
}

func planOnlyResult() *pipeline.Result {
	r := fullResult()
	r.Answer = pipeline.Answer{}
	r.Sources = nil
	// A real plan-only run never touches the answer phase, so the
	// answer-phase debug fields are zero. Clear them so debug-output
	// tests exercise the actual plan-only shape.
	r.Debug.AnswerInputTokens = 0
	r.Debug.AnswerOutputTokens = 0
	r.Debug.AnswerCacheReadTokens = 0
	r.Debug.SelectedSources = 0
	r.Debug.FetchedSources = 0
	return r
}

// rawResult is a --raw fixture: plan + sources populated, answer empty.
func rawResult() *pipeline.Result {
	r := fullResult()
	r.Answer = pipeline.Answer{}
	// Attach a fetched content blob to source 1 and leave source 2 unfetched,
	// so the renderer has to exercise both branches.
	r.Sources[0].Fetched = &fetch.Result{
		URL:     r.Sources[0].Result.URL,
		Content: "Channels are typed conduits. [... lots more content ...]",
	}
	r.Sources[1].Fetched = nil
	return r
}

// --- printSearchJSON ---------------------------------------------------------

func TestPrintSearchJSON_ValidJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchJSON(&buf, fullResult()); err != nil {
		t.Fatalf("printSearchJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	// Sanity: top-level keys.
	for _, k := range []string{"question", "plan", "answer", "sources", "debug"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("missing key %q in JSON output", k)
		}
	}
	if got := decoded["question"]; got != "How do Go channels work?" {
		t.Errorf("question = %v", got)
	}
	// Sources should round-trip with correct count.
	srcs, ok := decoded["sources"].([]any)
	if !ok {
		t.Fatalf("sources is not an array")
	}
	if len(srcs) != 2 {
		t.Errorf("sources count = %d, want 2", len(srcs))
	}
}

// --- printSearchText ---------------------------------------------------------

func TestPrintSearchText_FullAnswer(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchText(&buf, fullResult(), false, renderOptions{}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()
	// Answer text.
	if !strings.Contains(out, "Go channels are typed conduits") {
		t.Error("missing answer text")
	}
	// Citations.
	if !strings.Contains(out, "[1] A Tour of Go — Channels") {
		t.Error("missing citation [1]")
	}
	if !strings.Contains(out, "[2] Go by Example: Channel Buffering") {
		t.Error("missing citation [2]")
	}
	// Confidence.
	if !strings.Contains(out, "Confidence: high") {
		t.Error("missing confidence line")
	}
	// No debug by default.
	if strings.Contains(out, "Debug") {
		t.Error("debug section should be hidden without --debug")
	}
}

func TestPrintSearchText_PlanOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchText(&buf, planOnlyResult(), false, renderOptions{}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "=== Plan ===") {
		t.Error("missing plan header")
	}
	if !strings.Contains(out, "Rationale: Need official docs") {
		t.Error("missing rationale")
	}
	// fullResult() has 4 queries across 3 source types (docs, community, general_web).
	if !strings.Contains(out, "Queries:   4 across 3 source types") {
		t.Errorf("missing/incorrect query-count summary; output:\n%s", out)
	}
	if !strings.Contains(out, "- go channels") {
		t.Error("missing query bullet")
	}
	if !strings.Contains(out, "Expected answer:") {
		t.Error("missing expected answer line")
	}
	// MUST NOT show the answer-phase Confidence: line.
	if strings.Contains(out, "Confidence:") {
		t.Error("plan-only leaked answer-phase confidence line")
	}
}

func TestPrintSearchText_PlanOnlySingleType(t *testing.T) {
	// Verify the singular "1 source type" rendering.
	r := planOnlyResult()
	r.Plan.QueriesBySourceType = map[search.SourceType][]string{
		"docs": {"only one"},
	}
	var buf bytes.Buffer
	if err := printSearchText(&buf, r, false, renderOptions{}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Queries:   1 across 1 source type") {
		t.Errorf("missing singular source-type summary; output:\n%s", out)
	}
	if strings.Contains(out, "1 source types") {
		t.Error("used plural form for 1 source type")
	}
}

func TestPrintSearchText_PlanOnlyDebug_NoAnswerTokens(t *testing.T) {
	// Plan-only debug output must not include an Answer tokens line since
	// the answer phase never ran.
	var buf bytes.Buffer
	if err := printSearchText(&buf, planOnlyResult(), true, renderOptions{}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "--- Debug ---") {
		t.Error("missing debug header")
	}
	if !strings.Contains(out, "Plan tokens:") {
		t.Error("missing plan tokens line")
	}
	if strings.Contains(out, "Answer tokens:") {
		t.Errorf("plan-only should not show Answer tokens; output:\n%s", out)
	}
}

func TestPrintSearchText_Debug(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchText(&buf, fullResult(), true, renderOptions{}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "--- Debug ---") {
		t.Error("missing debug header")
	}
	if !strings.Contains(out, "Plan tokens:   1200 in / 400 out") {
		t.Error("missing plan token counts")
	}
	if !strings.Contains(out, "Search:        40 results → 12 selected → 10 fetched") {
		t.Error("missing search counts")
	}
}

// --- printSearchMarkdown -----------------------------------------------------

func TestPrintSearchMarkdown_FullAnswer(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchMarkdown(&buf, fullResult(), false, renderOptions{}); err != nil {
		t.Fatalf("printSearchMarkdown: %v", err)
	}
	out := buf.String()
	// Title as H1.
	if !strings.Contains(out, "# How do Go channels work?") {
		t.Error("missing H1 title")
	}
	// Answer body.
	if !strings.Contains(out, "Go channels are typed conduits") {
		t.Error("missing answer body")
	}
	// Sources section as H2.
	if !strings.Contains(out, "## Sources") {
		t.Error("missing sources header")
	}
	// Numbered link-list entries with source-type suffix.
	if !strings.Contains(out, "1. [A Tour of Go — Channels](https://go.dev/tour/concurrency/2) — `docs`") {
		t.Error("citation 1 not rendered as numbered markdown link")
	}
	if !strings.Contains(out, "2. [Go by Example: Channel Buffering](https://gobyexample.com/channel-buffering) — `community`") {
		t.Error("citation 2 not rendered as numbered markdown link")
	}
	// Confidence as bold.
	if !strings.Contains(out, "**Confidence**: high") {
		t.Error("missing bold confidence")
	}
}

func TestPrintSearchMarkdown_PlanOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchMarkdown(&buf, planOnlyResult(), false, renderOptions{}); err != nil {
		t.Fatalf("printSearchMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "# Plan: How do Go channels work?") {
		t.Error("missing plan H1")
	}
	if !strings.Contains(out, "**Rationale**:") {
		t.Error("missing bold rationale")
	}
	// fullResult() → 4 queries, 3 source types.
	if !strings.Contains(out, "**Queries**: 4 across 3 source types") {
		t.Errorf("missing/incorrect query-count summary; output:\n%s", out)
	}
	if !strings.Contains(out, "## Queries") {
		t.Error("missing queries H2")
	}
	if !strings.Contains(out, "### docs") {
		t.Error("missing source type subsection")
	}
	if !strings.Contains(out, "- go channels") {
		t.Error("missing query bullet")
	}
	if !strings.Contains(out, "**Expected answer**:") {
		t.Error("missing expected answer")
	}
	if strings.Contains(out, "**Confidence**:") {
		t.Error("plan-only markdown leaked answer-phase confidence line")
	}
}

func TestPrintSearchMarkdown_PlanOnlyDebug_NoAnswerTokens(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchMarkdown(&buf, planOnlyResult(), true, renderOptions{}); err != nil {
		t.Fatalf("printSearchMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "## Debug") {
		t.Error("missing debug H2")
	}
	if !strings.Contains(out, "**Plan tokens**") {
		t.Error("missing plan tokens bullet")
	}
	if strings.Contains(out, "**Answer tokens**") {
		t.Errorf("plan-only markdown should not show answer tokens; output:\n%s", out)
	}
}

func TestPluralSourceTypes(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0 source types"},
		{1, "1 source type"},
		{2, "2 source types"},
		{5, "5 source types"},
	}
	for _, tt := range tests {
		if got := pluralSourceTypes(tt.n); got != tt.want {
			t.Errorf("pluralSourceTypes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestCountNonEmpty(t *testing.T) {
	m := map[search.SourceType][]string{
		"docs":      {"a", "b"},
		"community": {},
		"general":   {"c"},
		"empty":     nil,
	}
	if got := countNonEmpty(m); got != 2 {
		t.Errorf("countNonEmpty = %d, want 2", got)
	}
	if got := countNonEmpty(map[search.SourceType][]string{}); got != 0 {
		t.Errorf("countNonEmpty(empty) = %d, want 0", got)
	}
}

func TestPrintSearchMarkdown_Debug(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchMarkdown(&buf, fullResult(), true, renderOptions{}); err != nil {
		t.Fatalf("printSearchMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "## Debug") {
		t.Error("missing debug H2")
	}
	if !strings.Contains(out, "**Plan tokens**") {
		t.Error("missing plan tokens bullet")
	}
	if !strings.Contains(out, "**Search**: 40 results → 12 selected → 10 fetched") {
		t.Error("missing search bullet")
	}
}

func TestPrintSearchMarkdown_Determinism(t *testing.T) {
	// Source-type iteration must be deterministic — run the same input
	// several times and assert byte-for-byte equality.
	first := new(bytes.Buffer)
	if err := printSearchMarkdown(first, planOnlyResult(), false, renderOptions{}); err != nil {
		t.Fatalf("first render: %v", err)
	}
	for i := 0; i < 20; i++ {
		var buf bytes.Buffer
		if err := printSearchMarkdown(&buf, planOnlyResult(), false, renderOptions{}); err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		if buf.String() != first.String() {
			t.Fatalf("run %d differs:\nwant:\n%s\ngot:\n%s", i, first.String(), buf.String())
		}
	}
}

// --- raw mode rendering -----------------------------------------------------

func TestClassifyResult(t *testing.T) {
	tests := []struct {
		name string
		r    *pipeline.Result
		want resultMode
	}{
		{"full", fullResult(), modeFull},
		{"plan-only", planOnlyResult(), modePlanOnly},
		{"raw", rawResult(), modeRaw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyResult(tt.r); got != tt.want {
				t.Errorf("classifyResult = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPrintSearchText_Raw(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchText(&buf, rawResult(), false, renderOptions{}); err != nil {
		t.Fatalf("printSearchText: %v", err)
	}
	out := buf.String()

	// Plan section must appear (raw still prints the plan so users can see
	// what queries were run).
	if !strings.Contains(out, "=== Plan ===") {
		t.Error("missing plan header in raw text")
	}
	// Sources section.
	if !strings.Contains(out, "=== Sources ===") {
		t.Error("missing sources header in raw text")
	}
	// Source 1: has fetched content.
	if !strings.Contains(out, "[1] A Tour of Go — Channels") {
		t.Error("missing source 1 title")
	}
	if !strings.Contains(out, "https://go.dev/tour/concurrency/2") {
		t.Error("missing source 1 URL")
	}
	if !strings.Contains(out, "fetched (") {
		t.Error("missing fetched marker for source 1")
	}
	// Source 2: not fetched.
	if !strings.Contains(out, "[2] Go by Example: Channel Buffering") {
		t.Error("missing source 2 title")
	}
	if !strings.Contains(out, "not fetched") {
		t.Error("missing 'not fetched' marker for source 2")
	}
	// Snippets surfaced.
	if !strings.Contains(out, "Channels are typed conduits") {
		t.Error("missing source 1 snippet")
	}
	// MUST NOT contain the answer-phase Confidence: line.
	if strings.Contains(out, "Confidence: high") {
		t.Error("raw mode leaked answer-phase confidence line")
	}
}

func TestPrintSearchMarkdown_Raw(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchMarkdown(&buf, rawResult(), false, renderOptions{}); err != nil {
		t.Fatalf("printSearchMarkdown: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "# Raw Results: How do Go channels work?") {
		t.Error("missing raw H1 title")
	}
	if !strings.Contains(out, "## Queries") {
		t.Error("missing queries H2")
	}
	if !strings.Contains(out, "## Sources") {
		t.Error("missing sources H2")
	}
	// Source list items with nested markdown.
	if !strings.Contains(out, "### 1. [A Tour of Go — Channels](https://go.dev/tour/concurrency/2)") {
		t.Error("missing source 1 heading")
	}
	if !strings.Contains(out, "- **Source type**: `docs`") {
		t.Error("missing source type bullet")
	}
	if !strings.Contains(out, "- **Score**: 0.92") {
		t.Error("missing score bullet")
	}
	if !strings.Contains(out, "- **Fetched**: yes (") {
		t.Error("missing fetched yes bullet for source 1")
	}
	if !strings.Contains(out, "- **Fetched**: no") {
		t.Error("missing fetched no bullet for source 2")
	}
	if !strings.Contains(out, "- **Snippet**:") {
		t.Error("missing snippet bullet")
	}
	// MUST NOT contain the answer-phase Confidence: line.
	if strings.Contains(out, "**Confidence**: high") {
		t.Error("raw markdown leaked answer-phase confidence line")
	}
}

func TestPrintSearchJSON_Raw(t *testing.T) {
	var buf bytes.Buffer
	if err := printSearchJSON(&buf, rawResult()); err != nil {
		t.Fatalf("printSearchJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// Sources array populated.
	srcs, _ := decoded["sources"].([]any)
	if len(srcs) != 2 {
		t.Errorf("sources count = %d, want 2", len(srcs))
	}
	// Source 1 marks fetched=true, source 2 fetched=false.
	s1 := srcs[0].(map[string]any)
	if s1["fetched"] != true {
		t.Errorf("source 1 fetched = %v, want true", s1["fetched"])
	}
	s2 := srcs[1].(map[string]any)
	if s2["fetched"] != false {
		t.Errorf("source 2 fetched = %v, want false", s2["fetched"])
	}
	// Answer.text is empty string in raw mode.
	ans := decoded["answer"].(map[string]any)
	if ans["answer"] != "" {
		t.Errorf("answer.answer = %v, want empty", ans["answer"])
	}
}

// --- renderSearch dispatch ---------------------------------------------------

func TestRenderSearch_Dispatch(t *testing.T) {
	tests := []struct {
		name      string
		format    outputFormat
		wantChars []string
	}{
		{"text", formatText, []string{"Confidence: high"}},
		{"markdown", formatMarkdown, []string{"# How do Go channels work?", "**Confidence**: high"}},
		{"json", formatJSON, []string{`"question"`, `"citations"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderSearch(&buf, fullResult(), tt.format, false, renderOptions{}); err != nil {
				t.Fatalf("renderSearch: %v", err)
			}
			out := buf.String()
			for _, want := range tt.wantChars {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in %s output:\n%s", want, tt.name, out)
				}
			}
		})
	}
}
