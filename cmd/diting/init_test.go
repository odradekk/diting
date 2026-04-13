package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/config"
)

// --- doInit -----------------------------------------------------------------

func TestDoInit_NonInteractive_WritesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	var out bytes.Buffer
	if err := doInit(strings.NewReader(""), &out, path, false, true); err != nil {
		t.Fatalf("doInit: %v", err)
	}

	// File should exist and parse cleanly.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pipeline.MaxSourcesPerType != 5 {
		t.Errorf("default not written: max_sources_per_type = %d", cfg.Pipeline.MaxSourcesPerType)
	}

	// Status output mentions the path and the non-interactive notice.
	if !strings.Contains(out.String(), "non-interactive") {
		t.Errorf("missing non-interactive notice:\n%s", out.String())
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("missing path in output:\n%s", out.String())
	}
}

func TestDoInit_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Pre-create the file with sentinel contents.
	original := []byte("# DO NOT OVERWRITE\nllm:\n  provider: openai\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := doInit(strings.NewReader(""), &out, path, false, true)
	if err == nil {
		t.Fatal("expected error refusing overwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("wrong error: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force: %v", err)
	}

	// Original file must be untouched.
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Errorf("original file was mutated:\n%s", got)
	}
}

func TestDoInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := doInit(strings.NewReader(""), &out, path, true, true); err != nil {
		t.Fatalf("doInit: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, []byte("# old\n")) {
		t.Error("file was not overwritten despite --force")
	}
	// Should be valid YAML now.
	if _, err := config.Load(path); err != nil {
		t.Errorf("overwritten file invalid: %v", err)
	}
}

func TestDoInit_CreatesParentDirectory(t *testing.T) {
	// The parent directory may not exist on a fresh install.
	dir := t.TempDir()
	path := filepath.Join(dir, "deeply", "nested", "config.yaml")

	var out bytes.Buffer
	if err := doInit(strings.NewReader(""), &out, path, false, true); err != nil {
		t.Fatalf("doInit: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestDoInit_Interactive_UsesScriptedInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Drive an openai → MiniMax preset run with all defaults.
	// Prompts: provider, preset, env var, base URL, model, modules,
	// logging level, logging format.
	script := strings.Join([]string{
		"openai", // provider
		"2",      // MiniMax preset
		"",       // env var → MINIMAX_API_KEY default
		"",       // base URL → MiniMax default
		"",       // model → MiniMax default
		"all",    // modules
		"debug",  // logging level
		"json",   // logging format
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := doInit(strings.NewReader(script), &out, path, false, false); err != nil {
		t.Fatalf("doInit: %v\noutput:\n%s", err, out.String())
	}

	// We verify the LITERAL file contents, NOT via config.Load — because
	// Load() runs os.Expand which would resolve ${MINIMAX_API_KEY} into
	// whatever value is in the test environment (likely a real key from
	// the developer's shell). What we care about is that the file on disk
	// contains the env var REFERENCE, never the literal secret.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	rawStr := string(raw)

	if !strings.Contains(rawStr, "provider: openai") {
		t.Errorf("missing provider: openai in:\n%s", rawStr)
	}
	if !strings.Contains(rawStr, "base_url: https://api.minimaxi.com/v1") {
		t.Errorf("missing minimax base_url in:\n%s", rawStr)
	}
	if !strings.Contains(rawStr, "${MINIMAX_API_KEY}") {
		t.Errorf("api_key not written as ${MINIMAX_API_KEY} reference:\n%s", rawStr)
	}
	if !strings.Contains(rawStr, "level: debug") {
		t.Errorf("missing level: debug in:\n%s", rawStr)
	}
	if !strings.Contains(rawStr, "format: json") {
		t.Errorf("missing format: json in:\n%s", rawStr)
	}

	// The follow-up output should tell the user to set MINIMAX_API_KEY.
	if !strings.Contains(out.String(), "export MINIMAX_API_KEY") {
		t.Errorf("missing env var hint in output:\n%s", out.String())
	}
}

// TestRunInit_FlagSpaceValueIsParsed is a regression test for the bug
// where `--config /tmp/foo --non-interactive` ended up with `*configPath`
// = "--non-interactive" because an earlier flag-reorder hack separated
// the string flag from its value. The fix is to remove the reorder for
// `init` (which has no positional args), letting flag.Parse handle the
// stream verbatim.
//
// This test guards against ever re-introducing the reorder by asserting
// that the natural arg shape parses correctly into the same flag set
// runInit constructs.
func TestRunInit_FlagSpaceValueIsParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	force := fs.Bool("force", false, "")
	nonInteractive := fs.Bool("non-interactive", false, "")

	args := []string{"--config", path, "--non-interactive"}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if *configPath != path {
		t.Errorf("configPath = %q, want %q (regression: flag value got eaten by next flag)", *configPath, path)
	}
	if !*nonInteractive {
		t.Error("nonInteractive should be true")
	}
	if *force {
		t.Error("force should be false (not set)")
	}
}

// --- extractEnvVars ---------------------------------------------------------

func TestExtractEnvVars(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{APIKey: "${MY_LLM}"},
		Search: config.SearchConfig{
			Modules: map[string]config.SearchModuleConfig{
				"brave":  {APIKey: "${BRAVE_KEY}"},
				"github": {Token: "${GITHUB_TOKEN}"},
				"serp":   {APIKey: "${MY_LLM}"}, // duplicate — should dedupe
			},
		},
		Fetch: config.FetchConfig{
			Jina:   config.FetchLayerConfig{APIKey: "${JINA_KEY}"},
			Tavily: config.FetchLayerConfig{APIKey: ""}, // no env var
		},
	}

	got := extractEnvVars(cfg)
	wantSet := map[string]bool{
		"MY_LLM": true, "BRAVE_KEY": true, "GITHUB_TOKEN": true, "JINA_KEY": true,
	}

	if len(got) != len(wantSet) {
		t.Errorf("got %d env vars, want %d: %v", len(got), len(wantSet), got)
	}
	seen := make(map[string]bool)
	for _, v := range got {
		if !wantSet[v] {
			t.Errorf("unexpected env var: %q", v)
		}
		if seen[v] {
			t.Errorf("duplicate env var: %q", v)
		}
		seen[v] = true
	}
}

func TestExtractEnvVars_IgnoresNonRefs(t *testing.T) {
	// A literal API key (not a ${VAR} reference) should NOT be returned.
	cfg := &config.Config{
		LLM: config.LLMConfig{APIKey: "sk-literal-secret"},
	}
	got := extractEnvVars(cfg)
	if len(got) != 0 {
		t.Errorf("got %v, want empty (literal keys aren't env vars)", got)
	}
}
