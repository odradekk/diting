// Package v2shared holds build helpers + pipeline→bench conversion
// logic reused by the v2-single and v2-raw variants.
//
// This logic deliberately lives under internal/bench/variants/ rather
// than in cmd/diting, because internal/bench/variants packages are
// library code (importable by tests, variant packages, and anything
// else in-tree) whereas cmd/diting is a main package and can't be
// imported. The trade-off is a small amount of duplication with
// cmd/diting/wire.go — kept minimal and tested independently.
//
// The conversion helper (ConvertPipelineResult) is the trickiest
// piece: pipeline.Result and bench.Result have overlapping but
// non-isomorphic shapes, and two different variant modes (full
// answer vs raw) produce citations from different sources. Keeping
// all of that in one function with a single test suite is clearer
// than scattering it across variants.
package v2shared

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/fetch/archive"
	fetchcache "github.com/odradekk/diting/internal/fetch/cache"
	cdp "github.com/odradekk/diting/internal/fetch/chromedp"
	"github.com/odradekk/diting/internal/fetch/extract"
	"github.com/odradekk/diting/internal/fetch/jina"
	"github.com/odradekk/diting/internal/fetch/tavily"
	"github.com/odradekk/diting/internal/fetch/utls"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/pricing"
	"github.com/odradekk/diting/internal/search"
)

// --- LLM construction -------------------------------------------------------

// LLMHandle holds a resolved LLM client plus the model name and
// provider name it was constructed from. Returned by BuildLLMFromEnv
// so callers can pass the model name to the pricing layer for
// per-run cost accounting.
type LLMHandle struct {
	Client   llm.Client
	Provider string
	Model    string
}

// BuildLLMFromEnv constructs an LLM client using the same env var
// cascade as runSearch (anthropic → openai auto-detect). Returns an
// error when no provider is configured, so benchmark variants that
// need an LLM fail their factory cleanly rather than silently
// producing empty results.
//
// Environment variable precedence:
//
//	ANTHROPIC_API_KEY, ANTHROPIC_MODEL
//	OPENAI_API_KEY, OPENAI_BASE_URL, OPENAI_MODEL
//
// Matches cmd/diting/main.go's buildLLMClient behaviour. Must stay in
// sync with the CLI when env var handling changes.
func BuildLLMFromEnv() (*LLMHandle, error) {
	candidates := []struct {
		name     string
		envKey   string
		envModel string
	}{
		{"anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"},
		{"openai", "OPENAI_API_KEY", "OPENAI_MODEL"},
	}

	for _, c := range candidates {
		key := os.Getenv(c.envKey)
		if key == "" {
			continue
		}
		factory, err := llm.Get(c.name)
		if err != nil {
			continue
		}
		model := os.Getenv(c.envModel)
		cfg := llm.ProviderConfig{APIKey: key, Model: model}
		if c.name == "openai" {
			cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
		client, err := factory(cfg)
		if err != nil {
			continue
		}
		return &LLMHandle{Client: client, Provider: c.name, Model: model}, nil
	}

	return nil, fmt.Errorf("no LLM provider configured (set ANTHROPIC_API_KEY or OPENAI_API_KEY)")
}

// --- Search modules ---------------------------------------------------------

// BuildSearchModules instantiates every registered search module
// whose env-var prerequisites are satisfied. Modules that require
// a BYOK env var (brave, serp, github) are silently skipped when
// the key is missing — matching runSearch's behaviour.
//
// Returns the module list and a closer function that closes every
// module. The caller is responsible for calling the closer when
// the variant is done.
func BuildSearchModules() ([]search.Module, func()) {
	type modSpec struct {
		name   string
		apiEnv string
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
			continue
		}
		factory, err := search.Get(s.name)
		if err != nil {
			continue
		}
		cfg := search.ModuleConfig{APIKey: os.Getenv(s.apiEnv)}
		if s.name == "github" {
			cfg.APIKey = os.Getenv("GITHUB_TOKEN")
		}
		m, err := factory(cfg)
		if err != nil {
			continue
		}
		modules = append(modules, m)
	}

	closer := func() {
		for _, m := range modules {
			_ = m.Close()
		}
	}
	return modules, closer
}

// --- Fetch chain ------------------------------------------------------------

// FetchChainHandle wraps a *fetch.Chain with its owned cache handle
// so callers get a single close function that tears down everything.
type FetchChainHandle struct {
	Chain *fetch.Chain
	cache *fetchcache.Cache
}

// Close releases the chain and its underlying cache (if any).
func (h *FetchChainHandle) Close() {
	if h == nil {
		return
	}
	if h.Chain != nil {
		_ = h.Chain.Close()
	}
	if h.cache != nil {
		_ = h.cache.Close()
	}
}

// BuildFetchChain constructs the canonical diting fetch chain:
// utls → chromedp (if available) → jina → archive → tavily (if key).
// Matches cmd/diting/main.go's buildChain — MUST stay in sync when
// the default chain evolves.
//
// Returns a non-nil handle whose Chain field is usable by pipeline.New.
func BuildFetchChain() (*FetchChainHandle, error) {
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

	opts := []fetch.ChainOption{fetch.WithExtractor(extract.New(extract.Options{}))}

	cache, err := fetchcache.Open(fetchcache.Options{})
	if err == nil {
		opts = append(opts, fetch.WithCache(cache))
	}

	return &FetchChainHandle{
		Chain: fetch.NewChain(layers, opts...),
		cache: cache,
	}, nil
}

// --- Silent logger ----------------------------------------------------------

// SilentLogger returns a slog.Logger that drops every event to
// io.Discard. Benchmark variants use this by default — the runner's
// own logger is what shows up in the markdown report, and extra
// pipeline chatter just clutters stderr during a 50-query run.
func SilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Result conversion ------------------------------------------------------

// ErrorResult builds a bench.Result for a failed pipeline run. It
// extracts any partial DebugInfo from a pipeline.PipelineError so the
// caller can see how far the query got (plan phase reached? search
// returned any results? fetch layer populated?) and what the token
// usage was before the failure. The full error string and the phase
// label are also written into Metadata for diagnostic visibility.
//
// Use this instead of a minimal `{QueryID, Latency, error}` result so
// post-Round-1 benchmark runs produce JSON dumps that can be analyzed
// without re-running the failing queries.
func ErrorResult(queryID string, err error, latency time.Duration, model string) bench.Result {
	meta := map[string]any{"error": err.Error()}

	// Best-effort extraction of PipelineError with its partial Debug snapshot.
	var pe *pipeline.PipelineError
	if errors.As(err, &pe) {
		meta["error_phase"] = pe.Phase
		d := pe.Debug
		if d.PlanInputTokens > 0 {
			meta["plan_input_tokens"] = d.PlanInputTokens
		}
		if d.PlanOutputTokens > 0 {
			meta["plan_output_tokens"] = d.PlanOutputTokens
		}
		if d.PlanCacheReadTokens > 0 {
			meta["plan_cache_read_tokens"] = d.PlanCacheReadTokens
		}
		if d.TotalSearchResults > 0 {
			meta["total_results"] = d.TotalSearchResults
		}
		if d.SelectedSources > 0 {
			meta["selected_sources"] = d.SelectedSources
		}
		if d.FetchedSources > 0 {
			meta["fetched_sources"] = d.FetchedSources
		}
		// Cost of the plan phase — non-zero even on failure, since the
		// plan LLM call did burn tokens before the failure. computeCost
		// tolerates zero answer tokens just fine.
		if cost := computeCost(model, d); cost > 0 {
			return bench.Result{
				QueryID:  queryID,
				Latency:  latency,
				Cost:     cost,
				Metadata: meta,
			}
		}
	}

	return bench.Result{
		QueryID:  queryID,
		Latency:  latency,
		Metadata: meta,
	}
}

// ConvertPipelineResult translates a pipeline.Result into a
// bench.Result. The function handles both full-answer and raw modes:
//
//   - modeFull  (Answer.Text != ""): citations come from
//     result.Answer.Citations (post-LLM synthesis)
//   - modeRaw   (Sources > 0 but no answer): citations come from
//     result.Sources (pre-answer, one entry per fetched source)
//
// latency is measured by the caller (time.Since around pipeline.Run).
// model is used to compute cost via the pricing table; pass "" for a
// best-effort fallback to the default price.
func ConvertPipelineResult(queryID string, r *pipeline.Result, latency time.Duration, model string) bench.Result {
	out := bench.Result{
		QueryID: queryID,
		Answer:  r.Answer.Text,
		Latency: latency,
		Cost:    computeCost(model, r.Debug),
		Metadata: map[string]any{
			"plan_queries":       r.Plan.TotalQueries(),
			"plan_input_tokens":  r.Debug.PlanInputTokens,
			"plan_output_tokens": r.Debug.PlanOutputTokens,
			"total_results":      r.Debug.TotalSearchResults,
			"selected_sources":   r.Debug.SelectedSources,
			"fetched_sources":    r.Debug.FetchedSources,
		},
	}
	if r.Debug.AnswerInputTokens > 0 {
		out.Metadata["answer_input_tokens"] = r.Debug.AnswerInputTokens
		out.Metadata["answer_output_tokens"] = r.Debug.AnswerOutputTokens
	}
	if r.Answer.Confidence != "" {
		out.Metadata["confidence"] = r.Answer.Confidence
	}

	// Citations: prefer Answer.Citations (set by the answer phase), fall
	// back to building from Sources (raw mode).
	if len(r.Answer.Citations) > 0 {
		out.Citations = make([]bench.Citation, 0, len(r.Answer.Citations))
		for _, c := range r.Answer.Citations {
			out.Citations = append(out.Citations, bench.Citation{
				URL:        c.URL,
				Domain:     extractDomain(c.URL),
				SourceType: bench.SourceType(string(c.SourceType)),
				Rank:       c.ID,
			})
		}
	} else if len(r.Sources) > 0 {
		out.Citations = make([]bench.Citation, 0, len(r.Sources))
		for _, s := range r.Sources {
			out.Citations = append(out.Citations, bench.Citation{
				URL:        s.Result.URL,
				Domain:     extractDomain(s.Result.URL),
				SourceType: bench.SourceType(string(s.Result.SourceType)),
				Rank:       s.ID,
			})
		}
	}

	return out
}

// computeCost wraps pricing.Lookup + ComputeCost for both phases,
// summing into a single per-query cost. An empty or unknown model
// falls back to pricing.DefaultPrice (conservatively high).
func computeCost(model string, d pipeline.DebugInfo) float64 {
	price, _ := pricing.Lookup(model)
	plan := pricing.ComputeCost(price, d.PlanInputTokens, d.PlanOutputTokens, d.PlanCacheReadTokens)
	answer := pricing.ComputeCost(price, d.AnswerInputTokens, d.AnswerOutputTokens, d.AnswerCacheReadTokens)
	return plan + answer
}

// extractDomain parses a URL and returns its host, lowercased, with
// any leading "www." stripped. Returns the raw URL on parse failure —
// we don't want benchmark runs to panic over a malformed URL from a
// search provider.
func extractDomain(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := u.Host
	// Strip "www." prefix for consistency with benchmark scoring,
	// which treats www.example.com and example.com as the same domain.
	for _, prefix := range []string{"www."} {
		if len(host) > len(prefix) && host[:len(prefix)] == prefix {
			host = host[len(prefix):]
		}
	}
	return host
}
