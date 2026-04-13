package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- loadSearchConfig -------------------------------------------------------

func TestLoadSearchConfig_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
llm:
  provider: openai
  model: gpt-4.1-mini
search:
  enabled: [bing]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, gotPath, err := loadSearchConfig(path)
	if err != nil {
		t.Fatalf("loadSearchConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg is nil")
	}
	if gotPath != path {
		t.Errorf("gotPath = %q, want %q", gotPath, path)
	}
	if cfg.LLM.Provider != "openai" {
		t.Errorf("provider = %q", cfg.LLM.Provider)
	}
}

func TestLoadSearchConfig_ExplicitPathMissing(t *testing.T) {
	_, _, err := loadSearchConfig("/nonexistent/missing.yaml")
	if err == nil {
		t.Fatal("expected error for explicit missing path")
	}
}

func TestLoadSearchConfig_DefaultPathMissing(t *testing.T) {
	// Point at a path that definitely doesn't exist. Expect silent
	// no-op (nil cfg, nil err) — this is the normal case for users
	// without a config file.
	t.Setenv("DITING_CONFIG", "/nonexistent/default/config.yaml")
	cfg, _, err := loadSearchConfig("")
	if err != nil {
		t.Errorf("missing default should be silent, got: %v", err)
	}
	if cfg != nil {
		t.Errorf("cfg = %+v, want nil", cfg)
	}
}

func TestLoadSearchConfig_DefaultPathBroken(t *testing.T) {
	// An unparseable file at the default path should surface the error
	// so the user knows to fix it — silently falling back to "no
	// config" would give them a misleading impression.
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("this: is: not: yaml:"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DITING_CONFIG", path)

	_, _, err := loadSearchConfig("")
	if err == nil {
		t.Fatal("expected error for broken default config")
	}
	if !strings.Contains(err.Error(), "loading default config") {
		t.Errorf("error should identify the default path: %v", err)
	}
}

// --- buildSearchModulesFiltered --------------------------------------------

func TestBuildSearchModulesFiltered_EmptyReturnsAll(t *testing.T) {
	// Clear BYOK env vars so only free modules get built — we're
	// checking filter behaviour, not module instantiation.
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("SERP_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "")

	modules := buildSearchModulesFiltered(nil)
	if len(modules) == 0 {
		t.Fatal("expected modules, got none")
	}

	// Verify at least the standard free set appears.
	names := make(map[string]bool)
	for _, m := range modules {
		names[m.Manifest().Name] = true
		m.Close()
	}
	for _, expected := range []string{"bing", "duckduckgo", "arxiv", "github", "stackexchange"} {
		if !names[expected] {
			t.Errorf("missing free module %q; got %v", expected, names)
		}
	}
}

func TestBuildSearchModulesFiltered_OnlyEnabledAreReturned(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("SERP_API_KEY", "")

	modules := buildSearchModulesFiltered([]string{"bing", "arxiv"})
	if len(modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(modules))
	}

	names := make(map[string]bool)
	for _, m := range modules {
		names[m.Manifest().Name] = true
		m.Close()
	}
	for _, want := range []string{"bing", "arxiv"} {
		if !names[want] {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"duckduckgo", "github", "stackexchange"} {
		if names[unwanted] {
			t.Errorf("unexpected module %q", unwanted)
		}
	}
}

func TestBuildSearchModulesFiltered_UnknownNameSkipped(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("SERP_API_KEY", "")

	// "nonexistent-module" should be silently dropped; "bing" should
	// still come through.
	modules := buildSearchModulesFiltered([]string{"bing", "nonexistent-module"})
	if len(modules) != 1 {
		t.Errorf("got %d modules, want 1 (bing only)", len(modules))
	}
	for _, m := range modules {
		if m.Manifest().Name != "bing" {
			t.Errorf("unexpected module: %q", m.Manifest().Name)
		}
		m.Close()
	}
}

func TestBuildSearchModulesFiltered_BYOKSkippedWithoutKey(t *testing.T) {
	// Even if brave is in the enabled list, it should be skipped when
	// BRAVE_API_KEY is not set — the old buildSearchModules behaviour
	// must be preserved.
	t.Setenv("BRAVE_API_KEY", "")

	modules := buildSearchModulesFiltered([]string{"bing", "brave"})
	for _, m := range modules {
		defer m.Close()
		if m.Manifest().Name == "brave" {
			t.Error("brave should be skipped without BRAVE_API_KEY")
		}
	}
}

// --- buildLLMClientResolved -------------------------------------------------

func TestBuildLLMClientResolved_NoProvider(t *testing.T) {
	client, _, _ := buildLLMClientResolved(resolvedSearchOptions{})
	if client != nil {
		t.Error("expected nil client when no provider resolved")
	}
}

func TestBuildLLMClientResolved_NoAPIKey(t *testing.T) {
	client, _, _ := buildLLMClientResolved(resolvedSearchOptions{Provider: "anthropic"})
	if client != nil {
		t.Error("expected nil client when API key missing")
	}
}

func TestBuildLLMClientResolved_AnthropicHappyPath(t *testing.T) {
	client, name, model := buildLLMClientResolved(resolvedSearchOptions{
		Provider: "anthropic",
		APIKey:   "sk-fake",
		Model:    "claude-sonnet-4",
	})
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if name != "anthropic" {
		t.Errorf("name = %q", name)
	}
	if model != "claude-sonnet-4" {
		t.Errorf("model = %q", model)
	}
}

func TestBuildLLMClientResolved_OpenAIWithBaseURL(t *testing.T) {
	client, name, _ := buildLLMClientResolved(resolvedSearchOptions{
		Provider: "openai",
		APIKey:   "sk-fake",
		BaseURL:  "https://api.minimaxi.com/v1",
		Model:    "MiniMax-M2.7-highspeed",
	})
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if name != "openai" {
		t.Errorf("name = %q", name)
	}
}

// --- buildLoggerResolved ---------------------------------------------------

func TestBuildLoggerResolved_JSONText(t *testing.T) {
	jsonLogger := buildLoggerResolved(resolvedSearchOptions{
		LogLevel: slog.LevelInfo, LogFormat: "json",
	})
	textLogger := buildLoggerResolved(resolvedSearchOptions{
		LogLevel: slog.LevelInfo, LogFormat: "text",
	})

	if jsonLogger == nil || textLogger == nil {
		t.Fatal("nil logger")
	}
	// Handlers should both enable Info level.
	for _, l := range []*slog.Logger{jsonLogger, textLogger} {
		if !l.Handler().Enabled(context.Background(), slog.LevelInfo) {
			t.Error("info level should be enabled")
		}
	}
}

// --- collectSetFlags --------------------------------------------------------

func TestCollectSetFlags(t *testing.T) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.String("provider", "", "")
	fs.String("model", "", "")
	fs.Duration("timeout", 0, "")
	fs.Bool("plan-only", false, "")

	if err := fs.Parse([]string{"--provider", "openai", "--plan-only", "question"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set := collectSetFlags(fs)
	if !set["provider"] {
		t.Error("provider should be marked set")
	}
	if !set["plan-only"] {
		t.Error("plan-only should be marked set")
	}
	if set["model"] {
		t.Error("model should NOT be marked set (never passed)")
	}
	if set["timeout"] {
		t.Error("timeout should NOT be marked set (never passed)")
	}
}
