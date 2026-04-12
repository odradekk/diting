// Package duckduckgo implements a search.Module that scrapes DuckDuckGo's
// HTML-only endpoint (html.duckduckgo.com). It uses the utls fetch layer for
// Chrome TLS fingerprinting and goquery for HTML parsing. No API key required.
//
// Only the first page of results is fetched (no pagination). DDG pagination
// requires POST with a per-session vqd token, which adds complexity without
// much value since the first page typically yields 20-30 results.
package duckduckgo

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/odradekk/diting/internal/fetch/utls"
	"github.com/odradekk/diting/internal/search"
)

const (
	// ModuleName is the registry key for this module.
	ModuleName = "duckduckgo"

	baseURL       = "https://html.duckduckgo.com/html/"
	defaultRegion = "wt-wt" // no region bias
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		opts := Options{Region: cfg.Extra["region"]}
		return New(opts), nil
	})
}

// Options configures the DuckDuckGo module.
type Options struct {
	// Region is the DDG "kl" parameter (e.g., "us-en", "uk-en", "de-de").
	// Defaults to "wt-wt" (no region bias).
	Region string
	// fetcher overrides the default utls-based HTTP client. Test-only.
	fetcher httpFetcher
}

// httpFetcher abstracts the HTTP GET for testability.
type httpFetcher interface {
	fetch(ctx context.Context, rawURL string) (string, error)
}

type module struct {
	region  string
	fetcher httpFetcher
}

// New creates a DuckDuckGo search module.
func New(opts Options) search.Module {
	region := opts.Region
	if region == "" {
		region = defaultRegion
	}

	f := opts.fetcher
	if f == nil {
		f = &utlsHTTP{f: utls.New(utls.Options{
			MaxBodyBytes: 2 << 20, // 2 MiB
		})}
	}

	return &module{region: region, fetcher: f}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeGeneralWeb,
		CostTier:   search.CostTierFree,
		Languages:  []string{"en", "zh-Hans", "ja", "de", "fr", "es"},
		Scope:      "Privacy-focused general web search via DuckDuckGo. Keyless scraping of the HTML endpoint.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	u := buildURL(query, m.region)

	html, err := m.fetcher.fetch(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: fetch: %w", err)
	}

	results, err := parseResults(html)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: parse: %w", err)
	}

	return results, nil
}

func (m *module) Close() error {
	if c, ok := m.fetcher.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// --- URL building ------------------------------------------------------------

func buildURL(query, region string) string {
	v := url.Values{}
	v.Set("q", query)
	v.Set("kl", region)
	v.Set("kp", "-1") // SafeSearch off
	return baseURL + "?" + v.Encode()
}

// --- HTML parsing ------------------------------------------------------------

// parseResults extracts organic search results from a DDG HTML SERP page.
// It filters out ads (div.result--ad) and extracts the real destination URL
// from DDG's redirect wrapper (?uddg=...).
func parseResults(html string) ([]search.SearchResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("goquery: %w", err)
	}

	var results []search.SearchResult

	doc.Find("div.result:not(.result--ad)").Each(func(_ int, s *goquery.Selection) {
		titleEl := s.Find("a.result__a").First()
		title := strings.TrimSpace(titleEl.Text())
		href, _ := titleEl.Attr("href")

		if title == "" || href == "" {
			return
		}

		realURL := extractRealURL(href)
		if realURL == "" {
			return
		}

		snippet := strings.TrimSpace(s.Find("a.result__snippet").First().Text())

		results = append(results, search.SearchResult{
			Title:   title,
			URL:     realURL,
			Snippet: snippet,
		})
	})

	return results, nil
}

// extractRealURL unwraps DDG's redirect URL. DDG wraps destination URLs as:
//
//	//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=...
//
// This function extracts the "uddg" query parameter. If the href is already
// a direct URL (no uddg param), it is returned as-is.
func extractRealURL(href string) string {
	// Make protocol-relative URLs absolute.
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}

	parsed, err := url.Parse(href)
	if err != nil {
		return href // best-effort
	}

	if uddg := parsed.Query().Get("uddg"); uddg != "" {
		return uddg
	}

	// No redirect wrapper — return as-is (some results may be direct links).
	return href
}

// --- utls HTTP adapter -------------------------------------------------------

type utlsHTTP struct {
	f *utls.Fetcher
}

func (u *utlsHTTP) fetch(ctx context.Context, rawURL string) (string, error) {
	r, err := u.f.Fetch(ctx, rawURL)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}

func (u *utlsHTTP) Close() error { return u.f.Close() }
