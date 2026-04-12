// Package extract implements the universal content extraction step that
// every successful fetch result passes through before being returned to
// the caller. See docs/adr/0002-universal-content-extraction.md.
//
// The extractor dispatches on Result.ContentType:
//
//   - text/html → go-readability article extraction + goquery residual
//     element removal → clean text with preserved paragraph structure
//   - text/markdown → light sanitization (collapse whitespace, truncate)
//   - text/plain → pass-through + truncate
//   - anything else → treated as plain text
//
// Every path applies a configurable character-level truncation as the last
// step so downstream consumers (LLMs) never exceed a token budget per page.
package extract

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// DefaultMaxChars is the default character-level truncation limit.
// ~32 000 chars ≈ ~8 000 tokens for most LLM tokenizers.
const DefaultMaxChars = 32_000

// Options configures the extractor.
type Options struct {
	// MaxChars caps the extracted content at this many characters.
	// Zero means DefaultMaxChars.
	MaxChars int
}

// Extractor transforms a raw fetch Result into cleaned, LLM-ready content.
type Extractor struct {
	maxChars int
}

// New constructs an Extractor.
func New(opts Options) *Extractor {
	mc := opts.MaxChars
	if mc <= 0 {
		mc = DefaultMaxChars
	}
	return &Extractor{maxChars: mc}
}

// Extract dispatches on result.ContentType and mutates Content + Title.
// It returns the modified result (same pointer) or an error if extraction
// produced empty content (signalling the chain should fall through).
func (e *Extractor) Extract(ctx context.Context, result *fetchpkg.Result) (*fetchpkg.Result, error) {
	if result == nil {
		return nil, fmt.Errorf("extract: nil result")
	}

	ct := normalizeContentType(result.ContentType)

	var content, title string
	var err error

	switch {
	case strings.Contains(ct, "text/html"):
		content, title, err = e.extractHTML(result.Content, result.FinalURL)
	case strings.Contains(ct, "text/markdown"):
		content = sanitizeMarkdown(result.Content)
		title = extractMarkdownTitle(result.Content)
	default:
		// text/plain and anything else — pass-through.
		content = sanitizeText(result.Content)
	}

	if err != nil {
		return nil, fmt.Errorf("extract (%s): %w", ct, err)
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("extract: empty content after extraction (contentType=%s, url=%s)", ct, result.URL)
	}

	// Apply token-budget truncation as the last step.
	content = truncateChars(content, e.maxChars)

	result.Content = content
	if title != "" && result.Title == "" {
		result.Title = title
	}

	return result, nil
}

// --- HTML extraction --------------------------------------------------------

// extractHTML runs go-readability on raw HTML and returns the extracted
// article text + title. goquery is used to strip residual non-article
// elements (nav, footer, aside, script, style) before readability runs,
// improving extraction quality on pages with complex layouts.
func (e *Extractor) extractHTML(rawHTML string, pageURL string) (string, string, error) {
	if strings.TrimSpace(rawHTML) == "" {
		return "", "", fmt.Errorf("empty HTML input")
	}

	// Pre-process with goquery: remove elements that confuse readability.
	cleaned, err := stripNonArticleElements(rawHTML)
	if err != nil {
		// If goquery fails, fall through to readability on the raw HTML.
		cleaned = rawHTML
	}

	parsedURL, _ := url.Parse(pageURL)
	if parsedURL == nil {
		parsedURL = &url.URL{}
	}

	article, err := readability.FromReader(strings.NewReader(cleaned), parsedURL)
	if err != nil {
		return "", "", fmt.Errorf("readability: %w", err)
	}

	content := article.TextContent
	title := strings.TrimSpace(article.Title)

	return content, title, nil
}

// stripNonArticleElements uses goquery to remove DOM elements that are
// typically non-article content: navigation, footers, sidebars, scripts,
// styles, and common ad/cookie-consent containers.
func stripNonArticleElements(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	// Remove by tag name.
	doc.Find("nav, footer, aside, script, style, noscript, iframe, svg").Remove()

	// Remove by common class/id patterns (non-article noise).
	selectors := []string{
		"[class*='nav']",
		"[class*='footer']",
		"[class*='sidebar']",
		"[class*='cookie']",
		"[class*='banner']",
		"[class*='advertisement']",
		"[class*='ad-']",
		"[id*='nav']",
		"[id*='footer']",
		"[id*='sidebar']",
		"[id*='cookie']",
	}
	for _, sel := range selectors {
		doc.Find(sel).Remove()
	}

	result, err := doc.Html()
	if err != nil {
		return "", err
	}
	return result, nil
}

// --- markdown sanitization --------------------------------------------------

func sanitizeMarkdown(content string) string {
	// Collapse 3+ consecutive newlines to 2.
	content = collapseNewlines(content)
	// Trim leading/trailing whitespace per line.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func extractMarkdownTitle(content string) string {
	for _, line := range strings.SplitN(content, "\n", 20) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// --- text sanitization ------------------------------------------------------

func sanitizeText(content string) string {
	return collapseNewlines(content)
}

// --- shared helpers ---------------------------------------------------------

var multiNewline = regexp.MustCompile(`\n{3,}`)

func collapseNewlines(s string) string {
	return multiNewline.ReplaceAllString(s, "\n\n")
}

func truncateChars(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Truncate at a word boundary if possible.
	cut := s[:max]
	if idx := strings.LastIndexAny(cut, " \n\t"); idx > max*3/4 {
		cut = cut[:idx]
	}
	return cut + "\n\n[content truncated]"
}

func normalizeContentType(ct string) string {
	// Strip charset and parameters: "text/html; charset=utf-8" → "text/html"
	ct = strings.ToLower(strings.TrimSpace(ct))
	if idx := strings.IndexByte(ct, ';'); idx >= 0 {
		ct = ct[:idx]
	}
	return strings.TrimSpace(ct)
}
