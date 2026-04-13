package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Load + env var interpolation --------------------------------------------

func TestLoad_FullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
llm:
  provider: openai
  base_url: https://api.minimaxi.com/v1
  model: MiniMax-M2.7-highspeed
  api_key: sk-abc123
  timeout: 60s
  max_tokens: 8192

search:
  enabled:
    - bing
    - arxiv
  modules:
    brave:
      api_key: brave-token
      timeout: 15s
      max_results: 20

fetch:
  layers: [utls, chromedp]
  cache:
    enabled: true
    max_mb: 500
    default_ttl_days: 3

pipeline:
  max_sources_per_type: 5
  max_fetched_total: 15
  fetch_timeout: 40s

scoring:
  weights:
    domain_authority: 0.4
    keyword_overlap: 0.4
    snippet_quality: 0.2
    language_match: 0.0

logging:
  level: info
  format: json
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.Provider != "openai" {
		t.Errorf("provider = %q, want openai", cfg.LLM.Provider)
	}
	if cfg.LLM.BaseURL != "https://api.minimaxi.com/v1" {
		t.Errorf("base_url = %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey != "sk-abc123" {
		t.Errorf("api_key = %q", cfg.LLM.APIKey)
	}
	if cfg.LLM.Timeout != 60*time.Second {
		t.Errorf("timeout = %v", cfg.LLM.Timeout)
	}
	if got := cfg.Search.Enabled; len(got) != 2 || got[0] != "bing" || got[1] != "arxiv" {
		t.Errorf("enabled = %v", got)
	}
	if brave := cfg.Search.Modules["brave"]; brave.APIKey != "brave-token" || brave.MaxResults != 20 {
		t.Errorf("brave module = %+v", brave)
	}
	if cfg.Pipeline.MaxSourcesPerType != 5 {
		t.Errorf("max_sources_per_type = %d", cfg.Pipeline.MaxSourcesPerType)
	}
	if cfg.Scoring.Weights.DomainAuthority != 0.4 {
		t.Errorf("domain_authority weight = %v", cfg.Scoring.Weights.DomainAuthority)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "json" {
		t.Errorf("logging = %+v", cfg.Logging)
	}
}

func TestLoad_EnvInterpolation(t *testing.T) {
	t.Setenv("DITING_TEST_API_KEY", "secret-from-env")
	t.Setenv("DITING_TEST_MODEL", "test-model")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
llm:
  provider: openai
  api_key: ${DITING_TEST_API_KEY}
  model: $DITING_TEST_MODEL
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.APIKey != "secret-from-env" {
		t.Errorf("api_key = %q, want 'secret-from-env'", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "test-model" {
		t.Errorf("model = %q, want 'test-model'", cfg.LLM.Model)
	}
}

func TestLoad_UnsetEnvVarBecomesEmpty(t *testing.T) {
	// Unset vars should resolve to empty strings, not leave ${VAR} literal.
	os.Unsetenv("DITING_UNSET_TEST")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
llm:
  api_key: ${DITING_UNSET_TEST}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.APIKey != "" {
		t.Errorf("api_key = %q, want empty (unset env)", cfg.LLM.APIKey)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("this: is: not: valid: yaml:"), 0o644)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// --- DefaultPath ------------------------------------------------------------

func TestDefaultPath_EnvOverride(t *testing.T) {
	t.Setenv("DITING_CONFIG", "/custom/path/config.yaml")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got != "/custom/path/config.yaml" {
		t.Errorf("DefaultPath = %q, want /custom/path/config.yaml", got)
	}
}

func TestDefaultPath_XDG(t *testing.T) {
	os.Unsetenv("DITING_CONFIG")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/fake-xdg")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := "/tmp/fake-xdg/diting/config.yaml"
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

// --- Default() sanity -------------------------------------------------------

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg == nil {
		t.Fatal("Default() returned nil")
	}
	if cfg.Pipeline.MaxSourcesPerType != 5 {
		t.Errorf("default max_sources_per_type = %d, want 5", cfg.Pipeline.MaxSourcesPerType)
	}
	if cfg.Pipeline.MaxFetchedTotal != 15 {
		t.Errorf("default max_fetched_total = %d, want 15", cfg.Pipeline.MaxFetchedTotal)
	}
	if len(cfg.Search.Enabled) == 0 {
		t.Error("default search.enabled should not be empty")
	}
	if !cfg.Fetch.Cache.Enabled {
		t.Error("default cache should be enabled")
	}
	// Should be valid out of the box.
	if err := cfg.Validate(ValidateOptions{}); err != nil {
		t.Errorf("Default() does not pass Validate: %v", err)
	}
}

// --- Validate ---------------------------------------------------------------

func TestValidate_Valid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(ValidateOptions{}); err != nil {
		t.Errorf("valid config failed validation: %v", err)
	}
}

func TestValidate_BadProvider(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "claude"
	err := cfg.Validate(ValidateOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "llm.provider") {
		t.Errorf("error should mention llm.provider: %v", err)
	}
}

func TestValidate_BadLogging(t *testing.T) {
	cfg := Default()
	cfg.Logging.Level = "verbose"
	cfg.Logging.Format = "xml"
	err := cfg.Validate(ValidateOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "logging.level") || !strings.Contains(err.Error(), "logging.format") {
		t.Errorf("error should mention both logging fields: %v", err)
	}
}

func TestValidate_NegativeValues(t *testing.T) {
	cfg := Default()
	cfg.LLM.Timeout = -1 * time.Second
	cfg.LLM.MaxTokens = -1
	cfg.Pipeline.MaxFetchedTotal = -1
	cfg.Fetch.Cache.MaxMB = -1
	err := cfg.Validate(ValidateOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"llm.timeout", "llm.max_tokens",
		"pipeline.max_fetched_total", "fetch.cache.max_mb",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestValidate_WeightsOutOfRange(t *testing.T) {
	cfg := Default()
	cfg.Scoring.Weights.DomainAuthority = 1.5
	cfg.Scoring.Weights.KeywordOverlap = -0.1
	err := cfg.Validate(ValidateOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "domain_authority") || !strings.Contains(err.Error(), "keyword_overlap") {
		t.Errorf("error should flag both weights: %v", err)
	}
}

func TestValidate_UnknownModule(t *testing.T) {
	cfg := Default()
	cfg.Search.Enabled = []string{"bing", "foo-bar"}
	err := cfg.Validate(ValidateOptions{
		KnownModules: []string{"bing", "duckduckgo", "arxiv"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "foo-bar") {
		t.Errorf("error should mention foo-bar: %v", err)
	}
	if strings.Contains(err.Error(), `"bing"`) {
		t.Error("error should not flag bing (it IS known)")
	}
}

func TestValidate_UnknownFetchLayer(t *testing.T) {
	cfg := Default()
	cfg.Fetch.Layers = []string{"utls", "magic-layer"}
	err := cfg.Validate(ValidateOptions{
		KnownFetchLayers: []string{"utls", "chromedp", "jina", "archive"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "magic-layer") {
		t.Errorf("error should mention magic-layer: %v", err)
	}
}

func TestValidate_SkippsKnownChecksWhenEmpty(t *testing.T) {
	cfg := Default()
	cfg.Search.Enabled = []string{"anything-goes"}
	// No KnownModules → the check is skipped.
	if err := cfg.Validate(ValidateOptions{}); err != nil {
		t.Errorf("should skip known-module check when empty, got: %v", err)
	}
}

// --- Redact -----------------------------------------------------------------

func TestRedact_MasksSecrets(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{APIKey: "sk-real-secret"},
		Search: SearchConfig{
			Modules: map[string]SearchModuleConfig{
				"brave":  {APIKey: "brave-real"},
				"github": {Token: "ghp_real"},
			},
		},
		Fetch: FetchConfig{
			Jina:   FetchLayerConfig{APIKey: "jina-real"},
			Tavily: FetchLayerConfig{APIKey: ""},
		},
	}

	red := cfg.Redact()

	if red.LLM.APIKey != "<set>" {
		t.Errorf("llm.api_key redacted = %q, want <set>", red.LLM.APIKey)
	}
	if red.Search.Modules["brave"].APIKey != "<set>" {
		t.Errorf("brave.api_key redacted = %q", red.Search.Modules["brave"].APIKey)
	}
	if red.Search.Modules["github"].Token != "<set>" {
		t.Errorf("github.token redacted = %q", red.Search.Modules["github"].Token)
	}
	if red.Fetch.Jina.APIKey != "<set>" {
		t.Errorf("jina.api_key redacted = %q", red.Fetch.Jina.APIKey)
	}
	if red.Fetch.Tavily.APIKey != "<not set>" {
		t.Errorf("tavily empty key = %q, want <not set>", red.Fetch.Tavily.APIKey)
	}

	// Original must NOT be mutated.
	if cfg.LLM.APIKey != "sk-real-secret" {
		t.Errorf("Redact() mutated original: llm.api_key = %q", cfg.LLM.APIKey)
	}
	if cfg.Search.Modules["brave"].APIKey != "brave-real" {
		t.Error("Redact() mutated original brave.api_key")
	}
}

func TestRedact_OutputIsPrintableYAML(t *testing.T) {
	// Ensure Marshal on a redacted config never contains the original
	// secret anywhere in the output.
	cfg := &Config{
		LLM: LLMConfig{APIKey: "SECRET-DO-NOT-LEAK-12345"},
		Search: SearchConfig{
			Modules: map[string]SearchModuleConfig{
				"brave": {APIKey: "BRAVE-SECRET-67890"},
			},
		},
	}
	out, err := cfg.Redact().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), "SECRET-DO-NOT-LEAK") {
		t.Errorf("redacted YAML leaked llm secret:\n%s", out)
	}
	if strings.Contains(string(out), "BRAVE-SECRET") {
		t.Errorf("redacted YAML leaked brave secret:\n%s", out)
	}
	if !strings.Contains(string(out), "<set>") {
		t.Errorf("redacted YAML missing <set> marker:\n%s", out)
	}
}

// --- Marshal ----------------------------------------------------------------

func TestMarshal_RoundTrip(t *testing.T) {
	orig := Default()
	data, err := orig.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.yaml")
	os.WriteFile(path, data, 0o644)

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Spot check a few fields.
	if loaded.Pipeline.MaxSourcesPerType != orig.Pipeline.MaxSourcesPerType {
		t.Errorf("round-trip mismatch: max_sources_per_type")
	}
	if loaded.Logging.Level != orig.Logging.Level {
		t.Errorf("round-trip mismatch: logging.level")
	}
}
