package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	"github.com/odradekk/diting/internal/fetch/chromedp"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
	"github.com/odradekk/diting/internal/search"
	_ "github.com/odradekk/diting/internal/search/arxiv"
	_ "github.com/odradekk/diting/internal/search/baidu"
	_ "github.com/odradekk/diting/internal/search/bing"
	_ "github.com/odradekk/diting/internal/search/duckduckgo"
	_ "github.com/odradekk/diting/internal/search/github"
	_ "github.com/odradekk/diting/internal/search/stackexchange"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "fetch":
		url := "https://en.wikipedia.org/wiki/Metasearch_engine"
		if len(os.Args) > 2 {
			url = os.Args[2]
		}
		runFetch(url)
	case "bing":
		query := "metasearch engine"
		if len(os.Args) > 2 {
			query = os.Args[2]
		}
		runSearch("bing", query)
	case "ddg":
		query := "metasearch engine"
		if len(os.Args) > 2 {
			query = os.Args[2]
		}
		runSearch("duckduckgo", query)
	case "baidu":
		query := "元搜索引擎"
		if len(os.Args) > 2 {
			query = os.Args[2]
		}
		runSearch("baidu", query)
	case "arxiv":
		query := "metasearch engine"
		if len(os.Args) > 2 {
			query = os.Args[2]
		}
		runSearch("arxiv", query)
	case "github":
		query := "metasearch engine"
		if len(os.Args) > 2 {
			query = os.Args[2]
		}
		runSearch("github", query)
	case "se":
		query := "metasearch engine"
		if len(os.Args) > 2 {
			query = os.Args[2]
		}
		runSearch("stackexchange", query)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

const usage = `Usage: debug <command> [arg]

Commands:
  fetch [url]    Fetch a URL through the full chain (fetch + extract)
  bing [query]   Search Bing and print/parse results
  ddg [query]    Search DuckDuckGo and print/parse results
  baidu [query]  Search Baidu and print/parse results
  arxiv [query]  Search arXiv and print/parse results
  github [query] Search GitHub repositories
  se [query]     Search StackExchange (StackOverflow)

Default URL:    https://en.wikipedia.org/wiki/Metasearch_engine
Default query:  metasearch engine / 元搜索引擎 (baidu)
`

// ---------------------------------------------------------------------------
// fetch sub-command
// ---------------------------------------------------------------------------

func runFetch(targetURL string) {
	chain := buildChain()
	defer chain.Close()

	result, err := chain.Fetch(context.Background(), targetURL)
	if err != nil {
		log.Fatalf("chain fetch failed: %+v", err)
	}

	fmt.Println("===== CHAIN FETCH RESULT =====")
	fmt.Printf("URL:         %s\n", result.URL)
	fmt.Printf("Final URL:   %s\n", result.FinalURL)
	fmt.Printf("ContentType: %s\n", result.ContentType)
	fmt.Printf("LayerUsed:   %s\n", result.LayerUsed)
	fmt.Printf("Title:       %s\n", result.Title)
	fmt.Printf("Latency:     %dms\n", result.LatencyMs)
	fmt.Printf("Content:     %d chars\n", len(result.Content))
	fmt.Printf("--- Content (first 1000 chars) ---\n%s\n", preview(result.Content, 1000))

	outDir := debugOutDir()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	slug := urlSlug(targetURL)
	p := filepath.Join(outDir, slug+"_extracted.txt")
	if err := os.WriteFile(p, []byte(result.Content), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Printf("\n[extracted saved] %s\n", p)
}

// ---------------------------------------------------------------------------
// bing sub-command
// ---------------------------------------------------------------------------

func runSearch(moduleName, query string) {
	factory, err := search.Get(moduleName)
	if err != nil {
		log.Fatalf("get %s factory: %v", moduleName, err)
	}
	mod, err := factory(search.ModuleConfig{})
	if err != nil {
		log.Fatalf("create %s module: %v", moduleName, err)
	}
	defer mod.Close()

	mf := mod.Manifest()
	fmt.Printf("===== %s SEARCH =====\n", strings.ToUpper(moduleName))
	fmt.Printf("Module:   %s\n", mf.Name)
	fmt.Printf("Type:     %s\n", mf.SourceType)
	fmt.Printf("Cost:     %s\n", mf.CostTier)
	fmt.Printf("Query:    %s\n\n", query)

	results, err := mod.Search(context.Background(), query)
	if err != nil {
		log.Fatalf("%s search: %v", moduleName, err)
	}

	fmt.Printf("Results:  %d\n", len(results))
	for i, r := range results {
		fmt.Printf("\n--- #%d ---\n", i+1)
		fmt.Printf("Title:   %s\n", r.Title)
		fmt.Printf("URL:     %s\n", r.URL)
		fmt.Printf("Snippet: %s\n", r.Snippet)
	}

	outDir := debugOutDir()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	slug := urlSlug(moduleName + "_" + query)
	pretty, _ := json.MarshalIndent(results, "", "  ")
	p := filepath.Join(outDir, slug+".json")
	if err := os.WriteFile(p, pretty, 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Printf("\n[results saved] %s\n", p)
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

func buildChain() *fetch.Chain {
	utlsLayer := utls.New(utls.Options{})
	chromedpLayer, err := chromedp.New(chromedp.Options{})
	if err != nil {
		log.Printf("chromedp init skipped: %v", err)
	}

	layers := []fetch.Layer{
		{Name: utls.LayerName, Fetcher: utlsLayer, Timeout: 15 * time.Second, Enabled: true},
	}
	if chromedpLayer != nil {
		layers = append(layers, fetch.Layer{
			Name: chromedp.LayerName, Fetcher: chromedpLayer, Timeout: 30 * time.Second, Enabled: true,
		})
	}
	layers = append(layers,
		fetch.Layer{Name: jina.LayerName, Fetcher: jina.New(jina.Options{}), Timeout: 20 * time.Second, Enabled: true},
		fetch.Layer{Name: archive.LayerName, Fetcher: archive.New(archive.Options{}), Timeout: 20 * time.Second, Enabled: true},
	)
	if apiKey := os.Getenv("TAVILY_API_KEY"); apiKey != "" {
		layers = append(layers, fetch.Layer{
			Name: tavily.LayerName, Fetcher: tavily.New(tavily.Options{APIKey: apiKey}), Timeout: 20 * time.Second, Enabled: true,
		})
	}

	return fetch.NewChain(layers,
		fetch.WithExtractor(extract.New(extract.Options{})),
	)
}

func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}

func urlSlug(u string) string {
	s := strings.NewReplacer(
		"https://", "", "http://", "",
		"/", "_", "?", "_", "&", "_", "=", "_", ":", "_",
		" ", "_",
	).Replace(u)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func debugOutDir() string {
	dir, _ := os.Getwd()
	return filepath.Join(dir, "debug_output")
}
