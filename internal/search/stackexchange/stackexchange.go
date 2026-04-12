// Package stackexchange implements a search.Module that queries the
// StackExchange REST API (v2.3). No API key is required; anonymous access
// allows 300 requests/day. An optional key parameter lifts the quota.
//
// Endpoint: GET https://api.stackexchange.com/2.3/search/advanced
// Docs: https://api.stackexchange.com/docs/advanced-search
//
// Note: StackExchange API responses are always gzip-compressed regardless
// of Accept-Encoding — the client must decompress.
package stackexchange

import (
	"compress/gzip"
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
	ModuleName = "stackexchange"

	baseURL      = "https://api.stackexchange.com/2.3/search/advanced"
	defaultCount = 10
	maxCount     = 30
	defaultSite  = "stackoverflow"
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		opts := Options{Key: cfg.APIKey}
		if s, ok := cfg.Extra["site"]; ok && s != "" {
			opts.Site = s
		}
		return New(opts), nil
	})
}

// Options configures the StackExchange module.
type Options struct {
	// Key is an optional StackExchange API key for higher quota.
	Key string
	// Site is the SE site to search (e.g., "stackoverflow", "serverfault").
	// Defaults to "stackoverflow".
	Site string
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
	key    string
	site   string
	count  int
	client httpClient
}

// New creates a StackExchange search module.
func New(opts Options) search.Module {
	site := opts.Site
	if site == "" {
		site = defaultSite
	}
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

	return &module{key: opts.Key, site: site, count: count, client: c}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeCommunity,
		CostTier:   search.CostTierFree,
		Languages:  []string{"en"},
		Scope:      "Q&A search via StackExchange API. Keyless, 300 req/day. Best for programming and technical questions.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	reqURL := buildURL(query, m.site, m.key, m.count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("stackexchange: build request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stackexchange: request: %w", err)
	}
	defer resp.Body.Close()

	// StackExchange always returns gzip-compressed responses.
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("stackexchange: gzip: %w", err)
		}
		defer gr.Close()
		reader = gr
	}

	body, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("stackexchange: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPError(resp.StatusCode, body)
	}

	return parseResponse(body)
}

func (m *module) Close() error { return nil }

// --- URL building ------------------------------------------------------------

func buildURL(query, site, key string, count int) string {
	v := url.Values{}
	v.Set("q", query)
	v.Set("site", site)
	v.Set("pagesize", strconv.Itoa(count))
	v.Set("order", "desc")
	v.Set("sort", "relevance")
	v.Set("filter", "withbody") // includes body excerpt
	if key != "" {
		v.Set("key", key)
	}
	return baseURL + "?" + v.Encode()
}

// --- response parsing --------------------------------------------------------

type apiResponse struct {
	Items      []questionItem `json:"items"`
	HasMore    bool           `json:"has_more"`
	QuotaMax   int            `json:"quota_max"`
	QuotaLeft  int            `json:"quota_remaining"`
	ErrorID    int            `json:"error_id"`
	ErrorName  string         `json:"error_name"`
	ErrorMsg   string         `json:"error_message"`
}

type questionItem struct {
	QuestionID  int      `json:"question_id"`
	Title       string   `json:"title"`
	Link        string   `json:"link"`
	Score       int      `json:"score"`
	AnswerCount int      `json:"answer_count"`
	Tags        []string `json:"tags"`
	IsAnswered  bool     `json:"is_answered"`
}

func parseResponse(body []byte) ([]search.SearchResult, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("stackexchange: unmarshal: %w", err)
	}

	if resp.ErrorID != 0 {
		return nil, fmt.Errorf("stackexchange: API error %d (%s): %s",
			resp.ErrorID, resp.ErrorName, resp.ErrorMsg)
	}

	results := make([]search.SearchResult, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.Title == "" || item.Link == "" {
			continue
		}

		snippet := formatSnippet(item)

		results = append(results, search.SearchResult{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: snippet,
		})
	}

	return results, nil
}

func formatSnippet(item questionItem) string {
	var parts []string

	if len(item.Tags) > 0 {
		tags := item.Tags
		if len(tags) > 4 {
			tags = tags[:4]
		}
		parts = append(parts, "["+strings.Join(tags, ", ")+"]")
	}

	parts = append(parts, fmt.Sprintf("Score: %d", item.Score))

	if item.IsAnswered {
		parts = append(parts, fmt.Sprintf("%d answers (accepted)", item.AnswerCount))
	} else if item.AnswerCount > 0 {
		parts = append(parts, fmt.Sprintf("%d answers", item.AnswerCount))
	} else {
		parts = append(parts, "unanswered")
	}

	return strings.Join(parts, " | ")
}

// --- error classification ----------------------------------------------------

func classifyHTTPError(status int, body []byte) error {
	var errResp apiResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.ErrorMsg != "" {
		return fmt.Errorf("stackexchange: HTTP %d: %s (%s)",
			status, errResp.ErrorMsg, errResp.ErrorName)
	}
	return fmt.Errorf("stackexchange: HTTP %d", status)
}
