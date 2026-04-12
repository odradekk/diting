// Package serp implements a search.Module that queries Google Search via the
// SerpAPI (serpapi.com). It requires a BYOK API key passed as a query
// parameter. Marked as "expensive" — the free tier is ~100 queries/month.
//
// Endpoint: GET https://serpapi.com/search.json?engine=google&q=...&api_key=...
// Docs: https://serpapi.com/search-api
package serp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/odradekk/diting/internal/search"
)

const (
	// ModuleName is the registry key for this module.
	ModuleName = "serp"

	baseURL      = "https://serpapi.com/search.json"
	defaultCount = 10
	maxCount     = 100
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{APIKey: cfg.APIKey}), nil
	})
}

// Options configures the SerpAPI module.
type Options struct {
	// APIKey is the SerpAPI key. Required.
	APIKey string
	// Count is the number of results per request. Defaults to 10.
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

// New creates a SerpAPI search module.
func New(opts Options) search.Module {
	count := opts.Count
	if count <= 0 {
		count = defaultCount
	}
	if count > maxCount {
		count = maxCount
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
		Languages:  []string{"en", "zh-Hans", "ja", "de", "fr", "es"},
		Scope:      "Google Search via SerpAPI. BYOK, paid. Highest quality general web results. Use as last resort.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	if m.apiKey == "" {
		return nil, fmt.Errorf("serp: API key required (set SERP_API_KEY)")
	}

	reqURL := buildURL(query, m.apiKey, m.count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("serp: build request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serp: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, fmt.Errorf("serp: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPError(resp.StatusCode, body)
	}

	return parseResponse(body)
}

func (m *module) Close() error { return nil }

// --- URL building ------------------------------------------------------------

func buildURL(query, apiKey string, count int) string {
	v := url.Values{}
	v.Set("engine", "google")
	v.Set("q", query)
	v.Set("api_key", apiKey)
	v.Set("num", strconv.Itoa(count))
	v.Set("output", "json")
	return baseURL + "?" + v.Encode()
}

// --- response parsing --------------------------------------------------------

type apiResponse struct {
	OrganicResults []organicResult `json:"organic_results"`
	Error          string          `json:"error"`
}

type organicResult struct {
	Position int    `json:"position"`
	Title    string `json:"title"`
	Link     string `json:"link"`
	Snippet  string `json:"snippet"`
}

func parseResponse(body []byte) ([]search.SearchResult, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("serp: unmarshal: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("serp: API error: %s", resp.Error)
	}

	results := make([]search.SearchResult, 0, len(resp.OrganicResults))
	for _, r := range resp.OrganicResults {
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

func classifyHTTPError(status int, body []byte) error {
	// Try to extract the error field from JSON body.
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		switch status {
		case http.StatusUnauthorized:
			return fmt.Errorf("serp: invalid API key: %s", errResp.Error)
		case http.StatusTooManyRequests:
			return fmt.Errorf("serp: quota exhausted: %s", errResp.Error)
		default:
			return fmt.Errorf("serp: HTTP %d: %s", status, errResp.Error)
		}
	}
	return fmt.Errorf("serp: HTTP %d", status)
}
