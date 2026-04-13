package config

import (
	"fmt"
	"strings"
)

// validProviders is the set of LLM provider names the CLI accepts.
// MiniMax and other OpenAI-compatible providers use provider=openai with
// a custom base_url, so they are not listed here.
var validProviders = map[string]bool{
	"":          true, // allow unset — auto-detect from env at runtime
	"anthropic": true,
	"openai":    true,
}

// validLogLevels is the set of log-level strings accepted in logging.level.
var validLogLevels = map[string]bool{
	"":      true,
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// validLogFormats is the set of log-format strings accepted in logging.format.
var validLogFormats = map[string]bool{
	"":     true,
	"text": true,
	"json": true,
}

// ValidateOptions controls optional checks performed by Validate.
type ValidateOptions struct {
	// KnownModules is the set of search module names that should be
	// accepted in search.enabled. Typically this is the output of
	// search.List(). If empty, the check is skipped.
	KnownModules []string

	// KnownFetchLayers is the set of fetch layer names accepted in
	// fetch.layers. If empty, the check is skipped.
	KnownFetchLayers []string
}

// Validate performs structural validation on the config. It returns a
// joined error containing every problem found so that `diting config
// validate` can print all issues at once rather than forcing the user
// into a fix-and-retry loop.
func (c *Config) Validate(opts ValidateOptions) error {
	var errs []string

	// LLM
	if !validProviders[strings.ToLower(c.LLM.Provider)] {
		errs = append(errs, fmt.Sprintf("llm.provider %q: not one of anthropic|openai", c.LLM.Provider))
	}
	if c.LLM.Timeout < 0 {
		errs = append(errs, fmt.Sprintf("llm.timeout %v: must be non-negative", c.LLM.Timeout))
	}
	if c.LLM.MaxTokens < 0 {
		errs = append(errs, fmt.Sprintf("llm.max_tokens %d: must be non-negative", c.LLM.MaxTokens))
	}

	// Search
	if len(opts.KnownModules) > 0 {
		known := stringSet(opts.KnownModules)
		for _, m := range c.Search.Enabled {
			if !known[m] {
				errs = append(errs, fmt.Sprintf("search.enabled: unknown module %q (known: %s)",
					m, strings.Join(opts.KnownModules, ", ")))
			}
		}
		for name := range c.Search.Modules {
			if !known[name] {
				errs = append(errs, fmt.Sprintf("search.modules: unknown module %q", name))
			}
		}
	}
	for name, mc := range c.Search.Modules {
		if mc.Timeout < 0 {
			errs = append(errs, fmt.Sprintf("search.modules.%s.timeout %v: must be non-negative", name, mc.Timeout))
		}
		if mc.MaxResults < 0 {
			errs = append(errs, fmt.Sprintf("search.modules.%s.max_results %d: must be non-negative", name, mc.MaxResults))
		}
	}

	// Fetch
	if len(opts.KnownFetchLayers) > 0 && len(c.Fetch.Layers) > 0 {
		known := stringSet(opts.KnownFetchLayers)
		for _, l := range c.Fetch.Layers {
			if !known[l] {
				errs = append(errs, fmt.Sprintf("fetch.layers: unknown layer %q (known: %s)",
					l, strings.Join(opts.KnownFetchLayers, ", ")))
			}
		}
	}
	if c.Fetch.Cache.MaxMB < 0 {
		errs = append(errs, fmt.Sprintf("fetch.cache.max_mb %d: must be non-negative", c.Fetch.Cache.MaxMB))
	}
	if c.Fetch.Cache.DefaultTTLDays < 0 {
		errs = append(errs, fmt.Sprintf("fetch.cache.default_ttl_days %d: must be non-negative", c.Fetch.Cache.DefaultTTLDays))
	}

	// Pipeline
	if c.Pipeline.MaxSourcesPerType < 0 {
		errs = append(errs, fmt.Sprintf("pipeline.max_sources_per_type %d: must be non-negative", c.Pipeline.MaxSourcesPerType))
	}
	if c.Pipeline.MaxFetchedTotal < 0 {
		errs = append(errs, fmt.Sprintf("pipeline.max_fetched_total %d: must be non-negative", c.Pipeline.MaxFetchedTotal))
	}
	if c.Pipeline.FetchTimeout < 0 {
		errs = append(errs, fmt.Sprintf("pipeline.fetch_timeout %v: must be non-negative", c.Pipeline.FetchTimeout))
	}

	// Scoring weights in [0, 1]
	w := c.Scoring.Weights
	checkWeight := func(name string, v float64) {
		if v < 0 || v > 1 {
			errs = append(errs, fmt.Sprintf("scoring.weights.%s %v: must be in [0, 1]", name, v))
		}
	}
	checkWeight("domain_authority", w.DomainAuthority)
	checkWeight("keyword_overlap", w.KeywordOverlap)
	checkWeight("snippet_quality", w.SnippetQuality)
	checkWeight("language_match", w.LanguageMatch)

	// Logging
	if !validLogLevels[strings.ToLower(c.Logging.Level)] {
		errs = append(errs, fmt.Sprintf("logging.level %q: not one of debug|info|warn|error", c.Logging.Level))
	}
	if !validLogFormats[strings.ToLower(c.Logging.Format)] {
		errs = append(errs, fmt.Sprintf("logging.format %q: not one of text|json", c.Logging.Format))
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("config: %d validation error(s):\n  - %s",
		len(errs), strings.Join(errs, "\n  - "))
}

func stringSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

// --- redaction --------------------------------------------------------------

// Redact returns a deep clone of the config with every sensitive field
// replaced by a placeholder. Use this before printing config to the user
// so that `diting config show` never leaks API keys.
//
// Masking rules:
//   - A non-empty secret (including an unresolved `${VAR}`) becomes "<set>".
//   - An empty secret becomes "<not set>".
//
// The receiver itself is not modified.
func (c *Config) Redact() *Config {
	clone := *c // shallow copy — fine because all nested structs are also copied by value

	clone.LLM.APIKey = maskSecret(c.LLM.APIKey)
	clone.Fetch.Jina.APIKey = maskSecret(c.Fetch.Jina.APIKey)
	clone.Fetch.Tavily.APIKey = maskSecret(c.Fetch.Tavily.APIKey)

	// Deep-copy the modules map and mask each secret field.
	if c.Search.Modules != nil {
		clone.Search.Modules = make(map[string]SearchModuleConfig, len(c.Search.Modules))
		for name, mc := range c.Search.Modules {
			mc.APIKey = maskSecret(mc.APIKey)
			mc.Token = maskSecret(mc.Token)
			clone.Search.Modules[name] = mc
		}
	}

	// Deep-copy slices so callers can't mutate the clone back into the
	// original. Scalars are already independent from the shallow copy.
	if len(c.Search.Enabled) > 0 {
		clone.Search.Enabled = append([]string(nil), c.Search.Enabled...)
	}
	if len(c.Fetch.Layers) > 0 {
		clone.Fetch.Layers = append([]string(nil), c.Fetch.Layers...)
	}

	return &clone
}

// maskSecret turns a secret string into a safe-to-print placeholder.
func maskSecret(s string) string {
	if s == "" {
		return "<not set>"
	}
	return "<set>"
}
