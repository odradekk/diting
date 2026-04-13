// Package config implements the diting configuration schema.
//
// The config file is a YAML document at ~/.config/diting/config.yaml
// (overridable via $DITING_CONFIG or --config). Its schema is
// documented in docs/architecture.md §9.
//
// Phase 4.5 adds only the `diting config show|path|validate` subcommand —
// the pipeline doesn't yet read the loaded Config struct. Phase 4.9 will
// wire the `--config` flag into runSearch.
//
// Design notes:
//
//   - BYOK: the YAML supports `${VAR}` env var interpolation via os.Expand,
//     so users can keep secrets in their shell env rather than in the file.
//     Unset variables resolve to empty strings (Validate flags that).
//
//   - Redact(): returns a deep clone with sensitive fields ("api_key",
//     "token") masked so that `diting config show` never leaks keys. The
//     mask values are "<set>" (a value was present) and "<not set>" (the
//     field was empty or missing).
//
//   - Default(): returns the built-in defaults that match what runSearch
//     currently hardcodes — matching the architecture defaults so that
//     `show` without a config file still prints something useful.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level diting configuration.
type Config struct {
	LLM      LLMConfig      `yaml:"llm"`
	Search   SearchConfig   `yaml:"search"`
	Fetch    FetchConfig    `yaml:"fetch"`
	Pipeline PipelineConfig `yaml:"pipeline"`
	Scoring  ScoringConfig  `yaml:"scoring"`
	Logging  LoggingConfig  `yaml:"logging"`
}

// LLMConfig configures the LLM provider used by the plan and answer phases.
type LLMConfig struct {
	Provider  string        `yaml:"provider"`             // anthropic | openai
	BaseURL   string        `yaml:"base_url,omitempty"`   // OpenAI-compatible override
	Model     string        `yaml:"model,omitempty"`      // model id
	APIKey    string        `yaml:"api_key,omitempty"`    // ${VAR} or literal
	Timeout   time.Duration `yaml:"timeout,omitempty"`
	MaxTokens int           `yaml:"max_tokens,omitempty"`
}

// SearchConfig lists enabled search modules and per-module overrides.
type SearchConfig struct {
	Enabled []string                       `yaml:"enabled"`
	Modules map[string]SearchModuleConfig  `yaml:"modules,omitempty"`
}

// SearchModuleConfig holds per-module overrides. All fields are optional.
type SearchModuleConfig struct {
	APIKey     string        `yaml:"api_key,omitempty"`
	Token      string        `yaml:"token,omitempty"`
	Timeout    time.Duration `yaml:"timeout,omitempty"`
	MaxResults int           `yaml:"max_results,omitempty"`
}

// FetchConfig configures the fetch chain and its layers.
type FetchConfig struct {
	Layers   []string         `yaml:"layers"`
	Jina     FetchLayerConfig `yaml:"jina,omitempty"`
	Tavily   FetchLayerConfig `yaml:"tavily,omitempty"`
	ChromeDP ChromeDPConfig   `yaml:"chromedp,omitempty"`
	Cache    CacheConfig      `yaml:"cache"`
}

// FetchLayerConfig is the per-layer config shared by API-key-based layers.
type FetchLayerConfig struct {
	APIKey string `yaml:"api_key,omitempty"`
}

// ChromeDPConfig configures the chromedp fetch layer.
type ChromeDPConfig struct {
	Headless    bool   `yaml:"headless"`
	UserDataDir string `yaml:"user_data_dir,omitempty"`
}

// CacheConfig configures the fetch content cache.
type CacheConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Path           string `yaml:"path,omitempty"`
	MaxMB          int    `yaml:"max_mb,omitempty"`
	DefaultTTLDays int    `yaml:"default_ttl_days,omitempty"`
}

// PipelineConfig configures the plan → search → fetch → answer pipeline.
type PipelineConfig struct {
	MaxSourcesPerType int           `yaml:"max_sources_per_type"`
	MaxFetchedTotal   int           `yaml:"max_fetched_total"`
	FetchTimeout      time.Duration `yaml:"fetch_timeout,omitempty"`
}

// ScoringConfig configures the heuristic scorer weights. The detailed
// threshold tables live in scorer_config.yaml — this section only holds
// the top-level weights for documentation/override purposes.
type ScoringConfig struct {
	Weights ScoringWeights `yaml:"weights"`
}

// ScoringWeights are the signal weights for the heuristic scorer.
type ScoringWeights struct {
	DomainAuthority float64 `yaml:"domain_authority"`
	KeywordOverlap  float64 `yaml:"keyword_overlap"`
	SnippetQuality  float64 `yaml:"snippet_quality"`
	LanguageMatch   float64 `yaml:"language_match"`
}

// LoggingConfig configures slog output.
type LoggingConfig struct {
	Level  string `yaml:"level"`            // debug | info | warn | error
	Format string `yaml:"format"`           // text | json
	File   string `yaml:"file,omitempty"`   // empty = stderr
}

// --- loading ----------------------------------------------------------------

// Load reads, interpolates env vars, and parses a YAML config file. It does
// NOT validate — call Validate() on the returned config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	// Expand ${VAR} and $VAR references. Unset variables become empty
	// strings — Validate() is responsible for flagging those.
	expanded := os.Expand(string(data), os.Getenv)

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return &cfg, nil
}

// DefaultPath resolves the default config file path. Priority:
//
//  1. $DITING_CONFIG environment variable
//  2. $XDG_CONFIG_HOME/diting/config.yaml (via os.UserConfigDir)
//  3. ~/.config/diting/config.yaml fallback
func DefaultPath() (string, error) {
	if p := os.Getenv("DITING_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "diting", "config.yaml"), nil
}

// Default returns the built-in default configuration. This matches the
// values currently hardcoded in runSearch so that `diting config show`
// without a config file still prints meaningful output.
func Default() *Config {
	return &Config{
		LLM: LLMConfig{
			Provider:  "anthropic",
			Timeout:   120 * time.Second,
			MaxTokens: 16384,
		},
		Search: SearchConfig{
			Enabled: []string{
				"bing", "duckduckgo", "baidu",
				"arxiv", "github", "stackexchange",
			},
		},
		Fetch: FetchConfig{
			Layers: []string{"utls", "chromedp", "jina", "archive"},
			ChromeDP: ChromeDPConfig{
				Headless: true,
			},
			Cache: CacheConfig{
				Enabled:        true,
				MaxMB:          500,
				DefaultTTLDays: 3,
			},
		},
		Pipeline: PipelineConfig{
			MaxSourcesPerType: 5,
			MaxFetchedTotal:   15,
			FetchTimeout:      5 * time.Minute,
		},
		Scoring: ScoringConfig{
			Weights: ScoringWeights{
				DomainAuthority: 0.4,
				KeywordOverlap:  0.4,
				SnippetQuality:  0.2,
				LanguageMatch:   0.0,
			},
		},
		Logging: LoggingConfig{
			Level:  "warn",
			Format: "text",
		},
	}
}

// Marshal renders the config as YAML. Used by `diting config show`.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}
