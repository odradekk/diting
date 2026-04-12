// Package jina implements a fetch.Fetcher that delegates content extraction
// to the r.jina.ai reader API. It is the third layer in the fetch chain —
// a fallback for pages where utls and chromedp both fail (e.g., extreme
// bot-protection that only server-side rendering can bypass).
//
// r.jina.ai accepts a URL, renders the page server-side, extracts the
// readable content, and returns it as markdown. The returned content is
// already cleaned — no local HTML extraction is needed (though the
// universal extractor in Phase 1.7 will apply light sanitization).
//
// BYOK: an API key is optional. Without one, the free tier applies
// (rate-limited). With a key, higher throughput is available. The key is
// passed via the Authorization header.
package jina

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// LayerName is the value the Chain assigns to Result.LayerUsed.
const LayerName = "jina"

// Default configuration values.
const (
	DefaultTimeout      = 20 * time.Second
	DefaultMaxBodyBytes = 1 << 20 // 1 MiB
	DefaultBaseURL      = "https://r.jina.ai"
)

// Options configures the jina Fetcher.
type Options struct {
	// Timeout is the per-request budget for the jina API call.
	Timeout time.Duration

	// MaxBodyBytes caps the returned content size.
	MaxBodyBytes int64

	// APIKey is the optional BYOK token for higher rate limits. If empty,
	// the free tier is used.
	APIKey string

	// BaseURL overrides the jina reader endpoint. Defaults to
	// "https://r.jina.ai". Tests inject a local httptest URL here.
	BaseURL string

	// HTTPClient overrides the HTTP client used to call the jina API.
	// If nil, a default client with the configured timeout is created.
	// Tests can inject a custom client here.
	HTTPClient *http.Client
}

// Fetcher implements fetch.Fetcher using the jina reader API.
type Fetcher struct {
	opts   Options
	client *http.Client
}

// New constructs a jina Fetcher. It does not make any network calls;
// connectivity is validated lazily on the first Fetch.
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

// Fetch calls the jina reader API with the target URL and returns the
// extracted markdown content.
func (f *Fetcher) Fetch(ctx context.Context, targetURL string) (*fetchpkg.Result, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), err)
	}

	// Build the jina reader URL: {baseURL}/{encoded-target-url}
	jinaURL := f.opts.BaseURL + "/" + url.PathEscape(targetURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("build request: %w", err))
	}

	// Request markdown output.
	req.Header.Set("Accept", "text/markdown")
	// Identify ourselves so jina can track diting usage if needed.
	req.Header.Set("User-Agent", "diting/2.0 (github.com/odradekk/diting)")

	if f.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.opts.APIKey)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), fmt.Errorf("jina request: %w", err))
	}
	defer resp.Body.Close()

	// Classify HTTP status before reading body.
	if kind, isErr := classifyStatus(resp.StatusCode); isErr {
		return nil, f.wrapErr(targetURL, kind, fmt.Errorf("jina http %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxBodyBytes))
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrTransport, fmt.Errorf("jina read body: %w", err))
	}

	content := strings.TrimSpace(string(body))
	if content == "" {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("jina returned empty content for %s", targetURL))
	}

	// Extract title from the first markdown heading if present.
	title := extractMarkdownTitle(content)

	return &fetchpkg.Result{
		URL:         targetURL,
		FinalURL:    targetURL, // jina doesn't expose the final URL after redirects
		Title:       title,
		Content:     content,
		ContentType: "text/markdown",
		LatencyMs:   time.Since(start).Milliseconds(),
	}, nil
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

// Close is a no-op — the jina layer holds no persistent state beyond the
// stdlib HTTP client, which does not require explicit cleanup.
func (f *Fetcher) Close() error { return nil }

func (f *Fetcher) wrapErr(targetURL string, kind fetchpkg.ErrKind, err error) error {
	return &fetchpkg.LayerError{
		Layer: LayerName,
		URL:   targetURL,
		Kind:  kind,
		Err:   err,
	}
}

// extractMarkdownTitle returns the text of the first `# heading` in the
// markdown content, or empty string if none is found.
func extractMarkdownTitle(content string) string {
	for _, line := range strings.SplitN(content, "\n", 20) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
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
