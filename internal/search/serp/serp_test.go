package serp

import (
	"context"
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

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// --- sample JSON responses ---------------------------------------------------

const sampleResponse = `{
  "search_metadata": {"status": "Success"},
  "organic_results": [
    {"position": 1, "title": "First Result", "link": "https://example.com/1", "snippet": "First snippet."},
    {"position": 2, "title": "Second Result", "link": "https://example.com/2", "snippet": "Second snippet."},
    {"position": 3, "title": "Third Result", "link": "https://example.com/3", "snippet": "Third snippet."}
  ]
}`

const emptyResponse = `{"search_metadata": {"status": "Success"}, "organic_results": []}`

const errorResponse = `{"error": "Invalid API key. Your API key should be here: https://serpapi.com/manage-api-key"}`

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

func TestParseResponse_APIError(t *testing.T) {
	_, err := parseResponse([]byte(errorResponse))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "API error") {
		t.Errorf("error = %v, want contains 'API error'", err)
	}
}

func TestParseResponse_SkipsMalformed(t *testing.T) {
	j := `{"organic_results":[
		{"position":1,"title":"","link":"https://no-title.com","snippet":"x"},
		{"position":2,"title":"Valid","link":"https://ok.com","snippet":"ok"}
	]}`
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

func TestParseResponse_NoOrganicResults(t *testing.T) {
	results, err := parseResponse([]byte(`{"search_metadata":{}}`))
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
	u := buildURL("golang concurrency", "test-key", 10)
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL prefix mismatch: %s", u)
	}
	if !strings.Contains(u, "q=golang+concurrency") {
		t.Errorf("URL missing query: %s", u)
	}
	if !strings.Contains(u, "api_key=test-key") {
		t.Errorf("URL missing api_key: %s", u)
	}
	if !strings.Contains(u, "engine=google") {
		t.Errorf("URL missing engine: %s", u)
	}
	if !strings.Contains(u, "num=10") {
		t.Errorf("URL missing num: %s", u)
	}
	if !strings.Contains(u, "output=json") {
		t.Errorf("URL missing output: %s", u)
	}
}

// --- error classification ----------------------------------------------------

func TestClassifyHTTPError_401(t *testing.T) {
	err := classifyHTTPError(401, []byte(errorResponse))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Errorf("error = %v, want contains 'invalid API key'", err)
	}
}

func TestClassifyHTTPError_429(t *testing.T) {
	err := classifyHTTPError(429, []byte(`{"error":"Your account has run out of searches."}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "quota exhausted") {
		t.Errorf("error = %v, want contains 'quota exhausted'", err)
	}
}

func TestClassifyHTTPError_500_NoJSON(t *testing.T) {
	err := classifyHTTPError(500, []byte("internal error"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %v, want contains 'HTTP 500'", err)
	}
}

// --- module Search tests (mocked HTTP) ---------------------------------------

func TestSearch_Success(t *testing.T) {
	var gotURL string
	m := New(Options{
		APIKey: "test-key",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return jsonResp(200, sampleResponse), nil
		}},
	})

	results, err := m.Search(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
	if !strings.Contains(gotURL, "q=golang") {
		t.Errorf("URL missing query: %s", gotURL)
	}
	if !strings.Contains(gotURL, "api_key=test-key") {
		t.Errorf("URL missing api_key: %s", gotURL)
	}
	if !strings.Contains(gotURL, "engine=google") {
		t.Errorf("URL missing engine: %s", gotURL)
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
		APIKey: "bad",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(401, errorResponse), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Errorf("error = %v", err)
	}
}

func TestSearch_HTTPError429(t *testing.T) {
	m := New(Options{
		APIKey: "k",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(429, `{"error":"out of searches"}`), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "quota exhausted") {
		t.Errorf("error = %v", err)
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
	if man.CostTier != search.CostTierExpensive {
		t.Errorf("CostTier = %q, want expensive", man.CostTier)
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
	m := New(Options{APIKey: "k", Count: 200}).(*module)
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
