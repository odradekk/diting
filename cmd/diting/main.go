// diting is a multi-source aggregated search CLI.
//
// Usage:
//
//	diting search <question>            # full pipeline: plan → search → fetch → answer
//	diting search <question> --json     # JSON output
//	diting search <question> --plan-only # show plan and stop
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
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	cdp "github.com/odradekk/diting/internal/fetch/chromedp"
	fetchcache "github.com/odradekk/diting/internal/fetch/cache"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
	"github.com/odradekk/diting/internal/llm"
	_ "github.com/odradekk/diting/internal/llm/anthropic"
	_ "github.com/odradekk/diting/internal/llm/openai"
	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/search"
	_ "github.com/odradekk/diting/internal/search/arxiv"
	_ "github.com/odradekk/diting/internal/search/baidu"
	_ "github.com/odradekk/diting/internal/search/bing"
	_ "github.com/odradekk/diting/internal/search/brave"
	_ "github.com/odradekk/diting/internal/search/duckduckgo"
	_ "github.com/odradekk/diting/internal/search/github"
	_ "github.com/odradekk/diting/internal/search/serp"
	_ "github.com/odradekk/diting/internal/search/stackexchange"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "search":
		runSearch(os.Args[2:])
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
  search <question>  Search and answer a question
  fetch <url>        Fetch and extract content from a URL
  version            Print version
  help               Show this help

Use "diting search --help" or "diting fetch --help" for options.
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

// --- search subcommand -------------------------------------------------------

func runSearch(args []string) {
	var flags, positional []string
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	reordered := append(flags, positional...)

	// Default timeout: DITING_SEARCH_TIMEOUT env var, else 5 minutes.
	defaultTimeout := 5 * time.Minute
	if env := os.Getenv("DITING_SEARCH_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil {
			defaultTimeout = d
		}
	}

	fs := flag.NewFlagSet("search", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	planOnly := fs.Bool("plan-only", false, "show plan and stop")
	provider := fs.String("provider", "", "LLM provider (anthropic|openai, default: auto-detect)")
	model := fs.String("model", "", "LLM model override")
	timeout := fs.Duration("timeout", defaultTimeout, "overall timeout (env: DITING_SEARCH_TIMEOUT)")
	scorerConfig := fs.String("scorer-config", os.Getenv("DITING_SCORER_CONFIG"), "path to scorer YAML config (env: DITING_SCORER_CONFIG)")
	debug := fs.Bool("debug", false, "show debug info (token usage, source counts)")
	fs.Parse(reordered)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: diting search [options] <question>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	question := strings.Join(fs.Args(), " ")

	// --- LLM client ---
	llmClient, providerName := buildLLMClient(*provider, *model)
	if llmClient == nil {
		fmt.Fprintln(os.Stderr, "error: no LLM provider configured. Set one of:")
		fmt.Fprintln(os.Stderr, "  ANTHROPIC_API_KEY or OPENAI_API_KEY")
		fmt.Fprintln(os.Stderr, "  or use --provider=<name>")
		fmt.Fprintln(os.Stderr, "\nFor OpenAI-compatible providers (MiniMax, Together, etc.),")
		fmt.Fprintln(os.Stderr, "set OPENAI_API_KEY and OPENAI_BASE_URL.")
		os.Exit(1)
	}

	// --- search modules ---
	modules := buildSearchModules()
	if len(modules) == 0 {
		fmt.Fprintln(os.Stderr, "error: no search modules available")
		os.Exit(1)
	}

	// --- fetch chain ---
	chain, cacheCloser := buildChain(false, false)
	defer chain.Close()
	if cacheCloser != nil {
		defer cacheCloser.Close()
	}

	// --- scorer ---
	var scorer pipeline.Scorer
	if *scorerConfig != "" {
		cfg, err := pipeline.LoadScorerConfigFromFile(*scorerConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		scorer = pipeline.NewScorer(cfg)
		fmt.Fprintf(os.Stderr, "diting: using scorer config from %s\n", *scorerConfig)
	}
	// nil scorer → pipeline.New uses DefaultScorer (embedded default config)

	// --- pipeline config ---
	planMode := pipeline.PlanModeAuto
	if *planOnly {
		planMode = pipeline.PlanModeShow
	}

	logger := slog.Default()
	if !*debug {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	p := pipeline.New(modules, chain, llmClient, scorer, pipeline.Config{
		PlanMode: planMode,
	}, logger)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	fmt.Fprintf(os.Stderr, "diting: using %s, %d search modules\n", providerName, len(modules))

	result, err := p.Run(ctx, question)
	if err != nil {
		fmt.Fprintf(os.Stderr, "search failed: %v\n", err)
		os.Exit(1)
	}

	// Close search modules.
	for _, m := range modules {
		m.Close()
	}

	if *jsonOut {
		printSearchJSON(result)
	} else {
		printSearchText(result, *debug)
	}
}

// buildLLMClient auto-detects or uses the specified LLM provider.
//
// Environment variables (per provider):
//
//	ANTHROPIC_API_KEY, ANTHROPIC_MODEL
//	OPENAI_API_KEY, OPENAI_BASE_URL, OPENAI_MODEL
//
// --model flag overrides the env var. For OpenAI-compatible providers
// (MiniMax, Together, etc.), set OPENAI_BASE_URL.
func buildLLMClient(provider, modelFlag string) (llm.Client, string) {
	// Auto-detect order: anthropic → openai.
	candidates := []struct {
		name     string
		envKey   string
		envModel string
	}{
		{"anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"},
		{"openai", "OPENAI_API_KEY", "OPENAI_MODEL"},
	}

	resolveModel := func(envModel, flagModel string) string {
		if flagModel != "" {
			return flagModel // --model flag wins
		}
		return os.Getenv(envModel) // env var, or "" for provider default
	}

	if provider != "" {
		// Explicit provider.
		var key, envModel string
		for _, c := range candidates {
			if c.name == provider {
				key = os.Getenv(c.envKey)
				envModel = c.envModel
				break
			}
		}
		if key == "" {
			key = os.Getenv(strings.ToUpper(provider) + "_API_KEY")
		}
		if key == "" {
			fmt.Fprintf(os.Stderr, "warning: no API key found for provider %q\n", provider)
			return nil, ""
		}
		factory, err := llm.Get(provider)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return nil, ""
		}
		cfg := llm.ProviderConfig{
			APIKey: key,
			Model:  resolveModel(envModel, modelFlag),
		}
		if provider == "openai" {
			cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
		client, err := factory(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return nil, ""
		}
		return client, provider
	}

	// Auto-detect.
	for _, c := range candidates {
		key := os.Getenv(c.envKey)
		if key == "" {
			continue
		}
		factory, err := llm.Get(c.name)
		if err != nil {
			continue
		}
		cfg := llm.ProviderConfig{
			APIKey: key,
			Model:  resolveModel(c.envModel, modelFlag),
		}
		if c.name == "openai" {
			cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
		client, err := factory(cfg)
		if err != nil {
			continue
		}
		return client, c.name
	}

	return nil, ""
}

// buildSearchModules creates all available search modules from env vars.
func buildSearchModules() []search.Module {
	type modSpec struct {
		name   string
		apiEnv string // if set, module needs this env var
	}

	specs := []modSpec{
		{"bing", ""},
		{"duckduckgo", ""},
		{"baidu", ""},
		{"arxiv", ""},
		{"github", ""},
		{"stackexchange", ""},
		{"brave", "BRAVE_API_KEY"},
		{"serp", "SERP_API_KEY"},
	}

	var modules []search.Module
	for _, s := range specs {
		if s.apiEnv != "" && os.Getenv(s.apiEnv) == "" {
			continue // skip BYOK module without key
		}
		factory, err := search.Get(s.name)
		if err != nil {
			continue
		}
		cfg := search.ModuleConfig{
			APIKey: os.Getenv(s.apiEnv),
		}
		if s.name == "github" {
			cfg.APIKey = os.Getenv("GITHUB_TOKEN")
		}
		m, err := factory(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: module %s init failed: %v\n", s.name, err)
			continue
		}
		modules = append(modules, m)
	}
	return modules
}

// --- search output -----------------------------------------------------------

type searchJSONResult struct {
	Question   string                  `json:"question"`
	Plan       pipeline.Plan           `json:"plan"`
	Answer     pipeline.Answer         `json:"answer"`
	Sources    []searchJSONSource      `json:"sources"`
	Debug      *pipeline.DebugInfo     `json:"debug,omitempty"`
}

type searchJSONSource struct {
	ID         int                     `json:"id"`
	Title      string                  `json:"title"`
	URL        string                  `json:"url"`
	SourceType string                  `json:"source_type"`
	Score      float64                 `json:"score"`
	Fetched    bool                    `json:"fetched"`
}

func printSearchJSON(r *pipeline.Result) {
	jr := searchJSONResult{
		Question: r.Question,
		Plan:     r.Plan,
		Answer:   r.Answer,
		Debug:    &r.Debug,
	}
	for _, s := range r.Sources {
		jr.Sources = append(jr.Sources, searchJSONSource{
			ID:         s.ID,
			Title:      s.Result.Title,
			URL:        s.Result.URL,
			SourceType: string(s.Result.SourceType),
			Score:      s.Result.Score,
			Fetched:    s.Fetched != nil,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(jr)
}

func printSearchText(r *pipeline.Result, showDebug bool) {
	// Plan-only mode.
	if r.Answer.Text == "" {
		fmt.Println("=== Plan ===")
		fmt.Printf("Rationale: %s\n", r.Plan.Rationale)
		for st, qs := range r.Plan.QueriesBySourceType {
			if len(qs) == 0 {
				continue
			}
			fmt.Printf("\n[%s]\n", st)
			for _, q := range qs {
				fmt.Printf("  - %s\n", q)
			}
		}
		fmt.Printf("\nExpected answer: %s\n", r.Plan.ExpectedAnswerShape)
		return
	}

	// Full answer.
	fmt.Println(r.Answer.Text)

	if len(r.Answer.Citations) > 0 {
		fmt.Println("\nSources:")
		for _, c := range r.Answer.Citations {
			fmt.Printf("  [%d] %s\n      %s\n", c.ID, c.Title, c.URL)
		}
	}

	fmt.Printf("\nConfidence: %s\n", r.Answer.Confidence)

	if showDebug {
		fmt.Println("\n--- Debug ---")
		fmt.Printf("Plan tokens:   %d in / %d out (cache: %d)\n",
			r.Debug.PlanInputTokens, r.Debug.PlanOutputTokens, r.Debug.PlanCacheReadTokens)
		fmt.Printf("Answer tokens: %d in / %d out (cache: %d)\n",
			r.Debug.AnswerInputTokens, r.Debug.AnswerOutputTokens, r.Debug.AnswerCacheReadTokens)
		fmt.Printf("Search:        %d results → %d selected → %d fetched\n",
			r.Debug.TotalSearchResults, r.Debug.SelectedSources, r.Debug.FetchedSources)
	}
}
