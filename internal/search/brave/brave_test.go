package brave

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// --- sample JSON responses ---------------------------------------------------

const sampleResponse = `{
  "type": "search",
  "web": {
    "results": [
      {"title": "First Result", "url": "https://example.com/1", "description": "First snippet."},
      {"title": "Second Result", "url": "https://example.com/2", "description": "Second snippet."},
      {"title": "Third Result", "url": "https://example.com/3", "description": "Third snippet."}
    ]
  }
}`

const emptyResponse = `{"type": "search", "web": {"results": []}}`

// --- parseResponse tests -----------------------------------------------------

func TestParseResponse_Basic(t *testing.T) {
	results, err := parseResponse([]byte(sampleResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}

	want := []struct{ title, url, snippet string }{
		{"First Result", "https://example.com/1", "First snippet."},
		{"Second Result", "https://example.com/2", "Second snippet."},
		{"Third Result", "https://example.com/3", "Third snippet."},
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

func TestParseResponse_Empty(t *testing.T) {
	results, err := parseResponse([]byte(emptyResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseResponse_SkipsMalformed(t *testing.T) {
	j := `{"web":{"results":[
		{"title":"","url":"https://no-title.com","description":"x"},
		{"title":"Valid","url":"https://ok.com","description":"ok"}
	]}}`
	results, err := parseResponse([]byte(j))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].Title != "Valid" {
		t.Errorf("Title = %q, want Valid", results[0].Title)
	}
}

func TestParseResponse_InvalidJSON(t *testing.T) {
	_, err := parseResponse([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseResponse_NoWebField(t *testing.T) {
	results, err := parseResponse([]byte(`{"type":"search"}`))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseResponse_NoModuleFieldsSet(t *testing.T) {
	results, err := parseResponse([]byte(sampleResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
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
	u := buildURL("golang concurrency", 20)
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL prefix mismatch: %s", u)
	}
	if !strings.Contains(u, "q=golang+concurrency") {
		t.Errorf("URL missing query: %s", u)
	}
	if !strings.Contains(u, "count=20") {
		t.Errorf("URL missing count: %s", u)
	}
	if !strings.Contains(u, "text_decorations=false") {
		t.Errorf("URL missing text_decorations: %s", u)
	}
	if !strings.Contains(u, "result_filter=web") {
		t.Errorf("URL missing result_filter: %s", u)
	}
}

// --- mock HTTP client --------------------------------------------------------

type mockClient struct {
	fn func(*http.Request) (*http.Response, error)
}

func (m *mockClient) Do(req *http.Request) (*http.Response, error) { return m.fn(req) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// --- module Search tests (mocked HTTP) ---------------------------------------

func TestSearch_Success(t *testing.T) {
	var gotURL, gotToken, gotAccept string
	m := New(Options{
		APIKey: "test-key",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			gotToken = req.Header.Get("X-Subscription-Token")
			gotAccept = req.Header.Get("Accept")
			return jsonResponse(200, sampleResponse), nil
		}},
	})

	results, err := m.Search(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
	if gotToken != "test-key" {
		t.Errorf("X-Subscription-Token = %q, want test-key", gotToken)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if !strings.Contains(gotURL, "q=golang") {
		t.Errorf("URL missing query: %s", gotURL)
	}
}

func TestSearch_MissingAPIKey(t *testing.T) {
	m := New(Options{})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "API key required") {
		t.Errorf("error = %v, want contains 'API key required'", err)
	}
}

func TestSearch_HTTPError401(t *testing.T) {
	m := New(Options{
		APIKey: "bad-key",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(401, "invalid key"), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Errorf("error = %v, want contains 'invalid API key'", err)
	}
}

func TestSearch_HTTPError429(t *testing.T) {
	m := New(Options{
		APIKey: "k",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(429, "rate limited"), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want contains 'rate limited'", err)
	}
}

func TestSearch_HTTPError500(t *testing.T) {
	m := New(Options{
		APIKey: "k",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(500, "server error"), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %v, want contains 'HTTP 500'", err)
	}
}

// --- manifest ----------------------------------------------------------------

func TestManifest(t *testing.T) {
	m := New(Options{APIKey: "k"})
	man := m.Manifest()
	if man.Name != ModuleName {
		t.Errorf("Name = %q, want %q", man.Name, ModuleName)
	}
	if man.SourceType != search.SourceTypeGeneralWeb {
		t.Errorf("SourceType = %q, want general_web", man.SourceType)
	}
	if man.CostTier != search.CostTierCheap {
		t.Errorf("CostTier = %q, want cheap", man.CostTier)
	}
	if len(man.Scope) == 0 || len(man.Scope) > 200 {
		t.Errorf("Scope length = %d, want 1-200", len(man.Scope))
	}
}

// --- defaults ----------------------------------------------------------------

func TestNew_DefaultCount(t *testing.T) {
	m := New(Options{APIKey: "k"}).(*module)
	if m.count != defaultCount {
		t.Errorf("count = %d, want %d", m.count, defaultCount)
	}
}

func TestNew_ClampCount(t *testing.T) {
	m := New(Options{APIKey: "k", Count: 50}).(*module)
	if m.count != defaultCount {
		t.Errorf("count = %d, want %d (clamped)", m.count, defaultCount)
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
