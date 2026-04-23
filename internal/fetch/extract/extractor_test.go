package extract

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

func result(ct, content string) *fetchpkg.Result {
	return &fetchpkg.Result{
		URL:         "https://example.com/page",
		FinalURL:    "https://example.com/page",
		Content:     content,
		ContentType: ct,
	}
}

// --- HTML extraction --------------------------------------------------------

func TestExtract_HTML_BasicArticle(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>Test Article</title></head>
<body>
<nav><a href="/">Home</a> <a href="/about">About</a></nav>
<article>
<h1>Test Article</h1>
<p>This is the main article content. It has multiple sentences to ensure
readability recognizes it as the primary content block. The article discusses
important topics that are relevant to the reader.</p>
<p>Second paragraph with more substantial content. Readability needs a certain
amount of text to identify the article correctly.</p>
</article>
<footer>Copyright 2024</footer>
<script>console.log("tracking");</script>
</body></html>`

	e := New(Options{MaxChars: 10000, MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/html", html))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if r.Title != "Test Article" {
		t.Errorf("Title = %q, want 'Test Article'", r.Title)
	}
	if !strings.Contains(r.Content, "main article content") {
		t.Errorf("Content missing article text: %s", r.Content[:min(200, len(r.Content))])
	}
	// Nav and footer should be stripped.
	if strings.Contains(r.Content, "Home") && strings.Contains(r.Content, "About") {
		t.Errorf("Content still contains nav links")
	}
	if strings.Contains(r.Content, "tracking") {
		t.Errorf("Content still contains script text")
	}
}

func TestExtract_HTML_EmptyBodyReturnsError(t *testing.T) {
	html := `<html><head><title>Empty</title></head><body></body></html>`
	e := New(Options{MinChars: 1})
	_, err := e.Extract(context.Background(), result("text/html", html))
	if err == nil {
		t.Fatal("expected error for empty body, got nil")
	}
	if !strings.Contains(err.Error(), "empty content") {
		t.Errorf("error = %v, want contains 'empty content'", err)
	}
}

func TestExtract_HTML_EmptyInputReturnsError(t *testing.T) {
	e := New(Options{MinChars: 1})
	_, err := e.Extract(context.Background(), result("text/html", ""))
	if err == nil {
		t.Fatal("expected error for empty HTML, got nil")
	}
}

func TestExtract_HTML_ContentTypeWithCharset(t *testing.T) {
	html := `<html><head><title>Charset</title></head>
<body><p>Article content with enough words to be recognized as the main body text
by the readability algorithm which needs substantial paragraphs.</p></body></html>`

	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/html; charset=utf-8", html))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(r.Content, "Article content") {
		t.Errorf("Content missing article text")
	}
}

func TestExtract_HTML_StripResidualElements(t *testing.T) {
	// Page with nav class and sidebar id that goquery should remove
	// before readability runs.
	html := `<html><head><title>Strip Test</title></head>
<body>
<div class="navigation-bar"><a href="/">Nav</a></div>
<div id="sidebar-left">Sidebar stuff</div>
<main>
<h1>Main Content</h1>
<p>This is the real article content that readability should extract. It contains
enough text in multiple paragraphs to pass the content-length heuristics that
readability uses to identify the main article.</p>
<p>Another paragraph of real content to help readability make the right choice
about what constitutes the main article body versus noise.</p>
</main>
<div class="cookie-banner">Accept cookies?</div>
</body></html>`

	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/html", html))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(r.Content, "Sidebar stuff") {
		t.Errorf("Content still contains sidebar content")
	}
	if strings.Contains(r.Content, "Accept cookies") {
		t.Errorf("Content still contains cookie banner")
	}
	if !strings.Contains(r.Content, "real article content") {
		t.Errorf("Content missing main article")
	}
}

// --- markdown sanitization --------------------------------------------------

func TestExtract_Markdown_Passthrough(t *testing.T) {
	md := "# Hello\n\nSome markdown content.\n\n## Section\n\nMore text."
	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/markdown", md))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(r.Content, "# Hello") {
		t.Errorf("Content lost heading: %q", r.Content)
	}
	if r.Title != "Hello" {
		t.Errorf("Title = %q, want 'Hello'", r.Title)
	}
}

func TestExtract_Markdown_CollapseNewlines(t *testing.T) {
	md := "# Title\n\n\n\n\nToo many newlines.\n\n\n\nEnd."
	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/markdown", md))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(r.Content, "\n\n") {
		t.Errorf("Content still has consecutive newlines: %q", r.Content)
	}
}

func TestExtract_Markdown_EmptyReturnsError(t *testing.T) {
	e := New(Options{MinChars: 1})
	_, err := e.Extract(context.Background(), result("text/markdown", "   \n\n  "))
	if err == nil {
		t.Fatal("expected error for whitespace-only markdown")
	}
}

// --- plain text -------------------------------------------------------------

func TestExtract_PlainText_Passthrough(t *testing.T) {
	text := "Just some plain text content.\n\nWith paragraphs."
	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/plain", text))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Consecutive \n are collapsed to a single \n.
	want := "Just some plain text content.\nWith paragraphs."
	if r.Content != want {
		t.Errorf("Content = %q, want %q", r.Content, want)
	}
}

func TestExtract_PlainText_EmptyReturnsError(t *testing.T) {
	e := New(Options{MinChars: 1})
	_, err := e.Extract(context.Background(), result("text/plain", ""))
	if err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestExtract_UnknownContentType_TreatedAsText(t *testing.T) {
	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), result("application/octet-stream", "raw data"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if r.Content != "raw data" {
		t.Errorf("Content = %q", r.Content)
	}
}

// --- truncation -------------------------------------------------------------

func TestExtract_Truncation(t *testing.T) {
	long := strings.Repeat("word ", 2000) // ~10000 chars
	e := New(Options{MaxChars: 100, MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/plain", long))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(r.Content) > 200 {
		t.Errorf("len(Content) = %d, want <= ~100 + truncation marker", len(r.Content))
	}
	if !strings.Contains(r.Content, "[content truncated]") {
		t.Errorf("Content missing truncation marker")
	}
}

func TestExtract_TruncationWordBoundary(t *testing.T) {
	// 10 words of 5 chars each = 59 chars total (with spaces)
	text := "aaaaa bbbbb ccccc ddddd eeeee fffff ggggg hhhhh iiiii jjjjj"
	e := New(Options{MaxChars: 35, MinChars: 1})
	r, err := e.Extract(context.Background(), result("text/plain", text))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Should cut at a word boundary, not mid-word.
	// 35 chars → "aaaaa bbbbb ccccc ddddd eeeee fff..."
	// Last space before pos 35 is at pos 29 (after "eeeee").
	// 29 > 35*3/4=26 → cuts at word boundary.
	if strings.Contains(r.Content, "fff") {
		t.Errorf("Content has a partial word: %q", r.Content)
	}
}

// --- min content length guard -----------------------------------------------

func TestExtract_MinCharsGuard(t *testing.T) {
	// Default MinChars is 200. Content shorter than that should error.
	e := New(Options{}) // uses DefaultMinChars = 200
	shortContent := "Only a site tagline." // ~20 chars, below threshold
	_, err := e.Extract(context.Background(), result("text/plain", shortContent))
	if err == nil {
		t.Fatal("expected error for content below MinChars, got nil")
	}
	if !strings.Contains(err.Error(), "content too short") {
		t.Errorf("error = %v, want contains 'content too short'", err)
	}
}

func TestExtract_MinCharsPassesAboveThreshold(t *testing.T) {
	e := New(Options{MinChars: 50})
	content := strings.Repeat("word ", 20) // 100 chars, above 50 threshold
	r, err := e.Extract(context.Background(), result("text/plain", content))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(r.Content, "word") {
		t.Errorf("Content = %q", r.Content)
	}
}

// --- nil result guard -------------------------------------------------------

func TestExtract_NilResult(t *testing.T) {
	e := New(Options{MinChars: 1})
	_, err := e.Extract(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil result")
	}
}

// --- title preservation (don't overwrite existing) --------------------------

func TestExtract_PreservesExistingTitle(t *testing.T) {
	r := &fetchpkg.Result{
		URL:         "https://example.com",
		FinalURL:    "https://example.com",
		Content:     "# New Title\n\nContent here.",
		ContentType: "text/markdown",
		Title:       "Original Title",
	}
	e := New(Options{MinChars: 1})
	r, err := e.Extract(context.Background(), r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if r.Title != "Original Title" {
		t.Errorf("Title = %q, want 'Original Title' (should not be overwritten)", r.Title)
	}
}

// --- Chain integration ------------------------------------------------------

func TestChain_WithExtractor_Success(t *testing.T) {
	// Minimal test that WithExtractor works with a real Chain.
	// The extractor receives the layer's result and modifies Content.
	fake := &fakeExtractor{
		fn: func(ctx context.Context, r *fetchpkg.Result) (*fetchpkg.Result, error) {
			r.Content = "extracted:" + r.Content
			return r, nil
		},
	}

	layer := fetchpkg.Layer{
		Name:    "test",
		Fetcher: &fakeFetcher{content: "raw html"},
		Enabled: true,
	}
	chain := fetchpkg.NewChain([]fetchpkg.Layer{layer}, fetchpkg.WithExtractor(fake))

	r, err := chain.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "extracted:raw html" {
		t.Errorf("Content = %q, want 'extracted:raw html'", r.Content)
	}
}

func TestChain_WithExtractor_FailureFallsThrough(t *testing.T) {
	var extractCalls atomic.Int32
	extractor := &fakeExtractor{
		fn: func(ctx context.Context, r *fetchpkg.Result) (*fetchpkg.Result, error) {
			extractCalls.Add(1)
			if r.Content == "bad html" {
				return nil, fmt.Errorf("extraction failed: empty article")
			}
			r.Content = "clean:" + r.Content
			return r, nil
		},
	}

	l1 := fetchpkg.Layer{
		Name:    "layer1",
		Fetcher: &fakeFetcher{content: "bad html"},
		Enabled: true,
	}
	// Small delay so layer1's extraction failure is observed before layer2
	// returns, making the test deterministic under parallel layer execution.
	l2 := fetchpkg.Layer{
		Name:    "layer2",
		Fetcher: &delayFetcher{content: "good html", delay: 5 * time.Millisecond},
		Enabled: true,
	}
	chain := fetchpkg.NewChain([]fetchpkg.Layer{l1, l2}, fetchpkg.WithExtractor(extractor))

	r, err := chain.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.LayerUsed != "layer2" {
		t.Errorf("LayerUsed = %q, want layer2 (layer1 extraction should fail)", r.LayerUsed)
	}
	if r.Content != "clean:good html" {
		t.Errorf("Content = %q, want 'clean:good html'", r.Content)
	}
	if got := extractCalls.Load(); got != 2 {
		t.Errorf("extractor called %d times, want 2", got)
	}
}

// --- test helpers -----------------------------------------------------------

type fakeExtractor struct {
	fn func(ctx context.Context, r *fetchpkg.Result) (*fetchpkg.Result, error)
}

func (e *fakeExtractor) Extract(ctx context.Context, r *fetchpkg.Result) (*fetchpkg.Result, error) {
	return e.fn(ctx, r)
}

type fakeFetcher struct {
	content string
}

func (f *fakeFetcher) Fetch(ctx context.Context, url string) (*fetchpkg.Result, error) {
	return &fetchpkg.Result{URL: url, FinalURL: url, Content: f.content, ContentType: "text/html"}, nil
}
func (f *fakeFetcher) FetchMany(ctx context.Context, urls []string) ([]*fetchpkg.Result, error) {
	return nil, nil
}
func (f *fakeFetcher) Close() error { return nil }

// delayFetcher is like fakeFetcher but waits before returning.
type delayFetcher struct {
	content string
	delay   time.Duration
}

func (f *delayFetcher) Fetch(ctx context.Context, url string) (*fetchpkg.Result, error) {
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &fetchpkg.Result{URL: url, FinalURL: url, Content: f.content, ContentType: "text/html"}, nil
}
func (f *delayFetcher) FetchMany(ctx context.Context, urls []string) ([]*fetchpkg.Result, error) {
	return nil, nil
}
func (f *delayFetcher) Close() error { return nil }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
