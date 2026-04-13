// Package v0baseline implements the "v0-baseline" bench variant.
//
// This is the simplest possible baseline: query Bing for each question,
// keep the top 3 results as citations, produce no synthesized answer.
// It establishes a floor for the composite score — any improvement
// over v0-baseline is evidence that the plan/execute/fetch/answer
// pipeline adds real value.
//
// No LLM is consumed, so this variant is safe to run in any
// environment (no API keys required beyond what Bing itself needs,
// which for us is nothing — the bing module uses utls TLS
// fingerprinting to scrape bing.com directly).
package v0baseline

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/bench/variants"
	"github.com/odradekk/diting/internal/search"
)

// Name is the registry key used by `diting bench run --variant v0-baseline`.
const Name = "v0-baseline"

// topK is the number of Bing results to keep as citations. Matches
// the "top 3 snippets" definition in architecture.md §5.
const topK = 3

func init() {
	variants.Register(Name, New)
}

// variant is the v0-baseline implementation. Kept unexported so
// tests in this same package can construct it directly with mocks
// without needing a factory indirection.
type variant struct {
	module search.Module
}

// New builds a v0-baseline variant backed by the real Bing module
// from the search registry. Returns an error if Bing is not
// registered — that would indicate a wiring bug (search/bing is
// blank-imported by the CLI binary).
func New() (bench.Variant, error) {
	factory, err := search.Get("bing")
	if err != nil {
		return nil, err
	}
	module, err := factory(search.ModuleConfig{})
	if err != nil {
		return nil, err
	}
	return &variant{module: module}, nil
}

// Name returns the registry key.
func (v *variant) Name() string { return Name }

// Run executes one query against Bing and maps the top results to
// a bench.Result with no Answer. Errors from the module (network
// failure, blocked query, etc.) are captured in Metadata["error"]
// rather than propagated — one bad query shouldn't fail the whole
// 50-query run.
func (v *variant) Run(ctx context.Context, in bench.RunInput) (bench.Result, error) {
	start := time.Now()
	results, err := v.module.Search(ctx, in.Query)
	latency := time.Since(start)

	out := bench.Result{
		QueryID:  in.ID,
		Latency:  latency,
		Cost:     0, // no LLM, no cost
		Metadata: map[string]any{},
	}

	if err != nil {
		out.Metadata["error"] = err.Error()
		return out, nil
	}

	if len(results) > topK {
		results = results[:topK]
	}

	citations := make([]bench.Citation, 0, len(results))
	for i, r := range results {
		citations = append(citations, bench.Citation{
			URL:        r.URL,
			Domain:     extractDomain(r.URL),
			SourceType: bench.SourceGeneralWeb, // bing is always general-web
			Rank:       i + 1,
		})
	}
	out.Citations = citations
	out.Metadata["raw_results"] = len(results)
	return out, nil
}

// extractDomain pulls the host from a URL and strips a leading "www.".
// Returns the original string if parsing fails — benchmark runs
// should never panic on a malformed URL from a search provider.
func extractDomain(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := strings.ToLower(u.Host)
	return strings.TrimPrefix(host, "www.")
}
