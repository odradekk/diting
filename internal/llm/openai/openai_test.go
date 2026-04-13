package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/llm"
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

// --- sample responses --------------------------------------------------------

const sampleResponse = `{
  "choices": [{"message": {"role": "assistant", "content": "Hello world"}}],
  "usage": {
    "prompt_tokens": 50,
    "completion_tokens": 10,
    "prompt_tokens_details": {"cached_tokens": 30}
  }
}`

const sampleResponseNoCache = `{
  "choices": [{"message": {"role": "assistant", "content": "No cache"}}],
  "usage": {"prompt_tokens": 50, "completion_tokens": 10}
}`

const emptyChoicesResponse = `{"choices": [], "usage": {"prompt_tokens": 0, "completion_tokens": 0}}`

// --- Complete tests ----------------------------------------------------------

func TestComplete_Success(t *testing.T) {
	var gotBody []byte
	var gotAuth, gotURL string

	c, _ := New(llm.ProviderConfig{APIKey: "test-key", Model: "gpt-4.1-mini"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			gotAuth = req.Header.Get("Authorization")
			gotBody, _ = io.ReadAll(req.Body)
			return jsonResp(200, sampleResponse), nil
		},
	}))

	resp, err := c.Complete(context.Background(), llm.Request{
		System:   "You are helpful.",
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Response fields.
	if resp.Content != "Hello world" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", resp.InputTokens)
	}
	if resp.OutputTokens != 10 {
		t.Errorf("OutputTokens = %d, want 10", resp.OutputTokens)
	}
	if resp.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", resp.CacheReadTokens)
	}

	// Auth header.
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}

	// URL ends with /chat/completions.
	if !strings.HasSuffix(gotURL, "/chat/completions") {
		t.Errorf("URL = %q, want suffix /chat/completions", gotURL)
	}

	// Request body: system message as first message.
	var cr chatRequest
	json.Unmarshal(gotBody, &cr)
	if len(cr.Messages) < 2 {
		t.Fatalf("Messages len = %d, want >= 2", len(cr.Messages))
	}
	if cr.Messages[0].Role != "system" || cr.Messages[0].Content != "You are helpful." {
		t.Errorf("Messages[0] = %+v, want system message", cr.Messages[0])
	}
	if cr.Messages[1].Role != "user" || cr.Messages[1].Content != "Hi" {
		t.Errorf("Messages[1] = %+v, want user message", cr.Messages[1])
	}
}

func TestComplete_NoCacheTokens(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(200, sampleResponseNoCache), nil
		},
	}))

	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want 0", resp.CacheReadTokens)
	}
}

func TestComplete_JSONSchema(t *testing.T) {
	var gotBody []byte
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			gotBody, _ = io.ReadAll(req.Body)
			return jsonResp(200, sampleResponse), nil
		},
	}))

	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`)
	_, err := c.Complete(context.Background(), llm.Request{
		Messages:   []llm.Message{{Role: "user", Content: "Hi"}},
		JSONSchema: &llm.JSONSchema{Name: "plan", Schema: schema},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var cr chatRequest
	json.Unmarshal(gotBody, &cr)
	if cr.ResponseFormat == nil {
		t.Fatal("ResponseFormat is nil")
	}
	if cr.ResponseFormat.Type != "json_schema" {
		t.Errorf("ResponseFormat.Type = %q, want json_schema", cr.ResponseFormat.Type)
	}
	if cr.ResponseFormat.JSONSchema == nil {
		t.Fatal("ResponseFormat.JSONSchema is nil")
	}
	if cr.ResponseFormat.JSONSchema.Name != "plan" {
		t.Errorf("JSONSchema.Name = %q, want plan", cr.ResponseFormat.JSONSchema.Name)
	}
	if !cr.ResponseFormat.JSONSchema.Strict {
		t.Error("JSONSchema.Strict = false, want true")
	}
}

func TestComplete_EmptyChoices(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(200, emptyChoicesResponse), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error = %v", err)
	}
}

// --- error handling ----------------------------------------------------------

func TestComplete_HTTPError401(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "bad"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(401, `{"error":{"message":"invalid key","type":"auth"}}`), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Errorf("error = %v", err)
	}
}

func TestComplete_HTTPError429(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(429, `{"error":{"message":"rate limited"}}`), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v", err)
	}
}

// --- New validation ----------------------------------------------------------

func TestNew_MissingAPIKey(t *testing.T) {
	_, err := New(llm.ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestNew_Defaults(t *testing.T) {
	c, err := New(llm.ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultBaseURL)
	}
	if c.model != defaultModel {
		t.Errorf("model = %q, want %q", c.model, defaultModel)
	}
}

func TestNew_CustomBaseURL(t *testing.T) {
	c, err := New(llm.ProviderConfig{APIKey: "k", BaseURL: "https://custom.api.com/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != "https://custom.api.com/v1" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

// --- registration ------------------------------------------------------------

func TestRegistration(t *testing.T) {
	names := llm.List()
	found := false
	for _, n := range names {
		if n == ProviderName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("provider %q not in registry; got %v", ProviderName, names)
	}
}
