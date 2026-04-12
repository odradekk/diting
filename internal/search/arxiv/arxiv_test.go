package arxiv

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// --- mock HTTP client --------------------------------------------------------

type mockClient struct {
	fn func(*http.Request) (*http.Response, error)
}

func (m *mockClient) Do(req *http.Request) (*http.Response, error) { return m.fn(req) }

func atomResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/atom+xml"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// --- sample Atom XML ---------------------------------------------------------

const sampleAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>ArXiv Query: all:transformer</title>
  <entry>
    <title>Attention Is All You Need</title>
    <summary>  We propose a new simple network architecture, the Transformer,
based solely on attention mechanisms.  </summary>
    <author><name>Ashish Vaswani</name></author>
    <author><name>Noam Shazeer</name></author>
    <author><name>Niki Parmar</name></author>
    <author><name>Jakob Uszkoreit</name></author>
    <link href="http://arxiv.org/abs/1706.03762v7" rel="alternate" type="text/html"/>
    <link href="http://arxiv.org/pdf/1706.03762v7" rel="related" type="application/pdf"/>
  </entry>
  <entry>
    <title>BERT: Pre-training of Deep Bidirectional Transformers</title>
    <summary>We introduce BERT, a new language representation model.</summary>
    <author><name>Jacob Devlin</name></author>
    <author><name>Ming-Wei Chang</name></author>
    <link href="http://arxiv.org/abs/1810.04805v2" rel="alternate" type="text/html"/>
  </entry>
</feed>`

const emptyAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>ArXiv Query: all:xyznonexistent</title>
</feed>`

// --- parseAtomFeed tests -----------------------------------------------------

func TestParseAtomFeed_Basic(t *testing.T) {
	results, err := parseAtomFeed([]byte(sampleAtom))
	if err != nil {
		t.Fatalf("parseAtomFeed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}

	r := results[0]
	if r.Title != "Attention Is All You Need" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.URL != "http://arxiv.org/abs/1706.03762v7" {
		t.Errorf("URL = %q, want abs link", r.URL)
	}
	// Snippet should have authors prepended and whitespace cleaned.
	if !strings.HasPrefix(r.Snippet, "Ashish Vaswani, Noam Shazeer, Niki Parmar et al.") {
		t.Errorf("Snippet prefix = %q, want authors", r.Snippet[:60])
	}
	if !strings.Contains(r.Snippet, "Transformer") {
		t.Errorf("Snippet missing abstract text")
	}
	// Should not have excessive whitespace.
	if strings.Contains(r.Snippet, "  ") {
		t.Errorf("Snippet has uncleaned whitespace: %q", r.Snippet)
	}
}

func TestParseAtomFeed_Empty(t *testing.T) {
	results, err := parseAtomFeed([]byte(emptyAtom))
	if err != nil {
		t.Fatalf("parseAtomFeed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseAtomFeed_InvalidXML(t *testing.T) {
	_, err := parseAtomFeed([]byte("not xml"))
	if err == nil {
		t.Fatal("expected error for invalid XML")
	}
}

func TestParseAtomFeed_NoModuleFieldsSet(t *testing.T) {
	results, err := parseAtomFeed([]byte(sampleAtom))
	if err != nil {
		t.Fatalf("parseAtomFeed: %v", err)
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

// --- extractAbsLink tests ----------------------------------------------------

func TestExtractAbsLink_Alternate(t *testing.T) {
	links := []atomLink{
		{Href: "http://arxiv.org/pdf/1234", Rel: "related", Type: "application/pdf"},
		{Href: "http://arxiv.org/abs/1234", Rel: "alternate", Type: "text/html"},
	}
	got := extractAbsLink(links)
	if got != "http://arxiv.org/abs/1234" {
		t.Errorf("extractAbsLink = %q, want abs link", got)
	}
}

func TestExtractAbsLink_FallbackFirst(t *testing.T) {
	links := []atomLink{
		{Href: "http://arxiv.org/pdf/1234", Rel: "related"},
	}
	got := extractAbsLink(links)
	if got != "http://arxiv.org/pdf/1234" {
		t.Errorf("extractAbsLink = %q, want first link", got)
	}
}

func TestExtractAbsLink_Empty(t *testing.T) {
	got := extractAbsLink(nil)
	if got != "" {
		t.Errorf("extractAbsLink = %q, want empty", got)
	}
}

// --- formatAuthors tests -----------------------------------------------------

func TestFormatAuthors_FewAuthors(t *testing.T) {
	authors := []atomAuthor{{"Alice"}, {"Bob"}}
	got := formatAuthors(authors)
	if got != "Alice, Bob" {
		t.Errorf("formatAuthors = %q, want 'Alice, Bob'", got)
	}
}

func TestFormatAuthors_ManyAuthors(t *testing.T) {
	authors := []atomAuthor{{"A"}, {"B"}, {"C"}, {"D"}}
	got := formatAuthors(authors)
	if got != "A, B, C et al." {
		t.Errorf("formatAuthors = %q, want 'A, B, C et al.'", got)
	}
}

func TestFormatAuthors_Empty(t *testing.T) {
	got := formatAuthors(nil)
	if got != "" {
		t.Errorf("formatAuthors = %q, want empty", got)
	}
}

// --- buildURL tests ----------------------------------------------------------

func TestBuildURL(t *testing.T) {
	u := buildURL("transformer attention", 10)
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL prefix mismatch: %s", u)
	}
	// Should add "all:" prefix since no field prefix in query.
	if !strings.Contains(u, "search_query=all") {
		t.Errorf("URL missing all: prefix: %s", u)
	}
	if !strings.Contains(u, "max_results=10") {
		t.Errorf("URL missing max_results: %s", u)
	}
	if !strings.Contains(u, "sortBy=relevance") {
		t.Errorf("URL missing sortBy: %s", u)
	}
}

func TestBuildURL_PreservesFieldPrefix(t *testing.T) {
	u := buildURL("au:hinton", 5)
	// Should NOT add "all:" when query already has a field prefix.
	if strings.Contains(u, "all%3A") || strings.Contains(u, "all:au") {
		t.Errorf("URL should not double-prefix: %s", u)
	}
}

// --- module Search tests (mocked HTTP) ---------------------------------------

func TestSearch_Success(t *testing.T) {
	var gotURL string
	m := New(Options{
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return atomResp(200, sampleAtom), nil
		}},
	})

	results, err := m.Search(context.Background(), "transformer")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len = %d, want 2", len(results))
	}
	if !strings.Contains(gotURL, "search_query=all") {
		t.Errorf("URL missing search_query: %s", gotURL)
	}
	if !strings.Contains(gotURL, "max_results=10") {
		t.Errorf("URL missing max_results: %s", gotURL)
	}
}

func TestSearch_HTTPError(t *testing.T) {
	m := New(Options{
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return atomResp(503, "service unavailable"), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("error = %v, want contains 'HTTP 503'", err)
	}
}

// --- cleanWhitespace ---------------------------------------------------------

func TestCleanWhitespace(t *testing.T) {
	got := cleanWhitespace("  hello   world  \n  foo  ")
	if got != "hello world foo" {
		t.Errorf("cleanWhitespace = %q", got)
	}
}

// --- XML round-trip sanity check ---------------------------------------------

func TestAtomFeed_XMLRoundTrip(t *testing.T) {
	var feed atomFeed
	err := xml.Unmarshal([]byte(sampleAtom), &feed)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(feed.Entries))
	}
	if feed.Entries[0].Title != "Attention Is All You Need" {
		t.Errorf("title = %q", feed.Entries[0].Title)
	}
	if len(feed.Entries[0].Authors) != 4 {
		t.Errorf("authors = %d, want 4", len(feed.Entries[0].Authors))
	}
	if len(feed.Entries[0].Links) != 2 {
		t.Errorf("links = %d, want 2", len(feed.Entries[0].Links))
	}
}

// --- manifest ----------------------------------------------------------------

func TestManifest(t *testing.T) {
	m := New(Options{})
	man := m.Manifest()
	if man.Name != ModuleName {
		t.Errorf("Name = %q, want %q", man.Name, ModuleName)
	}
	if man.SourceType != search.SourceTypeAcademic {
		t.Errorf("SourceType = %q, want academic", man.SourceType)
	}
	if man.CostTier != search.CostTierFree {
		t.Errorf("CostTier = %q, want free", man.CostTier)
	}
	if len(man.Scope) == 0 || len(man.Scope) > 200 {
		t.Errorf("Scope length = %d, want 1-200", len(man.Scope))
	}
}

// --- defaults ----------------------------------------------------------------

func TestNew_DefaultCount(t *testing.T) {
	m := New(Options{}).(*module)
	if m.count != defaultCount {
		t.Errorf("count = %d, want %d", m.count, defaultCount)
	}
}

func TestNew_ClampCount(t *testing.T) {
	m := New(Options{Count: 100}).(*module)
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
