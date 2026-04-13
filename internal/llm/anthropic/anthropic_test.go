package anthropic

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

const sampleTextResponse = `{
  "content": [{"type": "text", "text": "Hello from Claude"}],
  "usage": {
    "input_tokens": 100,
    "output_tokens": 20,
    "cache_read_input_tokens": 50
  }
}`

const sampleToolResponse = `{
  "content": [{"type": "tool_use", "name": "plan", "input": {"answer": "structured"}}],
  "usage": {"input_tokens": 80, "output_tokens": 15, "cache_read_input_tokens": 0}
}`

const emptyContentResponse = `{
  "content": [],
  "usage": {"input_tokens": 10, "output_tokens": 0, "cache_read_input_tokens": 0}
}`

// --- Complete tests ----------------------------------------------------------

func TestComplete_TextResponse(t *testing.T) {
	var gotBody []byte
	var gotAPIKey, gotVersion, gotURL string

	c, _ := New(llm.ProviderConfig{APIKey: "test-key"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			gotAPIKey = req.Header.Get("x-api-key")
			gotVersion = req.Header.Get("anthropic-version")
			gotBody, _ = io.ReadAll(req.Body)
			return jsonResp(200, sampleTextResponse), nil
		},
	}))

	resp, err := c.Complete(context.Background(), llm.Request{
		System:   "You are helpful.",
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Response.
	if resp.Content != "Hello from Claude" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", resp.InputTokens)
	}
	if resp.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", resp.OutputTokens)
	}
	if resp.CacheReadTokens != 50 {
		t.Errorf("CacheReadTokens = %d, want 50", resp.CacheReadTokens)
	}

	// Headers.
	if gotAPIKey != "test-key" {
		t.Errorf("x-api-key = %q", gotAPIKey)
	}
	if gotVersion != apiVersion {
		t.Errorf("anthropic-version = %q", gotVersion)
	}
	if !strings.HasSuffix(gotURL, "/v1/messages") {
		t.Errorf("URL = %q, want suffix /v1/messages", gotURL)
	}

	// Request body: system as top-level field.
	var mr messagesRequest
	json.Unmarshal(gotBody, &mr)
	if mr.System != "You are helpful." {
		t.Errorf("System = %q", mr.System)
	}
	if len(mr.Messages) != 1 || mr.Messages[0].Role != "user" {
		t.Errorf("Messages = %+v", mr.Messages)
	}
	// No tools when JSONSchema is nil.
	if len(mr.Tools) != 0 {
		t.Errorf("Tools = %+v, want empty", mr.Tools)
	}
}

func TestComplete_ToolUseResponse(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(200, sampleToolResponse), nil
		},
	}))

	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Tool use input is returned as content string.
	if !strings.Contains(resp.Content, "structured") {
		t.Errorf("Content = %q, want contains 'structured'", resp.Content)
	}
}

func TestComplete_JSONSchema_ToolChoice(t *testing.T) {
	var gotBody []byte
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			gotBody, _ = io.ReadAll(req.Body)
			return jsonResp(200, sampleToolResponse), nil
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

	var mr messagesRequest
	json.Unmarshal(gotBody, &mr)

	if len(mr.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(mr.Tools))
	}
	if mr.Tools[0].Name != "plan" {
		t.Errorf("Tool name = %q, want plan", mr.Tools[0].Name)
	}
	if mr.ToolChoice == nil || mr.ToolChoice.Type != "tool" || mr.ToolChoice.Name != "plan" {
		t.Errorf("ToolChoice = %+v, want {type:tool, name:plan}", mr.ToolChoice)
	}
}

func TestComplete_EmptyContent(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(200, emptyContentResponse), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "no content") {
		t.Errorf("error = %v", err)
	}
}

// --- error handling ----------------------------------------------------------

func TestComplete_HTTPError401(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "bad"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(401, `{"error":{"type":"authentication_error","message":"invalid key"}}`), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Errorf("error = %v", err)
	}
}

func TestComplete_HTTPError429(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(429, `{"error":{"type":"rate_limit_error","message":"too fast"}}`), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v", err)
	}
}

func TestComplete_HTTPError529(t *testing.T) {
	c, _ := New(llm.ProviderConfig{APIKey: "k"}, WithHTTPClient(&mockClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return jsonResp(529, `{"error":{"type":"overloaded_error","message":"overloaded"}}`), nil
		},
	}))

	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "Hi"}},
	})
	if !strings.Contains(err.Error(), "overloaded") {
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
	c, _ := New(llm.ProviderConfig{APIKey: "k"})
	if c.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.model != defaultModel {
		t.Errorf("model = %q", c.model)
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
