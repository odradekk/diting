// Package anthropic implements an llm.Client that calls the Anthropic
// Messages API. System prompt is a top-level field (not in messages).
// Structured JSON output uses tool_choice + tools.
//
// Endpoint: POST https://api.anthropic.com/v1/messages
// Docs: https://docs.anthropic.com/en/api/messages
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/odradekk/diting/internal/llm"
)

const (
	// ProviderName is the registry key.
	ProviderName = "anthropic"

	defaultBaseURL = "https://api.anthropic.com"
	defaultModel   = "claude-sonnet-4-20250514"
	apiVersion     = "2023-06-01"
)

func init() {
	llm.Register(ProviderName, func(cfg llm.ProviderConfig) (llm.Client, error) {
		return New(cfg)
	})
}

// httpClient abstracts HTTP for testability.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client implements llm.Client for the Anthropic Messages API.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	client  httpClient
}

// Option configures the Client.
type Option func(*Client)

// WithHTTPClient overrides the default HTTP client. Test-only.
func WithHTTPClient(c httpClient) Option {
	return func(cl *Client) { cl.client = c }
}

// New creates an Anthropic LLM client.
func New(cfg llm.ProviderConfig, opts ...Option) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic: API key required")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}

	c := &Client{
		apiKey:  cfg.APIKey,
		baseURL: baseURL,
		model:   model,
		client:  http.DefaultClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Complete sends a Messages API request and returns the response.
func (c *Client) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("anthropic: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyError(resp.StatusCode, respBody)
	}

	return parseResponse(respBody)
}

// --- request building --------------------------------------------------------

type messagesRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	System     string           `json:"system,omitempty"`
	Messages   []messageBlock   `json:"messages"`
	Tools      []tool           `json:"tools,omitempty"`
	ToolChoice *toolChoice      `json:"tool_choice,omitempty"`
}

type messageBlock struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

func (c *Client) buildRequestBody(req llm.Request) ([]byte, error) {
	msgs := make([]messageBlock, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, messageBlock{Role: m.Role, Content: m.Content})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	mr := messagesRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  msgs,
	}

	// JSON schema enforcement via tool use.
	if req.JSONSchema != nil {
		mr.Tools = []tool{{
			Name:        req.JSONSchema.Name,
			Description: "Structured output",
			InputSchema: req.JSONSchema.Schema,
		}}
		mr.ToolChoice = &toolChoice{
			Type: "tool",
			Name: req.JSONSchema.Name,
		}
	}

	return json.Marshal(mr)
}

// --- response parsing --------------------------------------------------------

type messagesResponse struct {
	Content []contentBlock `json:"content"`
	Usage   usage          `json:"usage"`
	Error   *apiError      `json:"error,omitempty"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`  // for tool_use blocks
	Input json.RawMessage `json:"input,omitempty"` // for tool_use blocks
}

type usage struct {
	InputTokens            int `json:"input_tokens"`
	OutputTokens           int `json:"output_tokens"`
	CacheReadInputTokens   int `json:"cache_read_input_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func parseResponse(body []byte) (*llm.Response, error) {
	var resp messagesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: unmarshal: %w", err)
	}

	// Extract content: prefer text blocks, fall back to tool_use input.
	content := extractContent(resp.Content)
	if content == "" {
		return nil, fmt.Errorf("anthropic: no content in response")
	}

	return &llm.Response{
		Content:         content,
		InputTokens:     resp.Usage.InputTokens,
		OutputTokens:    resp.Usage.OutputTokens,
		CacheReadTokens: resp.Usage.CacheReadInputTokens,
	}, nil
}

// extractContent returns the first text block content, or for tool_use
// responses, the JSON-encoded input (which is the structured output).
func extractContent(blocks []contentBlock) string {
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	// Fall back to tool_use input (structured JSON output).
	for _, b := range blocks {
		if b.Type == "tool_use" && len(b.Input) > 0 {
			return string(b.Input)
		}
	}
	return ""
}

// --- error classification ----------------------------------------------------

func classifyError(status int, body []byte) error {
	var resp messagesResponse
	msg := ""
	if json.Unmarshal(body, &resp) == nil && resp.Error != nil {
		msg = resp.Error.Message
	}

	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("anthropic: invalid API key (401): %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("anthropic: rate limited (429): %s", msg)
	case 529: // overloaded
		return fmt.Errorf("anthropic: overloaded (529): %s", msg)
	case http.StatusBadRequest:
		return fmt.Errorf("anthropic: bad request (400): %s", msg)
	default:
		return fmt.Errorf("anthropic: HTTP %d: %s", status, msg)
	}
}
