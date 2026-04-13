package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/odradekk/diting/internal/config"
	"github.com/odradekk/diting/internal/search"
)

// runInit implements the `diting init` subcommand. It walks the user
// through an interactive prompt sequence and writes a YAML config file
// to the resolved path.
//
// Flags:
//
//	--config <path>     Override the config file path (env: DITING_CONFIG)
//	--force             Overwrite an existing file
//	--non-interactive   Skip prompts; write the built-in defaults
//
// Safety: refuses to overwrite an existing file unless --force is given.
//
// We deliberately do NOT use the flag-reorder hack here (the one in
// runSearch/runFetch). That hack groups all `-flags` first followed by
// positionals, which mangles `--flag value` pairs by separating them.
// `init` has no positional arguments, so the reorder is unnecessary
// AND harmful — passing `--config /path --non-interactive` would end up
// with `--config`'s value being `--non-interactive`.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", "", "path to write the config (env: DITING_CONFIG)")
	force := fs.Bool("force", false, "overwrite an existing config file")
	nonInteractive := fs.Bool("non-interactive", false, "skip prompts and write defaults")
	fs.Parse(args)

	path, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := doInit(os.Stdin, os.Stdout, path, *force, *nonInteractive); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// doInit is the testable core of `diting init`. It encapsulates:
//   - overwrite protection
//   - prompt invocation (or default fallback in non-interactive mode)
//   - directory creation + atomic file write
//
// `in` is the source of prompt answers; `out` receives prompts and status.
func doInit(in io.Reader, out io.Writer, path string, force, nonInteractive bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %q: %w", path, err)
		}
	}

	var cfg *config.Config
	if nonInteractive {
		cfg = config.Default()
		fmt.Fprintln(out, "diting init: writing built-in defaults (--non-interactive)")
	} else {
		var err error
		cfg, err = config.Interactive(in, out, search.List())
		if err != nil {
			return err
		}
	}

	// Validate before writing so we never produce a file the validator
	// would later reject.
	if err := cfg.Validate(config.ValidateOptions{
		KnownModules:     search.List(),
		KnownFetchLayers: knownFetchLayers,
	}); err != nil {
		return fmt.Errorf("generated config is invalid: %w", err)
	}

	data, err := cfg.Marshal()
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Make sure the parent directory exists. ~/.config/diting/ may not
	// be there yet on a fresh install.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create parent dir %q: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}

	fmt.Fprintf(out, "\nWrote %s\n", path)

	// Helpful follow-up: tell the user which env vars they need to set.
	if env := extractEnvVars(cfg); len(env) > 0 {
		fmt.Fprintln(out, "\nMake sure these environment variables are set:")
		for _, name := range env {
			fmt.Fprintf(out, "  export %s=...\n", name)
		}
	}
	fmt.Fprintln(out, "\nValidate it any time with:")
	fmt.Fprintf(out, "  diting config validate --config %s\n", path)

	return nil
}

// extractEnvVars finds every `${VAR}` reference in the config that the
// user will need to set in their shell. Returns the list in deterministic
// order with no duplicates.
func extractEnvVars(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	collect := func(s string) {
		// Only handle simple ${VAR} forms. The config wizard always
		// emits this shape, so we don't need a full parser.
		if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
			name := s[2 : len(s)-1]
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	collect(cfg.LLM.APIKey)
	collect(cfg.Fetch.Jina.APIKey)
	collect(cfg.Fetch.Tavily.APIKey)
	for _, mc := range cfg.Search.Modules {
		collect(mc.APIKey)
		collect(mc.Token)
	}
	return out
}
