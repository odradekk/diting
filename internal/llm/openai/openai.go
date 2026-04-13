// Package openai implements an llm.Client that calls the OpenAI Chat
// Completions API. The BaseURL is configurable so that any OpenAI-compatible
// endpoint (e.g., MiniMax, Together, vLLM) can reuse this client.
//
// Endpoint: POST {BaseURL}/chat/completions
// Docs: https://platform.openai.com/docs/api-reference/chat/create
package openai

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
	ProviderName = "openai"

	defaultBaseURL = "https://api.openai.com/v1"
	defaultModel   = "gpt-4.1-mini"
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

// Client implements llm.Client for OpenAI-compatible APIs.
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

// New creates an OpenAI-compatible LLM client.
func New(cfg llm.ProviderConfig, opts ...Option) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openai: API key required")
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

// Complete sends a chat completion request and returns the response.
func (c *Client) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("openai: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyError(resp.StatusCode, respBody)
	}

	return parseResponse(respBody)
}

// --- request building --------------------------------------------------------

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float32        `json:"temperature,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *rfJSONSchema   `json:"json_schema,omitempty"`
}

type rfJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

func (c *Client) buildRequestBody(req llm.Request) ([]byte, error) {
	var msgs []chatMessage

	// System message as first message with role:"system".
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
	}

	cr := chatRequest{
		Model:    c.model,
		Messages: msgs,
	}

	if req.MaxTokens > 0 {
		cr.MaxTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		t := req.Temperature
		cr.Temperature = &t
	}

	// Only send response_format for the native OpenAI endpoint.
	// Many OpenAI-compatible providers (MiniMax, Together, vLLM) don't
	// support json_schema enforcement and silently ignore or error on it.
	if req.JSONSchema != nil && c.baseURL == defaultBaseURL {
		cr.ResponseFormat = &responseFormat{
			Type: "json_schema",
			JSONSchema: &rfJSONSchema{
				Name:   req.JSONSchema.Name,
				Schema: req.JSONSchema.Schema,
				Strict: true,
			},
		}
	}

	return json.Marshal(cr)
}

// --- response parsing --------------------------------------------------------

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
	Error   *apiError    `json:"error,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	PromptDetails    *promptDetails   `json:"prompt_tokens_details,omitempty"`
}

type promptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func parseResponse(body []byte) (*llm.Response, error) {
	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("openai: unmarshal: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in response")
	}

	cached := 0
	if resp.Usage.PromptDetails != nil {
		cached = resp.Usage.PromptDetails.CachedTokens
	}

	return &llm.Response{
		Content:         resp.Choices[0].Message.Content,
		InputTokens:     resp.Usage.PromptTokens,
		OutputTokens:    resp.Usage.CompletionTokens,
		CacheReadTokens: cached,
	}, nil
}

// --- error classification ----------------------------------------------------

func classifyError(status int, body []byte) error {
	var resp chatResponse
	msg := ""
	if json.Unmarshal(body, &resp) == nil && resp.Error != nil {
		msg = resp.Error.Message
	}

	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("openai: invalid API key (401): %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("openai: rate limited (429): %s", msg)
	case http.StatusBadRequest:
		return fmt.Errorf("openai: bad request (400): %s", msg)
	default:
		return fmt.Errorf("openai: HTTP %d: %s", status, msg)
	}
}
