// Package llm defines the Client interface and types for LLM provider
// implementations. Each provider (anthropic, openai, minimax) lives in a
// subpackage and registers itself via Register at init time.
//
// See docs/architecture.md §7 for the full LLM layer specification.
package llm

import (
	"context"
	"encoding/json"
)

// Client is the interface every LLM provider implements.
type Client interface {
	// Complete sends a conversation and returns the next assistant message.
	// The returned Response includes token counts for cost reporting.
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Request describes a single LLM completion request.
type Request struct {
	// System is the system prompt. Providers map this to their native
	// system message format (top-level field for Anthropic, role:"system"
	// message for OpenAI-compatible).
	System string

	// Messages is the conversation history (user/assistant turns).
	Messages []Message

	// JSONSchema, when non-nil, instructs the provider to enforce
	// structured JSON output matching this schema.
	JSONSchema *JSONSchema

	// MaxTokens caps the response length. Zero means provider default.
	MaxTokens int

	// Temperature controls randomness. Zero means provider default.
	Temperature float32
}

// Message is a single turn in the conversation.
type Message struct {
	Role    string // "user" | "assistant"
	Content string
}

// JSONSchema describes a JSON schema for structured output enforcement.
type JSONSchema struct {
	// Name identifies the schema (used by OpenAI's response_format and
	// Anthropic's tool name).
	Name string

	// Schema is the raw JSON Schema bytes.
	Schema json.RawMessage
}

// Response is the result of a successful completion.
type Response struct {
	Content         string
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int // prompt caching hits (provider-specific)
}

// ProviderConfig is the configuration passed to provider factories.
type ProviderConfig struct {
	// APIKey is the authentication credential. Required for all providers.
	APIKey string

	// BaseURL overrides the provider's default API endpoint.
	// Empty means use the provider's default.
	BaseURL string

	// Model overrides the default model name.
	// Empty means use the provider's default.
	Model string
}
