// Package archive implements a fetch.Fetcher that retrieves cached pages
// from the Wayback Machine (web.archive.org). It is the fourth layer in
// the fetch chain — a fallback for pages where all live-fetch techniques
// fail (site down, geo-blocked, extreme bot protection).
//
// Flow:
//  1. Query the Wayback availability API to find the most recent snapshot.
//  2. If a snapshot exists, fetch its raw content (using the id_ URL
//     variant that returns the original page without the Wayback toolbar).
//  3. Return the archived HTML.
//
// archive.today support is deferred to a future extension — it has no
// structured API and requires scraping.
package archive

import (
	"context"
	"encoding/json"
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
const LayerName = "archive"

// Default configuration values.
const (
	DefaultTimeout      = 25 * time.Second
	DefaultMaxBodyBytes = 1 << 20 // 1 MiB
	DefaultAvailURL     = "https://archive.org/wayback/available"
)

// Options configures the archive Fetcher.
type Options struct {
	// Timeout is the per-request budget covering both the availability
	// check and the snapshot fetch.
	Timeout time.Duration

	// MaxBodyBytes caps the archived page content.
	MaxBodyBytes int64

	// AvailabilityURL overrides the Wayback availability API endpoint.
	// Tests inject a local httptest URL here.
	AvailabilityURL string

	// SnapshotBaseURL overrides the base used when constructing raw
	// snapshot URLs. If empty, the URL from the API response is used
	// directly (with the id_ transform applied). Tests can set this to
	// redirect snapshot fetches to a local server.
	SnapshotBaseURL string

	// HTTPClient overrides the HTTP client. If nil, a default client
	// with the configured timeout is created.
	HTTPClient *http.Client
}

// Fetcher implements fetch.Fetcher using the Wayback Machine.
type Fetcher struct {
	opts   Options
	client *http.Client
}

// New constructs an archive Fetcher. No network calls are made at
// construction time.
func New(opts Options) *Fetcher {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.AvailabilityURL == "" {
		opts.AvailabilityURL = DefaultAvailURL
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}

	return &Fetcher{opts: opts, client: client}
}

// Fetch checks the Wayback availability API for a snapshot of targetURL,
// then fetches the raw archived content.
func (f *Fetcher) Fetch(ctx context.Context, targetURL string) (*fetchpkg.Result, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), err)
	}

	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()

	// Step 1: check availability.
	snap, err := f.checkAvailability(ctx, targetURL)
	if err != nil {
		return nil, err // already wrapped
	}

	// Step 2: fetch the raw snapshot content.
	rawURL := toRawURL(snap.URL)
	if f.opts.SnapshotBaseURL != "" {
		// Test override: redirect to the local server.
		rawURL = f.opts.SnapshotBaseURL + "/raw"
	}

	content, err := f.fetchSnapshot(ctx, targetURL, rawURL)
	if err != nil {
		return nil, err // already wrapped
	}

	return &fetchpkg.Result{
		URL:         targetURL,
		FinalURL:    snap.URL,
		Title:       "", // extraction is Phase 1.7's job
		Content:     content,
		ContentType: "text/html",
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

// Close is a no-op.
func (f *Fetcher) Close() error { return nil }

// --- availability API -------------------------------------------------------

// availabilityResponse models the Wayback Machine availability API response.
//
//	{
//	  "archived_snapshots": {
//	    "closest": {
//	      "status": "200",
//	      "available": true,
//	      "url": "http://web.archive.org/web/20240101120000/https://example.com",
//	      "timestamp": "20240101120000"
//	    }
//	  }
//	}
type availabilityResponse struct {
	ArchivedSnapshots struct {
		Closest *snapshot `json:"closest"`
	} `json:"archived_snapshots"`
}

type snapshot struct {
	Status    string `json:"status"`
	Available bool   `json:"available"`
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
}

func (f *Fetcher) checkAvailability(ctx context.Context, targetURL string) (*snapshot, error) {
	u, err := url.Parse(f.opts.AvailabilityURL)
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("parse availability url: %w", err))
	}
	q := u.Query()
	q.Set("url", targetURL)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("build availability request: %w", err))
	}
	req.Header.Set("User-Agent", "diting/2.0 (github.com/odradekk/diting)")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), fmt.Errorf("availability request: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrTransport,
			fmt.Errorf("availability api http %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrTransport, fmt.Errorf("read availability: %w", err))
	}

	var ar availabilityResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("parse availability json: %w", err))
	}

	if ar.ArchivedSnapshots.Closest == nil || !ar.ArchivedSnapshots.Closest.Available {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrNotFound,
			fmt.Errorf("no wayback snapshot available for %s", targetURL))
	}

	snap := ar.ArchivedSnapshots.Closest
	if snap.URL == "" {
		return nil, f.wrapErr(targetURL, fetchpkg.ErrParse,
			fmt.Errorf("wayback snapshot has empty url for %s", targetURL))
	}

	return snap, nil
}

// --- snapshot fetch ---------------------------------------------------------

func (f *Fetcher) fetchSnapshot(ctx context.Context, targetURL, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("build snapshot request: %w", err))
	}
	req.Header.Set("User-Agent", "diting/2.0 (github.com/odradekk/diting)")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", f.wrapErr(targetURL, classifyError(err), fmt.Errorf("snapshot request: %w", err))
	}
	defer resp.Body.Close()

	if kind, isErr := classifyStatus(resp.StatusCode); isErr {
		return "", f.wrapErr(targetURL, kind, fmt.Errorf("snapshot http %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxBodyBytes))
	if err != nil {
		return "", f.wrapErr(targetURL, fetchpkg.ErrTransport, fmt.Errorf("read snapshot: %w", err))
	}

	content := strings.TrimSpace(string(body))
	if content == "" {
		return "", f.wrapErr(targetURL, fetchpkg.ErrParse, fmt.Errorf("wayback returned empty snapshot for %s", targetURL))
	}

	return content, nil
}

// --- helpers ----------------------------------------------------------------

// toRawURL transforms a Wayback snapshot URL into its "raw" variant (id_)
// which returns the original page without the Wayback toolbar/banner.
//
// Normal:  https://web.archive.org/web/20240101120000/https://example.com
// Raw:     https://web.archive.org/web/20240101120000id_/https://example.com
func toRawURL(snapshotURL string) string {
	// Find the timestamp portion (14-digit string after /web/).
	const marker = "/web/"
	idx := strings.Index(snapshotURL, marker)
	if idx < 0 {
		return snapshotURL // can't parse — return as-is
	}
	afterWeb := snapshotURL[idx+len(marker):]

	// The timestamp is followed by "/" then the original URL.
	slashIdx := strings.Index(afterWeb, "/")
	if slashIdx < 0 {
		return snapshotURL
	}
	timestamp := afterWeb[:slashIdx]

	// If already has id_ suffix, return unchanged.
	if strings.HasSuffix(timestamp, "id_") {
		return snapshotURL
	}

	return snapshotURL[:idx+len(marker)] + timestamp + "id_" + afterWeb[slashIdx:]
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
