// Package exa implements a search.Module that queries the Exa neural search
// API. It requires a BYOK API key (x-api-key). Exa is credit-based and
// English-first, returning semantically ranked results with highlight snippets.
//
// Endpoint: POST https://api.exa.ai/search
// Docs: https://docs.exa.ai/reference/search
package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/odradekk/diting/internal/search"
)

const (
	// ModuleName is the registry key for this module.
	ModuleName = "exa"

	baseURL      = "https://api.exa.ai/search"
	defaultCount = 10
	maxCount     = 100
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{APIKey: cfg.APIKey}), nil
	})
}

// Options configures the Exa module.
type Options struct {
	// APIKey is the Exa API key. Required.
	APIKey string
	// Count is the number of results per request. Defaults to 10, max 100.
	Count int
	// client overrides the default HTTP client. Test-only.
	client httpClient
}

// httpClient abstracts HTTP for testability.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type module struct {
	apiKey string
	count  int
	client httpClient
}

// New creates an Exa search module.
func New(opts Options) search.Module {
	count := opts.Count
	if count <= 0 || count > maxCount {
		count = defaultCount
	}

	var c httpClient = http.DefaultClient
	if opts.client != nil {
		c = opts.client
	}

	return &module{apiKey: opts.APIKey, count: count, client: c}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeGeneralWeb,
		CostTier:   search.CostTierExpensive,
		Languages:  []string{"en"},
		Scope:      "Semantic web search via Exa neural index. Returns ranked URLs with highlight snippets. Best for conceptual queries where keyword match is insufficient. Paid (credits).",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	if m.apiKey == "" {
		return nil, fmt.Errorf("exa: API key required (set EXA_API_KEY)")
	}

	body, err := buildRequestBody(query, m.count)
	if err != nil {
		return nil, fmt.Errorf("exa: build request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("exa: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", m.apiKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exa: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPError(resp)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, fmt.Errorf("exa: read body: %w", err)
	}

	return parseResponse(respBody)
}

func (m *module) Close() error { return nil }

// --- request building --------------------------------------------------------

type apiRequest struct {
	Query      string         `json:"query"`
	Type       string         `json:"type"`
	NumResults int            `json:"numResults"`
	Highlights map[string]any `json:"highlights"`
}

func buildRequestBody(query string, count int) ([]byte, error) {
	r := apiRequest{
		Query:      query,
		Type:       "auto",
		NumResults: count,
		Highlights: map[string]any{},
	}
	return json.Marshal(r)
}

// --- response parsing --------------------------------------------------------

// apiResponse is the top-level Exa Search API response.
type apiResponse struct {
	Results []apiResult `json:"results"`
}

type apiResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	ID            string   `json:"id"`
	PublishedDate string   `json:"publishedDate"`
	Score         float64  `json:"score"`
	Highlights    []string `json:"highlights"`
}

func parseResponse(body []byte) ([]search.SearchResult, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("exa: unmarshal: %w", err)
	}

	results := make([]search.SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Title == "" || r.URL == "" {
			continue
		}
		results = append(results, search.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: strings.Join(r.Highlights, " "),
		})
	}

	return results, nil
}

// --- error classification ----------------------------------------------------

func classifyHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(body))

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("exa: invalid API key (HTTP %d): %s", resp.StatusCode, msg)
	case http.StatusPaymentRequired:
		return fmt.Errorf("exa: credits exhausted (HTTP 402): %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("exa: rate limited (HTTP 429): %s", msg)
	default:
		return fmt.Errorf("exa: HTTP %d: %s", resp.StatusCode, msg)
	}
}
