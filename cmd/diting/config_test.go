package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- resolveConfigPath ------------------------------------------------------

func TestResolveConfigPath_FlagWins(t *testing.T) {
	t.Setenv("DITING_CONFIG", "/env/path.yaml")
	got, err := resolveConfigPath("/flag/path.yaml")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if got != "/flag/path.yaml" {
		t.Errorf("got %q, want /flag/path.yaml (flag should win)", got)
	}
}

func TestResolveConfigPath_EnvFallback(t *testing.T) {
	t.Setenv("DITING_CONFIG", "/env/path.yaml")
	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if got != "/env/path.yaml" {
		t.Errorf("got %q, want /env/path.yaml (env fallback)", got)
	}
}

// --- runConfigPath ----------------------------------------------------------

func TestRunConfigPath_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("llm:\n  provider: openai\n"), 0o644)

	var buf bytes.Buffer
	if err := runConfigPath(&buf, path); err != nil {
		t.Fatalf("runConfigPath: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, path) {
		t.Errorf("missing path: %s", out)
	}
	if !strings.Contains(out, "(exists)") {
		t.Errorf("missing existence marker: %s", out)
	}
}

func TestRunConfigPath_NotExists(t *testing.T) {
	path := "/nonexistent/config.yaml"
	var buf bytes.Buffer
	if err := runConfigPath(&buf, path); err != nil {
		t.Fatalf("runConfigPath: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, path) {
		t.Errorf("missing path: %s", out)
	}
	if !strings.Contains(out, "does not exist") {
		t.Errorf("missing 'does not exist' marker: %s", out)
	}
}

// --- runConfigShow ----------------------------------------------------------

func TestRunConfigShow_FromFile_RedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
llm:
  provider: openai
  api_key: SUPER-SECRET-ABC-123
search:
  enabled: [bing]
  modules:
    brave:
      api_key: BRAVE-SECRET-XYZ
`
	os.WriteFile(path, []byte(content), 0o644)

	var buf bytes.Buffer
	if err := runConfigShow(&buf, path); err != nil {
		t.Fatalf("runConfigShow: %v", err)
	}
	out := buf.String()

	// Header identifies the source file.
	if !strings.Contains(out, path) {
		t.Errorf("missing path in header: %s", out)
	}
	// Secrets MUST NOT leak.
	if strings.Contains(out, "SUPER-SECRET") {
		t.Errorf("llm secret leaked: %s", out)
	}
	if strings.Contains(out, "BRAVE-SECRET") {
		t.Errorf("brave secret leaked: %s", out)
	}
	// Mask markers should be present.
	if !strings.Contains(out, "<set>") {
		t.Errorf("missing <set> mask: %s", out)
	}
	// Non-secret values should still appear.
	if !strings.Contains(out, "provider: openai") {
		t.Errorf("missing provider value: %s", out)
	}
}

func TestRunConfigShow_MissingFile_ShowsDefaults(t *testing.T) {
	path := "/nonexistent/missing.yaml"
	var buf bytes.Buffer
	if err := runConfigShow(&buf, path); err != nil {
		t.Fatalf("runConfigShow: %v", err)
	}
	out := buf.String()
	// Should mention "no config file" and show defaults (no error).
	if !strings.Contains(out, "no config file") {
		t.Errorf("missing 'no config file' header: %s", out)
	}
	if !strings.Contains(out, "built-in defaults") {
		t.Errorf("missing defaults notice: %s", out)
	}
	// Default provider is anthropic.
	if !strings.Contains(out, "provider: anthropic") {
		t.Errorf("missing default provider: %s", out)
	}
}

// --- runConfigValidate ------------------------------------------------------

func TestRunConfigValidate_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
llm:
  provider: openai
search:
  enabled: [bing, duckduckgo]
fetch:
  layers: [utls, chromedp]
  cache:
    enabled: true
pipeline:
  max_sources_per_type: 5
  max_fetched_total: 15
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
	os.WriteFile(path, []byte(content), 0o644)

	var buf bytes.Buffer
	if err := runConfigValidate(&buf, path); err != nil {
		t.Fatalf("runConfigValidate: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "OK") {
		t.Errorf("missing OK marker: %s", out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("missing path: %s", out)
	}
}

func TestRunConfigValidate_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	err := runConfigValidate(&buf, "/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "no config file") {
		t.Errorf("error should say 'no config file': %v", err)
	}
}

func TestRunConfigValidate_InvalidStructure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	content := `
llm:
  provider: claude-not-real
  max_tokens: -5
logging:
  level: verbose
pipeline:
  max_fetched_total: -1
`
	os.WriteFile(path, []byte(content), 0o644)

	var buf bytes.Buffer
	err := runConfigValidate(&buf, path)
	if err == nil {
		t.Fatal("expected validation error")
	}
	errStr := err.Error()
	for _, want := range []string{
		"llm.provider", "llm.max_tokens",
		"logging.level", "pipeline.max_fetched_total",
	} {
		if !strings.Contains(errStr, want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestRunConfigValidate_UnknownModule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-module.yaml")
	content := `
search:
  enabled: [bing, imaginary-module]
logging:
  level: info
  format: text
`
	os.WriteFile(path, []byte(content), 0o644)

	var buf bytes.Buffer
	err := runConfigValidate(&buf, path)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "imaginary-module") {
		t.Errorf("error should mention imaginary-module: %v", err)
	}
}
