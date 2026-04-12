//go:build integration

// Integration tests for the full fetch chain. These hit real URLs over the
// network and require:
//   - Internet access
//   - Chrome/Chromium installed (for chromedp tests)
//
// Run with: go test ./internal/fetch/ -tags=integration -v -timeout 120s
package fetch_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	cdp "github.com/odradekk/diting/internal/fetch/chromedp"
	"github.com/odradekk/diting/internal/fetch/cache"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
)

func buildIntegrationChain(t *testing.T, cc *cache.Cache) *fetch.Chain {
	t.Helper()

	layers := []fetch.Layer{
		{Name: utls.LayerName, Fetcher: utls.New(utls.Options{}), Timeout: 15 * time.Second, Enabled: true},
	}
	if cdpLayer, err := cdp.New(cdp.Options{}); err == nil {
		layers = append(layers, fetch.Layer{
			Name: cdp.LayerName, Fetcher: cdpLayer, Timeout: 30 * time.Second, Enabled: true,
		})
		t.Cleanup(func() { cdpLayer.Close() })
	} else {
		t.Logf("chromedp skipped: %v", err)
	}
	layers = append(layers,
		fetch.Layer{Name: jina.LayerName, Fetcher: jina.New(jina.Options{}), Timeout: 20 * time.Second, Enabled: true},
		fetch.Layer{Name: archive.LayerName, Fetcher: archive.New(archive.Options{}), Timeout: 25 * time.Second, Enabled: true},
	)
	if key := os.Getenv("TAVILY_API_KEY"); key != "" {
		layers = append(layers, fetch.Layer{
			Name: tavily.LayerName, Fetcher: tavily.New(tavily.Options{APIKey: key}), Timeout: 30 * time.Second, Enabled: true,
		})
	}

	opts := []fetch.ChainOption{
		fetch.WithExtractor(extract.New(extract.Options{})),
		fetch.WithConcurrency(2),
	}
	if cc != nil {
		opts = append(opts, fetch.WithCache(cc))
	}

	chain := fetch.NewChain(layers, opts...)
	t.Cleanup(func() { chain.Close() })
	return chain
}

func TestIntegration_Wikipedia(t *testing.T) {
	chain := buildIntegrationChain(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := chain.Fetch(ctx, "https://en.wikipedia.org/wiki/Go_(programming_language)")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.LayerUsed == "" {
		t.Error("LayerUsed is empty")
	}
	if len(r.Content) < 500 {
		t.Errorf("Content too short: %d chars (want >= 500)", len(r.Content))
	}
	if r.Title == "" {
		t.Error("Title is empty")
	}
	t.Logf("layer=%s title=%q content=%d chars latency=%dms",
		r.LayerUsed, r.Title, len(r.Content), r.LatencyMs)
}

func TestIntegration_GitHub(t *testing.T) {
	chain := buildIntegrationChain(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := chain.Fetch(ctx, "https://github.com/golang/go")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(r.Content) < 200 {
		t.Errorf("Content too short: %d chars", len(r.Content))
	}
	t.Logf("layer=%s title=%q content=%d chars", r.LayerUsed, r.Title, len(r.Content))
}

func TestIntegration_DocsPage(t *testing.T) {
	chain := buildIntegrationChain(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := chain.Fetch(ctx, "https://go.dev/doc/effective_go")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(r.Content) < 1000 {
		t.Errorf("Content too short: %d chars", len(r.Content))
	}
	if !strings.Contains(strings.ToLower(r.Content), "go") {
		t.Error("Content doesn't mention 'go'")
	}
	t.Logf("layer=%s title=%q content=%d chars", r.LayerUsed, r.Title, len(r.Content))
}

func TestIntegration_CacheHit(t *testing.T) {
	cc, err := cache.Open(cache.Options{
		Path:        t.TempDir() + "/test.db",
		FallbackTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer cc.Close()

	chain := buildIntegrationChain(t, cc)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	url := "https://en.wikipedia.org/wiki/Metasearch_engine"

	// First fetch — cold cache.
	r1, err := chain.Fetch(ctx, url)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if r1.FromCache {
		t.Error("first fetch should NOT be from cache")
	}
	t.Logf("cold: layer=%s content=%d chars latency=%dms", r1.LayerUsed, len(r1.Content), r1.LatencyMs)

	// Second fetch — should hit cache (near-instant).
	start := time.Now()
	r2, err := chain.Fetch(ctx, url)
	cacheLatency := time.Since(start)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if !r2.FromCache {
		t.Error("second fetch should be from cache")
	}
	if cacheLatency > 100*time.Millisecond {
		t.Errorf("cache hit took %v, want < 100ms", cacheLatency)
	}
	if r2.Content != r1.Content {
		t.Error("cached content differs from original")
	}
	t.Logf("warm: FromCache=%v latency=%v", r2.FromCache, cacheLatency)
}

func TestIntegration_FetchMany(t *testing.T) {
	chain := buildIntegrationChain(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	urls := []string{
		"https://en.wikipedia.org/wiki/HTTP",
		"https://go.dev/doc/faq",
		"https://docs.python.org/3/faq/general.html",
	}

	results, err := chain.FetchMany(ctx, urls)
	if err != nil {
		t.Logf("FetchMany partial error (expected for some URLs): %v", err)
	}

	successes := 0
	for i, r := range results {
		if r != nil {
			successes++
			t.Logf("[%d] %s → layer=%s content=%d chars", i, urls[i], r.LayerUsed, len(r.Content))
		} else {
			t.Logf("[%d] %s → FAILED", i, urls[i])
		}
	}
	if successes < 2 {
		t.Errorf("only %d/3 succeeded, want >= 2", successes)
	}
}
