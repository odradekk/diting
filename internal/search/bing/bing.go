// Package bing implements a search.Module that scrapes Bing web search results.
// It uses the utls fetch layer for Chrome TLS fingerprinting and goquery for
// HTML parsing. No API key is required.
package bing

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
	ModuleName = "bing"

	baseURL       = "https://www.bing.com/search"
	defaultMarket = "en-US"
	defaultCount  = 10
	maxCount      = 50
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		opts := Options{Market: cfg.Extra["market"]}
		return New(opts), nil
	})
}

// Options configures the Bing module.
type Options struct {
	// Market is the Bing "mkt" parameter (e.g., "en-US"). Defaults to "en-US".
	Market string
	// Count is the number of results per request. Defaults to 10, max 50.
	Count int
	// fetcher overrides the default utls-based HTTP client. Test-only.
	fetcher httpFetcher
}

// httpFetcher abstracts the HTTP GET for testability.
type httpFetcher interface {
	fetch(ctx context.Context, rawURL string) (string, error)
}

type module struct {
	market  string
	count   int
	fetcher httpFetcher
}

// New creates a Bing search module.
func New(opts Options) search.Module {
	market := opts.Market
	if market == "" {
		market = defaultMarket
	}
	count := opts.Count
	if count <= 0 {
		count = defaultCount
	}
	if count > maxCount {
		count = maxCount
	}

	f := opts.fetcher
	if f == nil {
		f = &utlsHTTP{f: utls.New(utls.Options{
			MaxBodyBytes: 2 << 20, // 2 MiB — Bing SERPs can be large
		})}
	}

	return &module{market: market, count: count, fetcher: f}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeGeneralWeb,
		CostTier:   search.CostTierFree,
		Languages:  []string{"en", "zh-Hans", "ja", "de", "fr", "es"},
		Scope:      "General web search via Bing. Good for broad queries, news, and technical topics. Keyless scraping.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	u := buildURL(query, m.market, m.count)

	html, err := m.fetcher.fetch(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("bing: fetch: %w", err)
	}

	results, err := parseResults(html)
	if err != nil {
		return nil, fmt.Errorf("bing: parse: %w", err)
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

func buildURL(query, market string, count int) string {
	v := url.Values{}
	v.Set("q", query)
	v.Set("count", fmt.Sprintf("%d", count))
	v.Set("mkt", market)
	return baseURL + "?" + v.Encode()
}

// --- HTML parsing ------------------------------------------------------------

// parseResults extracts organic search results from a Bing SERP HTML page.
// It returns an empty slice (not an error) when no organic results are found,
// consistent with the Module contract (empty results is not an error).
func parseResults(html string) ([]search.SearchResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("goquery: %w", err)
	}

	var results []search.SearchResult

	doc.Find("ol#b_results li.b_algo").Each(func(_ int, s *goquery.Selection) {
		titleLink := s.Find("h2 a").First()
		title := strings.TrimSpace(titleLink.Text())
		href, _ := titleLink.Attr("href")

		if title == "" || href == "" {
			return // skip malformed results
		}

		// Primary snippet selector, then fallback.
		snippet := strings.TrimSpace(s.Find(".b_caption p").First().Text())
		if snippet == "" {
			snippet = strings.TrimSpace(s.Find("p").First().Text())
		}

		results = append(results, search.SearchResult{
			Title:   title,
			URL:     href,
			Snippet: snippet,
		})
	})

	return results, nil
}

// --- utls HTTP adapter -------------------------------------------------------

// utlsHTTP wraps the utls fetch layer for search scraping.
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
