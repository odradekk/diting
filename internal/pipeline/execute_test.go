package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// discardLogger returns a slog.Logger that drops all events. Used by unit
// tests that don't care about log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// parseJSONLines splits buffered slog JSON output into one decoded map per
// line. Used by tests that want to assert on the contents of individual
// structured events.
func parseJSONLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("slog line is not valid JSON: %v\n%s", err, line)
		}
		events = append(events, ev)
	}
	return events
}

// ensure the unused bytes import is retained if only parseJSONLines uses it
var _ = bytes.NewBuffer

// --- stub module for tests ---------------------------------------------------

type stubModule struct {
	name       string
	sourceType search.SourceType
	results    []search.SearchResult
	err        error
	// searchCalls tracks how many times Search() has been invoked — used
	// by plan-only tests to assert the execute phase was genuinely skipped,
	// not just rendered empty.
	searchCalls int
}

func (s *stubModule) Manifest() search.Manifest {
	return search.Manifest{Name: s.name, SourceType: s.sourceType, CostTier: search.CostTierFree}
}
func (s *stubModule) Search(_ context.Context, query string) ([]search.SearchResult, error) {
	s.searchCalls++
	if s.err != nil {
		return nil, s.err
	}
	// Copy results and set query.
	out := make([]search.SearchResult, len(s.results))
	copy(out, s.results)
	return out, nil
}
func (s *stubModule) Close() error { return nil }

func makeResult(title, url, snippet string) search.SearchResult {
	return search.SearchResult{Title: title, URL: url, Snippet: snippet}
}

// --- parallelSearch tests ----------------------------------------------------

func TestParallelSearch_Basic(t *testing.T) {
	mod := &stubModule{
		name:       "bing",
		sourceType: search.SourceTypeGeneralWeb,
		results: []search.SearchResult{
			makeResult("Result 1", "https://example.com/1", "snippet 1"),
			makeResult("Result 2", "https://example.com/2", "snippet 2"),
		},
	}

	tasks := []searchTask{
		{module: mod, query: "test query"},
	}

	results := parallelSearch(context.Background(), tasks, 4, discardLogger())
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	// Module/SourceType/Query should be annotated.
	if results[0].Module != "bing" {
		t.Errorf("Module = %q, want bing", results[0].Module)
	}
	if results[0].SourceType != search.SourceTypeGeneralWeb {
		t.Errorf("SourceType = %q", results[0].SourceType)
	}
	if results[0].Query != "test query" {
		t.Errorf("Query = %q", results[0].Query)
	}
}

func TestParallelSearch_SkipsFailedModules(t *testing.T) {
	good := &stubModule{
		name: "bing", sourceType: search.SourceTypeGeneralWeb,
		results: []search.SearchResult{makeResult("R1", "https://a.com", "s")},
	}
	bad := &stubModule{
		name: "baidu", sourceType: search.SourceTypeGeneralWeb,
		err: fmt.Errorf("blocked"),
	}

	tasks := []searchTask{
		{module: good, query: "q"},
		{module: bad, query: "q"},
	}

	results := parallelSearch(context.Background(), tasks, 4, discardLogger())
	if len(results) != 1 {
		t.Errorf("len = %d, want 1 (failed module skipped)", len(results))
	}
}

// TestParallelSearch_LogsFailures captures the slog output and asserts that
// module failures produce a debug-level event with the module name and
// error — this is the Phase 4.4 guarantee that --debug reveals silent
// module failures.
func TestParallelSearch_LogsFailures(t *testing.T) {
	good := &stubModule{
		name: "bing", sourceType: search.SourceTypeGeneralWeb,
		results: []search.SearchResult{makeResult("R1", "https://a.com", "s")},
	}
	bad := &stubModule{
		name: "baidu", sourceType: search.SourceTypeGeneralWeb,
		err: fmt.Errorf("blocked"),
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	tasks := []searchTask{
		{module: good, query: "test query"},
		{module: bad, query: "test query"},
	}
	parallelSearch(context.Background(), tasks, 4, logger)

	// Collect all events. Each line is one JSON object.
	events := parseJSONLines(t, buf.String())

	var sawFailure, sawSuccess bool
	for _, e := range events {
		msg, _ := e["msg"].(string)
		switch msg {
		case "execute: module search failed":
			sawFailure = true
			if e["module"] != "baidu" {
				t.Errorf("failure event module = %v, want baidu", e["module"])
			}
			if e["query"] != "test query" {
				t.Errorf("failure event query = %v, want 'test query'", e["query"])
			}
			if e["error"] == nil {
				t.Error("failure event has no error field")
			}
		case "execute: module search success":
			sawSuccess = true
			if e["module"] != "bing" {
				t.Errorf("success event module = %v, want bing", e["module"])
			}
			if n, _ := e["results"].(float64); n != 1 {
				t.Errorf("success event results = %v, want 1", e["results"])
			}
		}
	}
	if !sawFailure {
		t.Error("no failure event logged for bad module")
	}
	if !sawSuccess {
		t.Error("no success event logged for good module")
	}
}

func TestParallelSearch_CancelledContext(t *testing.T) {
	mod := &stubModule{
		name: "slow", sourceType: search.SourceTypeGeneralWeb,
		results: []search.SearchResult{makeResult("R1", "https://a.com", "s")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tasks := []searchTask{{module: mod, query: "q"}}
	results := parallelSearch(ctx, tasks, 1, discardLogger())
	// May or may not get results depending on goroutine scheduling.
	_ = results
}

// --- dedupByURL tests --------------------------------------------------------

func TestDedupByURL(t *testing.T) {
	results := []search.SearchResult{
		makeResult("R1", "https://example.com/page", "s1"),
		makeResult("R2", "https://example.com/page", "s2"),    // exact dup
		makeResult("R3", "https://example.com/page/", "s3"),   // trailing slash dup
		makeResult("R4", "https://example.com/other", "s4"),   // unique
	}

	dedupped := dedupByURL(results)
	if len(dedupped) != 2 {
		t.Errorf("len = %d, want 2", len(dedupped))
	}
}

func TestDedupByURL_CaseInsensitive(t *testing.T) {
	results := []search.SearchResult{
		makeResult("R1", "https://Example.COM/page", "s1"),
		makeResult("R2", "https://example.com/page", "s2"),
	}

	dedupped := dedupByURL(results)
	if len(dedupped) != 1 {
		t.Errorf("len = %d, want 1", len(dedupped))
	}
}

func TestDedupByURL_WWWAndFragment(t *testing.T) {
	// www. and fragments should be normalized away.
	results := []search.SearchResult{
		makeResult("R1", "https://www.example.com/page", "s1"),
		makeResult("R2", "https://example.com/page#section", "s2"), // fragment dup
		makeResult("R3", "https://example.com/page#other", "s3"),   // another fragment dup
	}

	dedupped := dedupByURL(results)
	if len(dedupped) != 1 {
		t.Errorf("len = %d, want 1 (www and fragments normalized)", len(dedupped))
	}
}

func TestDedupByURL_TrackingParams(t *testing.T) {
	// Tracking parameters should be stripped for dedup comparison.
	results := []search.SearchResult{
		makeResult("R1", "https://example.com/page?utm_source=twitter&utm_campaign=launch", "s1"),
		makeResult("R2", "https://example.com/page?fbclid=xyz", "s2"),
		makeResult("R3", "https://example.com/page?id=42&utm_source=email", "s3"), // id=42 preserved
		makeResult("R4", "https://example.com/page", "s4"),                        // no params
	}

	dedupped := dedupByURL(results)
	// R1, R2, R4 should dedup to 1; R3 preserves id=42 so it's unique.
	if len(dedupped) != 2 {
		t.Errorf("len = %d, want 2 (tracking params stripped)", len(dedupped))
	}
}

func TestDedupByURL_QueryParamOrder(t *testing.T) {
	// Query parameters in different orders should dedup.
	results := []search.SearchResult{
		makeResult("R1", "https://example.com/search?q=go&page=1", "s1"),
		makeResult("R2", "https://example.com/search?page=1&q=go", "s2"), // reordered
	}

	dedupped := dedupByURL(results)
	if len(dedupped) != 1 {
		t.Errorf("len = %d, want 1 (query param order normalized)", len(dedupped))
	}
}

// --- selectTopSources tests --------------------------------------------------

func TestSelectTopSources_PerTypeGuarantee(t *testing.T) {
	scored := []ScoredResult{
		{SearchResult: search.SearchResult{Title: "Web1", SourceType: search.SourceTypeGeneralWeb}, Score: 0.9},
		{SearchResult: search.SearchResult{Title: "Web2", SourceType: search.SourceTypeGeneralWeb}, Score: 0.85},
		{SearchResult: search.SearchResult{Title: "Web3", SourceType: search.SourceTypeGeneralWeb}, Score: 0.8},
		{SearchResult: search.SearchResult{Title: "Acad1", SourceType: search.SourceTypeAcademic}, Score: 0.7},
		{SearchResult: search.SearchResult{Title: "Code1", SourceType: search.SourceTypeCode}, Score: 0.6},
	}

	// maxPerType=2, maxTotal=4: should get 2 web + 1 acad + 1 code
	selected := selectTopSources(scored, 2, 4)
	if len(selected) != 4 {
		t.Fatalf("len = %d, want 4", len(selected))
	}

	typeCounts := make(map[search.SourceType]int)
	for _, s := range selected {
		typeCounts[s.SourceType]++
	}
	if typeCounts[search.SourceTypeGeneralWeb] != 2 {
		t.Errorf("general_web = %d, want 2", typeCounts[search.SourceTypeGeneralWeb])
	}
	if typeCounts[search.SourceTypeAcademic] != 1 {
		t.Errorf("academic = %d, want 1", typeCounts[search.SourceTypeAcademic])
	}
}

func TestSelectTopSources_MaxTotalCap(t *testing.T) {
	var scored []ScoredResult
	for i := 0; i < 20; i++ {
		scored = append(scored, ScoredResult{
			SearchResult: search.SearchResult{
				Title:      fmt.Sprintf("R%d", i),
				SourceType: search.SourceTypeGeneralWeb,
			},
			Score: 1.0 - float64(i)*0.01,
		})
	}

	selected := selectTopSources(scored, 10, 5)
	if len(selected) != 5 {
		t.Errorf("len = %d, want 5 (maxTotal cap)", len(selected))
	}
}

func TestSelectTopSources_ScoreOrder(t *testing.T) {
	scored := []ScoredResult{
		{SearchResult: search.SearchResult{Title: "Low"}, Score: 0.1},
		{SearchResult: search.SearchResult{Title: "High"}, Score: 0.9},
		{SearchResult: search.SearchResult{Title: "Mid"}, Score: 0.5},
	}

	selected := selectTopSources(scored, 10, 10)
	if selected[0].Title != "High" {
		t.Errorf("first = %q, want High", selected[0].Title)
	}
}

// --- buildSearchTasks tests --------------------------------------------------

func TestBuildSearchTasks(t *testing.T) {
	modules := []search.Module{
		&stubModule{name: "bing", sourceType: search.SourceTypeGeneralWeb},
		&stubModule{name: "ddg", sourceType: search.SourceTypeGeneralWeb},
		&stubModule{name: "arxiv", sourceType: search.SourceTypeAcademic},
	}

	plan := Plan{
		QueriesBySourceType: map[search.SourceType][]string{
			search.SourceTypeGeneralWeb: {"query1", "query2"},
			search.SourceTypeAcademic:   {"paper query"},
			search.SourceTypeCode:       {"code query"}, // no module for this
		},
	}

	tasks := buildSearchTasks(plan, modules)
	// general_web: 2 queries × 2 modules = 4
	// academic: 1 query × 1 module = 1
	// code: 1 query × 0 modules = 0
	if len(tasks) != 5 {
		t.Errorf("len = %d, want 5", len(tasks))
	}
}

func TestBuildSearchTasks_SkipsEmptyQueries(t *testing.T) {
	modules := []search.Module{
		&stubModule{name: "bing", sourceType: search.SourceTypeGeneralWeb},
	}
	plan := Plan{
		QueriesBySourceType: map[search.SourceType][]string{
			search.SourceTypeGeneralWeb: {"", "valid", ""},
		},
	}
	tasks := buildSearchTasks(plan, modules)
	if len(tasks) != 1 {
		t.Errorf("len = %d, want 1 (empty queries skipped)", len(tasks))
	}
}

// --- RunExecutePhase integration test ----------------------------------------

func TestRunExecutePhase_Success(t *testing.T) {
	modules := []search.Module{
		&stubModule{
			name: "bing", sourceType: search.SourceTypeGeneralWeb,
			results: []search.SearchResult{
				makeResult("Go Concurrency", "https://go.dev/doc", "Long snippet about goroutines and channels in Go programming"),
				makeResult("Go Tutorial", "https://gobyexample.com", "Go by Example is a hands-on introduction to Go"),
			},
		},
		&stubModule{
			name: "arxiv", sourceType: search.SourceTypeAcademic,
			results: []search.SearchResult{
				makeResult("CSP Paper", "https://arxiv.org/abs/1234", "Communicating sequential processes theory and practice"),
			},
		},
	}

	plan := Plan{
		QueriesBySourceType: map[search.SourceType][]string{
			search.SourceTypeGeneralWeb: {"go concurrency"},
			search.SourceTypeAcademic:   {"CSP concurrency"},
		},
	}

	result, err := RunExecutePhase(
		context.Background(), plan, modules, DefaultScorer(),
		"How does Go concurrency work?",
		ExecuteConfig{MaxSourcesPerType: 5, MaxFetchedTotal: 10},
	)
	if err != nil {
		t.Fatalf("RunExecutePhase: %v", err)
	}

	if len(result.AllResults) != 3 {
		t.Errorf("AllResults = %d, want 3", len(result.AllResults))
	}
	if len(result.Selected) < 1 {
		t.Error("Selected is empty")
	}

	// All selected results should have scores.
	for i, s := range result.Selected {
		if s.Score <= 0 {
			t.Errorf("Selected[%d] score = %f, want > 0", i, s.Score)
		}
	}
}

func TestRunExecutePhase_NoMatchingModules(t *testing.T) {
	modules := []search.Module{
		&stubModule{name: "arxiv", sourceType: search.SourceTypeAcademic},
	}
	plan := Plan{
		QueriesBySourceType: map[search.SourceType][]string{
			search.SourceTypeCode: {"test"}, // no code module
		},
	}

	_, err := RunExecutePhase(context.Background(), plan, modules, DefaultScorer(), "q", ExecuteConfig{})
	if err == nil {
		t.Fatal("expected error for no matching modules")
	}
}
