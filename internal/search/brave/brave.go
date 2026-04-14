// Package brave implements a search.Module that queries the Brave Web Search
// API. It requires a BYOK API key (X-Subscription-Token). The free tier
// provides 2000 queries/month at 1 query/second.
//
// Endpoint: GET https://api.search.brave.com/res/v1/web/search
// Docs: https://api-dashboard.search.brave.com/app/documentation/web-search
package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/odradekk/diting/internal/search"
)

const (
	// ModuleName is the registry key for this module.
	ModuleName = "brave"

	baseURL      = "https://api.search.brave.com/res/v1/web/search"
	defaultCount = 20 // max allowed by Brave API
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{APIKey: cfg.APIKey}), nil
	})
}

// Options configures the Brave module.
type Options struct {
	// APIKey is the Brave Search API subscription token. Required.
	APIKey string
	// Count is the number of results per request. Defaults to 20 (max).
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

// New creates a Brave search module.
func New(opts Options) search.Module {
	count := opts.Count
	if count <= 0 || count > defaultCount {
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
		Languages:  []string{"en", "de", "fr", "es", "ja", "zh-Hans"},
		Scope:      "General web search via Brave Search API. BYOK, 2000 free queries/month. Good for broad queries.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	if m.apiKey == "" {
		return nil, fmt.Errorf("brave: API key required (set BRAVE_API_KEY)")
	}

	reqURL := buildURL(query, m.count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Do NOT manually set Accept-Encoding here. net/http's Transport
	// auto-adds "Accept-Encoding: gzip" and transparently decodes the
	// response body only when the caller has NOT set the header itself.
	// Setting it manually disables transparent decoding, causing the
	// raw gzipped bytes to reach json.Unmarshal and fail at the first
	// byte ("invalid character '\x1f'" — the gzip magic).
	req.Header.Set("X-Subscription-Token", m.apiKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPError(resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, fmt.Errorf("brave: read body: %w", err)
	}

	return parseResponse(body)
}

func (m *module) Close() error { return nil }

// --- URL building ------------------------------------------------------------

func buildURL(query string, count int) string {
	v := url.Values{}
	v.Set("q", query)
	v.Set("count", strconv.Itoa(count))
	v.Set("text_decorations", "false") // no highlight markers in snippets
	v.Set("result_filter", "web")      // only web results
	return baseURL + "?" + v.Encode()
}

// --- response parsing --------------------------------------------------------

// apiResponse is the top-level Brave Search API response.
type apiResponse struct {
	Web struct {
		Results []apiResult `json:"results"`
	} `json:"web"`
}

type apiResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

func parseResponse(body []byte) ([]search.SearchResult, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("brave: unmarshal: %w", err)
	}

	results := make([]search.SearchResult, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		if r.Title == "" || r.URL == "" {
			continue
		}
		results = append(results, search.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
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
		return fmt.Errorf("brave: invalid API key (HTTP %d): %s", resp.StatusCode, msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("brave: rate limited (HTTP 429): %s", msg)
	default:
		return fmt.Errorf("brave: HTTP %d: %s", resp.StatusCode, msg)
	}
}
