package doctor

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/odradekk/diting/internal/config"
)

// --- helpers for building mock environments --------------------------------

// envBuilder is a fluent helper for constructing a test Environment.
// It starts with an empty env map and a LookPath that finds nothing.
type envBuilder struct {
	env   map[string]string
	paths map[string]string
	stat  func(string) (os.FileInfo, error)
	cfg   *config.Config
	cfgPath string
	cfgErr  error
	modules []string
}

func newEnv() *envBuilder {
	return &envBuilder{
		env:   map[string]string{},
		paths: map[string]string{},
		stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		modules: []string{"bing", "duckduckgo", "baidu", "arxiv", "github", "stackexchange", "brave", "serp"},
	}
}

func (b *envBuilder) withEnv(k, v string) *envBuilder          { b.env[k] = v; return b }
func (b *envBuilder) withPath(name, path string) *envBuilder   { b.paths[name] = path; return b }
func (b *envBuilder) withConfig(c *config.Config) *envBuilder  { b.cfg = c; return b }
func (b *envBuilder) withConfigPath(p string) *envBuilder      { b.cfgPath = p; return b }
func (b *envBuilder) withConfigErr(err error) *envBuilder      { b.cfgErr = err; return b }
func (b *envBuilder) withModules(ms []string) *envBuilder      { b.modules = ms; return b }
func (b *envBuilder) withStat(f func(string) (os.FileInfo, error)) *envBuilder {
	b.stat = f
	return b
}

func (b *envBuilder) build() Environment {
	return Environment{
		LookupEnv: func(k string) (string, bool) {
			v, ok := b.env[k]
			return v, ok
		},
		LookPath: func(f string) (string, error) {
			if p, ok := b.paths[f]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Stat:             b.stat,
		Config:           b.cfg,
		ConfigPath:       b.cfgPath,
		ConfigLoadErr:    b.cfgErr,
		AvailableModules: b.modules,
		Version:          "v2.0.0-test",
	}
}

// existingFileInfo is used by the cache-dir test to signal "this dir
// exists" via env.Stat.
type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() any           { return nil }

func existingDirStat() func(string) (os.FileInfo, error) {
	return func(string) (os.FileInfo, error) { return fakeFileInfo{}, nil }
}

// findCheck returns the first check with the given name, or nil.
func findCheck(checks []Check, name string) *Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

// --- Status.String --------------------------------------------------------

func TestStatusString(t *testing.T) {
	tests := []struct {
		s    Status
		want string
	}{
		{StatusOK, "[ OK ]"},
		{StatusWarn, "[WARN]"},
		{StatusFail, "[FAIL]"},
		{Status(99), "[????]"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

// --- checkVersion ----------------------------------------------------------

func TestCheckVersion(t *testing.T) {
	env := newEnv().build()
	checks := checkVersion(env)
	if len(checks) != 1 {
		t.Fatalf("len = %d, want 1", len(checks))
	}
	c := checks[0]
	if c.Status != StatusOK {
		t.Errorf("status = %v, want OK", c.Status)
	}
	if !strings.Contains(c.Message, "v2.0.0-test") {
		t.Errorf("missing version: %q", c.Message)
	}
	if !strings.Contains(c.Message, "Go ") {
		t.Errorf("missing Go version: %q", c.Message)
	}
}

func TestCheckVersion_Unknown(t *testing.T) {
	env := newEnv().build()
	env.Version = ""
	checks := checkVersion(env)
	if !strings.Contains(checks[0].Message, "unknown") {
		t.Errorf("should say unknown when version is empty: %q", checks[0].Message)
	}
}

// --- checkConfigFile -------------------------------------------------------

func TestCheckConfigFile_Loaded(t *testing.T) {
	env := newEnv().
		withConfig(config.Default()).
		withConfigPath("/home/u/.config/diting/config.yaml").
		build()
	c := checkConfigFile(env)[0]
	if c.Status != StatusOK {
		t.Errorf("status = %v, want OK", c.Status)
	}
	if !strings.Contains(c.Message, "loaded from") {
		t.Errorf("message: %q", c.Message)
	}
}

func TestCheckConfigFile_NotFound(t *testing.T) {
	env := newEnv().
		withConfigPath("/tmp/no-such.yaml").
		build()
	c := checkConfigFile(env)[0]
	if c.Status != StatusWarn {
		t.Errorf("status = %v, want WARN", c.Status)
	}
	if !strings.Contains(c.Message, "not found") {
		t.Errorf("message: %q", c.Message)
	}
	if !strings.Contains(c.Message, "diting init") {
		t.Errorf("message should suggest `diting init`: %q", c.Message)
	}
}

func TestCheckConfigFile_LoadError(t *testing.T) {
	env := newEnv().
		withConfigPath("/tmp/bad.yaml").
		withConfigErr(fmt.Errorf("invalid yaml")).
		build()
	c := checkConfigFile(env)[0]
	if c.Status != StatusFail {
		t.Errorf("status = %v, want FAIL", c.Status)
	}
	if !strings.Contains(c.Message, "load failed") || !strings.Contains(c.Message, "invalid yaml") {
		t.Errorf("message: %q", c.Message)
	}
}

func TestCheckConfigFile_NoPath(t *testing.T) {
	env := newEnv().build() // no ConfigPath
	c := checkConfigFile(env)[0]
	if c.Status != StatusWarn {
		t.Errorf("status = %v, want WARN", c.Status)
	}
}

// --- checkLLMProvider ------------------------------------------------------

func TestCheckLLMProvider_Anthropic(t *testing.T) {
	env := newEnv().withEnv("ANTHROPIC_API_KEY", "sk-abc").build()
	c := checkLLMProvider(env)[0]
	if c.Status != StatusOK {
		t.Errorf("status = %v, want OK", c.Status)
	}
	if !strings.Contains(c.Message, "anthropic") {
		t.Errorf("message: %q", c.Message)
	}
	// MUST NOT leak the key value.
	if strings.Contains(c.Message, "sk-abc") {
		t.Errorf("message leaked API key: %q", c.Message)
	}
}

func TestCheckLLMProvider_OpenAIOnly(t *testing.T) {
	env := newEnv().withEnv("OPENAI_API_KEY", "sk-xyz").build()
	c := checkLLMProvider(env)[0]
	if c.Status != StatusOK {
		t.Errorf("status = %v, want OK", c.Status)
	}
	if !strings.Contains(c.Message, "openai") {
		t.Errorf("message: %q", c.Message)
	}
}

func TestCheckLLMProvider_Both(t *testing.T) {
	env := newEnv().
		withEnv("ANTHROPIC_API_KEY", "a").
		withEnv("OPENAI_API_KEY", "b").
		build()
	c := checkLLMProvider(env)[0]
	if c.Status != StatusOK {
		t.Errorf("status = %v, want OK", c.Status)
	}
	// Anthropic wins per auto-detect order; openai listed as fallback.
	if !strings.Contains(c.Message, "anthropic") {
		t.Errorf("message should lead with anthropic: %q", c.Message)
	}
	if !strings.Contains(c.Message, "openai") {
		t.Errorf("message should mention openai fallback: %q", c.Message)
	}
}

func TestCheckLLMProvider_None(t *testing.T) {
	env := newEnv().build()
	c := checkLLMProvider(env)[0]
	if c.Status != StatusFail {
		t.Errorf("status = %v, want FAIL", c.Status)
	}
	if !strings.Contains(c.Message, "ANTHROPIC_API_KEY") {
		t.Errorf("error should name the env var: %q", c.Message)
	}
	if !strings.Contains(c.Message, "OPENAI_API_KEY") {
		t.Errorf("error should name the env var: %q", c.Message)
	}
}

func TestCheckLLMProvider_EmptyValue(t *testing.T) {
	// Explicit empty value in env should NOT count as configured.
	env := newEnv().withEnv("ANTHROPIC_API_KEY", "").build()
	c := checkLLMProvider(env)[0]
	if c.Status != StatusFail {
		t.Errorf("empty env var should FAIL, got %v", c.Status)
	}
}

// --- checkSearchModules ----------------------------------------------------

func TestCheckSearchModules_AllFreeNoBYOK(t *testing.T) {
	env := newEnv().build() // no BYOK keys set
	checks := checkSearchModules(env)

	// All free modules should be OK.
	for _, m := range []string{"bing", "duckduckgo", "arxiv", "baidu", "stackexchange"} {
		c := findCheck(checks, m)
		if c == nil {
			t.Errorf("missing check for %q", m)
			continue
		}
		if c.Status != StatusOK {
			t.Errorf("%s = %v, want OK", m, c.Status)
		}
	}

	// BYOK modules without keys: brave + serp should WARN.
	for _, m := range []string{"brave", "serp"} {
		c := findCheck(checks, m)
		if c == nil {
			t.Errorf("missing check for %q", m)
			continue
		}
		if c.Status != StatusWarn {
			t.Errorf("%s = %v, want WARN (no key)", m, c.Status)
		}
		if !strings.Contains(c.Message, "not set") {
			t.Errorf("%s message should say 'not set': %q", m, c.Message)
		}
	}

	// github without token: WARN (anonymous rate limit).
	gh := findCheck(checks, "github")
	if gh == nil || gh.Status != StatusWarn {
		t.Errorf("github = %v, want WARN", gh)
	}
	if !strings.Contains(gh.Message, "rate limit") {
		t.Errorf("github message should mention rate limit: %q", gh.Message)
	}
}

func TestCheckSearchModules_BYOKSet(t *testing.T) {
	env := newEnv().
		withEnv("BRAVE_API_KEY", "x").
		withEnv("SERP_API_KEY", "y").
		withEnv("GITHUB_TOKEN", "z").
		build()
	checks := checkSearchModules(env)

	for _, m := range []string{"brave", "serp", "github"} {
		c := findCheck(checks, m)
		if c == nil || c.Status != StatusOK {
			t.Errorf("%s = %v, want OK (key set)", m, c)
		}
	}
}

func TestCheckSearchModules_FiltersUnavailable(t *testing.T) {
	// Only "bing" available — other modules should NOT produce checks.
	env := newEnv().withModules([]string{"bing"}).build()
	checks := checkSearchModules(env)

	if findCheck(checks, "bing") == nil {
		t.Error("missing bing check")
	}
	for _, m := range []string{"arxiv", "brave", "serp", "github", "duckduckgo"} {
		if findCheck(checks, m) != nil {
			t.Errorf("unexpected check for unavailable module %q", m)
		}
	}
}

func TestCheckSearchModules_Sorted(t *testing.T) {
	env := newEnv().build()
	checks := checkSearchModules(env)
	for i := 1; i < len(checks); i++ {
		if checks[i-1].Name > checks[i].Name {
			t.Errorf("not sorted: %s before %s", checks[i-1].Name, checks[i].Name)
		}
	}
}

// --- checkFetchLayers ------------------------------------------------------

func TestCheckFetchLayers_AllGood(t *testing.T) {
	env := newEnv().
		withPath("google-chrome", "/usr/bin/google-chrome").
		withEnv("JINA_API_KEY", "jk").
		build()
	checks := checkFetchLayers(env)

	for _, name := range []string{"utls", "chromedp", "jina", "archive"} {
		c := findCheck(checks, name)
		if c == nil {
			t.Errorf("missing %s", name)
			continue
		}
		if c.Status != StatusOK {
			t.Errorf("%s = %v, want OK", name, c.Status)
		}
	}
	// chromedp message should include the binary path.
	chromedp := findCheck(checks, "chromedp")
	if !strings.Contains(chromedp.Message, "/usr/bin/google-chrome") {
		t.Errorf("chromedp message should include path: %q", chromedp.Message)
	}
}

func TestCheckFetchLayers_NoChrome(t *testing.T) {
	env := newEnv().build() // no chrome on PATH
	checks := checkFetchLayers(env)
	c := findCheck(checks, "chromedp")
	if c == nil || c.Status != StatusWarn {
		t.Errorf("chromedp = %v, want WARN", c)
	}
	if !strings.Contains(c.Message, "no Chrome") {
		t.Errorf("message should warn about missing Chrome: %q", c.Message)
	}
	// Should list all the candidate names searched.
	for _, cand := range chromeCandidates {
		if !strings.Contains(c.Message, cand) {
			t.Errorf("message should mention candidate %q: %q", cand, c.Message)
		}
	}
}

func TestCheckFetchLayers_ChromiumFallback(t *testing.T) {
	// No google-chrome, but chromium-browser is on PATH.
	env := newEnv().
		withPath("chromium-browser", "/usr/bin/chromium-browser").
		build()
	c := findCheck(checkFetchLayers(env), "chromedp")
	if c == nil || c.Status != StatusOK {
		t.Errorf("chromedp = %v, want OK (chromium fallback)", c)
	}
	if !strings.Contains(c.Message, "chromium-browser") {
		t.Errorf("message: %q", c.Message)
	}
}

func TestCheckFetchLayers_JinaWithoutKey(t *testing.T) {
	env := newEnv().build()
	c := findCheck(checkFetchLayers(env), "jina")
	if c == nil || c.Status != StatusWarn {
		t.Errorf("jina = %v, want WARN", c)
	}
	if !strings.Contains(c.Message, "rate limit") {
		t.Errorf("message should mention rate limit: %q", c.Message)
	}
}

func TestCheckFetchLayers_TavilyEnabledWithoutKey(t *testing.T) {
	cfg := config.Default()
	cfg.Fetch.Layers = append(cfg.Fetch.Layers, "tavily")
	env := newEnv().withConfig(cfg).build()
	c := findCheck(checkFetchLayers(env), "tavily")
	if c == nil {
		t.Fatal("missing tavily check when enabled in config")
	}
	if c.Status != StatusFail {
		t.Errorf("tavily = %v, want FAIL (enabled without key)", c.Status)
	}
}

func TestCheckFetchLayers_TavilyEnabledWithKey(t *testing.T) {
	cfg := config.Default()
	cfg.Fetch.Layers = append(cfg.Fetch.Layers, "tavily")
	env := newEnv().
		withConfig(cfg).
		withEnv("TAVILY_API_KEY", "tv").
		build()
	c := findCheck(checkFetchLayers(env), "tavily")
	if c == nil || c.Status != StatusOK {
		t.Errorf("tavily = %v, want OK", c)
	}
}

func TestCheckFetchLayers_TavilyNotEnabledNoCheck(t *testing.T) {
	// Default config has no tavily in layers — no check should appear.
	env := newEnv().withConfig(config.Default()).build()
	if c := findCheck(checkFetchLayers(env), "tavily"); c != nil {
		t.Errorf("unexpected tavily check when not enabled: %v", c)
	}
}

// --- checkCacheDir --------------------------------------------------------

func TestCheckCacheDir_Exists(t *testing.T) {
	env := newEnv().
		withEnv("HOME", "/home/u").
		withStat(existingDirStat()).
		build()
	c := checkCacheDir(env)[0]
	if c.Status != StatusOK {
		t.Errorf("status = %v, want OK", c.Status)
	}
	if !strings.Contains(c.Message, "/home/u/.cache/diting") {
		t.Errorf("message: %q", c.Message)
	}
}

func TestCheckCacheDir_Missing(t *testing.T) {
	env := newEnv().withEnv("HOME", "/home/u").build() // default stat = not exist
	c := checkCacheDir(env)[0]
	if c.Status != StatusWarn {
		t.Errorf("status = %v, want WARN", c.Status)
	}
	if !strings.Contains(c.Message, "does not exist") {
		t.Errorf("message: %q", c.Message)
	}
}

func TestCheckCacheDir_NoHome(t *testing.T) {
	env := newEnv().build() // no HOME
	c := checkCacheDir(env)[0]
	if c.Status != StatusWarn {
		t.Errorf("status = %v, want WARN", c.Status)
	}
}

func TestCheckCacheDir_ConfigOverride(t *testing.T) {
	cfg := config.Default()
	cfg.Fetch.Cache.Path = "/custom/cache.db"
	env := newEnv().
		withConfig(cfg).
		withStat(existingDirStat()).
		build()
	c := checkCacheDir(env)[0]
	if !strings.Contains(c.Message, "/custom") {
		t.Errorf("config override not used: %q", c.Message)
	}
}

// --- RunChecks aggregation -------------------------------------------------

func TestRunChecks_AggregatesCounts(t *testing.T) {
	env := newEnv().
		withEnv("ANTHROPIC_API_KEY", "x").
		withPath("google-chrome", "/usr/bin/google-chrome").
		withEnv("HOME", "/home/u").
		withStat(existingDirStat()).
		withConfig(config.Default()).
		withConfigPath("/x/config.yaml").
		build()
	report := RunChecks(env)
	if report.OKCount == 0 {
		t.Error("expected at least some OK checks")
	}
	// Without BYOK keys set, some checks should still WARN.
	if report.WarnCount == 0 {
		t.Error("expected at least some WARN checks (brave/serp)")
	}
	// Total must equal the sum of buckets.
	total := report.OKCount + report.WarnCount + report.FailCount
	if total != len(report.Checks) {
		t.Errorf("total %d != len(checks) %d", total, len(report.Checks))
	}
}

func TestRunChecks_HasFailures(t *testing.T) {
	// No LLM key → at least one FAIL.
	env := newEnv().build()
	report := RunChecks(env)
	if !report.HasFailures() {
		t.Error("expected HasFailures=true without LLM key")
	}
	if report.FailCount == 0 {
		t.Error("FailCount should be > 0")
	}
}

func TestRunChecks_Deterministic(t *testing.T) {
	// Same input should produce byte-for-byte identical output.
	env := newEnv().
		withEnv("ANTHROPIC_API_KEY", "x").
		withEnv("HOME", "/h").
		withStat(existingDirStat()).
		build()
	r1 := RunChecks(env)
	r2 := RunChecks(env)
	if len(r1.Checks) != len(r2.Checks) {
		t.Fatalf("len mismatch: %d vs %d", len(r1.Checks), len(r2.Checks))
	}
	for i := range r1.Checks {
		if r1.Checks[i] != r2.Checks[i] {
			t.Errorf("check[%d] differs:\n  r1: %+v\n  r2: %+v", i, r1.Checks[i], r2.Checks[i])
		}
	}
}

// --- leak regression -------------------------------------------------------

// TestNoSecretLeak verifies that no check message contains the raw API
// key value — the doctor must NEVER print secrets per architecture §9.3.
func TestNoSecretLeak(t *testing.T) {
	const secret = "sk-THIS-IS-A-SECRET-DO-NOT-LEAK-12345"
	env := newEnv().
		withEnv("ANTHROPIC_API_KEY", secret).
		withEnv("OPENAI_API_KEY", secret).
		withEnv("BRAVE_API_KEY", secret).
		withEnv("SERP_API_KEY", secret).
		withEnv("GITHUB_TOKEN", secret).
		withEnv("JINA_API_KEY", secret).
		withEnv("HOME", "/h").
		build()
	report := RunChecks(env)
	for _, c := range report.Checks {
		if strings.Contains(c.Message, secret) {
			t.Errorf("check %q leaked secret: %q", c.Name, c.Message)
		}
	}
}
