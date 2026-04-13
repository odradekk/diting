package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// --- sample answer JSON ------------------------------------------------------

const sampleAnswerJSON = `{
  "answer": "Go uses goroutines for concurrency [1]. Channels provide communication between goroutines [2][3].",
  "citations": [
    {"id": 1, "url": "https://go.dev/doc", "title": "Go Documentation", "source_type": "docs"},
    {"id": 2, "url": "https://gobyexample.com/channels", "title": "Go by Example: Channels", "source_type": "docs"},
    {"id": 3, "url": "https://stackoverflow.com/q/123", "title": "Go channels explained", "source_type": "community"}
  ],
  "confidence": "high"
}`

// --- ParseAnswer tests -------------------------------------------------------

func TestParseAnswer_Basic(t *testing.T) {
	answer, err := ParseAnswer(sampleAnswerJSON)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if !strings.Contains(answer.Text, "goroutines") {
		t.Errorf("Text missing goroutines: %s", answer.Text[:50])
	}
	if len(answer.Citations) != 3 {
		t.Errorf("Citations = %d, want 3", len(answer.Citations))
	}
	if answer.Citations[0].ID != 1 || answer.Citations[0].URL != "https://go.dev/doc" {
		t.Errorf("Citations[0] = %+v", answer.Citations[0])
	}
	if answer.Confidence != "high" {
		t.Errorf("Confidence = %q, want high", answer.Confidence)
	}
}

func TestParseAnswer_WithCodeFences(t *testing.T) {
	fenced := "```json\n" + sampleAnswerJSON + "\n```"
	answer, err := ParseAnswer(fenced)
	if err != nil {
		t.Fatalf("ParseAnswer fenced: %v", err)
	}
	if len(answer.Citations) != 3 {
		t.Errorf("Citations = %d, want 3", len(answer.Citations))
	}
}

func TestParseAnswer_InvalidJSON(t *testing.T) {
	_, err := ParseAnswer("not json")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseAnswer_EmptyText(t *testing.T) {
	_, err := ParseAnswer(`{"answer":"","citations":[],"confidence":"low"}`)
	if err == nil {
		t.Fatal("expected error for empty answer text")
	}
}

func TestParseAnswer_UnknownConfidence(t *testing.T) {
	answer, err := ParseAnswer(`{"answer":"test","citations":[],"confidence":"very_high"}`)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if answer.Confidence != "medium" {
		t.Errorf("Confidence = %q, want medium (normalized from unknown)", answer.Confidence)
	}
}

func TestParseAnswer_MissingConfidence(t *testing.T) {
	answer, err := ParseAnswer(`{"answer":"test","citations":[]}`)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if answer.Confidence != "medium" {
		t.Errorf("Confidence = %q, want medium (default)", answer.Confidence)
	}
}

// --- ParseAnswer permissive backslash recovery ------------------------------
//
// Guards the Phase 5.7 Round 1 Patch 4 fix: MiniMax M2.7 HighSpeed
// occasionally emits literal backslashes (LaTeX commands, Windows paths,
// regex patterns) inside JSON string values without escaping them, which
// Go's strict json parser rejects. ParseAnswer now retries once with a
// permissive escape pass that doubles invalid backslashes.

func TestParseAnswer_RecoversInvalidBackslashInStringValue(t *testing.T) {
	// et_002's failure shape: a string value contains an unescaped \w
	// (which isn't a valid JSON escape).
	input := `{"answer":"Use \w+ to match word characters","citations":[],"confidence":"high"}`
	answer, err := ParseAnswer(input)
	if err != nil {
		t.Fatalf("ParseAnswer should recover: %v", err)
	}
	if !strings.Contains(answer.Text, `\w+`) {
		t.Errorf("Text should contain literal backslash-w: %q", answer.Text)
	}
	if answer.Confidence != "high" {
		t.Errorf("Confidence = %q, want high", answer.Confidence)
	}
}

func TestParseAnswer_RecoversWindowsPathInStringValue(t *testing.T) {
	input := `{"answer":"Check C:\Users\app for the file","citations":[],"confidence":"medium"}`
	answer, err := ParseAnswer(input)
	if err != nil {
		t.Fatalf("ParseAnswer should recover: %v", err)
	}
	if !strings.Contains(answer.Text, `C:\Users`) {
		t.Errorf("Text should contain literal Windows path: %q", answer.Text)
	}
}

func TestParseAnswer_RecoversStructuralBackslash(t *testing.T) {
	// A stray backslash in structural position (outside strings) is
	// never legal JSON — the recovery drops it. The input here has a
	// \ before the closing brace, which the permissive path strips.
	input := `{"answer":"hi","citations":[],"confidence":"low"\}`
	answer, err := ParseAnswer(input)
	if err != nil {
		t.Fatalf("ParseAnswer should recover: %v", err)
	}
	if answer.Text != "hi" {
		t.Errorf("Text = %q, want 'hi'", answer.Text)
	}
}

func TestParseAnswer_DoesNotCorruptValidEscapes(t *testing.T) {
	// Valid \n, \\, \", and \uXXXX escapes must NOT be altered by the
	// permissive recovery path. If the strict parse succeeds on the
	// original, the permissive path is never reached — but even when
	// it runs, it must preserve valid escapes.
	input := `{"answer":"line 1\nline 2 with \"quote\" and \\backslash and \u00e9","citations":[],"confidence":"high"}`
	answer, err := ParseAnswer(input)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if !strings.Contains(answer.Text, "\n") {
		t.Error("newline lost")
	}
	if !strings.Contains(answer.Text, `"quote"`) {
		t.Error("quotes lost")
	}
	if !strings.Contains(answer.Text, `\backslash`) {
		t.Error("backslash lost")
	}
	if !strings.Contains(answer.Text, "é") {
		t.Error("unicode escape lost")
	}
}

// --- escapeStrayBackslashes unit tests --------------------------------------

func TestEscapeStrayBackslashes(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"clean JSON unchanged", `{"a":"b"}`, `{"a":"b"}`},
		{"stray backslash outside string dropped", `{"a":1,\"b":2}`, `{"a":1,"b":2}`},
		{"invalid \\w doubled", `"hello\world"`, `"hello\\world"`},
		{"Windows path doubled", `"c:\windows\system32"`, `"c:\\windows\\system32"`},
		{"valid \\n preserved", `"a\nb"`, `"a\nb"`},
		{"valid \\\" preserved", `"a\"b"`, `"a\"b"`},
		{"valid \\\\ preserved", `"a\\b"`, `"a\\b"`},
		{"valid \\u preserved", `"\u00e9"`, `"\u00e9"`},
		{"bad \\u doubled", `"\uZZZZ"`, `"\\uZZZZ"`},
		{"trailing backslash in string doubled", `"abc\`, `"abc\\`},
	}
	for _, tt := range tests {
		got := escapeStrayBackslashes(tt.in)
		if got != tt.want {
			t.Errorf("%s: escapeStrayBackslashes(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

// --- AnswerSchema validity ---------------------------------------------------

func TestAnswerSchema_ValidJSON(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal(AnswerSchema.Schema, &parsed); err != nil {
		t.Fatalf("AnswerSchema is not valid JSON: %v", err)
	}
}

// --- FormatFetchedContent tests ----------------------------------------------

func TestFormatFetchedContent_Basic(t *testing.T) {
	sources := []FetchedSource{
		{
			ID: 1,
			Result: ScoredResult{
				SearchResult: search.SearchResult{Title: "Go Docs", URL: "https://go.dev/doc", SourceType: search.SourceTypeDocs},
				Score:        0.92,
			},
			Fetched: &fetch.Result{Content: "Go is an open-source programming language."},
		},
		{
			ID: 2,
			Result: ScoredResult{
				SearchResult: search.SearchResult{Title: "SO Answer", URL: "https://stackoverflow.com/q/1", SourceType: search.SourceTypeCommunity},
				Score:        0.85,
			},
			Fetched: &fetch.Result{Content: "Use goroutines for concurrency."},
		},
	}

	formatted := FormatFetchedContent(sources)
	if !strings.Contains(formatted, "SOURCE 1 [docs / score 0.92]") {
		t.Errorf("missing SOURCE 1 header: %s", formatted[:100])
	}
	if !strings.Contains(formatted, "SOURCE 2 [community / score 0.85]") {
		t.Error("missing SOURCE 2 header")
	}
	if !strings.Contains(formatted, "URL: https://go.dev/doc") {
		t.Error("missing URL")
	}
	if !strings.Contains(formatted, "Go is an open-source") {
		t.Error("missing content")
	}
}

func TestFormatFetchedContent_SkipsNilFetch(t *testing.T) {
	sources := []FetchedSource{
		{ID: 1, Result: ScoredResult{}, Fetched: nil}, // fetch failed
		{
			ID: 2,
			Result: ScoredResult{
				SearchResult: search.SearchResult{Title: "T", URL: "https://a.com"},
				Score:        0.5,
			},
			Fetched: &fetch.Result{Content: "ok"},
		},
	}

	formatted := FormatFetchedContent(sources)
	if strings.Contains(formatted, "SOURCE 1") {
		t.Error("should skip nil-fetched source")
	}
	if !strings.Contains(formatted, "SOURCE 2") {
		t.Error("should include fetched source")
	}
}

func TestFormatFetchedContent_TruncatesLongContent(t *testing.T) {
	longContent := strings.Repeat("a", 10000)
	sources := []FetchedSource{
		{
			ID: 1,
			Result: ScoredResult{
				SearchResult: search.SearchResult{Title: "T", URL: "https://a.com"},
				Score:        0.5,
			},
			Fetched: &fetch.Result{Content: longContent},
		},
	}

	formatted := FormatFetchedContent(sources)
	if !strings.Contains(formatted, "[content truncated]") {
		t.Error("missing truncation marker")
	}
	if len(formatted) > 9000 {
		t.Errorf("formatted too long: %d chars", len(formatted))
	}
}

// --- RunAnswerPhase with mock LLM --------------------------------------------

func TestRunAnswerPhase_Success(t *testing.T) {
	var gotMsgCount int
	client := &mockLLM{fn: func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		gotMsgCount = len(req.Messages)
		return &llm.Response{
			Content:      sampleAnswerJSON,
			InputTokens:  500,
			OutputTokens: 100,
		}, nil
	}}

	conv := NewConversation("system")
	conv.AddUser("How does Go concurrency work?")

	sources := []FetchedSource{
		{
			ID:     1,
			Result: ScoredResult{SearchResult: search.SearchResult{Title: "T", URL: "https://a.com", SourceType: search.SourceTypeDocs}, Score: 0.9},
			Fetched: &fetch.Result{Content: "Goroutines are lightweight threads."},
		},
	}

	result, err := RunAnswerPhase(context.Background(), client, conv, `{"plan":"..."}`, sources, 4096)
	if err != nil {
		t.Fatalf("RunAnswerPhase: %v", err)
	}

	if result.Answer.Text == "" {
		t.Error("Answer text is empty")
	}
	if len(result.Answer.Citations) != 3 {
		t.Errorf("Citations = %d, want 3", len(result.Answer.Citations))
	}
	if result.InputTokens != 500 {
		t.Errorf("InputTokens = %d", result.InputTokens)
	}

	// Conversation should have: user(question) + assistant(plan) + user(answer instructions) = 3 messages
	if gotMsgCount != 3 {
		t.Errorf("messages sent to LLM = %d, want 3", gotMsgCount)
	}
}

func TestRunAnswerPhase_LLMError(t *testing.T) {
	client := &mockLLM{fn: func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		return nil, fmt.Errorf("overloaded")
	}}

	conv := NewConversation("sys")
	conv.AddUser("q")

	_, err := RunAnswerPhase(context.Background(), client, conv, `{}`, nil, 4096)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "llm") {
		t.Errorf("error = %v", err)
	}
}

func TestRunAnswerPhase_ParseError(t *testing.T) {
	client := &mockLLM{fn: func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		return &llm.Response{Content: "not json"}, nil
	}}

	conv := NewConversation("sys")
	conv.AddUser("q")

	_, err := RunAnswerPhase(context.Background(), client, conv, `{}`, nil, 4096)
	if err == nil {
		t.Fatal("expected error for invalid answer JSON")
	}
}
