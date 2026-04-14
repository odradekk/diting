// Package metaso implements a search.Module that queries the Metaso AI search
// API (秘塔搜索, metaso.cn). It requires a BYOK API key (Authorization: Bearer).
// Metaso is Chinese-first but handles English queries; results include snippets.
//
// Endpoint: POST https://metaso.cn/api/v1/search
// Docs: https://metaso.cn/api/docs
package metaso

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/odradekk/diting/internal/search"
)

const (
	// ModuleName is the registry key for this module.
	ModuleName = "metaso"

	baseURL      = "https://metaso.cn/api/v1/search"
	defaultCount = 10
	maxCount     = 50
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{APIKey: cfg.APIKey}), nil
	})
}

// Options configures the Metaso module.
type Options struct {
	// APIKey is the Metaso API key. Required.
	APIKey string
	// Count is the number of results per request. Defaults to 10, max 50.
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

// New creates a Metaso search module.
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
		CostTier:   search.CostTierCheap,
		Languages:  []string{"zh-Hans", "en"},
		Scope:      "AI-powered web search via metaso.cn. Strong for Chinese-language sources, decent English coverage. Returns ranked URLs with snippets. Paid via API key.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	if m.apiKey == "" {
		return nil, fmt.Errorf("metaso: API key required (set METASO_API_KEY)")
	}

	body, err := buildRequestBody(query, m.count)
	if err != nil {
		return nil, fmt.Errorf("metaso: build request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("metaso: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metaso: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPError(resp)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, fmt.Errorf("metaso: read body: %w", err)
	}

	return parseResponse(respBody)
}

func (m *module) Close() error { return nil }

// --- request building --------------------------------------------------------

type apiRequest struct {
	Q                 string `json:"q"`
	Scope             string `json:"scope"`
	IncludeSummary    bool   `json:"includeSummary"`
	Size              string `json:"size"`
	IncludeRawContent bool   `json:"includeRawContent"`
	ConciseSnippet    bool   `json:"conciseSnippet"`
}

func buildRequestBody(query string, count int) ([]byte, error) {
	r := apiRequest{
		Q:                 query,
		Scope:             "webpage",
		IncludeSummary:    false,
		Size:              strconv.Itoa(count),
		IncludeRawContent: false,
		ConciseSnippet:    false,
	}
	return json.Marshal(r)
}

// --- response parsing --------------------------------------------------------

// apiResponse is the top-level Metaso Search API response.
type apiResponse struct {
	Credits  int         `json:"credits"`
	Total    int         `json:"total"`
	Webpages []apiResult `json:"webpages"`
}

type apiResult struct {
	Title    string `json:"title"`
	Link     string `json:"link"`
	Score    string `json:"score"`
	Snippet  string `json:"snippet"`
	Position int    `json:"position"`
	Date     string `json:"date"`
}

func parseResponse(body []byte) ([]search.SearchResult, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("metaso: unmarshal: %w", err)
	}

	results := make([]search.SearchResult, 0, len(resp.Webpages))
	for _, r := range resp.Webpages {
		if r.Title == "" || r.Link == "" {
			continue
		}
		results = append(results, search.SearchResult{
			Title:   r.Title,
			URL:     r.Link,
			Snippet: r.Snippet,
		})
	}

	return results, nil
}

// --- error classification ----------------------------------------------------

func classifyHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(body))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("metaso: invalid API key (HTTP %d): %s", resp.StatusCode, msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("metaso: rate limited (HTTP 429): %s", msg)
	default:
		return fmt.Errorf("metaso: HTTP %d: %s", resp.StatusCode, msg)
	}
}
