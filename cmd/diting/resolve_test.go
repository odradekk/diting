package main

import (
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/odradekk/diting/internal/config"
)

// defaultResolved mirrors the built-in defaults runSearch uses when
// nothing is explicitly set. Keep in sync with the values passed to
// resolveSearchOptions in runSearch.
func defaultResolved() resolvedSearchOptions {
	return resolvedSearchOptions{
		Timeout:   5 * time.Minute,
		LogLevel:  slog.LevelWarn,
		LogFormat: "text",
	}
}

// --- basic cases ------------------------------------------------------------

func TestResolve_AllDefaults(t *testing.T) {
	out := resolveSearchOptions(
		searchFlags{},
		searchEnv{},
		nil,
		defaultResolved(),
	)
	if out.Timeout != 5*time.Minute {
		t.Errorf("timeout = %v, want 5m", out.Timeout)
	}
	if out.LogLevel != slog.LevelWarn {
		t.Errorf("log level = %v, want Warn", out.LogLevel)
	}
	if out.LogFormat != "text" {
		t.Errorf("log format = %q, want text", out.LogFormat)
	}
	if out.Provider != "" {
		t.Errorf("provider should be empty for auto-detect, got %q", out.Provider)
	}
}

// --- config-only overrides (no env, no flags) ------------------------------

func TestResolve_ConfigOnly(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "openai",
			Model:    "gpt-4.1-mini",
			BaseURL:  "https://api.minimaxi.com/v1",
			APIKey:   "sk-from-config",
			Timeout:  90 * time.Second,
		},
		Search: config.SearchConfig{
			Enabled: []string{"bing", "arxiv"},
		},
		Pipeline: config.PipelineConfig{
			MaxSourcesPerType: 3,
			MaxFetchedTotal:   10,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, defaultResolved())

	if out.Provider != "openai" {
		t.Errorf("provider = %q, want openai", out.Provider)
	}
	if out.Model != "gpt-4.1-mini" {
		t.Errorf("model = %q", out.Model)
	}
	if out.BaseURL != "https://api.minimaxi.com/v1" {
		t.Errorf("base_url = %q", out.BaseURL)
	}
	if out.APIKey != "sk-from-config" {
		t.Errorf("api_key = %q", out.APIKey)
	}
	if out.Timeout != 90*time.Second {
		t.Errorf("timeout = %v", out.Timeout)
	}
	if !reflect.DeepEqual(out.EnabledModules, []string{"bing", "arxiv"}) {
		t.Errorf("enabled = %v", out.EnabledModules)
	}
	if out.MaxSourcesPerType != 3 {
		t.Errorf("max_sources_per_type = %d", out.MaxSourcesPerType)
	}
	if out.MaxFetchedTotal != 10 {
		t.Errorf("max_fetched_total = %d", out.MaxFetchedTotal)
	}
	if out.LogLevel != slog.LevelInfo {
		t.Errorf("log level = %v, want Info", out.LogLevel)
	}
	if out.LogFormat != "json" {
		t.Errorf("log format = %q, want json", out.LogFormat)
	}
}

// --- env-over-config precedence --------------------------------------------

func TestResolve_EnvBeatsConfig(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4",
			APIKey:   "sk-from-config",
		},
	}
	env := searchEnv{
		AnthropicAPIKey: "sk-from-env",
		AnthropicModel:  "claude-opus-4",
	}
	out := resolveSearchOptions(searchFlags{}, env, cfg, defaultResolved())

	if out.Provider != "anthropic" {
		t.Errorf("provider = %q", out.Provider)
	}
	if out.APIKey != "sk-from-env" {
		t.Errorf("api_key = %q, want env value", out.APIKey)
	}
	if out.Model != "claude-opus-4" {
		t.Errorf("model = %q, want env value", out.Model)
	}
}

func TestResolve_EnvBaseURLBeatsConfig(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{BaseURL: "https://config.example.com/v1"},
	}
	env := searchEnv{OpenAIBaseURL: "https://env.example.com/v1"}
	out := resolveSearchOptions(searchFlags{}, env, cfg, defaultResolved())
	if out.BaseURL != "https://env.example.com/v1" {
		t.Errorf("base_url = %q, env should win", out.BaseURL)
	}
}

// --- flag-over-env precedence ----------------------------------------------

func TestResolve_FlagBeatsEverything(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4",
			Timeout:  90 * time.Second,
		},
	}
	env := searchEnv{
		AnthropicAPIKey: "sk-env-key",
		AnthropicModel:  "claude-opus-4",
	}
	flags := searchFlags{
		Provider: "openai",
		Model:    "gpt-5",
		Timeout:  10 * time.Minute,
		setFlags: map[string]bool{
			"provider": true,
			"model":    true,
			"timeout":  true,
		},
	}
	// With flag.Parse normally, if a user sets --provider=openai then
	// buildLLMClient would look up OPENAI_API_KEY next. Simulate that by
	// providing an openai env key too.
	env.OpenAIAPIKey = "sk-openai-env"

	out := resolveSearchOptions(flags, env, cfg, defaultResolved())

	if out.Provider != "openai" {
		t.Errorf("provider = %q, want openai (flag wins)", out.Provider)
	}
	if out.Model != "gpt-5" {
		t.Errorf("model = %q, want gpt-5 (flag wins)", out.Model)
	}
	if out.Timeout != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m (flag wins)", out.Timeout)
	}
	if out.APIKey != "sk-openai-env" {
		t.Errorf("api_key = %q, want openai env key after provider flip", out.APIKey)
	}
}

// --- model resolution edge cases -------------------------------------------

func TestResolve_ModelFromEnvForSpecificProvider(t *testing.T) {
	// When config sets provider=anthropic but env sets ANTHROPIC_MODEL,
	// env should win over config.LLM.Model.
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4",
		},
	}
	env := searchEnv{AnthropicModel: "claude-opus-4"}
	out := resolveSearchOptions(searchFlags{}, env, cfg, defaultResolved())
	if out.Model != "claude-opus-4" {
		t.Errorf("model = %q, want claude-opus-4 (env > config)", out.Model)
	}
}

func TestResolve_ModelEnvIgnoredForOtherProvider(t *testing.T) {
	// OPENAI_MODEL should NOT apply when provider resolves to anthropic.
	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-4"},
	}
	env := searchEnv{
		OpenAIModel: "gpt-4.1",
	}
	out := resolveSearchOptions(searchFlags{}, env, cfg, defaultResolved())
	if out.Model != "claude-sonnet-4" {
		t.Errorf("model = %q, OPENAI_MODEL should not apply to anthropic", out.Model)
	}
}

// --- auto-detect ------------------------------------------------------------

func TestResolve_AutoDetectAnthropicFromEnv(t *testing.T) {
	// No config, no flag, only env → provider auto-detects to anthropic.
	env := searchEnv{AnthropicAPIKey: "sk-ant"}
	out := resolveSearchOptions(searchFlags{}, env, nil, defaultResolved())
	if out.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", out.Provider)
	}
	if out.APIKey != "sk-ant" {
		t.Errorf("api_key = %q", out.APIKey)
	}
}

func TestResolve_AutoDetectPrefersAnthropic(t *testing.T) {
	// Both keys set → anthropic wins (matches runSearch auto-detect order).
	env := searchEnv{
		AnthropicAPIKey: "sk-ant",
		OpenAIAPIKey:    "sk-oai",
	}
	out := resolveSearchOptions(searchFlags{}, env, nil, defaultResolved())
	if out.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (preferred)", out.Provider)
	}
}

// --- logging precedence ----------------------------------------------------

func TestResolve_DebugForcesJSONDebug(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "text"},
	}
	flags := searchFlags{Debug: true}
	out := resolveSearchOptions(flags, searchEnv{}, cfg, defaultResolved())
	if out.LogLevel != slog.LevelDebug {
		t.Errorf("log level = %v, --debug should force Debug", out.LogLevel)
	}
	if out.LogFormat != "json" {
		t.Errorf("log format = %q, --debug should force json", out.LogFormat)
	}
}

func TestResolve_LogConfigAppliedWithoutDebug(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, defaultResolved())
	if out.LogLevel != slog.LevelInfo {
		t.Errorf("log level = %v, want Info from config", out.LogLevel)
	}
	if out.LogFormat != "json" {
		t.Errorf("log format = %q", out.LogFormat)
	}
}

func TestResolve_InvalidLogLevelFallsBack(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "garbage", Format: "text"},
	}
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, defaultResolved())
	if out.LogLevel != slog.LevelWarn {
		t.Errorf("log level = %v, want Warn (fallback)", out.LogLevel)
	}
}

// --- enabled modules --------------------------------------------------------

func TestResolve_EnabledModulesFromConfig(t *testing.T) {
	cfg := &config.Config{
		Search: config.SearchConfig{Enabled: []string{"bing", "github"}},
	}
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, defaultResolved())
	if !reflect.DeepEqual(out.EnabledModules, []string{"bing", "github"}) {
		t.Errorf("enabled = %v", out.EnabledModules)
	}
}

func TestResolve_EnabledModulesEmptyConfigStaysEmpty(t *testing.T) {
	// Empty list in config → resolver returns empty, runSearch falls
	// back to its hardcoded default in buildSearchModules.
	cfg := &config.Config{
		Search: config.SearchConfig{Enabled: []string{}},
	}
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, defaultResolved())
	if len(out.EnabledModules) != 0 {
		t.Errorf("enabled = %v, want empty", out.EnabledModules)
	}
}

// --- pipeline limits -------------------------------------------------------

func TestResolve_PipelineLimits(t *testing.T) {
	cfg := &config.Config{
		Pipeline: config.PipelineConfig{
			MaxSourcesPerType: 7,
			MaxFetchedTotal:   25,
		},
	}
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, defaultResolved())
	if out.MaxSourcesPerType != 7 {
		t.Errorf("max_sources_per_type = %d, want 7", out.MaxSourcesPerType)
	}
	if out.MaxFetchedTotal != 25 {
		t.Errorf("max_fetched_total = %d, want 25", out.MaxFetchedTotal)
	}
}

func TestResolve_PipelineZerosLeaveDefaults(t *testing.T) {
	// Zero values in config mean "not set" — don't overwrite defaults.
	d := defaultResolved()
	d.MaxSourcesPerType = 5
	d.MaxFetchedTotal = 15
	cfg := &config.Config{} // pipeline fields zero
	out := resolveSearchOptions(searchFlags{}, searchEnv{}, cfg, d)
	if out.MaxSourcesPerType != 5 {
		t.Errorf("max_sources_per_type = %d, want 5", out.MaxSourcesPerType)
	}
	if out.MaxFetchedTotal != 15 {
		t.Errorf("max_fetched_total = %d, want 15", out.MaxFetchedTotal)
	}
}

// --- parseLogLevel ---------------------------------------------------------

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
		ok   bool
	}{
		{"debug", slog.LevelDebug, true},
		{"INFO", slog.LevelInfo, true},
		{" Warn ", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"", 0, false},
		{"verbose", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseLogLevel(tt.in)
		if ok != tt.ok {
			t.Errorf("parseLogLevel(%q) ok = %v, want %v", tt.in, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
