package metaso

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// --- sample JSON responses ---------------------------------------------------

const sampleResponse = `{
  "credits": 3,
  "total": 124,
  "webpages": [
    {
      "title": "First Result",
      "link": "https://example.com/1",
      "score": "high",
      "snippet": "First snippet.",
      "position": 1,
      "date": "2025年08月09日"
    },
    {
      "title": "Second Result",
      "link": "https://example.com/2",
      "score": "medium",
      "snippet": "Second snippet.",
      "position": 2,
      "date": "2025年08月08日"
    },
    {
      "title": "Third Result",
      "link": "https://example.com/3",
      "score": "low",
      "snippet": "Third snippet.",
      "position": 3,
      "date": "2025年08月07日"
    }
  ]
}`

const emptyResponse = `{"credits": 0, "total": 0, "webpages": []}`

// --- parseResponse tests -----------------------------------------------------

func TestParseResponse_Empty(t *testing.T) {
	results, err := parseResponse([]byte(emptyResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseResponse_BasicResults(t *testing.T) {
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

func TestParseResponse_SkipsMalformed(t *testing.T) {
	j := `{"webpages":[
		{"title":"","link":"https://no-title.com","snippet":"x"},
		{"title":"Valid","link":"https://ok.com","snippet":"ok"}
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
	var gotAuth, gotContentType, gotAccept, gotMethod, gotURL string
	var gotBody map[string]any
	m := New(Options{
		APIKey: "test-key",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			gotContentType = req.Header.Get("Content-Type")
			gotAccept = req.Header.Get("Accept")
			gotMethod = req.Method
			gotURL = req.URL.String()
			if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			return jsonResponse(200, sampleResponse), nil
		}},
	})

	results, err := m.Search(context.Background(), "golang concurrency")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want 'Bearer test-key'", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotURL != baseURL {
		t.Errorf("URL = %q, want %q", gotURL, baseURL)
	}
	if gotBody["q"] != "golang concurrency" {
		t.Errorf("body.q = %v, want 'golang concurrency'", gotBody["q"])
	}
	// size must be encoded as a string, not a number — Metaso API requires this.
	if size, ok := gotBody["size"].(string); !ok || size == "" {
		t.Errorf("body.size = %v (%T), want non-empty string", gotBody["size"], gotBody["size"])
	}
	if gotBody["scope"] != "webpage" {
		t.Errorf("body.scope = %v, want 'webpage'", gotBody["scope"])
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

func TestSearch_AuthError(t *testing.T) {
	m := New(Options{
		APIKey: "bad-key",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(401, "unauthorized"), nil
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

func TestSearch_RateLimit(t *testing.T) {
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

func TestSearch_BadRequest(t *testing.T) {
	m := New(Options{
		APIKey: "k",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(400, "bad request"), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("error = %v, want contains 'HTTP 400'", err)
	}
}

func TestSearch_ServerError(t *testing.T) {
	m := New(Options{
		APIKey: "k",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(503, "service unavailable"), nil
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

func TestSearch_NetworkError(t *testing.T) {
	m := New(Options{
		APIKey: "k",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "metaso: request") {
		t.Errorf("error = %v, want contains 'metaso: request'", err)
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
	if len(man.Languages) == 0 {
		t.Error("Languages is empty")
	}
	if len(man.Scope) == 0 || len(man.Scope) > 200 {
		t.Errorf("Scope length = %d, want 1-200", len(man.Scope))
	}
}

// --- defaults ----------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	m := New(Options{APIKey: "k"}).(*module)
	if m.count != defaultCount {
		t.Errorf("count = %d, want %d", m.count, defaultCount)
	}
}

func TestNew_CustomCount(t *testing.T) {
	m := New(Options{APIKey: "k", Count: 25}).(*module)
	if m.count != 25 {
		t.Errorf("count = %d, want 25", m.count)
	}
}

func TestNew_CountAboveMaxResetsToDefault(t *testing.T) {
	m := New(Options{APIKey: "k", Count: 999}).(*module)
	if m.count != defaultCount {
		t.Errorf("count = %d, want %d (reset to default)", m.count, defaultCount)
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
