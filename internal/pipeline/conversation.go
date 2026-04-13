// Package pipeline implements the diting search pipeline: plan → search →
// fetch → answer. The Conversation type manages the two-turn LLM interaction
// described in architecture §7.4.
package pipeline

import (
	"github.com/odradekk/diting/internal/llm"
)

// Conversation accumulates messages for the two-turn LLM pipeline.
// It manages the system prompt separately (Anthropic uses a top-level field,
// OpenAI uses a role:"system" message — the llm.Client handles this).
type Conversation struct {
	system   string
	messages []llm.Message
}

// NewConversation creates a conversation with the given system prompt.
func NewConversation(system string) *Conversation {
	return &Conversation{system: system}
}

// AddUser appends a user message.
func (c *Conversation) AddUser(content string) {
	c.messages = append(c.messages, llm.Message{Role: "user", Content: content})
}

// AddAssistant appends an assistant message (e.g., the plan response).
func (c *Conversation) AddAssistant(content string) {
	c.messages = append(c.messages, llm.Message{Role: "assistant", Content: content})
}

// Messages returns the current message list (read-only view).
func (c *Conversation) Messages() []llm.Message {
	out := make([]llm.Message, len(c.messages))
	copy(out, c.messages)
	return out
}

// AsRequest builds an llm.Request from the conversation state.
// If schema is non-nil, the LLM is instructed to emit structured JSON.
func (c *Conversation) AsRequest(schema *llm.JSONSchema, maxTokens int, temperature float32) llm.Request {
	return llm.Request{
		System:      c.system,
		Messages:    c.Messages(),
		JSONSchema:  schema,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}
}

// Len returns the number of messages (excluding system prompt).
func (c *Conversation) Len() int {
	return len(c.messages)
}
