// Package chromedp implements a fetch.Fetcher that launches a headless
// Chrome browser via the Chrome DevTools Protocol. It is the second layer
// in the fetch chain, used as a fallback for sites that block HTTP-only
// techniques (DataDome, advanced Cloudflare challenges, JS-rendered SPAs).
//
// ADR 0001 §4 point 4 identifies g2.com and quora.com as targets for this
// layer. ADR 0001 §4 point 5 confirms that no single HTTP technique wins
// everywhere — the chromedp fallback is architecturally mandatory.
//
// Browser lifecycle:
//
//   - New allocates a Chrome process with stealth flags.
//   - Each Fetch call creates a new tab (isolated browser context).
//   - Close terminates the browser process.
//
// Stealth:
//
//   - --disable-blink-features=AutomationControlled prevents Chrome from
//     setting navigator.webdriver = true. This is the single most effective
//     anti-detection flag.
//   - Realistic User-Agent, viewport, and language headers.
//   - No extensions, no default apps, no background pages.
package chromedp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// LayerName is the value the Chain assigns to Result.LayerUsed.
const LayerName = "chromedp"

// Default configuration values.
const (
	DefaultTimeout      = 30 * time.Second
	DefaultMaxBodyBytes = 1 << 20 // 1 MiB
)

// DefaultUserAgent matches the Chrome header set in internal/fetch/utls.
const DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

// Options configures the chromedp Fetcher.
type Options struct {
	// Timeout is the per-page navigation + render budget.
	Timeout time.Duration

	// MaxBodyBytes caps the returned HTML. The truncation happens on the
	// rendered outer HTML string, not on the wire bytes.
	MaxBodyBytes int64

	// UserAgent overrides the browser's User-Agent header.
	UserAgent string

	// ExecAllocatorOptions allows callers (typically tests) to override
	// the Chrome launch flags entirely. If nil, defaultExecOpts is used.
	ExecAllocatorOptions []chromedp.ExecAllocatorOption
}

// Fetcher implements fetch.Fetcher using a headless Chrome browser.
type Fetcher struct {
	opts          Options
	allocCtx      context.Context
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
}

// New launches a headless Chrome instance and returns a Fetcher. If Chrome
// is not installed or cannot start, New returns an error immediately (the
// caller can then omit this layer from the chain).
func New(opts Options) (*Fetcher, error) {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}

	execOpts := opts.ExecAllocatorOptions
	if execOpts == nil {
		execOpts = defaultExecOpts(opts.UserAgent)
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), execOpts...)

	// Create the browser process eagerly so New fails fast if Chrome is
	// missing, rather than failing lazily on the first Fetch.
	//
	// Silence chromedp's default stderr logger — it emits noisy messages
	// like "ERROR: unhandled node event *dom.EventAdoptedStyleSheetsModified"
	// that are harmless but pollute CLI output.
	silent := func(string, ...any) {}
	browserCtx, browserCancel := chromedp.NewContext(allocCtx,
		chromedp.WithErrorf(silent),
		chromedp.WithLogf(silent),
	)
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("chromedp: start browser: %w", err)
	}

	return &Fetcher{
		opts:          opts,
		allocCtx:      allocCtx,
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
	}, nil
}

func defaultExecOpts(userAgent string) []chromedp.ExecAllocatorOption {
	return append(chromedp.DefaultExecAllocatorOptions[:],
		// Stealth: prevent navigator.webdriver from being set to true.
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// Reduce noise from extensions and background pages.
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-component-extensions-with-background-pages", true),
		// Realistic viewport and UA.
		chromedp.UserAgent(userAgent),
		chromedp.WindowSize(1920, 1080),
	)
}

// Fetch navigates a new tab to targetURL, waits for the page to render,
// and returns the rendered HTML. It captures the HTTP status code via
// Chrome DevTools network events to classify blocked / not-found responses.
func (f *Fetcher) Fetch(ctx context.Context, targetURL string) (*fetchpkg.Result, error) {
	start := time.Now()

	// Fast path: if the caller's ctx is already done, don't launch a tab.
	if err := ctx.Err(); err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), err)
	}

	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()

	// Each Fetch gets its own tab (browser context), isolated from other
	// concurrent calls. chromedp.NewContext from a browser-level parent
	// creates a tab, not a new browser.
	tabCtx, tabCancel := chromedp.NewContext(f.browserCtx)
	defer tabCancel()

	// Bridge the caller's ctx into the tab. The tab is parented to the
	// browser context (not the caller's ctx), so cancellation /
	// deadline expiry must be propagated explicitly: when ctx fires,
	// we cancel the tab so the in-flight chromedp.Run aborts.
	bridgeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			tabCancel()
		case <-bridgeDone:
		}
	}()
	defer close(bridgeDone)

	// Listen for the main-document HTTP response to capture its status
	// code. We take the LAST document-type response because redirects
	// produce multiple events.
	var mu sync.Mutex
	var statusCode int
	var finalURL string
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		if e, ok := ev.(*network.EventResponseReceived); ok {
			if e.Type == network.ResourceTypeDocument {
				mu.Lock()
				statusCode = int(e.Response.Status)
				finalURL = e.Response.URL
				mu.Unlock()
			}
		}
	})

	var html string
	var title string
	err := chromedp.Run(tabCtx,
		network.Enable(),
		chromedp.Navigate(targetURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		// Wait for Cloudflare / DDoS-Guard challenge pages to resolve.
		// These interstitials set document.title to "Just a moment..."
		// while the JS challenge runs. We poll until the title changes
		// (indicating the real page has loaded) or the tab timeout fires.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForChallengeResolution(ctx)
		}),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		chromedp.Title(&title),
	)
	if err != nil {
		return nil, f.wrapErr(targetURL, classifyError(err), err)
	}

	// Classify the captured HTTP status.
	mu.Lock()
	sc := statusCode
	fu := finalURL
	mu.Unlock()

	if kind, isErr := classifyStatus(sc); isErr {
		return nil, f.wrapErr(targetURL, kind, fmt.Errorf("http %d", sc))
	}

	content := html
	if int64(len(content)) > f.opts.MaxBodyBytes {
		content = content[:f.opts.MaxBodyBytes]
	}

	if fu == "" {
		fu = targetURL
	}

	return &fetchpkg.Result{
		URL:         targetURL,
		FinalURL:    fu,
		Title:       title,
		Content:     content,
		ContentType: "text/html",
		LatencyMs:   time.Since(start).Milliseconds(),
	}, nil
}

// FetchMany fetches URLs serially. The Chain provides parallelism on top;
// chromedp tabs share one browser process so true parallelism at this
// layer would contend for the same GPU / compositor resources.
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

// challengeTitles are page titles used by common bot-protection challenge
// interstitials. When chromedp detects one of these titles after initial
// page load, it polls until the title changes (the challenge resolved and
// the real page loaded) or the context deadline fires.
var challengeTitles = []string{
	"Just a moment...",       // Cloudflare
	"Attention Required!",    // Cloudflare (alternate)
	"Please Wait...",         // DDoS-Guard
	"Access denied",          // PerimeterX
	"Checking your browser",  // Generic
}

// waitForChallengeResolution polls the document title at 300ms intervals.
// If the title matches a known challenge interstitial, it keeps polling
// until the title changes or the context expires. For non-challenge pages
// this returns immediately after one check.
func waitForChallengeResolution(ctx context.Context) error {
	const pollInterval = 300 * time.Millisecond

	for {
		var title string
		if err := chromedp.Run(ctx, chromedp.Title(&title)); err != nil {
			return err
		}

		isChallenge := false
		for _, ct := range challengeTitles {
			if strings.EqualFold(strings.TrimSpace(title), ct) {
				isChallenge = true
				break
			}
		}
		if !isChallenge {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Close terminates the browser process and releases the allocator.
func (f *Fetcher) Close() error {
	f.browserCancel()
	f.allocCancel()
	return nil
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
	case status == 0:
		// No status captured (e.g., about:blank, local error). Not an
		// HTTP error — let the caller decide.
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
