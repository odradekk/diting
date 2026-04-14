// Package doctor implements the `diting doctor` environment health check.
//
// The design keeps all check logic in pure functions that take an
// Environment (injectable env lookup + PATH lookup) and return
// []Check values. This lets unit tests drive every status combination
// without touching the real process environment or filesystem.
//
// The CLI wrapper in cmd/diting/doctor.go owns the printing and exit
// code logic, and simply passes a real Environment through to RunChecks.
package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/odradekk/diting/internal/config"
)

// Status is the outcome of a single check.
type Status int

const (
	// StatusOK means the check passed — feature works as expected.
	StatusOK Status = iota
	// StatusWarn means the feature works but is degraded or has a
	// soft dependency missing (e.g. optional API key not set).
	StatusWarn
	// StatusFail means the feature is broken and something is
	// genuinely not going to work (e.g. no LLM provider configured).
	StatusFail
)

// String returns a short label for the status: [ OK ], [WARN], or [FAIL].
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "[ OK ]"
	case StatusWarn:
		return "[WARN]"
	case StatusFail:
		return "[FAIL]"
	default:
		return "[????]"
	}
}

// Check is the outcome of one probe.
type Check struct {
	// Category groups related checks (e.g. "LLM", "Search modules",
	// "Fetch layers") so the CLI can print section headers.
	Category string
	// Name is a short one-line identifier for the thing being checked.
	Name string
	// Status is the overall outcome.
	Status Status
	// Message is a one-line human-readable explanation. Never contains
	// an API key value — the doctor design explicitly forbids printing
	// secrets (architecture §9.3).
	Message string
}

// Environment is the injectable surface the doctor needs to run. The
// default (real-process) values are returned by DefaultEnvironment().
type Environment struct {
	// LookupEnv mirrors os.LookupEnv. The doctor never falls back to
	// os.Getenv — it needs the (value, ok) pair to distinguish an
	// explicitly empty env var from an unset one.
	LookupEnv func(key string) (string, bool)
	// LookPath mirrors exec.LookPath. Used to probe for the Chrome /
	// Chromium binary that the chromedp fetch layer depends on.
	LookPath func(file string) (string, error)
	// Stat mirrors os.Stat. Used to probe config file presence and
	// cache directory existence.
	Stat func(path string) (os.FileInfo, error)
	// Config is the parsed configuration, or nil if the user doesn't
	// have a config file.
	Config *config.Config
	// ConfigPath is the resolved path (regardless of whether the file
	// exists) — used in check messages.
	ConfigPath string
	// ConfigLoadErr is the error (if any) encountered while loading
	// the config file at ConfigPath. nil means either the file
	// doesn't exist OR it parsed cleanly; inspect Config to tell
	// those apart.
	ConfigLoadErr error
	// AvailableModules is the list of search module names registered
	// at startup (typically search.List()). Used to report which
	// modules exist regardless of what the config enables.
	AvailableModules []string
	// Version is the diting binary version string, printed in the
	// header section.
	Version string
}

// DefaultEnvironment builds an Environment that pokes the real process:
// os.LookupEnv, exec.LookPath, os.Stat, etc. The caller fills in the
// Config / ConfigPath / AvailableModules / Version fields.
func DefaultEnvironment() Environment {
	return Environment{
		LookupEnv: os.LookupEnv,
		LookPath:  lookPath,
		Stat:      os.Stat,
	}
}

// Report is the return value of RunChecks. It carries the check list
// plus aggregated counts for the CLI summary line and exit code logic.
type Report struct {
	Checks   []Check
	OKCount  int
	WarnCount int
	FailCount int
}

// HasFailures returns true if at least one check returned StatusFail.
// The CLI uses this to set a non-zero exit code.
func (r *Report) HasFailures() bool {
	return r.FailCount > 0
}

// RunChecks invokes every doctor probe in order and returns a summary.
// Checks are run in a deterministic sequence so report output is stable.
func RunChecks(env Environment) *Report {
	var checks []Check
	checks = append(checks, checkVersion(env)...)
	checks = append(checks, checkConfigFile(env)...)
	checks = append(checks, checkLLMProvider(env)...)
	checks = append(checks, checkSearchModules(env)...)
	checks = append(checks, checkFetchLayers(env)...)
	checks = append(checks, checkCacheDir(env)...)

	r := &Report{Checks: checks}
	for _, c := range checks {
		switch c.Status {
		case StatusOK:
			r.OKCount++
		case StatusWarn:
			r.WarnCount++
		case StatusFail:
			r.FailCount++
		}
	}
	return r
}

// --- individual checks ------------------------------------------------------

func checkVersion(env Environment) []Check {
	version := env.Version
	if version == "" {
		version = "unknown"
	}
	return []Check{{
		Category: "System",
		Name:     "diting version",
		Status:   StatusOK,
		Message:  fmt.Sprintf("%s (%s/%s, Go %s)", version, runtime.GOOS, runtime.GOARCH, runtime.Version()),
	}}
}

func checkConfigFile(env Environment) []Check {
	if env.ConfigPath == "" {
		return []Check{{
			Category: "Config",
			Name:     "config file",
			Status:   StatusWarn,
			Message:  "no path resolved (using built-in defaults)",
		}}
	}

	if env.ConfigLoadErr != nil {
		return []Check{{
			Category: "Config",
			Name:     "config file",
			Status:   StatusFail,
			Message:  fmt.Sprintf("load failed at %s: %v", env.ConfigPath, env.ConfigLoadErr),
		}}
	}

	if env.Config == nil {
		// No error, no config → file doesn't exist.
		return []Check{{
			Category: "Config",
			Name:     "config file",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("not found at %s (using built-in defaults; run `diting init` to create one)", env.ConfigPath),
		}}
	}

	return []Check{{
		Category: "Config",
		Name:     "config file",
		Status:   StatusOK,
		Message:  fmt.Sprintf("loaded from %s", env.ConfigPath),
	}}
}

// llmProviders is the per-provider key check table. Order matters for
// the auto-detect hierarchy: anthropic is preferred when both are set,
// matching runSearch's buildLLMClient behaviour.
var llmProviders = []struct {
	name   string
	envKey string
}{
	{"anthropic", "ANTHROPIC_API_KEY"},
	{"openai", "OPENAI_API_KEY"},
}

func checkLLMProvider(env Environment) []Check {
	var configured []string
	for _, p := range llmProviders {
		if v, ok := env.LookupEnv(p.envKey); ok && v != "" {
			configured = append(configured, p.name)
		}
	}

	if len(configured) == 0 {
		return []Check{{
			Category: "LLM",
			Name:     "API key",
			Status:   StatusFail,
			Message:  "no provider configured (set ANTHROPIC_API_KEY or OPENAI_API_KEY)",
		}}
	}

	msg := fmt.Sprintf("%s configured", configured[0])
	if len(configured) > 1 {
		msg = fmt.Sprintf("%s configured (also: %s)", configured[0], strings.Join(configured[1:], ", "))
	}
	return []Check{{
		Category: "LLM",
		Name:     "API key",
		Status:   StatusOK,
		Message:  msg,
	}}
}

// byokModules names the modules that require an API key. For each of
// these we emit a dedicated check: OK if the key is present, WARN
// otherwise (the module just gets skipped at runtime rather than
// breaking the whole search). Free modules don't need a check — they
// always work.
var byokModules = []struct {
	module string
	envKey string
}{
	{"brave", "BRAVE_API_KEY"},
	{"exa", "EXA_API_KEY"},
	{"metaso", "METASO_API_KEY"},
	{"serp", "SERP_API_KEY"},
	{"github", "GITHUB_TOKEN"}, // optional; lifts rate limits
}

// freeModules are the search modules that need no API key.
var freeModules = []string{
	"bing", "duckduckgo", "baidu", "arxiv", "stackexchange",
}

func checkSearchModules(env Environment) []Check {
	var checks []Check

	// One check per free module that is actually available.
	available := stringSet(env.AvailableModules)
	for _, m := range freeModules {
		if len(env.AvailableModules) == 0 || available[m] {
			checks = append(checks, Check{
				Category: "Search",
				Name:     m,
				Status:   StatusOK,
				Message:  "free, no key required",
			})
		}
	}

	// BYOK modules: OK with key, WARN without.
	for _, b := range byokModules {
		if len(env.AvailableModules) > 0 && !available[b.module] {
			continue
		}
		_, ok := env.LookupEnv(b.envKey)
		if ok {
			checks = append(checks, Check{
				Category: "Search",
				Name:     b.module,
				Status:   StatusOK,
				Message:  fmt.Sprintf("%s set", b.envKey),
			})
		} else {
			severity := StatusWarn
			msg := fmt.Sprintf("%s not set — module will be skipped", b.envKey)
			if b.module == "github" {
				// github works anonymously with strict rate limits.
				msg = fmt.Sprintf("%s not set — will hit GitHub's 10 req/min anonymous rate limit", b.envKey)
			}
			checks = append(checks, Check{
				Category: "Search",
				Name:     b.module,
				Status:   severity,
				Message:  msg,
			})
		}
	}

	// Sort within the category for stable output.
	sort.SliceStable(checks, func(i, j int) bool { return checks[i].Name < checks[j].Name })
	return checks
}

// chromeCandidates is the list of executable names chromedp searches
// for (in this order). Matches what chromedp does internally.
var chromeCandidates = []string{
	"google-chrome", "google-chrome-stable",
	"chrome",
	"chromium", "chromium-browser",
}

func checkFetchLayers(env Environment) []Check {
	var checks []Check

	// utls: always OK (pure-Go stdlib).
	checks = append(checks, Check{
		Category: "Fetch",
		Name:     "utls",
		Status:   StatusOK,
		Message:  "pure-Go, no external deps",
	})

	// chromedp: look for a Chrome-compatible binary on PATH.
	var found string
	for _, cand := range chromeCandidates {
		if path, err := env.LookPath(cand); err == nil && path != "" {
			found = path
			break
		}
	}
	if found != "" {
		checks = append(checks, Check{
			Category: "Fetch",
			Name:     "chromedp",
			Status:   StatusOK,
			Message:  fmt.Sprintf("browser at %s", found),
		})
	} else {
		checks = append(checks, Check{
			Category: "Fetch",
			Name:     "chromedp",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("no Chrome/Chromium binary found on PATH (tried: %s)",
				strings.Join(chromeCandidates, ", ")),
		})
	}

	// jina: works without a key but is rate-limited.
	if _, ok := env.LookupEnv("JINA_API_KEY"); ok {
		checks = append(checks, Check{
			Category: "Fetch",
			Name:     "jina",
			Status:   StatusOK,
			Message:  "JINA_API_KEY set",
		})
	} else {
		checks = append(checks, Check{
			Category: "Fetch",
			Name:     "jina",
			Status:   StatusWarn,
			Message:  "JINA_API_KEY not set — anonymous rate limit applies",
		})
	}

	// archive.org: always OK (public service, no auth).
	checks = append(checks, Check{
		Category: "Fetch",
		Name:     "archive",
		Status:   StatusOK,
		Message:  "web.archive.org (no key required)",
	})

	// tavily: only relevant if the user explicitly opted in via config.
	// Without config layers, don't generate a check at all.
	if env.Config != nil && containsString(env.Config.Fetch.Layers, "tavily") {
		if _, ok := env.LookupEnv("TAVILY_API_KEY"); ok {
			checks = append(checks, Check{
				Category: "Fetch",
				Name:     "tavily",
				Status:   StatusOK,
				Message:  "TAVILY_API_KEY set",
			})
		} else {
			checks = append(checks, Check{
				Category: "Fetch",
				Name:     "tavily",
				Status:   StatusFail,
				Message:  "tavily enabled in config but TAVILY_API_KEY not set",
			})
		}
	}

	return checks
}

func checkCacheDir(env Environment) []Check {
	path := defaultCachePath(env)
	if path == "" {
		return []Check{{
			Category: "Cache",
			Name:     "content cache",
			Status:   StatusWarn,
			Message:  "no cache path resolved (non-standard HOME?)",
		}}
	}

	dir := filepath.Dir(path)
	if _, err := env.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Check{{
				Category: "Cache",
				Name:     "content cache",
				Status:   StatusWarn,
				Message:  fmt.Sprintf("parent dir %s does not exist (will be created on first run)", dir),
			}}
		}
		return []Check{{
			Category: "Cache",
			Name:     "content cache",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("stat %s: %v", dir, err),
		}}
	}

	return []Check{{
		Category: "Cache",
		Name:     "content cache",
		Status:   StatusOK,
		Message:  fmt.Sprintf("parent dir %s exists", dir),
	}}
}

// defaultCachePath returns the path the fetch cache will use. If the
// user's config specifies one, it wins; otherwise fall back to
// ~/.cache/diting/content.db via os.UserCacheDir().
func defaultCachePath(env Environment) string {
	if env.Config != nil && env.Config.Fetch.Cache.Path != "" {
		return env.Config.Fetch.Cache.Path
	}
	home, ok := env.LookupEnv("HOME")
	if !ok || home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "diting", "content.db")
}

// --- helpers ----------------------------------------------------------------

func stringSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
