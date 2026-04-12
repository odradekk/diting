// bench_fetch runs the full fetch chain + extraction against a set of URLs
// loaded from docs/bench/fetch/*.yaml. It measures:
//
//  1. Fetch success rate (per difficulty, per content type, overall)
//  2. Content quality proxies (length, title, noise indicators, structure)
//
// Usage:
//
//	go run ./test/bench_fetch/                          # all URLs
//	go run ./test/bench_fetch/ --concurrency 2          # limit parallelism
//	go run ./test/bench_fetch/ --filter medium          # only medium difficulty
//	go run ./test/bench_fetch/ --out report.md          # write markdown report
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	cdp "github.com/odradekk/diting/internal/fetch/chromedp"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
)

// --- URL entry from YAML ----------------------------------------------------

type urlEntry struct {
	URL            string `yaml:"url"`
	Domain         string `yaml:"domain"`
	Difficulty     string `yaml:"difficulty"`
	ContentType    string `yaml:"content_type"`
	Language       string `yaml:"language"`
	BotProtection  string `yaml:"bot_protection"`
	JSRendered     bool   `yaml:"js_rendered"`
	HasCodeBlocks  bool   `yaml:"has_code_blocks"`
	HasTables      bool   `yaml:"has_tables"`
	ExpectedTitle  string `yaml:"expected_title"`
	Notes          string `yaml:"notes"`
}

// --- result per URL ---------------------------------------------------------

type fetchResult struct {
	Entry       urlEntry
	Success     bool
	LayerUsed   string
	LatencyMs   int64
	ContentLen  int
	Title       string
	Error       string
	Quality     qualityMetrics
}

type qualityMetrics struct {
	HasTitle       bool
	ParagraphCount int
	AvgParaLen     int
	NoiseCount     int    // count of noise indicators found
	NoiseDetails   string // which indicators triggered
	EncodingOK     bool   // no U+FFFD or mojibake
	CodeHTMLCount  int    // angle-bracket patterns (typically from code examples, not real remnants)
}

// --- flags ------------------------------------------------------------------

var (
	flagConcurrency = flag.Int("concurrency", 4, "max parallel fetches")
	flagFilter      = flag.String("filter", "", "filter by difficulty: easy|medium|hard")
	flagOut         = flag.String("out", "", "output markdown report to file (default: stdout)")
	flagURLDir      = flag.String("urls", "docs/bench/fetch", "directory containing URL YAML files")
	flagTimeout     = flag.Duration("timeout", 45*time.Second, "per-URL fetch timeout")
)

func main() {
	flag.Parse()

	entries, err := loadURLs(*flagURLDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load URLs: %v\n", err)
		os.Exit(1)
	}

	if *flagFilter != "" {
		var filtered []urlEntry
		for _, e := range entries {
			if e.Difficulty == *flagFilter {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	fmt.Fprintf(os.Stderr, "bench_fetch: %d URLs, concurrency=%d, timeout=%s\n",
		len(entries), *flagConcurrency, *flagTimeout)

	chain := buildChain()
	defer chain.Close()

	results := runAll(chain, entries)
	report := generateReport(results)

	if *flagOut != "" {
		if err := os.WriteFile(*flagOut, []byte(report), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "report written to %s\n", *flagOut)
	} else {
		fmt.Print(report)
	}
}

// --- chain setup ------------------------------------------------------------

func buildChain() *fetch.Chain {
	utlsLayer := utls.New(utls.Options{})

	layers := []fetch.Layer{
		{Name: utls.LayerName, Fetcher: utlsLayer, Timeout: 15 * time.Second, Enabled: true},
	}

	if cdpLayer, err := cdp.New(cdp.Options{}); err == nil {
		layers = append(layers, fetch.Layer{
			Name: cdp.LayerName, Fetcher: cdpLayer, Timeout: 30 * time.Second, Enabled: true,
		})
	} else {
		fmt.Fprintf(os.Stderr, "chromedp skipped: %v\n", err)
	}

	layers = append(layers,
		fetch.Layer{Name: jina.LayerName, Fetcher: jina.New(jina.Options{}), Timeout: 20 * time.Second, Enabled: true},
		fetch.Layer{Name: archive.LayerName, Fetcher: archive.New(archive.Options{}), Timeout: 25 * time.Second, Enabled: true},
	)

	if apiKey := os.Getenv("TAVILY_API_KEY"); apiKey != "" {
		layers = append(layers, fetch.Layer{
			Name: tavily.LayerName, Fetcher: tavily.New(tavily.Options{APIKey: apiKey}), Timeout: 30 * time.Second, Enabled: true,
		})
	}

	return fetch.NewChain(layers,
		fetch.WithExtractor(extract.New(extract.Options{})),
		fetch.WithConcurrency(*flagConcurrency),
	)
}

// --- execution --------------------------------------------------------------

func runAll(chain *fetch.Chain, entries []urlEntry) []fetchResult {
	results := make([]fetchResult, len(entries))
	sem := make(chan struct{}, *flagConcurrency)
	var wg sync.WaitGroup
	var done atomic.Int32

	total := len(entries)
	for i, entry := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, e urlEntry) {
			defer wg.Done()
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), *flagTimeout)
			defer cancel()

			start := time.Now()
			r, err := chain.Fetch(ctx, e.URL)
			latency := time.Since(start).Milliseconds()

			fr := fetchResult{
				Entry:     e,
				LatencyMs: latency,
			}

			if err != nil {
				fr.Error = truncStr(err.Error(), 200)
			} else {
				fr.Success = true
				fr.LayerUsed = r.LayerUsed
				fr.ContentLen = len(r.Content)
				fr.Title = r.Title
				fr.Quality = assessQuality(r.Content, r.Title)
			}

			results[idx] = fr
			n := done.Add(1)
			fmt.Fprintf(os.Stderr, "[%d/%d] %s %s (%dms) layer=%s err=%s\n",
				n, total, statusIcon(fr.Success), truncStr(e.URL, 60), latency,
				fr.LayerUsed, truncStr(fr.Error, 80))
		}(i, entry)
	}
	wg.Wait()
	return results
}

// --- quality assessment (proxy metrics) -------------------------------------

var (
	noisePatterns = []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{"cookie_banner", regexp.MustCompile(`(?i)(accept\s+cookies?|cookie\s+policy|we\s+use\s+cookies)`)},
		{"subscribe_cta", regexp.MustCompile(`(?i)(subscribe\s+(to|now)|sign\s+up\s+for|newsletter)`)},
		{"nav_breadcrumb", regexp.MustCompile(`(?i)(breadcrumb|skip\s+to\s+(main|content)|jump\s+to)`)},
		{"copyright", regexp.MustCompile(`(?i)©\s*\d{4}|all\s+rights\s+reserved`)},
		{"social_share", regexp.MustCompile(`(?i)(share\s+on|tweet\s+this|share\s+this|follow\s+us)`)},
		{"related_articles", regexp.MustCompile(`(?i)(related\s+(articles?|posts?)|you\s+might\s+also|recommended\s+for)`)},
		{"login_prompt", regexp.MustCompile(`(?i)(log\s*in\s+to|sign\s+in\s+to|create\s+(an\s+)?account)`)},
	}
	// Counts angle-bracket patterns in extracted text. These are almost
	// always from code examples in documentation (e.g., <div>, <span>),
	// NOT residual DOM elements — go-readability's TextContent only
	// returns text nodes. Tracked as informational, not a defect.
	htmlLikePattern = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
)

func assessQuality(content, title string) qualityMetrics {
	qm := qualityMetrics{
		HasTitle:   title != "",
		EncodingOK: !strings.Contains(content, "\uFFFD") && utf8.ValidString(content),
	}

	// Paragraph structure.
	paras := splitParagraphs(content)
	qm.ParagraphCount = len(paras)
	if len(paras) > 0 {
		totalLen := 0
		for _, p := range paras {
			totalLen += len(p)
		}
		qm.AvgParaLen = totalLen / len(paras)
	}

	// Noise indicators.
	var triggered []string
	for _, np := range noisePatterns {
		if np.pattern.MatchString(content) {
			triggered = append(triggered, np.name)
		}
	}
	qm.NoiseCount = len(triggered)
	qm.NoiseDetails = strings.Join(triggered, ", ")

	// Angle-bracket patterns (code examples, not actual DOM remnants).
	qm.CodeHTMLCount = len(htmlLikePattern.FindAllString(content, -1))

	return qm
}

func splitParagraphs(s string) []string {
	raw := strings.Split(s, "\n\n")
	var out []string
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if len(p) > 20 { // skip tiny fragments
			out = append(out, p)
		}
	}
	return out
}

// --- report generation ------------------------------------------------------

func generateReport(results []fetchResult) string {
	var b strings.Builder

	now := time.Now().Format("2006-01-02 15:04")
	b.WriteString(fmt.Sprintf("# Fetch Layer Benchmark Report\n\n**Date**: %s\n\n", now))

	// Overall summary.
	total := len(results)
	successes := 0
	for _, r := range results {
		if r.Success {
			successes++
		}
	}
	b.WriteString(fmt.Sprintf("## Summary\n\n**Total**: %d URLs | **Success**: %d (%.1f%%) | **Failed**: %d (%.1f%%)\n\n",
		total, successes, pct(successes, total), total-successes, pct(total-successes, total)))

	// By difficulty.
	b.WriteString("## Success Rate by Difficulty\n\n")
	b.WriteString("| Difficulty | Total | Success | Rate | Avg Latency |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, diff := range []string{"easy", "medium", "hard"} {
		sub := filterResults(results, func(r fetchResult) bool { return r.Entry.Difficulty == diff })
		s, t, avgLat := countSuccess(sub)
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% | %dms |\n", diff, t, s, pct(s, t), avgLat))
	}

	// By content type.
	b.WriteString("\n## Success Rate by Content Type\n\n")
	b.WriteString("| Content Type | Total | Success | Rate |\n")
	b.WriteString("|---|---|---|---|\n")
	ctypes := uniqueField(results, func(r fetchResult) string { return r.Entry.ContentType })
	for _, ct := range ctypes {
		sub := filterResults(results, func(r fetchResult) bool { return r.Entry.ContentType == ct })
		s, t, _ := countSuccess(sub)
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% |\n", ct, t, s, pct(s, t)))
	}

	// By layer used.
	b.WriteString("\n## Layer Distribution (successful fetches)\n\n")
	b.WriteString("| Layer | Count | % of Success |\n")
	b.WriteString("|---|---|---|\n")
	layerCounts := map[string]int{}
	for _, r := range results {
		if r.Success {
			layerCounts[r.LayerUsed]++
		}
	}
	layerOrder := []string{"utls", "chromedp", "jina", "archive", "tavily"}
	for _, l := range layerOrder {
		if c, ok := layerCounts[l]; ok {
			b.WriteString(fmt.Sprintf("| %s | %d | %.1f%% |\n", l, c, pct(c, successes)))
		}
	}

	// Content quality summary (successful only).
	b.WriteString("\n## Content Quality (successful fetches)\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("|---|---|\n")
	var titleCount, encodingOK, noiseZero, htmlClean int
	var totalParas, totalContentLen int
	for _, r := range results {
		if !r.Success {
			continue
		}
		if r.Quality.HasTitle {
			titleCount++
		}
		if r.Quality.EncodingOK {
			encodingOK++
		}
		if r.Quality.NoiseCount == 0 {
			noiseZero++
		}
		if r.Quality.CodeHTMLCount == 0 {
			htmlClean++
		}
		totalParas += r.Quality.ParagraphCount
		totalContentLen += r.ContentLen
	}
	b.WriteString(fmt.Sprintf("| Title extracted | %d / %d (%.0f%%) |\n", titleCount, successes, pct(titleCount, successes)))
	b.WriteString(fmt.Sprintf("| Encoding OK (valid UTF-8) | %d / %d (%.0f%%) |\n", encodingOK, successes, pct(encodingOK, successes)))
	b.WriteString(fmt.Sprintf("| Zero noise indicators | %d / %d (%.0f%%) |\n", noiseZero, successes, pct(noiseZero, successes)))
	b.WriteString(fmt.Sprintf("| No code-sample HTML | %d / %d (%.0f%%) |\n", htmlClean, successes, pct(htmlClean, successes)))
	if successes > 0 {
		b.WriteString(fmt.Sprintf("| Avg content length | %d chars |\n", totalContentLen/successes))
		b.WriteString(fmt.Sprintf("| Avg paragraphs | %d |\n", totalParas/successes))
	}

	// Per-URL details.
	b.WriteString("\n## Per-URL Results\n\n")
	b.WriteString("| # | Diff | Type | URL | OK | Layer | ms | Chars | Title | Noise | Code |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for i, r := range results {
		ok := "❌"
		if r.Success {
			ok = "✅"
		}
		urlShort := truncStr(r.Entry.URL, 50)
		titleShort := truncStr(r.Title, 25)
		noise := fmt.Sprintf("%d", r.Quality.NoiseCount)
		html := fmt.Sprintf("%d", r.Quality.CodeHTMLCount)
		if !r.Success {
			noise = "-"
			html = "-"
			titleShort = truncStr(r.Error, 30)
		}
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %d | %d | %s | %s | %s |\n",
			i+1, r.Entry.Difficulty, r.Entry.ContentType, urlShort, ok,
			r.LayerUsed, r.LatencyMs, r.ContentLen, titleShort, noise, html))
	}

	// Failures detail.
	failures := filterResults(results, func(r fetchResult) bool { return !r.Success })
	if len(failures) > 0 {
		b.WriteString("\n## Failed URLs\n\n")
		for _, r := range failures {
			b.WriteString(fmt.Sprintf("- **%s** (%s, %s): %s\n", r.Entry.URL, r.Entry.Difficulty, r.Entry.BotProtection, r.Error))
		}
	}

	// Noise offenders.
	b.WriteString("\n## Noise Offenders (noise > 0)\n\n")
	noisy := filterResults(results, func(r fetchResult) bool { return r.Success && r.Quality.NoiseCount > 0 })
	if len(noisy) == 0 {
		b.WriteString("None — all successful extractions are noise-free.\n")
	} else {
		for _, r := range noisy {
			b.WriteString(fmt.Sprintf("- **%s** (noise=%d): %s\n",
				truncStr(r.Entry.URL, 60), r.Quality.NoiseCount, r.Quality.NoiseDetails))
		}
	}

	return b.String()
}

// --- helpers ----------------------------------------------------------------

func loadURLs(dir string) ([]urlEntry, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no YAML files found in %s", dir)
	}
	sort.Strings(files)

	var all []urlEntry
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		var entries []urlEntry
		if err := yaml.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		all = append(all, entries...)
	}
	return all, nil
}

func filterResults(results []fetchResult, pred func(fetchResult) bool) []fetchResult {
	var out []fetchResult
	for _, r := range results {
		if pred(r) {
			out = append(out, r)
		}
	}
	return out
}

func countSuccess(results []fetchResult) (success, total int, avgLatency int64) {
	total = len(results)
	var latSum int64
	for _, r := range results {
		if r.Success {
			success++
		}
		latSum += r.LatencyMs
	}
	if total > 0 {
		avgLatency = latSum / int64(total)
	}
	return
}

func uniqueField(results []fetchResult, fn func(fetchResult) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range results {
		v := fn(r)
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func statusIcon(ok bool) string {
	if ok {
		return "✅"
	}
	return "❌"
}

func truncStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
