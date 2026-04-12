// Package arxiv implements a search.Module that queries the arXiv Atom API.
// No API key is required. The API returns Atom XML with paper metadata
// (title, abstract, authors, links).
//
// Endpoint: GET http://export.arxiv.org/api/query?search_query=...
// Docs: https://info.arxiv.org/help/api/basics.html
package arxiv

import (
	"context"
	"encoding/xml"
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
	ModuleName = "arxiv"

	baseURL      = "http://export.arxiv.org/api/query"
	defaultCount = 10
	maxCount     = 50
)

func init() {
	search.Register(ModuleName, func(cfg search.ModuleConfig) (search.Module, error) {
		return New(Options{}), nil
	})
}

// Options configures the arXiv module.
type Options struct {
	// Count is the number of results per request. Defaults to 10, max 50.
	Count int
	// client overrides the default HTTP client. Test-only.
	client httpClient
}

// httpClient abstracts HTTP for testability.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type module struct {
	count  int
	client httpClient
}

// New creates an arXiv search module.
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

	return &module{count: count, client: c}
}

func (m *module) Manifest() search.Manifest {
	return search.Manifest{
		Name:       ModuleName,
		SourceType: search.SourceTypeAcademic,
		CostTier:   search.CostTierFree,
		Languages:  []string{"en"},
		Scope:      "Academic papers via arXiv Atom API. Keyless. Best for physics, CS, math, and quantitative biology.",
	}
}

func (m *module) Search(ctx context.Context, query string) ([]search.SearchResult, error) {
	reqURL := buildURL(query, m.count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("arxiv: build request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arxiv: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, fmt.Errorf("arxiv: read body: %w", err)
	}

	return parseAtomFeed(body)
}

func (m *module) Close() error { return nil }

// --- URL building ------------------------------------------------------------

func buildURL(query string, count int) string {
	// arXiv search_query uses field prefixes: all:, ti:, au:, abs:, etc.
	// Using "all:" for broad full-text search when no prefix is specified.
	sq := query
	if !strings.ContainsAny(query, ":") {
		sq = "all:" + query
	}

	v := url.Values{}
	v.Set("search_query", sq)
	v.Set("start", "0")
	v.Set("max_results", strconv.Itoa(count))
	v.Set("sortBy", "relevance")
	v.Set("sortOrder", "descending")
	return baseURL + "?" + v.Encode()
}

// --- Atom XML parsing --------------------------------------------------------

// atomFeed represents the arXiv Atom response.
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string       `xml:"title"`
	Summary string       `xml:"summary"`
	Links   []atomLink   `xml:"link"`
	Authors []atomAuthor `xml:"author"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

func parseAtomFeed(body []byte) ([]search.SearchResult, error) {
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("arxiv: xml unmarshal: %w", err)
	}

	results := make([]search.SearchResult, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		title := cleanWhitespace(e.Title)
		link := extractAbsLink(e.Links)
		if title == "" || link == "" {
			continue
		}

		snippet := cleanWhitespace(e.Summary)
		// Prepend authors for context.
		if authors := formatAuthors(e.Authors); authors != "" {
			snippet = authors + " — " + snippet
		}

		results = append(results, search.SearchResult{
			Title:   title,
			URL:     link,
			Snippet: snippet,
		})
	}

	return results, nil
}

// extractAbsLink returns the "alternate" HTML link (the abs page), or the
// first link if no alternate is found.
func extractAbsLink(links []atomLink) string {
	for _, l := range links {
		if l.Rel == "alternate" {
			return l.Href
		}
	}
	if len(links) > 0 {
		return links[0].Href
	}
	return ""
}

func formatAuthors(authors []atomAuthor) string {
	if len(authors) == 0 {
		return ""
	}
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + " et al."
	}
	return strings.Join(names, ", ")
}

func cleanWhitespace(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
