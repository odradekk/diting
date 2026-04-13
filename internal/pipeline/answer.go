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
//
// If the strict parse fails, ParseAnswer retries once with a permissive
// pre-processor that (a) drops stray backslashes in structural positions
// and (b) doubles stray backslashes inside string literals that don't
// form a valid JSON escape sequence. This recovers from LLMs — notably
// MiniMax M2.7 — that emit raw LaTeX commands, Windows paths, or regex
// literals without escaping them for JSON. The Phase 5.7 first run hit
// this on et_002 ("invalid character '\\' looking for beginning of
// object key string"), which this recovery fixes.
func ParseAnswer(content string) (Answer, error) {
	content = trimJSONFences(content)

	answer, err := unmarshalAnswer(content)
	if err != nil {
		// Retry once with permissive escaping. The patched content is
		// byte-different from the original only if there was a stray
		// backslash to fix — so we don't burn the retry on already-clean
		// inputs.
		patched := escapeStrayBackslashes(content)
		if patched != content {
			if recovered, rerr := unmarshalAnswer(patched); rerr == nil {
				answer = recovered
				err = nil
			}
		}
	}
	if err != nil {
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

func unmarshalAnswer(content string) (Answer, error) {
	var a Answer
	return a, json.Unmarshal([]byte(content), &a)
}

// escapeStrayBackslashes walks JSON text and fixes two common LLM
// escaping bugs:
//
//  1. A bare '\' in structural position (outside string literals) is
//     never legal JSON. We drop it entirely. This recovers input like
//     `{"a": 1, \"b": 2}` → `{"a": 1, "b": 2}`.
//
//  2. A '\' inside a string literal that isn't followed by a valid
//     escape character (", \, /, b, f, n, r, t, u + 4 hex) is doubled
//     to become a literal backslash. This recovers input like
//     `"c:\windows"` → `"c:\\windows"`, which Go's strict json parser
//     would otherwise reject.
//
// The function is a zero-cost noop when the input is already clean —
// it only allocates a new string when the output differs from the input.
// Used by ParseAnswer's permissive fallback only.
func escapeStrayBackslashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			if c == '\\' {
				// Structural stray backslash — drop it.
				continue
			}
			if c == '"' {
				inString = true
			}
			b.WriteByte(c)
			continue
		}
		// Inside a string literal.
		if c == '"' {
			inString = false
			b.WriteByte(c)
			continue
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		// Backslash inside a string: check if it's a valid escape.
		if i+1 >= len(s) {
			// Trailing backslash at EOF — double it.
			b.WriteString(`\\`)
			continue
		}
		next := s[i+1]
		switch next {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			// Valid two-char escape — emit as-is, consume both bytes.
			b.WriteByte(c)
			b.WriteByte(next)
			i++
		case 'u':
			// \uXXXX is valid only if followed by 4 hex digits.
			if i+5 < len(s) && isHexByte(s[i+2]) && isHexByte(s[i+3]) && isHexByte(s[i+4]) && isHexByte(s[i+5]) {
				b.WriteByte(c)
				b.WriteByte(next)
				i++
				continue
			}
			// Invalid \u sequence — treat as literal backslash.
			b.WriteString(`\\`)
		default:
			// Invalid escape — treat as literal backslash.
			b.WriteString(`\\`)
		}
	}
	return b.String()
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
