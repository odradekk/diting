package config

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Interactive walks the user through a configuration wizard, returning a
// populated *Config. It reads from `in` (one line per question) and writes
// prompts to `out`. The keys themselves are NEVER prompted for — the
// wizard only collects an env var name and writes `${VAR}` references
// into the config (per architecture §9.3 BYOK rules).
//
// The wizard is split into a pure function so unit tests can drive it with
// a strings.NewReader script. The CLI wrapper in cmd/diting/init.go
// connects it to os.Stdin and handles the file write.
//
// `availableModules` is the list of search modules the user can pick from.
// Typically search.List() at runtime; tests can pass a fixed slice.
func Interactive(in io.Reader, out io.Writer, availableModules []string) (*Config, error) {
	r := bufio.NewReader(in)
	cfg := Default()

	fmt.Fprintln(out, "diting init — interactive configuration")
	fmt.Fprintln(out, "Press Enter to accept the default shown in [brackets].")
	fmt.Fprintln(out)

	// --- Step 1: LLM provider --------------------------------------------
	fmt.Fprintln(out, "Step 1: LLM provider")
	provider, err := promptChoice(r, out, "Provider", []string{"anthropic", "openai"}, "anthropic")
	if err != nil {
		return nil, err
	}
	cfg.LLM.Provider = provider

	// --- Step 2: provider details ----------------------------------------
	switch provider {
	case "anthropic":
		// Default model and env var are baked into the anthropic client,
		// but we still write them out so the user sees them.
		envVar, err := promptString(r, out, "API key env var", "ANTHROPIC_API_KEY")
		if err != nil {
			return nil, err
		}
		cfg.LLM.APIKey = "${" + envVar + "}"
		model, err := promptString(r, out, "Model (empty = client default)", "")
		if err != nil {
			return nil, err
		}
		cfg.LLM.Model = model

	case "openai":
		fmt.Fprintln(out)
		fmt.Fprintln(out, "OpenAI client supports the native API and any OpenAI-compatible")
		fmt.Fprintln(out, "endpoint (MiniMax, Together, vLLM, …) — pick a preset:")
		fmt.Fprintln(out, "  1. OpenAI (api.openai.com)")
		fmt.Fprintln(out, "  2. MiniMax (api.minimaxi.com)")
		fmt.Fprintln(out, "  3. Custom base URL")
		preset, err := promptChoice(r, out, "Preset", []string{"1", "2", "3"}, "1")
		if err != nil {
			return nil, err
		}

		var defaultEnv, defaultBase, defaultModel string
		switch preset {
		case "1":
			defaultEnv = "OPENAI_API_KEY"
		case "2":
			defaultEnv = "MINIMAX_API_KEY"
			defaultBase = "https://api.minimaxi.com/v1"
			defaultModel = "MiniMax-M2.7-highspeed"
		case "3":
			defaultEnv = "OPENAI_API_KEY"
		}

		envVar, err := promptString(r, out, "API key env var", defaultEnv)
		if err != nil {
			return nil, err
		}
		cfg.LLM.APIKey = "${" + envVar + "}"

		baseURL, err := promptString(r, out, "Base URL (empty = OpenAI default)", defaultBase)
		if err != nil {
			return nil, err
		}
		cfg.LLM.BaseURL = baseURL

		model, err := promptString(r, out, "Model (empty = client default)", defaultModel)
		if err != nil {
			return nil, err
		}
		cfg.LLM.Model = model
	}

	// --- Step 3: search modules ------------------------------------------
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Step 2: Search modules")
	if len(availableModules) > 0 {
		sorted := append([]string(nil), availableModules...)
		sort.Strings(sorted)
		fmt.Fprintf(out, "Available: %s\n", strings.Join(sorted, ", "))
	}
	fmt.Fprintln(out, "Enter a comma-separated list, or one of:")
	fmt.Fprintln(out, "  all     — every available module")
	fmt.Fprintln(out, "  minimal — bing, duckduckgo, arxiv, github, stackexchange (free, no key)")

	modulesAnswer, err := promptString(r, out, "Modules", "minimal")
	if err != nil {
		return nil, err
	}
	cfg.Search.Enabled = expandModulesPreset(modulesAnswer, availableModules)

	// --- Step 4: logging --------------------------------------------------
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Step 3: Logging")
	level, err := promptChoice(r, out, "Level", []string{"debug", "info", "warn", "error"}, "warn")
	if err != nil {
		return nil, err
	}
	cfg.Logging.Level = level

	format, err := promptChoice(r, out, "Format", []string{"text", "json"}, "text")
	if err != nil {
		return nil, err
	}
	cfg.Logging.Format = format

	return cfg, nil
}

// expandModulesPreset converts a user answer ("minimal", "all", or a CSV)
// into the actual []string of module names. Unknown names in a CSV are
// kept as-is; Validate() will reject them downstream.
func expandModulesPreset(answer string, available []string) []string {
	answer = strings.TrimSpace(strings.ToLower(answer))

	switch answer {
	case "", "minimal":
		// Free modules that don't require an API key. Filter against
		// the available set so we never produce a list with an unknown
		// name (e.g. if the binary was built without one of them).
		preset := []string{"bing", "duckduckgo", "arxiv", "github", "stackexchange"}
		return intersect(preset, available)
	case "all":
		out := append([]string(nil), available...)
		sort.Strings(out)
		return out
	}

	// Custom CSV.
	parts := strings.Split(answer, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func intersect(want, have []string) []string {
	if len(have) == 0 {
		// No "have" set means we trust the preset as-is (used in tests).
		out := append([]string(nil), want...)
		return out
	}
	haveSet := stringSet(have)
	out := make([]string, 0, len(want))
	for _, w := range want {
		if haveSet[w] {
			out = append(out, w)
		}
	}
	return out
}

// --- prompt helpers ---------------------------------------------------------

// readLine reads one line from r, returning it with the trailing newline
// stripped. EOF returns an empty string with no error so prompts can
// gracefully fall back to defaults.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptString asks for a free-form string. Empty answer → default.
func promptString(r *bufio.Reader, out io.Writer, label, def string) (string, error) {
	if def == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", label, def)
	}
	line, err := readLine(r)
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// promptChoice asks for a value constrained to one of `choices`. The
// answer is matched case-insensitively. Empty input → default. Invalid
// input is rejected and the prompt is repeated up to 3 times before
// returning an error.
func promptChoice(r *bufio.Reader, out io.Writer, label string, choices []string, def string) (string, error) {
	const maxAttempts = 3
	choiceSet := stringSet(choices)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		fmt.Fprintf(out, "%s [%s] (default: %s): ", label, strings.Join(choices, "|"), def)
		line, err := readLine(r)
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" {
			return def, nil
		}
		if choiceSet[line] {
			return line, nil
		}
		fmt.Fprintf(out, "  invalid: %q is not one of %s\n", line, strings.Join(choices, "|"))
	}
	return "", fmt.Errorf("no valid answer for %s after %d attempts", label, maxAttempts)
}
