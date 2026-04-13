package config

import (
	"bytes"
	"strings"
	"testing"
)

// availableModulesForTest is the canonical set of search module names used
// in init tests. Real runtime uses search.List() which we don't import
// here to avoid a dependency cycle.
var availableModulesForTest = []string{
	"bing", "duckduckgo", "baidu", "arxiv", "github", "stackexchange", "brave", "serp",
}

// --- expandModulesPreset ----------------------------------------------------

func TestExpandModulesPreset_Minimal(t *testing.T) {
	got := expandModulesPreset("minimal", availableModulesForTest)
	want := []string{"bing", "duckduckgo", "arxiv", "github", "stackexchange"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandModulesPreset_EmptyDefaultsToMinimal(t *testing.T) {
	got := expandModulesPreset("", availableModulesForTest)
	if len(got) == 0 {
		t.Error("empty answer should default to minimal preset, got nothing")
	}
	for _, m := range got {
		if m == "brave" || m == "serp" {
			t.Errorf("minimal preset should NOT include BYOK module %q", m)
		}
	}
}

func TestExpandModulesPreset_All(t *testing.T) {
	got := expandModulesPreset("all", availableModulesForTest)
	if len(got) != len(availableModulesForTest) {
		t.Errorf("got %d modules, want %d", len(got), len(availableModulesForTest))
	}
	// Sorted output expected.
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("'all' result not sorted: %v", got)
		}
	}
}

func TestExpandModulesPreset_CSV(t *testing.T) {
	got := expandModulesPreset("bing,arxiv,github", availableModulesForTest)
	want := []string{"bing", "arxiv", "github"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandModulesPreset_CSVDeduped(t *testing.T) {
	got := expandModulesPreset("bing, bing, arxiv, bing", availableModulesForTest)
	want := []string{"bing", "arxiv"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandModulesPreset_MinimalFiltersUnavailable(t *testing.T) {
	// Build with only a subset available.
	got := expandModulesPreset("minimal", []string{"bing", "arxiv"})
	want := []string{"bing", "arxiv"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v (only available modules should survive)", got, want)
	}
}

// --- Interactive (full wizard) ----------------------------------------------

func TestInteractive_AnthropicFullDefaults(t *testing.T) {
	// All-defaults walkthrough. Prompt order (anthropic branch):
	//   1. provider       → blank (default anthropic)
	//   2. api key env    → blank (default ANTHROPIC_API_KEY)
	//   3. model          → blank (default empty)
	//   4. modules        → blank (default minimal)
	//   5. logging level  → blank (default warn)
	//   6. logging format → blank (default text)
	script := buildScript("", "", "", "", "", "")
	var out bytes.Buffer
	cfg, err := Interactive(strings.NewReader(script), &out, availableModulesForTest)
	if err != nil {
		t.Fatalf("Interactive: %v\noutput:\n%s", err, out.String())
	}

	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", cfg.LLM.Provider)
	}
	if cfg.LLM.APIKey != "${ANTHROPIC_API_KEY}" {
		t.Errorf("api_key = %q, want ${ANTHROPIC_API_KEY}", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "" {
		t.Errorf("model = %q, want empty", cfg.LLM.Model)
	}
	if len(cfg.Search.Enabled) == 0 {
		t.Error("Search.Enabled should not be empty for minimal preset")
	}
	// Minimal preset must not include BYOK modules.
	for _, m := range cfg.Search.Enabled {
		if m == "brave" || m == "serp" {
			t.Errorf("minimal preset should not include %q", m)
		}
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("logging.level = %q, want warn", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "text" {
		t.Errorf("logging.format = %q, want text", cfg.Logging.Format)
	}

	// Sanity: result must validate.
	if err := cfg.Validate(ValidateOptions{
		KnownModules: availableModulesForTest,
	}); err != nil {
		t.Errorf("generated config does not validate: %v", err)
	}
}

func TestInteractive_OpenAIMiniMaxPreset(t *testing.T) {
	// openai branch prompt order:
	//   1. provider        → "openai"
	//   2. preset          → "2" (MiniMax)
	//   3. api key env     → blank (default MINIMAX_API_KEY)
	//   4. base URL        → blank (default MiniMax URL)
	//   5. model           → blank (default MiniMax-M2.7-highspeed)
	//   6. modules         → "all"
	//   7. logging level   → "debug"
	//   8. logging format  → "json"
	script := buildScript("openai", "2", "", "", "", "all", "debug", "json")
	var out bytes.Buffer
	cfg, err := Interactive(strings.NewReader(script), &out, availableModulesForTest)
	if err != nil {
		t.Fatalf("Interactive: %v\noutput:\n%s", err, out.String())
	}

	if cfg.LLM.Provider != "openai" {
		t.Errorf("provider = %q, want openai", cfg.LLM.Provider)
	}
	if cfg.LLM.BaseURL != "https://api.minimaxi.com/v1" {
		t.Errorf("base_url = %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey != "${MINIMAX_API_KEY}" {
		t.Errorf("api_key = %q, want ${MINIMAX_API_KEY}", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "MiniMax-M2.7-highspeed" {
		t.Errorf("model = %q", cfg.LLM.Model)
	}
	if len(cfg.Search.Enabled) != len(availableModulesForTest) {
		t.Errorf("Search.Enabled count = %d, want %d (all)",
			len(cfg.Search.Enabled), len(availableModulesForTest))
	}
	if cfg.Logging.Level != "debug" || cfg.Logging.Format != "json" {
		t.Errorf("logging = %+v, want {debug json}", cfg.Logging)
	}
}

func TestInteractive_OpenAINativeWithCustomEnvVar(t *testing.T) {
	// openai + preset 1 (native OpenAI):
	//   1. provider       → "openai"
	//   2. preset         → "1"
	//   3. api key env    → "MY_OPENAI_KEY"
	//   4. base URL       → blank (empty)
	//   5. model          → blank (empty)
	//   6. modules        → "bing,arxiv"
	//   7. logging level  → blank (default warn)
	//   8. logging format → blank (default text)
	script := buildScript("openai", "1", "MY_OPENAI_KEY", "", "", "bing,arxiv", "", "")
	var out bytes.Buffer
	cfg, err := Interactive(strings.NewReader(script), &out, availableModulesForTest)
	if err != nil {
		t.Fatalf("Interactive: %v\noutput:\n%s", err, out.String())
	}

	if cfg.LLM.APIKey != "${MY_OPENAI_KEY}" {
		t.Errorf("api_key = %q", cfg.LLM.APIKey)
	}
	if cfg.LLM.BaseURL != "" {
		t.Errorf("base_url = %q, want empty", cfg.LLM.BaseURL)
	}
	if !equalStringSlices(cfg.Search.Enabled, []string{"bing", "arxiv"}) {
		t.Errorf("Search.Enabled = %v, want [bing arxiv]", cfg.Search.Enabled)
	}
}

func TestInteractive_InvalidThenValidChoice(t *testing.T) {
	// Provider prompt retries: 2 garbage answers, then "anthropic".
	// After provider resolves, the rest take defaults.
	script := buildScript(
		"garbage",       // provider attempt 1: invalid
		"also-garbage",  // provider attempt 2: invalid
		"anthropic",     // provider attempt 3: ✓
		"",              // api key env: default
		"",              // model: default
		"",              // modules: default
		"",              // level: default
		"",              // format: default
	)
	var out bytes.Buffer
	cfg, err := Interactive(strings.NewReader(script), &out, availableModulesForTest)
	if err != nil {
		t.Fatalf("Interactive: %v\noutput:\n%s", err, out.String())
	}
	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("provider = %q", cfg.LLM.Provider)
	}
	// The error message for the invalid attempts should appear in output.
	if !strings.Contains(out.String(), "invalid:") {
		t.Errorf("output should warn about invalid choice:\n%s", out.String())
	}
}

func TestInteractive_TooManyInvalidChoices(t *testing.T) {
	// Three garbage answers in a row → should error.
	script := buildScript("garbage1", "garbage2", "garbage3")
	var out bytes.Buffer
	_, err := Interactive(strings.NewReader(script), &out, availableModulesForTest)
	if err == nil {
		t.Fatal("expected error after 3 invalid attempts")
	}
}

// --- helpers ----------------------------------------------------------------

// buildScript turns a list of prompt answers into a stdin script. Each
// answer becomes one '\n'-terminated line. This avoids the off-by-one
// blank-line bugs that raw multi-line strings invite when one of the
// answers is itself an empty string (i.e. "accept the default").
func buildScript(answers ...string) string {
	return strings.Join(answers, "\n") + "\n"
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
