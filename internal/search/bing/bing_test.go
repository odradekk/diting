package bing

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// --- fake fetcher for unit tests ---------------------------------------------

type fakeFetcher struct {
	html string
	err  error
}

func (f *fakeFetcher) fetch(_ context.Context, _ string) (string, error) {
	return f.html, f.err
}

// --- parseResults tests ------------------------------------------------------

const sampleSERP = `<!DOCTYPE html><html><head><title>test - Bing</title></head>
<body>
<ol id="b_results">
  <li class="b_algo">
    <h2><a href="https://example.com/first">First Result</a></h2>
    <div class="b_caption"><p>Snippet for the first result.</p></div>
  </li>
  <li class="b_algo">
    <h2><a href="https://example.com/second">Second Result</a></h2>
    <div class="b_caption"><p>Snippet for the second result.</p></div>
  </li>
  <li class="b_algo">
    <h2><a href="https://example.com/third">Third Result</a></h2>
    <div class="b_caption"><p>Snippet for the third result.</p></div>
  </li>
</ol>
</body></html>`

func TestParseResults_Basic(t *testing.T) {
	results, err := parseResults(sampleSERP)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}

	want := []struct{ title, url, snippet string }{
		{"First Result", "https://example.com/first", "Snippet for the first result."},
		{"Second Result", "https://example.com/second", "Snippet for the second result."},
		{"Third Result", "https://example.com/third", "Snippet for the third result."},
	}
	for i, w := range want {
		r := results[i]
		if r.Title != w.title {
			t.Errorf("[%d] Title = %q, want %q", i, r.Title, w.title)
		}
		if r.URL != w.url {
			t.Errorf("[%d] URL = %q, want %q", i, r.URL, w.url)
		}
		if r.Snippet != w.snippet {
			t.Errorf("[%d] Snippet = %q, want %q", i, r.Snippet, w.snippet)
		}
	}
}

func TestParseResults_FallbackSnippetSelector(t *testing.T) {
	// Snippet not in .b_caption p but in a bare <p> tag.
	html := `<html><body><ol id="b_results">
	<li class="b_algo">
		<h2><a href="https://x.com/1">Title</a></h2>
		<p>Fallback snippet text.</p>
	</li>
	</ol></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Snippet != "Fallback snippet text." {
		t.Errorf("Snippet = %q, want fallback text", results[0].Snippet)
	}
}

func TestParseResults_Empty(t *testing.T) {
	html := `<html><body><ol id="b_results"></ol></body></html>`
	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseResults_MalformedSkipped(t *testing.T) {
	// Missing href → should be skipped.
	html := `<html><body><ol id="b_results">
	<li class="b_algo">
		<h2><a>No Href Title</a></h2>
		<div class="b_caption"><p>Snippet.</p></div>
	</li>
	<li class="b_algo">
		<h2><a href="https://ok.com">Valid</a></h2>
		<div class="b_caption"><p>OK snippet.</p></div>
	</li>
	</ol></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1 (malformed should be skipped)", len(results))
	}
	if results[0].Title != "Valid" {
		t.Errorf("Title = %q, want Valid", results[0].Title)
	}
}

func TestParseResults_IgnoresAdsAndAnswers(t *testing.T) {
	// li.b_ad and li.b_ans should not be picked up.
	html := `<html><body><ol id="b_results">
	<li class="b_ad">
		<h2><a href="https://ad.com">Sponsored</a></h2>
		<div class="b_caption"><p>Ad text.</p></div>
	</li>
	<li class="b_ans">
		<h2><a href="https://ans.com">Answer Box</a></h2>
	</li>
	<li class="b_algo">
		<h2><a href="https://organic.com">Organic</a></h2>
		<div class="b_caption"><p>Real result.</p></div>
	</li>
	</ol></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Title != "Organic" {
		t.Errorf("Title = %q, want Organic", results[0].Title)
	}
}

func TestParseResults_NoModuleFieldsSet(t *testing.T) {
	// Module, SourceType, Query must NOT be set by the parser — pipeline does that.
	results, err := parseResults(sampleSERP)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	for i, r := range results {
		if r.Module != "" {
			t.Errorf("[%d] Module = %q, want empty", i, r.Module)
		}
		if r.SourceType != "" {
			t.Errorf("[%d] SourceType = %q, want empty", i, r.SourceType)
		}
		if r.Query != "" {
			t.Errorf("[%d] Query = %q, want empty", i, r.Query)
		}
	}
}

// --- buildURL tests ----------------------------------------------------------

func TestBuildURL(t *testing.T) {
	u := buildURL("golang concurrency", "en-US", 10)
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL = %q, want prefix %s?", u, baseURL)
	}
	if !strings.Contains(u, "q=golang+concurrency") {
		t.Errorf("URL missing query: %s", u)
	}
	if !strings.Contains(u, "count=10") {
		t.Errorf("URL missing count: %s", u)
	}
	if !strings.Contains(u, "mkt=en-US") {
		t.Errorf("URL missing mkt: %s", u)
	}
}

func TestBuildURL_SpecialChars(t *testing.T) {
	u := buildURL("c++ templates", "en-US", 10)
	if !strings.Contains(u, "q=c%2B%2B+templates") {
		t.Errorf("URL did not encode special chars: %s", u)
	}
}

// --- module Search tests (with fake fetcher) ---------------------------------

func TestSearch_Success(t *testing.T) {
	m := New(Options{
		fetcher: &fakeFetcher{html: sampleSERP},
	}).(*module)

	results, err := m.Search(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
}

func TestSearch_FetchError(t *testing.T) {
	m := New(Options{
		fetcher: &fakeFetcher{err: fmt.Errorf("connection refused")},
	}).(*module)

	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("error = %v, want contains 'fetch'", err)
	}
}

func TestSearch_EmptyResults(t *testing.T) {
	m := New(Options{
		fetcher: &fakeFetcher{html: `<html><body><ol id="b_results"></ol></body></html>`},
	}).(*module)

	results, err := m.Search(context.Background(), "obscure query")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

// --- manifest ----------------------------------------------------------------

func TestManifest(t *testing.T) {
	m := New(Options{fetcher: &fakeFetcher{}})
	man := m.Manifest()
	if man.Name != ModuleName {
		t.Errorf("Name = %q, want %q", man.Name, ModuleName)
	}
	if man.SourceType != search.SourceTypeGeneralWeb {
		t.Errorf("SourceType = %q, want general_web", man.SourceType)
	}
	if man.CostTier != search.CostTierFree {
		t.Errorf("CostTier = %q, want free", man.CostTier)
	}
	if len(man.Languages) == 0 {
		t.Error("Languages is empty")
	}
	if len(man.Scope) == 0 || len(man.Scope) > 200 {
		t.Errorf("Scope length = %d, want 1-200", len(man.Scope))
	}
}

// --- defaults ----------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	m := New(Options{fetcher: &fakeFetcher{}}).(*module)
	if m.market != defaultMarket {
		t.Errorf("market = %q, want %q", m.market, defaultMarket)
	}
	if m.count != defaultCount {
		t.Errorf("count = %d, want %d", m.count, defaultCount)
	}
}

func TestNew_ClampCount(t *testing.T) {
	m := New(Options{Count: 100, fetcher: &fakeFetcher{}}).(*module)
	if m.count != maxCount {
		t.Errorf("count = %d, want %d (clamped)", m.count, maxCount)
	}
}

// --- registration ------------------------------------------------------------

func TestRegistration(t *testing.T) {
	names := search.List()
	found := false
	for _, n := range names {
		if n == ModuleName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("module %q not found in registry; got %v", ModuleName, names)
	}
}
