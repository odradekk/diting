package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// Answer is the parsed output of the answer phase (LLM turn 2).
type Answer struct {
	Text       string     `json:"answer"`
	Citations  []Citation `json:"citations"`
	Confidence string     `json:"confidence"` // "high" | "medium" | "low" | "insufficient"
}

// Citation links an inline reference [N] to a source.
type Citation struct {
	ID         int                `json:"id"`
	URL        string             `json:"url"`
	Title      string             `json:"title"`
	SourceType search.SourceType  `json:"source_type"`
}

// AnswerSchema is the JSON schema enforced on the LLM during the answer phase.
var AnswerSchema = &llm.JSONSchema{
	Name: "search_answer",
	Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "answer": {
      "type": "string",
      "description": "The answer with inline citations like [1] or [2][3]"
    },
    "citations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id":          { "type": "integer" },
          "url":         { "type": "string" },
          "title":       { "type": "string" },
          "source_type": { "type": "string" }
        },
        "required": ["id", "url", "title", "source_type"],
        "additionalProperties": false
      }
    },
    "confidence": {
      "type": "string",
      "enum": ["high", "medium", "low", "insufficient"]
    }
  },
  "required": ["answer", "citations", "confidence"],
  "additionalProperties": false
}`),
}

// FetchedSource pairs a scored search result with its fetched content.
type FetchedSource struct {
	ID      int               // 1-based index for citation
	Result  ScoredResult      // scored search result metadata
	Fetched *fetch.Result     // fetched content (nil if fetch failed)
}

// AnswerResult holds the answer phase output plus the raw LLM response.
type AnswerResult struct {
	Answer          Answer
	RawContent      string
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
}

// RunAnswerPhase executes the answer phase: formats fetched content, appends
// to the conversation (preserving turn-1 context), calls the LLM, and parses
// the structured answer.
func RunAnswerPhase(
	ctx context.Context,
	client llm.Client,
	conv *Conversation,
	planRawContent string,
	sources []FetchedSource,
	maxTokens int,
) (*AnswerResult, error) {
	// Append plan response as assistant message (turn 1 continuation).
	conv.AddAssistant(planRawContent)

	// Format fetched content and render answer instructions.
	formatted := FormatFetchedContent(sources)
	answerInstructions, err := RenderAnswer(AnswerPromptData{Sources: formatted})
	if err != nil {
		return nil, fmt.Errorf("answer: render instructions: %w", err)
	}
	conv.AddUser(answerInstructions)

	// Call LLM with answer schema.
	req := conv.AsRequest(AnswerSchema, maxTokens, 0)
	resp, err := client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("answer: llm: %w", err)
	}

	answer, err := ParseAnswer(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("answer: parse: %w", err)
	}

	return &AnswerResult{
		Answer:          answer,
		RawContent:      resp.Content,
		InputTokens:     resp.InputTokens,
		OutputTokens:    resp.OutputTokens,
		CacheReadTokens: resp.CacheReadTokens,
	}, nil
}

// FormatFetchedContent renders the fetched sources into the structured content
// block that the LLM receives in turn 2. Format matches architecture §7.4.
func FormatFetchedContent(sources []FetchedSource) string {
	var b strings.Builder
	for _, src := range sources {
		if src.Fetched == nil {
			continue
		}
		fmt.Fprintf(&b, "SOURCE %d [%s / score %.2f]\n", src.ID, src.Result.SourceType, src.Result.Score)
		fmt.Fprintf(&b, "URL: %s\n", src.Result.URL)
		fmt.Fprintf(&b, "Title: %s\n", src.Result.Title)

		content := src.Fetched.Content
		// Cap content per source to avoid blowing the context window.
		const maxContentPerSource = 8000
		if len(content) > maxContentPerSource {
			content = content[:maxContentPerSource] + "\n[content truncated]"
		}
		fmt.Fprintf(&b, "Content:\n%s\n\n", content)
	}
	return b.String()
}

// ParseAnswer extracts an Answer from the LLM's JSON response.
func ParseAnswer(content string) (Answer, error) {
	content = trimJSONFences(content)

	var answer Answer
	if err := json.Unmarshal([]byte(content), &answer); err != nil {
		return Answer{}, fmt.Errorf("invalid answer JSON: %w", err)
	}

	if answer.Text == "" {
		return Answer{}, fmt.Errorf("answer text is empty")
	}

	// Normalize confidence.
	switch answer.Confidence {
	case "high", "medium", "low", "insufficient":
		// valid
	case "":
		answer.Confidence = "medium" // default
	default:
		answer.Confidence = "medium" // unknown → medium
	}

	return answer, nil
}
