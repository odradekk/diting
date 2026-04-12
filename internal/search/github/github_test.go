package github

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
  "total_count": 3,
  "items": [
    {
      "full_name": "golang/go",
      "html_url": "https://github.com/golang/go",
      "description": "The Go programming language",
      "language": "Go",
      "stargazers_count": 125000
    },
    {
      "full_name": "avelino/awesome-go",
      "html_url": "https://github.com/avelino/awesome-go",
      "description": "A curated list of awesome Go frameworks",
      "language": "Go",
      "stargazers_count": 130000
    },
    {
      "full_name": "gin-gonic/gin",
      "html_url": "https://github.com/gin-gonic/gin",
      "description": "Gin is a HTTP web framework written in Go",
      "language": "Go",
      "stargazers_count": 78000
    }
  ]
}`

const emptyResponse = `{"total_count": 0, "items": []}`

// --- parseResponse tests -----------------------------------------------------

func TestParseResponse_Basic(t *testing.T) {
	results, err := parseResponse([]byte(sampleResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}

	r := results[0]
	if r.Title != "golang/go" {
		t.Errorf("Title = %q, want golang/go", r.Title)
	}
	if r.URL != "https://github.com/golang/go" {
		t.Errorf("URL = %q", r.URL)
	}
	if !strings.Contains(r.Snippet, "[Go]") {
		t.Errorf("Snippet missing language: %q", r.Snippet)
	}
	if !strings.Contains(r.Snippet, "125000 stars") {
		t.Errorf("Snippet missing stars: %q", r.Snippet)
	}
	if !strings.Contains(r.Snippet, "Go programming language") {
		t.Errorf("Snippet missing description: %q", r.Snippet)
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

func TestParseResponse_NoLanguageNoStars(t *testing.T) {
	j := `{"total_count":1,"items":[{
		"full_name":"user/repo",
		"html_url":"https://github.com/user/repo",
		"description":"A project",
		"language":"",
		"stargazers_count":0
	}]}`
	results, err := parseResponse([]byte(j))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	// No language prefix, no stars suffix.
	if results[0].Snippet != "A project" {
		t.Errorf("Snippet = %q, want 'A project'", results[0].Snippet)
	}
}

func TestParseResponse_SkipsMalformed(t *testing.T) {
	j := `{"items":[
		{"full_name":"","html_url":"https://x.com","description":"no name"},
		{"full_name":"ok/repo","html_url":"https://github.com/ok/repo","description":"ok"}
	]}`
	results, err := parseResponse([]byte(j))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
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

// --- buildURL tests ----------------------------------------------------------

func TestBuildURL(t *testing.T) {
	u := buildURL("golang http framework", 10)
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL prefix mismatch: %s", u)
	}
	if !strings.Contains(u, "q=golang+http+framework") {
		t.Errorf("URL missing query: %s", u)
	}
	if !strings.Contains(u, "per_page=10") {
		t.Errorf("URL missing per_page: %s", u)
	}
}

// --- error classification ----------------------------------------------------

func TestClassifyHTTPError_401(t *testing.T) {
	err := classifyHTTPError(401, []byte(`{"message":"Bad credentials"}`))
	if !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("error = %v, want contains 'invalid token'", err)
	}
}

func TestClassifyHTTPError_403(t *testing.T) {
	err := classifyHTTPError(403, []byte(`{"message":"API rate limit exceeded"}`))
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want contains 'rate limited'", err)
	}
}

func TestClassifyHTTPError_422(t *testing.T) {
	err := classifyHTTPError(422, []byte(`{"message":"Validation Failed"}`))
	if !strings.Contains(err.Error(), "invalid query") {
		t.Errorf("error = %v, want contains 'invalid query'", err)
	}
}

// --- module Search tests (mocked HTTP) ---------------------------------------

func TestSearch_Success(t *testing.T) {
	var gotURL, gotAuth, gotAccept, gotVersion string
	m := New(Options{
		Token: "test-pat",
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			gotAuth = req.Header.Get("Authorization")
			gotAccept = req.Header.Get("Accept")
			gotVersion = req.Header.Get("X-GitHub-Api-Version")
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
	if gotAuth != "Bearer test-pat" {
		t.Errorf("Authorization = %q, want 'Bearer test-pat'", gotAuth)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", gotVersion)
	}
	if !strings.Contains(gotURL, "q=golang") {
		t.Errorf("URL missing query: %s", gotURL)
	}
}

func TestSearch_Anonymous(t *testing.T) {
	var gotAuth string
	m := New(Options{
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			return jsonResp(200, emptyResponse), nil
		}},
	})

	_, err := m.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty for anonymous", gotAuth)
	}
}

func TestSearch_HTTPError403(t *testing.T) {
	m := New(Options{
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(403, `{"message":"API rate limit exceeded"}`), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v", err)
	}
}

// --- manifest ----------------------------------------------------------------

func TestManifest(t *testing.T) {
	m := New(Options{})
	man := m.Manifest()
	if man.Name != ModuleName {
		t.Errorf("Name = %q, want %q", man.Name, ModuleName)
	}
	if man.SourceType != search.SourceTypeCode {
		t.Errorf("SourceType = %q, want code", man.SourceType)
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

// Verify context propagation.
func TestSearch_CancelledContext(t *testing.T) {
	m := New(Options{}).(*module)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := m.Search(ctx, "test")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
