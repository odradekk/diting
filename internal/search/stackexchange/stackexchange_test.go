package stackexchange

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
  "items": [
    {
      "question_id": 100,
      "title": "How to use goroutines?",
      "link": "https://stackoverflow.com/questions/100",
      "score": 42,
      "answer_count": 5,
      "tags": ["go", "concurrency", "goroutine"],
      "is_answered": true
    },
    {
      "question_id": 200,
      "title": "Go channels explained",
      "link": "https://stackoverflow.com/questions/200",
      "score": 18,
      "answer_count": 3,
      "tags": ["go", "channels"],
      "is_answered": false
    },
    {
      "question_id": 300,
      "title": "Select statement in Go",
      "link": "https://stackoverflow.com/questions/300",
      "score": 5,
      "answer_count": 0,
      "tags": ["go"],
      "is_answered": false
    }
  ],
  "has_more": false,
  "quota_max": 300,
  "quota_remaining": 295
}`

const emptyResponse = `{"items":[],"has_more":false,"quota_max":300,"quota_remaining":299}`

const errorAPIResponse = `{
  "error_id": 502,
  "error_name": "throttle_violation",
  "error_message": "too many requests from this IP"
}`

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
	if r.Title != "How to use goroutines?" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.URL != "https://stackoverflow.com/questions/100" {
		t.Errorf("URL = %q", r.URL)
	}
	if !strings.Contains(r.Snippet, "go, concurrency, goroutine") {
		t.Errorf("Snippet missing tags: %q", r.Snippet)
	}
	if !strings.Contains(r.Snippet, "Score: 42") {
		t.Errorf("Snippet missing score: %q", r.Snippet)
	}
	if !strings.Contains(r.Snippet, "5 answers (accepted)") {
		t.Errorf("Snippet missing accepted answers: %q", r.Snippet)
	}
}

func TestParseResponse_Unanswered(t *testing.T) {
	results, err := parseResponse([]byte(sampleResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	// Third result has 0 answers.
	r := results[2]
	if !strings.Contains(r.Snippet, "unanswered") {
		t.Errorf("Snippet missing 'unanswered': %q", r.Snippet)
	}
}

func TestParseResponse_NotAccepted(t *testing.T) {
	results, err := parseResponse([]byte(sampleResponse))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	// Second result has answers but not accepted.
	r := results[1]
	if !strings.Contains(r.Snippet, "3 answers") {
		t.Errorf("Snippet missing answer count: %q", r.Snippet)
	}
	if strings.Contains(r.Snippet, "accepted") {
		t.Errorf("Snippet should not say accepted: %q", r.Snippet)
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
	_, err := parseResponse([]byte(errorAPIResponse))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "throttle_violation") {
		t.Errorf("error = %v, want contains 'throttle_violation'", err)
	}
}

func TestParseResponse_SkipsMalformed(t *testing.T) {
	j := `{"items":[
		{"question_id":1,"title":"","link":"https://x.com","score":0},
		{"question_id":2,"title":"Valid","link":"https://stackoverflow.com/q/2","score":1,"answer_count":0,"tags":["go"],"is_answered":false}
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

// --- formatSnippet tests -----------------------------------------------------

func TestFormatSnippet_ManyTags(t *testing.T) {
	item := questionItem{
		Tags:        []string{"a", "b", "c", "d", "e"},
		Score:       10,
		AnswerCount: 2,
		IsAnswered:  true,
	}
	s := formatSnippet(item)
	// Should cap at 4 tags.
	if !strings.Contains(s, "[a, b, c, d]") {
		t.Errorf("snippet = %q, want 4 tags capped", s)
	}
	if strings.Contains(s, ", e]") {
		t.Errorf("snippet has 5th tag: %q", s)
	}
}

// --- buildURL tests ----------------------------------------------------------

func TestBuildURL(t *testing.T) {
	u := buildURL("goroutine leak", "stackoverflow", "", 10)
	if !strings.HasPrefix(u, baseURL+"?") {
		t.Fatalf("URL prefix mismatch: %s", u)
	}
	if !strings.Contains(u, "q=goroutine+leak") {
		t.Errorf("URL missing query: %s", u)
	}
	if !strings.Contains(u, "site=stackoverflow") {
		t.Errorf("URL missing site: %s", u)
	}
	if !strings.Contains(u, "pagesize=10") {
		t.Errorf("URL missing pagesize: %s", u)
	}
	if !strings.Contains(u, "sort=relevance") {
		t.Errorf("URL missing sort: %s", u)
	}
	// No key parameter when empty.
	if strings.Contains(u, "key=") {
		t.Errorf("URL has key= when key is empty: %s", u)
	}
}

func TestBuildURL_WithKey(t *testing.T) {
	u := buildURL("test", "stackoverflow", "mykey", 10)
	if !strings.Contains(u, "key=mykey") {
		t.Errorf("URL missing key: %s", u)
	}
}

// --- error classification ----------------------------------------------------

func TestClassifyHTTPError_WithBody(t *testing.T) {
	err := classifyHTTPError(400, []byte(errorAPIResponse))
	if !strings.Contains(err.Error(), "too many requests") {
		t.Errorf("error = %v, want contains error message", err)
	}
}

func TestClassifyHTTPError_NoBody(t *testing.T) {
	err := classifyHTTPError(500, []byte(""))
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %v, want contains 'HTTP 500'", err)
	}
}

// --- module Search tests (mocked HTTP) ---------------------------------------

func TestSearch_Success(t *testing.T) {
	var gotURL string
	m := New(Options{
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return jsonResp(200, sampleResponse), nil
		}},
	})

	results, err := m.Search(context.Background(), "goroutine leak")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len = %d, want 3", len(results))
	}
	if !strings.Contains(gotURL, "q=goroutine+leak") {
		t.Errorf("URL missing query: %s", gotURL)
	}
	if !strings.Contains(gotURL, "site=stackoverflow") {
		t.Errorf("URL missing site: %s", gotURL)
	}
}

func TestSearch_HTTPError(t *testing.T) {
	m := New(Options{
		client: &mockClient{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(400, errorAPIResponse), nil
		}},
	})
	_, err := m.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "too many requests") {
		t.Errorf("error = %v", err)
	}
}

func TestSearch_CancelledContext(t *testing.T) {
	m := New(Options{}).(*module)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.Search(ctx, "test")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// --- manifest ----------------------------------------------------------------

func TestManifest(t *testing.T) {
	m := New(Options{})
	man := m.Manifest()
	if man.Name != ModuleName {
		t.Errorf("Name = %q, want %q", man.Name, ModuleName)
	}
	if man.SourceType != search.SourceTypeCommunity {
		t.Errorf("SourceType = %q, want community", man.SourceType)
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
	m := New(Options{}).(*module)
	if m.site != defaultSite {
		t.Errorf("site = %q, want %q", m.site, defaultSite)
	}
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

func TestNew_CustomSite(t *testing.T) {
	m := New(Options{Site: "serverfault"}).(*module)
	if m.site != "serverfault" {
		t.Errorf("site = %q, want serverfault", m.site)
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
