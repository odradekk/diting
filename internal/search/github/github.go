// Package github implements a search.Module that queries the GitHub REST
// Search API for repositories. An optional PAT (personal access token) lifts
// the rate limit from 10 to 30 requests/minute.
//
// Endpoint: GET https://api.github.com/search/repositories?q=...
// Docs: https://docs.github.com/en/rest/search/search#search-repositories
package github

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
	ModuleName = "github"

	baseURL      = "https://api.github.com/search/repositories"
	defaultCount = 10
	maxCount     = 30
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{Token: cfg.APIKey}), nil
	})
}

// Options configures the GitHub module.
type Options struct {
	// Token is an optional GitHub PAT. Anonymous works but has lower rate limits.
	Token string
	// Count is the number of results per request. Defaults to 10, max 30.
	Count int
	// client overrides the default HTTP client. Test-only.
	client httpClient
}

// httpClient abstracts HTTP for testability.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type module struct {
	token  string
	count  int
	client httpClient
}

// New creates a GitHub search module.
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

	return &module{token: opts.Token, count: count, client: c}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeCode,
		CostTier:   search.CostTierFree,
		Languages:  []string{"en"},
		Scope:      "Code repository search via GitHub REST API. Optional PAT for higher rate limits. Best for code and OSS queries.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	reqURL := buildURL(query, m.count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("github: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPError(resp.StatusCode, body)
	}

	return parseResponse(body)
}

func (m *module) Close() error { return nil }

// --- URL building ------------------------------------------------------------

func buildURL(query string, count int) string {
	v := url.Values{}
	v.Set("q", query)
	v.Set("per_page", strconv.Itoa(count))
	v.Set("sort", "best match")
	return baseURL + "?" + v.Encode()
}

// --- response parsing --------------------------------------------------------

type apiResponse struct {
	TotalCount int      `json:"total_count"`
	Items      []repoItem `json:"items"`
}

type repoItem struct {
	FullName    string `json:"full_name"`
	HTMLURL     string `json:"html_url"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Stars       int    `json:"stargazers_count"`
}

func parseResponse(body []byte) ([]search.SearchResult, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("github: unmarshal: %w", err)
	}

	results := make([]search.SearchResult, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.FullName == "" || item.HTMLURL == "" {
			continue
		}

		snippet := item.Description
		if item.Language != "" {
			snippet = fmt.Sprintf("[%s] %s", item.Language, snippet)
		}
		if item.Stars > 0 {
			snippet = fmt.Sprintf("%s (%d stars)", snippet, item.Stars)
		}

		results = append(results, search.SearchResult{
			Title:   item.FullName,
			URL:     item.HTMLURL,
			Snippet: snippet,
		})
	}

	return results, nil
}

// --- error classification ----------------------------------------------------

func classifyHTTPError(status int, body []byte) error {
	var errResp struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
		msg = errResp.Message
	}

	switch {
	case status == http.StatusUnauthorized:
		return fmt.Errorf("github: invalid token (HTTP 401): %s", msg)
	case status == http.StatusForbidden:
		return fmt.Errorf("github: rate limited or forbidden (HTTP 403): %s", msg)
	case status == http.StatusUnprocessableEntity:
		return fmt.Errorf("github: invalid query (HTTP 422): %s", msg)
	default:
		return fmt.Errorf("github: HTTP %d: %s", status, msg)
	}
}
