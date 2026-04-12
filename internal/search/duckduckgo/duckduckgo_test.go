package duckduckgo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// --- fake fetcher ------------------------------------------------------------

type fakeFetcher struct {
	html string
	err  error
}

func (f *fakeFetcher) fetch(_ context.Context, _ string) (string, error) {
	return f.html, f.err
}

// --- sample HTML -------------------------------------------------------------

const sampleSERP = `<!DOCTYPE html><html><head><title>test at DuckDuckGo</title></head>
<body>
<div id="links">
  <div class="result results_links results_links_deep">
    <h2 class="result__title">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffirst&amp;rut=abc">First Result</a>
    </h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffirst">Snippet for the first result.</a>
    <span class="result__url">example.com/first</span>
  </div>
  <div class="result results_links results_links_deep">
    <h2 class="result__title">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fsecond&amp;rut=def">Second Result</a>
    </h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fsecond">Snippet for the second result.</a>
  </div>
  <div class="result results_links results_links_deep">
    <h2 class="result__title">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fthird&amp;rut=ghi">Third Result</a>
    </h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fthird">Snippet for the third result.</a>
  </div>
</div>
</body></html>`

// --- parseResults tests ------------------------------------------------------

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

func TestParseResults_Empty(t *testing.T) {
	html := `<html><body><div id="links"></div></body></html>`
	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseResults_IgnoresAds(t *testing.T) {
	html := `<html><body><div id="links">
	<div class="result result--ad">
		<h2 class="result__title">
			<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fad.com">Sponsored</a>
		</h2>
		<a class="result__snippet">Ad text.</a>
	</div>
	<div class="result results_links">
		<h2 class="result__title">
			<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Forganic.com">Organic</a>
		</h2>
		<a class="result__snippet">Real result.</a>
	</div>
	</div></body></html>`

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

func TestParseResults_MalformedSkipped(t *testing.T) {
	html := `<html><body><div id="links">
	<div class="result">
		<h2 class="result__title"><a class="result__a">No Href</a></h2>
	</div>
	<div class="result">
		<h2 class="result__title">
			<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fok.com">Valid</a>
		</h2>
		<a class="result__snippet">OK snippet.</a>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Title != "Valid" {
		t.Errorf("Title = %q, want Valid", results[0].Title)
	}
}

func TestParseResults_NoModuleFieldsSet(t *testing.T) {
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
	}
}

// --- extractRealURL tests ----------------------------------------------------

func TestExtractRealURL_Uddg(t *testing.T) {
	href := "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc123"
	got := extractRealURL(href)
	if got != "https://example.com/page" {
		t.Errorf("extractRealURL = %q, want https://example.com/page", got)
	}
}

func TestExtractRealURL_DirectURL(t *testing.T) {
	href := "https://example.com/direct"
	got := extractRealURL(href)
	if got != "https://example.com/direct" {
		t.Errorf("extractRealURL = %q, want original URL", got)
	}
}

func TestExtractRealURL_ProtocolRelative(t *testing.T) {
	href := "//duckduckgo.com/l/?uddg=https%3A%2F%2Ftest.org"
	got := extractRealURL(href)
	if got != "https://test.org" {
		t.Errorf("extractRealURL = %q, want https://test.org", got)
	}
}

// --- buildURL tests ----------------------------------------------------------

func TestBuildURL(t *testing.T) {
	u := buildURL("golang concurrency", "us-en")
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL = %q, want prefix %s?", u, baseURL)
	}
	if !strings.Contains(u, "q=golang+concurrency") {
		t.Errorf("URL missing query: %s", u)
	}
	if !strings.Contains(u, "kl=us-en") {
		t.Errorf("URL missing kl: %s", u)
	}
	if !strings.Contains(u, "kp=-1") {
		t.Errorf("URL missing kp: %s", u)
	}
}

// --- module Search tests -----------------------------------------------------

func TestSearch_Success(t *testing.T) {
	m := New(Options{fetcher: &fakeFetcher{html: sampleSERP}}).(*module)

	results, err := m.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
}

func TestSearch_FetchError(t *testing.T) {
	m := New(Options{fetcher: &fakeFetcher{err: fmt.Errorf("timeout")}}).(*module)

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
		fetcher: &fakeFetcher{html: `<html><body><div id="links"></div></body></html>`},
	}).(*module)

	results, err := m.Search(context.Background(), "obscure")
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
	if len(man.Scope) == 0 || len(man.Scope) > 200 {
		t.Errorf("Scope length = %d, want 1-200", len(man.Scope))
	}
}

// --- defaults ----------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	m := New(Options{fetcher: &fakeFetcher{}}).(*module)
	if m.region != defaultRegion {
		t.Errorf("region = %q, want %q", m.region, defaultRegion)
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
