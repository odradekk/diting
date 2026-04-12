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

// Default configuration values.
const (
	// DefaultMaxChars is the character-level truncation limit.
	// ~32 000 chars ≈ ~8 000 tokens for most LLM tokenizers.
	DefaultMaxChars = 32_000

	// DefaultMinChars is the minimum content length after extraction.
	// If the extracted text is shorter than this, the extractor returns
	// an error so the chain falls through to the next layer. This
	// prevents a JS-rendered SPA's tagline (e.g., 20 chars) from
	// consuming the chain's success slot.
	DefaultMinChars = 200
)

// Options configures the extractor.
type Options struct {
	// MaxChars caps the extracted content at this many characters.
	// Zero means DefaultMaxChars.
	MaxChars int

	// MinChars is the minimum acceptable content length after extraction.
	// Content shorter than this triggers a fallthrough error. Zero means
	// DefaultMinChars.
	MinChars int
}

// Extractor transforms a raw fetch Result into cleaned, LLM-ready content.
type Extractor struct {
	maxChars int
	minChars int
}

// New constructs an Extractor.
func New(opts Options) *Extractor {
	mc := opts.MaxChars
	if mc <= 0 {
		mc = DefaultMaxChars
	}
	mn := opts.MinChars
	if mn <= 0 {
		mn = DefaultMinChars
	}
	return &Extractor{maxChars: mc, minChars: mn}
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
	if len(content) < e.minChars {
		return nil, fmt.Errorf("extract: content too short (%d chars, min %d, url=%s)", len(content), e.minChars, result.URL)
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
	content = stripNoiseLines(content)
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
	// Conservative set: only patterns that are unambiguously non-article.
	// Overly broad selectors (e.g., [class*='header'], [class*='menu'])
	// risk stripping article content on sites that use these classes for
	// in-article UI components. Kept deliberately narrow.
	selectors := []string{
		// Navigation
		"[class*='nav']",
		"[class*='breadcrumb']",
		"[id*='nav']",
		"[id*='breadcrumb']",
		"[role='navigation']",
		"[aria-label*='breadcrumb' i]",
		// Footer
		"[class*='footer']",
		"[id*='footer']",
		"[role='contentinfo']",
		// Sidebar
		"[class*='sidebar']",
		"[id*='sidebar']",
		"[role='complementary']",
		// Cookie / consent / banner
		"[class*='cookie']",
		"[class*='consent']",
		"[class*='banner']",
		"[id*='cookie']",
		"[id*='consent']",
		// Ads
		"[class*='advertisement']",
		"[class*='ad-']",
		// Subscribe / newsletter
		"[class*='subscribe']",
		"[class*='newsletter']",
		"[id*='subscribe']",
		"[id*='newsletter']",
		// Related content
		"[class*='related-post']",
		"[class*='recommended']",
		// Copyright
		"[class*='copyright']",
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

// stripNoiseLines removes lines from readability's TextContent output that
// match common non-article noise patterns. These are lines that readability
// kept because they were inside the article DOM subtree but are not part of
// the article text (breadcrumbs, subscribe CTAs, copyright notices, etc.).
func stripNoiseLines(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isNoiseLine(trimmed) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// noiseLinePatterns are regexes for full-line matches against noise text
// that readability sometimes preserves.
var noiseLinePatterns = []*regexp.Regexp{
	// Breadcrumb-style navigation: "Home > Docs > API" or "Home / Docs / API"
	regexp.MustCompile(`^(\w[\w\s]*[>›/»]\s*){2,}`),
	// "Skip to content" / "Skip to main" / "Jump to navigation"
	regexp.MustCompile(`(?i)^(skip|jump)\s+to\s+(main|content|navigation)`),
	// Subscribe / newsletter CTAs (standalone lines)
	regexp.MustCompile(`(?i)^subscribe\s+(to\s+|now|for\s+)`),
	regexp.MustCompile(`(?i)^sign\s+up\s+for\s+(our\s+)?(newsletter|updates)`),
	regexp.MustCompile(`(?i)^get\s+(our\s+)?newsletter`),
	// Copyright lines
	regexp.MustCompile(`(?i)^©\s*\d{4}`),
	regexp.MustCompile(`(?i)^copyright\s+(©\s*)?\d{4}`),
	regexp.MustCompile(`(?i)^all\s+rights\s+reserved`),
	// Social share buttons (standalone lines)
	regexp.MustCompile(`(?i)^(share|tweet|pin)\s+(this|on|to)\s`),
	regexp.MustCompile(`(?i)^follow\s+us\s+on\s`),
}

func isNoiseLine(line string) bool {
	if line == "" {
		return false
	}
	for _, p := range noiseLinePatterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
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
