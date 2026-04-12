// diting is a multi-source aggregated search CLI.
//
// Phase 1.10 delivers only the `fetch` subcommand. The full CLI surface
// (search, config, doctor, etc.) is Phase 4.
//
// Usage:
//
//	diting fetch <url>                  # fetch + extract, print content
//	diting fetch <url> --json           # JSON output
//	diting fetch <url> --no-cache       # bypass cache
//	diting fetch <url> --no-extract     # skip extraction, print raw body
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	cdp "github.com/odradekk/diting/internal/fetch/chromedp"
	fetchcache "github.com/odradekk/diting/internal/fetch/cache"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "fetch":
		runFetch(os.Args[2:])
	case "version":
		fmt.Println("diting v2.0.0-dev")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `diting — multi-source aggregated search

Commands:
  fetch <url>    Fetch and extract content from a URL
  version        Print version
  help           Show this help

Use "diting fetch --help" for fetch options.
`)
}

// --- fetch subcommand -------------------------------------------------------

func runFetch(args []string) {
	// Reorder args so flags and the URL can appear in any order.
	// Go's flag package stops parsing at the first non-flag argument,
	// so "fetch <url> --json" wouldn't parse --json. We move any
	// non-flag arg (the URL) to the end.
	var flags, positional []string
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	reordered := append(flags, positional...)

	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	noCache := fs.Bool("no-cache", false, "bypass content cache")
	noExtract := fs.Bool("no-extract", false, "skip extraction, print raw body")
	timeout := fs.Duration("timeout", 45*time.Second, "per-URL timeout")
	fs.Parse(reordered)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: diting fetch [options] <url>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	targetURL := fs.Arg(0)

	chain, cacheCloser := buildChain(*noCache, *noExtract)
	defer chain.Close()
	if cacheCloser != nil {
		defer cacheCloser.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := chain.Fetch(ctx, targetURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch failed: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(result)
	} else {
		printText(result)
	}
}

func buildChain(noCache, noExtract bool) (*fetch.Chain, *fetchcache.Cache) {
	layers := []fetch.Layer{
		{Name: utls.LayerName, Fetcher: utls.New(utls.Options{}), Timeout: 15 * time.Second, Enabled: true},
	}

	if cdpLayer, err := cdp.New(cdp.Options{}); err == nil {
		layers = append(layers, fetch.Layer{
			Name: cdp.LayerName, Fetcher: cdpLayer, Timeout: 30 * time.Second, Enabled: true,
		})
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

	var opts []fetch.ChainOption

	if !noExtract {
		opts = append(opts, fetch.WithExtractor(extract.New(extract.Options{})))
	}

	var cc *fetchcache.Cache
	if !noCache {
		var err error
		cc, err = fetchcache.Open(fetchcache.Options{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cache disabled: %v\n", err)
		} else {
			opts = append(opts, fetch.WithCache(cc))
		}
	}

	return fetch.NewChain(layers, opts...), cc
}

type jsonResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Title       string `json:"title"`
	ContentType string `json:"content_type"`
	LayerUsed   string `json:"layer_used"`
	LatencyMs   int64  `json:"latency_ms"`
	FromCache   bool   `json:"from_cache"`
	ContentLen  int    `json:"content_length"`
	Content     string `json:"content"`
}

func printJSON(r *fetch.Result) {
	jr := jsonResult{
		URL:         r.URL,
		FinalURL:    r.FinalURL,
		Title:       r.Title,
		ContentType: r.ContentType,
		LayerUsed:   r.LayerUsed,
		LatencyMs:   r.LatencyMs,
		FromCache:   r.FromCache,
		ContentLen:  len(r.Content),
		Content:     r.Content,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(jr)
}

func printText(r *fetch.Result) {
	fmt.Printf("URL:          %s\n", r.URL)
	fmt.Printf("Final URL:    %s\n", r.FinalURL)
	fmt.Printf("Title:        %s\n", r.Title)
	fmt.Printf("Content-Type: %s\n", r.ContentType)
	fmt.Printf("Layer:        %s\n", r.LayerUsed)
	fmt.Printf("Latency:      %dms\n", r.LatencyMs)
	fmt.Printf("From Cache:   %v\n", r.FromCache)
	fmt.Printf("Content:      %d chars\n", len(r.Content))
	fmt.Println("---")
	fmt.Println(r.Content)
}
