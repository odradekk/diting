package baidu

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

// Realistic modern Baidu SERP HTML. Real URL in mu attribute, snippet in
// [data-module="abstract"], title in h3.t a.
const sampleSERP = `<!DOCTYPE html><html><head><title>test_百度搜索</title></head>
<body>
<div id="content_left">
  <div class="result c-container xpath-log new-pmd" mu="https://example.com/first">
    <h3 class="t _sc-title_10ku5_63"><a class="sc-link" href="http://www.baidu.com/link?url=abc123">第一个结果</a></h3>
    <div data-module="abstract">这是第一个结果的摘要。</div>
  </div>
  <div class="result c-container xpath-log new-pmd" mu="https://example.com/second">
    <h3 class="t _sc-title_10ku5_63"><a class="sc-link" href="http://www.baidu.com/link?url=def456">Second Result</a></h3>
    <div data-module="abstract">Snippet for the second result.</div>
  </div>
  <div class="result c-container xpath-log new-pmd" mu="https://example.com/third">
    <h3 class="t _sc-title_10ku5_63"><a class="sc-link" href="http://www.baidu.com/link?url=ghi789">Third Result</a></h3>
    <div data-module="abstract">Snippet for the third result.</div>
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
		{"第一个结果", "https://example.com/first", "这是第一个结果的摘要。"},
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

func TestParseResults_MuExtraction(t *testing.T) {
	html := `<html><body><div id="content_left">
	<div class="result c-container" mu="https://real.example.com/page">
		<h3 class="t"><a href="http://www.baidu.com/link?url=opaque">Title</a></h3>
		<div data-module="abstract">Snippet.</div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	// Should use real URL from mu attribute, not the Baidu redirect.
	if results[0].URL != "https://real.example.com/page" {
		t.Errorf("URL = %q, want real URL from mu attr", results[0].URL)
	}
}

func TestParseResults_FallbackToRedirectURL(t *testing.T) {
	// No mu attribute → falls back to href (Baidu redirect URL).
	html := `<html><body><div id="content_left">
	<div class="result c-container">
		<h3 class="t"><a href="http://www.baidu.com/link?url=xyz">Title</a></h3>
		<div data-module="abstract">Snippet.</div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].URL != "http://www.baidu.com/link?url=xyz" {
		t.Errorf("URL = %q, want Baidu redirect URL as fallback", results[0].URL)
	}
}

func TestParseResults_SnippetFromAbstractModule(t *testing.T) {
	html := `<html><body><div id="content_left">
	<div class="result c-container" mu="https://x.com">
		<h3 class="t"><a href="http://www.baidu.com/link?url=a">Title</a></h3>
		<div data-module="abstract"><span>2天前</span><span>Abstract snippet here.</span></div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if !strings.Contains(results[0].Snippet, "Abstract snippet here.") {
		t.Errorf("Snippet = %q, want contains 'Abstract snippet here.'", results[0].Snippet)
	}
}

func TestParseResults_SnippetFallbackLineClamp(t *testing.T) {
	// No data-module="abstract" → falls back to cu-line-clamp class.
	html := `<html><body><div id="content_left">
	<div class="result c-container" mu="https://x.com">
		<h3 class="t"><a href="http://www.baidu.com/link?url=a">Title</a></h3>
		<div class="cu-line-clamp-2">Line clamp snippet.</div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Snippet != "Line clamp snippet." {
		t.Errorf("Snippet = %q, want 'Line clamp snippet.'", results[0].Snippet)
	}
}

func TestParseResults_SnippetFallbackLegacy(t *testing.T) {
	// Legacy div.c-abstract still works as last resort.
	html := `<html><body><div id="content_left">
	<div class="result c-container" mu="https://x.com">
		<h3 class="t"><a href="http://www.baidu.com/link?url=a">Title</a></h3>
		<div class="c-abstract">Legacy snippet.</div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Snippet != "Legacy snippet." {
		t.Errorf("Snippet = %q, want 'Legacy snippet.'", results[0].Snippet)
	}
}

func TestParseResults_Empty(t *testing.T) {
	html := `<html><body><div id="content_left"></div></body></html>`
	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseResults_MalformedSkipped(t *testing.T) {
	html := `<html><body><div id="content_left">
	<div class="result c-container">
		<h3 class="t"><a>No Href</a></h3>
	</div>
	<div class="result c-container" mu="https://ok.com">
		<h3 class="t"><a href="http://www.baidu.com/link?url=ok">Valid</a></h3>
		<div data-module="abstract">OK.</div>
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

func TestParseResults_BroadH3Fallback(t *testing.T) {
	// h3 without class="t" — should still find the link.
	html := `<html><body><div id="content_left">
	<div class="result c-container" mu="https://example.com">
		<h3><a href="http://www.baidu.com/link?url=x">No T Class</a></h3>
		<div data-module="abstract">Snippet.</div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Title != "No T Class" {
		t.Errorf("Title = %q, want 'No T Class'", results[0].Title)
	}
}

func TestParseResults_IncludesUsefulResultOp(t *testing.T) {
	// result-op blocks with valid titles/URLs (baike, knowledge graph) are included.
	html := `<html><body><div id="content_left">
	<div class="result-op c-container" tpl="sg_kg_entity_san" mu="https://baike.com/something">
		<h3 class="t"><a href="http://www.baidu.com/link?url=kg">Knowledge Graph</a></h3>
	</div>
	<div class="result c-container" mu="https://organic.com">
		<h3 class="t"><a href="http://www.baidu.com/link?url=org">Organic</a></h3>
		<div data-module="abstract">Real result.</div>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	if results[0].Title != "Knowledge Graph" {
		t.Errorf("[0] Title = %q, want Knowledge Graph", results[0].Title)
	}
	if results[0].URL != "https://baike.com/something" {
		t.Errorf("[0] URL = %q, want mu URL", results[0].URL)
	}
}

func TestParseResults_SkipsImageGridAndRecommend(t *testing.T) {
	// image_grid_san and recommend_list templates should be filtered out.
	html := `<html><body><div id="content_left">
	<div class="result-op c-container" tpl="image_grid_san" mu="https://image.baidu.com/search">
		<h3><a href="http://www.baidu.com/link?url=img">Images</a></h3>
	</div>
	<div class="result-op c-container" tpl="recommend_list" mu="http://recommend.baidu.com">
		<h3><a href="#">Related</a></h3>
	</div>
	<div class="result c-container" mu="https://organic.com">
		<h3 class="t"><a href="http://www.baidu.com/link?url=org">Organic</a></h3>
	</div>
	</div></body></html>`

	results, err := parseResults(html)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1 (image_grid and recommend_list should be skipped)", len(results))
	}
	if results[0].Title != "Organic" {
		t.Errorf("Title = %q, want Organic", results[0].Title)
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

// --- buildURL tests ----------------------------------------------------------

func TestBuildURL(t *testing.T) {
	u := buildURL("golang 并发")
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL = %q, want prefix %s?", u, baseURL)
	}
	if !strings.Contains(u, "ie=utf-8") {
		t.Errorf("URL missing ie: %s", u)
	}
	if !strings.Contains(u, "wd=") {
		t.Errorf("URL missing wd: %s", u)
	}
}

// --- CAPTCHA detection -------------------------------------------------------

func TestSearch_CAPTCHADetected_Wappass(t *testing.T) {
	captchaHTML := `<html><head><meta http-equiv="refresh" content="0;url=https://wappass.baidu.com/passport/?checktype=3"></head><body></body></html>`
	m := New(Options{fetcher: &fakeFetcher{html: captchaHTML}}).(*module)

	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for CAPTCHA page")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error = %v, want contains 'blocked'", err)
	}
}

func TestSearch_CAPTCHADetected_SecurityVerify(t *testing.T) {
	verifyHTML := `<!DOCTYPE html><html lang="zh-CN"><head><title>百度安全验证</title></head><body></body></html>`
	m := New(Options{fetcher: &fakeFetcher{html: verifyHTML}}).(*module)

	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for security verification page")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error = %v, want contains 'blocked'", err)
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
		fetcher: &fakeFetcher{html: `<html><body><div id="content_left"></div></body></html>`},
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
	if len(man.Languages) == 0 {
		t.Error("Languages is empty")
	}
	if man.Languages[0] != "zh-Hans" {
		t.Errorf("Languages[0] = %q, want zh-Hans", man.Languages[0])
	}
	if len(man.Scope) == 0 || len(man.Scope) > 200 {
		t.Errorf("Scope length = %d, want 1-200", len(man.Scope))
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
