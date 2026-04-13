package main

import (
	"log/slog"
	"strings"
	"time"

	"github.com/odradekk/diting/internal/config"
)

// searchFlags holds the values parsed from the CLI flag set plus a
// record of which flags were explicitly set by the user. The resolver
// uses `setFlags` to decide whether a flag's value came from the user
// or from flag.Parse's built-in default — that's the only way to know
// whether config/env overrides should apply.
//
// All fields map 1:1 to their CLI flag names; documentation for each
// is in the flag.Set* calls in runSearch.
type searchFlags struct {
	// Values
	Provider     string
	Model        string
	Timeout      time.Duration
	Debug        bool
	ScorerConfig string

	// setFlags["name"] == true iff the user explicitly passed the flag.
	setFlags map[string]bool
}

// searchEnv is the process environment the resolver needs. Injected
// as a struct so unit tests can drive the resolver with a fixed
// environment independent of os.Getenv.
type searchEnv struct {
	AnthropicAPIKey string
	AnthropicModel  string
	OpenAIAPIKey    string
	OpenAIModel     string
	OpenAIBaseURL   string
}

// resolvedSearchOptions holds the effective values after cascading
// CLI flag → env var → config file → built-in default. This is the
// only struct runSearch needs once cascading is complete.
type resolvedSearchOptions struct {
	// --- LLM ---
	Provider string // "anthropic" / "openai" / "" (auto-detect)
	Model    string // model id (empty = provider default)
	BaseURL  string // empty = provider default
	APIKey   string // resolved final key (never empty for a usable client)

	// --- Pipeline ---
	Timeout           time.Duration
	MaxSourcesPerType int // 0 = pipeline default (5)
	MaxFetchedTotal   int // 0 = pipeline default (15)

	// --- Modules ---
	EnabledModules []string // empty = use the hardcoded default list in buildSearchModules

	// --- Logging ---
	LogLevel  slog.Level
	LogFormat string // "text" | "json"
}

// resolveSearchOptions cascades CLI flags → env vars → config → defaults.
//
// Precedence rules per setting:
//
//	provider        : CLI flag > env-detected > config.llm.provider > auto-detect
//	model           : CLI flag > ANTHROPIC_MODEL/OPENAI_MODEL env > config.llm.model > provider default
//	base_url        : OPENAI_BASE_URL env > config.llm.base_url
//	api_key         : provider-specific env > config.llm.api_key
//	timeout         : CLI flag > DITING_SEARCH_TIMEOUT env > config.llm.timeout > built-in default
//	enabled modules : config.search.enabled > hardcoded default set (no CLI flag)
//	pipeline limits : config.pipeline.* > built-in defaults (no CLI flags yet)
//	log level       : --debug=true forces Debug; else cfg.logging.level; else Warn
//	log format      : --debug=true forces JSON; else cfg.logging.format; else text
//
// cfg may be nil (no config file loaded) — the function handles that
// case by returning the flag/env/default cascade without any config
// contribution.
func resolveSearchOptions(
	flags searchFlags,
	env searchEnv,
	cfg *config.Config,
	defaults resolvedSearchOptions,
) resolvedSearchOptions {
	out := defaults
	has := func(name string) bool { return flags.setFlags[name] }

	// --- Provider ---
	switch {
	case has("provider") && flags.Provider != "":
		out.Provider = flags.Provider
	case cfg != nil && cfg.LLM.Provider != "":
		out.Provider = cfg.LLM.Provider
	default:
		out.Provider = "" // signals auto-detect
	}

	// --- Model: CLI > env var for the *effective* provider > config ---
	switch {
	case has("model") && flags.Model != "":
		out.Model = flags.Model
	default:
		// Env var depends on which provider is winning. If the
		// resolver hasn't locked in a provider yet (auto-detect),
		// fall through to config, and let buildLLMClient's later
		// re-check pick up the env var at the right moment.
		switch out.Provider {
		case "anthropic":
			if env.AnthropicModel != "" {
				out.Model = env.AnthropicModel
			} else if cfg != nil && cfg.LLM.Model != "" {
				out.Model = cfg.LLM.Model
			}
		case "openai":
			if env.OpenAIModel != "" {
				out.Model = env.OpenAIModel
			} else if cfg != nil && cfg.LLM.Model != "" {
				out.Model = cfg.LLM.Model
			}
		default:
			// Auto-detect: config value still applies if set.
			if cfg != nil && cfg.LLM.Model != "" {
				out.Model = cfg.LLM.Model
			}
		}
	}

	// --- BaseURL: only relevant for openai-compatible endpoints ---
	switch {
	case env.OpenAIBaseURL != "":
		out.BaseURL = env.OpenAIBaseURL
	case cfg != nil && cfg.LLM.BaseURL != "":
		out.BaseURL = cfg.LLM.BaseURL
	}

	// --- APIKey: provider-specific env wins; config is a fallback ---
	// The key from `env` is already the one matching the effective
	// provider (runSearch looks them up both before calling us).
	switch out.Provider {
	case "anthropic":
		if env.AnthropicAPIKey != "" {
			out.APIKey = env.AnthropicAPIKey
		} else if cfg != nil && cfg.LLM.APIKey != "" {
			out.APIKey = cfg.LLM.APIKey
		}
	case "openai":
		if env.OpenAIAPIKey != "" {
			out.APIKey = env.OpenAIAPIKey
		} else if cfg != nil && cfg.LLM.APIKey != "" {
			out.APIKey = cfg.LLM.APIKey
		}
	default:
		// Auto-detect: prefer whichever env var is set, else config.
		switch {
		case env.AnthropicAPIKey != "":
			out.Provider = "anthropic"
			out.APIKey = env.AnthropicAPIKey
		case env.OpenAIAPIKey != "":
			out.Provider = "openai"
			out.APIKey = env.OpenAIAPIKey
		case cfg != nil && cfg.LLM.APIKey != "":
			// Config has a key but no provider → use config provider
			// (already set above) or leave unresolved for caller.
			out.APIKey = cfg.LLM.APIKey
		}
	}

	// --- Timeout ---
	switch {
	case has("timeout"):
		out.Timeout = flags.Timeout
	case cfg != nil && cfg.LLM.Timeout > 0:
		out.Timeout = cfg.LLM.Timeout
	}
	// else: out.Timeout stays at defaults.Timeout (already set)

	// --- Enabled search modules ---
	if cfg != nil && len(cfg.Search.Enabled) > 0 {
		out.EnabledModules = append([]string(nil), cfg.Search.Enabled...)
	}

	// --- Pipeline limits ---
	if cfg != nil {
		if cfg.Pipeline.MaxSourcesPerType > 0 {
			out.MaxSourcesPerType = cfg.Pipeline.MaxSourcesPerType
		}
		if cfg.Pipeline.MaxFetchedTotal > 0 {
			out.MaxFetchedTotal = cfg.Pipeline.MaxFetchedTotal
		}
	}

	// --- Logging ---
	// --debug=true always forces Debug level + JSON format (Phase 4.4
	// contract). Otherwise, let the config decide, falling back to the
	// defaults-struct values.
	if flags.Debug {
		out.LogLevel = slog.LevelDebug
		out.LogFormat = "json"
	} else if cfg != nil {
		if lvl, ok := parseLogLevel(cfg.Logging.Level); ok {
			out.LogLevel = lvl
		}
		if f := strings.ToLower(cfg.Logging.Format); f == "text" || f == "json" {
			out.LogFormat = f
		}
	}

	return out
}

// parseLogLevel maps "debug"/"info"/"warn"/"error" (case-insensitive)
// to the corresponding slog.Level. Returns (_, false) for any other
// value so the caller falls back to the default.
func parseLogLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}
