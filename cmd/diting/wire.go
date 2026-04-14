package main

import (
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/odradekk/diting/internal/config"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// loadSearchConfig resolves a config file path and loads it. The
// semantics depend on whether the user passed `--config` explicitly:
//
//   - If `explicitPath` is non-empty, that path MUST exist and parse
//     cleanly. Any error is surfaced as-is.
//
//   - If `explicitPath` is empty, try `config.DefaultPath()` silently.
//     A missing file at the default path returns (nil, path, nil) —
//     the user hasn't created a config yet and is relying on env vars
//     and flags, which is a perfectly valid workflow.
//
// The returned path is the resolved file location regardless of whether
// it exists; runSearch uses it for the "loaded from X" stderr note.
func loadSearchConfig(explicitPath string) (*config.Config, string, error) {
	if explicitPath != "" {
		cfg, err := config.Load(explicitPath)
		if err != nil {
			return nil, explicitPath, err
		}
		return cfg, explicitPath, nil
	}

	defaultPath, err := config.DefaultPath()
	if err != nil {
		// DefaultPath failure is exotic (no HOME, broken OS). Silently
		// fall back to no-config rather than blocking all searches.
		return nil, "", nil
	}

	if _, err := os.Stat(defaultPath); err != nil {
		if os.IsNotExist(err) {
			return nil, defaultPath, nil
		}
		return nil, defaultPath, nil // any other stat error → silent no-op
	}

	cfg, err := config.Load(defaultPath)
	if err != nil {
		// A broken config at the default path would silently disable
		// search — surface the error so the user knows to fix it.
		return nil, defaultPath, fmt.Errorf("loading default config at %s: %w", defaultPath, err)
	}
	return cfg, defaultPath, nil
}

// buildLLMClientResolved constructs the LLM client from the already-
// resolved options. Unlike the old buildLLMClient, this function does
// NOT re-read the environment — resolveSearchOptions has already
// applied the full cascade.
//
// Returns (client, providerName, modelName). Any nil client means the
// caller should print an error and exit.
func buildLLMClientResolved(opts resolvedSearchOptions) (llm.Client, string, string) {
	// If the resolver couldn't pin down a provider, auto-detect based
	// on which API key is set. This mirrors the old buildLLMClient
	// behaviour for the common env-only case.
	provider := opts.Provider
	if provider == "" {
		return nil, "", ""
	}
	if opts.APIKey == "" {
		return nil, "", ""
	}

	factory, err := llm.Get(provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, "", ""
	}

	cfg := llm.ProviderConfig{
		APIKey: opts.APIKey,
		Model:  opts.Model,
	}
	if provider == "openai" {
		cfg.BaseURL = opts.BaseURL
	}

	client, err := factory(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, "", ""
	}
	return client, provider, opts.Model
}

// buildSearchModulesFiltered is like buildSearchModules but filters
// the constructed modules against an "enabled" allow-list. An empty
// allow-list means "use every available module" — preserving the
// legacy behaviour when no config file is in effect.
//
// An enabled module name that doesn't correspond to a registered
// module is silently skipped (the validator in `diting config
// validate` is the intended place to warn about unknown names).
func buildSearchModulesFiltered(enabled []string) []search.Module {
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
		{"exa", "EXA_API_KEY"},
		{"metaso", "METASO_API_KEY"},
		{"serp", "SERP_API_KEY"},
	}

	useAll := len(enabled) == 0

	var modules []search.Module
	for _, s := range specs {
		if !useAll && !slices.Contains(enabled, s.name) {
			continue
		}
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

// buildLoggerResolved returns a slog.Logger configured from the
// resolved options. `--debug` is already baked into opts.LogLevel /
// opts.LogFormat by resolveSearchOptions.
func buildLoggerResolved(opts resolvedSearchOptions) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{Level: opts.LogLevel}
	switch opts.LogFormat {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, handlerOpts))
	default:
		return slog.New(slog.NewTextHandler(os.Stderr, handlerOpts))
	}
}
