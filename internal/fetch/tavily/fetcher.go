// Package tavily implements a fetch.Fetcher that delegates content
// extraction to the Tavily Extract API (api.tavily.com/extract). It is the
// fifth and final layer in the fetch chain — a last-resort paid fallback
// used only when utls, chromedp, jina, and archive all fail.
//
// Tavily is BYOK only — an API key is required. The layer is disabled by
// default in config; users must explicitly provide a key and enable it.
// If called without an API key, Fetch returns ErrDisabled immediately.
package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// LayerName is the value the Chain assigns to Result.LayerUsed.
const LayerName = "tavily"

// Default configuration values.
const (
	DefaultTimeout      = 30 * time.Second
	DefaultMaxBodyBytes = 1 << 20 // 1 MiB
	DefaultBaseURL      = "https://api.tavily.com"
)

// Options configures the tavily Fetcher.
type Options struct {
	// Timeout is the per-request budget for the Tavily API call.
	Timeout time.Duration

	// MaxBodyBytes caps the returned content size.
	MaxBodyBytes int64

	// APIKey is the BYOK token. Required — Fetch returns ErrDisabled
	// without it.
	APIKey string

	// BaseURL overrides the Tavily API endpoint. Tests inject a local
	// httptest URL here.
	BaseURL string

	// HTTPClient overrides the HTTP client. If nil, a default client
	// with the configured timeout is created.
	HTTPClient *http.Client
}

// Fetcher implements fetch.Fetcher using the Tavily Extract API.
type Fetcher struct {
	opts   Options
	client *http.Client
}

// New constructs a tavily Fetcher. It succeeds even without an API key
// (so the chain can be assembled unconditionally), but Fetch will return
// ErrDisabled if called without a key.
func New(opts Options) *Fetcher {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}

	return &Fetcher{opts: opts, client: client}
}

// Fetch calls the Tavily Extract API to retrieve content for targetURL.
func (f *Fetcher) Fetch(ctx context.Context, targetURL string) (*fetchpkg.Result, error) {
	start := time.Now()

	if f.opts.APIKey == "" {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrDisabled,
			fmt.Errorf("tavily api key not configured (BYOK required)"))
	}

	if err := ctx.Err(); err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), err)
	}

	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()

	// Build the extract request.
	reqBody := extractRequest{URLs: []string{targetURL}}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("marshal request: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		f.opts.BaseURL+"/extract", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.opts.APIKey)
	req.Header.Set("User-Agent", "diting/2.0 (github.com/odradekk/diting)")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), fmt.Errorf("tavily request: %w", err))
	}
	defer resp.Body.Close()

	if kind, isErr := classifyStatus(resp.StatusCode); isErr {
		return nil, f.wrapErr(targetURL, kind, fmt.Errorf("tavily http %d", resp.StatusCode))
	}

	// Read the full JSON envelope with a generous safety cap (the content
	// truncation is applied to the extracted text below, not to the wire
	// response). 4 MiB is far more than any single-URL extract response.
	const apiResponseCap = 4 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, apiResponseCap))
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrTransport, fmt.Errorf("tavily read body: %w", err))
	}

	var er extractResponse
	if err := json.Unmarshal(respBody, &er); err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("tavily parse json: %w", err))
	}

	// Check for extraction failure.
	for _, fail := range er.FailedResults {
		if fail.URL == targetURL {
			return nil, f.wrapErr(targetURL, fetchpkg.ErrTransport,
				fmt.Errorf("tavily extraction failed: %s", fail.Error))
		}
	}

	// Find our URL in the results.
	for _, res := range er.Results {
		if res.URL == targetURL || res.URL == "" {
			content := strings.TrimSpace(res.RawContent)
			if content == "" {
				content = strings.TrimSpace(res.Content)
			}
			if content == "" {
				return nil, f.wrapErr(targetURL, fetchpkg.ErrParse,
					fmt.Errorf("tavily returned empty content for %s", targetURL))
			}

			return &fetchpkg.Result{
				URL:         targetURL,
				FinalURL:    targetURL,
				Title:       "", // tavily doesn't return a title in extract API
				Content:     truncate(content, f.opts.MaxBodyBytes),
				ContentType: "text/plain",
				LatencyMs:   time.Since(start).Milliseconds(),
			}, nil
		}
	}

	return nil, f.wrapErr(targetURL, fetchpkg.ErrParse,
		fmt.Errorf("tavily response contains no result for %s", targetURL))
}

// FetchMany fetches URLs serially. The Chain provides parallelism on top.
func (f *Fetcher) FetchMany(ctx context.Context, urls []string) ([]*fetchpkg.Result, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	results := make([]*fetchpkg.Result, len(urls))
	var errs []error
	for i, u := range urls {
		if err := ctx.Err(); err != nil {
			errs = append(errs, f.wrapErr(u, classifyError(err), err))
			break
		}
		r, err := f.Fetch(ctx, u)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results[i] = r
	}
	if len(errs) == 0 {
		return results, nil
	}
	return results, errors.Join(errs...)
}

// Close is a no-op.
func (f *Fetcher) Close() error { return nil }

// --- request / response types -----------------------------------------------

type extractRequest struct {
	URLs []string `json:"urls"`
}

type extractResponse struct {
	Results       []extractResult `json:"results"`
	FailedResults []failedResult  `json:"failed_results"`
}

type extractResult struct {
	URL        string `json:"url"`
	RawContent string `json:"raw_content"`
	Content    string `json:"content"`
}

type failedResult struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// --- helpers ----------------------------------------------------------------

func truncate(s string, max int64) string {
	if int64(len(s)) <= max {
		return s
	}
	return s[:max]
}

func (f *Fetcher) wrapErr(targetURL string, kind fetchpkg.ErrKind, err error) error {
	return &fetchpkg.LayerError{
		Layer: LayerName,
		URL:   targetURL,
		Kind:  kind,
		Err:   err,
	}
}

// --- classification ---------------------------------------------------------

func classifyError(err error) fetchpkg.ErrKind {
	if err == nil {
		return fetchpkg.ErrUnknown
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fetchpkg.ErrTimeout
	case errors.Is(err, context.Canceled):
		return fetchpkg.ErrCanceled
	}
	return fetchpkg.ErrTransport
}

func classifyStatus(status int) (fetchpkg.ErrKind, bool) {
	switch {
	case status >= 200 && status < 300:
		return 0, false
	case status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		status == http.StatusTooManyRequests:
		return fetchpkg.ErrBlocked, true
	case status == http.StatusNotFound, status == http.StatusGone:
		return fetchpkg.ErrNotFound, true
	case status >= 500 && status < 600:
		return fetchpkg.ErrTransport, true
	default:
		return fetchpkg.ErrUnknown, true
	}
}
