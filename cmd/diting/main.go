// diting is a multi-source aggregated search CLI.
//
// Usage:
//
//	diting search <question>                      # full pipeline: plan → search → fetch → answer
//	diting search <question> --format=markdown    # markdown output (also: json, text)
//	diting search <question> --plan-only          # show plan and stop
//	diting fetch <url>                            # fetch + extract, print content
//	diting fetch <url> --json                     # JSON output
//	diting fetch <url> --no-cache                 # bypass cache
//	diting fetch <url> --no-extract               # skip extraction, print raw body
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

	// Bench variants self-register via init(). Keep these imports
	// separate from the other blank-import groups so their intent
	// is explicit — removing one removes a whole benchmark variant
	// from the `diting bench run --variant` menu.
	_ "github.com/odradekk/diting/internal/bench/variants/v0baseline"
	_ "github.com/odradekk/diting/internal/bench/variants/v2raw"
	_ "github.com/odradekk/diting/internal/bench/variants/v2single"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	fetchcache "github.com/odradekk/diting/internal/fetch/cache"
	cdp "github.com/odradekk/diting/internal/fetch/chromedp"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
	_ "github.com/odradekk/diting/internal/llm/anthropic"
	_ "github.com/odradekk/diting/internal/llm/openai"
	"github.com/odradekk/diting/internal/pipeline"
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
	case "config":
		runConfig(os.Args[2:])
	case "init":
		runInit(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "bench":
		runBench(os.Args[2:])
	case "version":
		fmt.Println("diting " + ditingVersion)
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
  search <question>              Search and answer a question
  fetch <url>                    Fetch and extract content from a URL
  config <show|path|validate>    Inspect or validate the config file
  init                           Interactive config generator
  doctor                         Environment health check
  bench <run|report>             Run benchmark suite or view latest report
  version                        Print version
  help                           Show this help

Use "diting search --help" or "diting fetch --help" for options.
`)
}

// --- fetch subcommand -------------------------------------------------------

// fetchBoolFlags mirrors searchBoolFlags for the fetch subcommand.
// Keep in sync with the flag.Bool() calls in runFetch.
var fetchBoolFlags = map[string]bool{
	"json":       true,
	"no-cache":   true,
	"no-extract": true,
}

func reorderFetchArgs(args []string) []string {
	var flagTokens []string
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) == 0 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		if strings.Contains(a, "=") {
			flagTokens = append(flagTokens, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if fetchBoolFlags[name] {
			flagTokens = append(flagTokens, a)
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flagTokens = append(flagTokens, a, args[i+1])
			i++
			continue
		}
		flagTokens = append(flagTokens, a)
	}
	return append(flagTokens, positional...)
}

func runFetch(args []string) {
	// Reorder args so flags and the URL can appear in any order, while
	// preserving `--flag value` pairs (see reorderSearchArgs comment).
	reordered := reorderFetchArgs(args)

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

// searchBoolFlags is the set of search-subcommand flag names that do
// NOT take a value. Any flag NOT in this set is assumed to take a
// following value token — this matters for the reorder hack below,
// which needs to know which `--flag next-token` pairs must stay
// glued together.
//
// Keep this list in sync with the flag.Bool() calls in runSearch.
var searchBoolFlags = map[string]bool{
	"json":      true,
	"plan-only": true,
	"raw":       true,
	"debug":     true,
}

// reorderSearchArgs moves flags in front of positional arguments so
// that `diting search "query" --format=json` works the same as
// `diting search --format=json "query"` (Go's `flag` package stops
// parsing at the first non-flag argument otherwise).
//
// Critical subtlety (regression caught in Phase 4.8 smoke test): the
// naive reorder `[all flags first, all positionals last]` splits
// `--max-cost 1.00` into `[--max-cost, ..., 1.00]`, causing flag.Parse
// to treat the next flag as the --max-cost value. The fix is to walk
// pairwise and keep `--flag value` pairs intact.
func reorderSearchArgs(args []string) []string {
	var flagTokens []string
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) == 0 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}

		// `--flag=value` is self-contained, always safe to move.
		if strings.Contains(a, "=") {
			flagTokens = append(flagTokens, a)
			continue
		}

		// Strip the leading dashes to get the flag name.
		name := strings.TrimLeft(a, "-")

		// Bool flags have no value — standalone.
		if searchBoolFlags[name] {
			flagTokens = append(flagTokens, a)
			continue
		}

		// Non-bool flag: grab the next token as its value (if present
		// and not itself a flag).
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flagTokens = append(flagTokens, a, args[i+1])
			i++
			continue
		}

		// Value missing or followed by another flag — let flag.Parse
		// report the error with its own diagnostic.
		flagTokens = append(flagTokens, a)
	}

	return append(flagTokens, positional...)
}

func runSearch(args []string) {
	reordered := reorderSearchArgs(args)

	// Default timeout: DITING_SEARCH_TIMEOUT env var, else 5 minutes.
	// This is the fallback used when neither --timeout nor config
	// specifies a value.
	defaultTimeout := 5 * time.Minute
	if env := os.Getenv("DITING_SEARCH_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil {
			defaultTimeout = d
		}
	}

	fs := flag.NewFlagSet("search", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config.yaml (env: DITING_CONFIG)")
	format := fs.String("format", "text", "output format: text|json|markdown (aliases: txt|t, j, md|m)")
	jsonOut := fs.Bool("json", false, "shortcut for --format=json (deprecated)")
	planOnly := fs.Bool("plan-only", false, "run plan phase only, then stop")
	raw := fs.Bool("raw", false, "run plan + search + fetch, skip answer synthesis")
	provider := fs.String("provider", "", "LLM provider (anthropic|openai, default: auto-detect)")
	model := fs.String("model", "", "LLM model override")
	timeout := fs.Duration("timeout", defaultTimeout, "overall timeout (env: DITING_SEARCH_TIMEOUT)")
	scorerConfig := fs.String("scorer-config", os.Getenv("DITING_SCORER_CONFIG"), "path to scorer YAML config (env: DITING_SCORER_CONFIG)")
	debug := fs.Bool("debug", false, "show debug info (token usage, source counts)")
	maxCost := fs.Float64("max-cost", 0, "abort if the estimated cost (USD) would exceed this budget (0 = no guard)")
	fs.Parse(reordered)

	// Resolve format: --json forces json regardless of --format.
	outFormat, err := parseOutputFormat(*format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		outFormat = formatJSON
	}

	// --plan-only and --raw are mutually exclusive (one skips answer, the
	// other skips search — you can't do both).
	if *planOnly && *raw {
		fmt.Fprintln(os.Stderr, "error: --plan-only and --raw are mutually exclusive")
		os.Exit(1)
	}

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: diting search [options] <question>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	question := strings.Join(fs.Args(), " ")

	// --- Config file load ---
	//
	// When --config is explicit, missing/invalid file is a hard error.
	// When it's empty, we try the default path silently — a missing
	// file at the default location is OK (user hasn't run `diting init`
	// yet and is relying on env vars and flags).
	cfg, cfgPath, err := loadSearchConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if cfg != nil && cfgPath != "" {
		// Print a one-line info note on stderr so the user knows
		// their config is in effect.
		fmt.Fprintf(os.Stderr, "diting: loaded config from %s\n", cfgPath)
	}

	// --- Resolve effective options (CLI > env > config > defaults) ---
	flagsStruct := searchFlags{
		Provider:     *provider,
		Model:        *model,
		Timeout:      *timeout,
		Debug:        *debug,
		ScorerConfig: *scorerConfig,
		setFlags:     collectSetFlags(fs),
	}
	envStruct := searchEnv{
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:  os.Getenv("ANTHROPIC_MODEL"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:     os.Getenv("OPENAI_MODEL"),
		OpenAIBaseURL:   os.Getenv("OPENAI_BASE_URL"),
	}
	defaults := resolvedSearchOptions{
		Timeout:   defaultTimeout,
		LogLevel:  slog.LevelWarn,
		LogFormat: "text",
	}
	effective := resolveSearchOptions(flagsStruct, envStruct, cfg, defaults)

	// --- LLM client ---
	llmClient, providerName, modelName := buildLLMClientResolved(effective)
	if llmClient == nil {
		fmt.Fprintln(os.Stderr, "error: no LLM provider configured. Set one of:")
		fmt.Fprintln(os.Stderr, "  ANTHROPIC_API_KEY or OPENAI_API_KEY")
		fmt.Fprintln(os.Stderr, "  or use --provider=<name>")
		fmt.Fprintln(os.Stderr, "\nFor OpenAI-compatible providers (MiniMax, Together, etc.),")
		fmt.Fprintln(os.Stderr, "set OPENAI_API_KEY and OPENAI_BASE_URL,")
		fmt.Fprintln(os.Stderr, "or put them in your config file (see `diting config show`).")
		os.Exit(1)
	}

	// --- --max-cost pre-flight guard ---
	// Run the estimate BEFORE spinning up the fetch chain or search
	// modules — we want to abort as cheaply and early as possible when
	// the user's budget is too tight.
	if *maxCost > 0 {
		if err := enforceMaxCost(os.Stderr, modelName, *maxCost); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	// --- search modules ---
	//
	// Even in --plan-only mode we still instantiate modules: the pipeline
	// needs their Manifests to build the system prompt (so the LLM knows
	// which source types are available). Factories don't do network I/O —
	// they just construct client structs — so this is cheap.
	modules := buildSearchModulesFiltered(effective.EnabledModules)
	if len(modules) == 0 {
		fmt.Fprintln(os.Stderr, "error: no search modules available")
		os.Exit(1)
	}

	// --- fetch chain ---
	//
	// Skip fetch chain construction entirely in --plan-only mode: the
	// chromedp layer spawns a headless Chrome and the cache opens a BoltDB
	// file, both of which add hundreds of ms to cold start. Plan-only never
	// fetches anything, so none of this is needed.
	var chain *fetch.Chain
	if !*planOnly {
		var cacheCloser *fetchcache.Cache
		chain, cacheCloser = buildChain(false, false)
		defer chain.Close()
		if cacheCloser != nil {
			defer cacheCloser.Close()
		}
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
	switch {
	case *planOnly:
		planMode = pipeline.PlanModeShow
	case *raw:
		planMode = pipeline.PlanModeRaw
	}

	logger := buildLoggerResolved(effective)

	// Pass the fetcher as an untyped nil in plan-only mode so that
	// pipeline's `p.fetcher != nil` check works (a typed *fetch.Chain nil
	// would satisfy the interface and pass the check — classic Go gotcha).
	var fetcher fetch.Fetcher
	if chain != nil {
		fetcher = chain
	}
	// Wire llm.max_tokens from config.yaml through to both pipeline phases.
	// Needed for OpenAI-compatible providers with completion caps below the
	// pipeline default (24576) — e.g., DeepSeek's 8192 ceiling rejects any
	// request above 8192 with "Invalid max_tokens value". Zero preserves the
	// pipeline default (see Config.planMaxTokens / answerMaxTokens in
	// internal/pipeline/pipeline.go).
	var llmMaxTokens int
	if cfg != nil {
		llmMaxTokens = cfg.LLM.MaxTokens
	}
	p := pipeline.New(modules, fetcher, llmClient, scorer, pipeline.Config{
		PlanMode:          planMode,
		MaxSourcesPerType: effective.MaxSourcesPerType,
		MaxFetchedTotal:   effective.MaxFetchedTotal,
		PlanMaxTokens:     llmMaxTokens,
		AnswerMaxTokens:   llmMaxTokens,
	}, logger)

	ctx, cancel := context.WithTimeout(context.Background(), effective.Timeout)
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

	if err := renderSearch(os.Stdout, result, outFormat, *debug, renderOptions{Model: modelName}); err != nil {
		fmt.Fprintf(os.Stderr, "render failed: %v\n", err)
		os.Exit(1)
	}
}

// collectSetFlags walks the FlagSet and returns a map of flag-name →
// true for every flag the user explicitly passed. This is the only
// way to distinguish "flag defaulted" from "flag set to its default
// value" when cascading CLI > env > config precedence.
func collectSetFlags(fs *flag.FlagSet) map[string]bool {
	out := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		out[f.Name] = true
	})
	return out
}

// --- search output (see output.go for format-specific rendering) ------------
