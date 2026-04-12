// Package baidu implements a search.Module that scrapes Baidu web search
// results. It uses the utls fetch layer for Chrome TLS fingerprinting with
// Chinese Accept-Language headers, and goquery for HTML parsing.
//
// Baidu wraps destination URLs in redirect links (baidu.com/link?url=...).
// The parser extracts the real URL from the data-log JSON attribute when
// available, falling back to the redirect URL otherwise.
//
// No API key is required. Baidu may trigger a slider CAPTCHA under high
// request volume; this is detected and surfaced as an error.
package baidu

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
	ModuleName = "baidu"

	baseURL = "https://www.baidu.com/s"
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{}), nil
	})
}

// Options configures the Baidu module.
type Options struct {
	// fetcher overrides the default utls-based HTTP client. Test-only.
	fetcher httpFetcher
}

// httpFetcher abstracts the HTTP GET for testability.
type httpFetcher interface {
	fetch(ctx context.Context, rawURL string) (string, error)
}

type module struct {
	fetcher httpFetcher
}

// New creates a Baidu search module.
func New(opts Options) search.Module {
	f := opts.fetcher
	if f == nil {
		// Clone default Chrome headers and set Chinese Accept-Language.
		headers := make(map[string]string, len(utls.DefaultHeaders))
		for k, v := range utls.DefaultHeaders {
			headers[k] = v
		}
		headers["Accept-Language"] = "zh-CN,zh;q=0.9,en;q=0.8"

		f = &utlsHTTP{f: utls.New(utls.Options{
			MaxBodyBytes: 2 << 20, // 2 MiB
			Headers:      headers,
		})}
	}
	return &module{fetcher: f}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeGeneralWeb,
		CostTier:   search.CostTierFree,
		Languages:  []string{"zh-Hans", "en"},
		Scope:      "Chinese web search via Baidu. Best for queries in Chinese or about Chinese topics. Keyless scraping.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	u := buildURL(query)

	html, err := m.fetcher.fetch(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("baidu: fetch: %w", err)
	}

	// Detect CAPTCHA / verification pages. Baidu uses two block patterns:
	// 1. Redirect to wappass.baidu.com (slider CAPTCHA)
	// 2. "百度安全验证" inline page (rate-limit block)
	if strings.Contains(html, "wappass.baidu.com") || strings.Contains(html, "百度安全验证") {
		return nil, fmt.Errorf("baidu: blocked by verification challenge")
	}

	results, err := parseResults(html)
	if err != nil {
		return nil, fmt.Errorf("baidu: parse: %w", err)
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

func buildURL(query string) string {
	v := url.Values{}
	v.Set("wd", query)
	v.Set("rn", "10") // request 10 results per page
	v.Set("ie", "utf-8")
	return baseURL + "?" + v.Encode()
}

// --- HTML parsing ------------------------------------------------------------

// skipTemplates are Baidu result-op templates that are not useful as search
// results (image grids, related searches, etc.).
var skipTemplates = map[string]bool{
	"image_grid_san": true,
	"recommend_list": true,
}

// parseResults extracts search results from a Baidu SERP HTML page.
//
// Baidu's modern SERP uses two result container classes:
//   - div.result.c-container — organic web results
//   - div.result-op.c-container — enriched results (knowledge graph, baike, etc.)
//
// Both share the same inner structure: mu attribute for real URL, h3 a for
// title, [data-module="abstract"] for snippet. Non-useful result-op templates
// (image grid, recommendations) are filtered out.
func parseResults(html string) ([]search.SearchResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("goquery: %w", err)
	}

	var results []search.SearchResult

	// Match both organic (div.result.c-container) and enriched (div.result-op.c-container).
	doc.Find("div.c-container").Each(func(_ int, s *goquery.Selection) {
		// Skip non-useful result-op templates.
		if tpl, exists := s.Attr("tpl"); exists && skipTemplates[tpl] {
			return
		}
		titleEl := s.Find("h3.t a").First()
		if titleEl.Length() == 0 {
			titleEl = s.Find("h3 a").First() // broader fallback
		}
		title := strings.TrimSpace(titleEl.Text())
		href, _ := titleEl.Attr("href")

		if title == "" || href == "" {
			return
		}

		// Real URL: mu attribute on the container div, fall back to href.
		realURL := extractMuURL(s)
		if realURL == "" {
			realURL = href
		}

		// Snippet: data-module="abstract" contains the description text.
		snippet := strings.TrimSpace(s.Find("[data-module=abstract]").First().Text())
		if snippet == "" {
			// Fallback: cu-line-clamp-* divs inside the result.
			snippet = strings.TrimSpace(s.Find("[class*=cu-line-clamp]").First().Text())
		}
		if snippet == "" {
			// Legacy fallback: div.c-abstract (older Baidu layouts).
			snippet = strings.TrimSpace(s.Find("div.c-abstract").First().Text())
		}

		results = append(results, search.SearchResult{
			Title:   title,
			URL:     realURL,
			Snippet: snippet,
		})
	})

	return results, nil
}

// extractMuURL reads the "mu" attribute on the result container div, which
// contains the real destination URL (bypassing Baidu's redirect wrapper).
func extractMuURL(s *goquery.Selection) string {
	mu, exists := s.Attr("mu")
	if !exists || mu == "" {
		return ""
	}
	return mu
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
