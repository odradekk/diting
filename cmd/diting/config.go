package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/odradekk/diting/internal/config"
	"github.com/odradekk/diting/internal/search"
)

// knownFetchLayers is the authoritative list of fetch layer names for
// config validation. It matches the LayerName constants in
// internal/fetch/<layer>/.
var knownFetchLayers = []string{"utls", "chromedp", "jina", "archive", "tavily"}

// runConfig dispatches to the show/path/validate subcommands.
func runConfig(args []string) {
	if len(args) == 0 {
		printConfigUsage(os.Stderr)
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]

	// No flag-reorder hack here. The reorder in runSearch/runFetch groups
	// all "-flags" before positionals, but it only works for bool flags
	// or `--flag=value` syntax — string flags using `--flag value` get
	// separated from their values. The config subcommand takes no
	// positional arguments after the subcommand name, so reordering would
	// be both unnecessary and buggy if it were applied.
	fs := flag.NewFlagSet("config "+sub, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml (env: DITING_CONFIG)")
	fs.Parse(rest)

	path, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	switch sub {
	case "path":
		if err := runConfigPath(os.Stdout, path); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "show":
		if err := runConfigShow(os.Stdout, path); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "validate":
		if err := runConfigValidate(os.Stdout, path); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n\n", sub)
		printConfigUsage(os.Stderr)
		os.Exit(1)
	}
}

func printConfigUsage(w io.Writer) {
	fmt.Fprintln(w, `usage: diting config <subcommand> [--config path]

Subcommands:
  path       Print the resolved config file path
  show       Print the effective config with secrets redacted
  validate   Validate the config file structure

Flags:
  --config   Override the config file path (env: DITING_CONFIG)`)
}

// resolveConfigPath determines the config file path from (in order):
//   1. explicit --config flag
//   2. $DITING_CONFIG environment variable (handled by DefaultPath)
//   3. ~/.config/diting/config.yaml default
func resolveConfigPath(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	return config.DefaultPath()
}

// runConfigPath prints the resolved config path plus an existence marker
// so the user knows whether the file is actually there.
func runConfigPath(w io.Writer, path string) error {
	fmt.Fprintln(w, path)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(w, "(file does not exist — `diting init` can create one)")
		} else {
			fmt.Fprintf(w, "(stat error: %v)\n", err)
		}
	} else {
		fmt.Fprintln(w, "(exists)")
	}
	return nil
}

// runConfigShow prints the effective config as YAML with secrets redacted.
// If the file does not exist, the built-in defaults are shown instead
// (with a heading note).
func runConfigShow(w io.Writer, path string) error {
	var cfg *config.Config

	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		loaded, err := config.Load(path)
		if err != nil {
			return err
		}
		cfg = loaded
		fmt.Fprintf(w, "# effective config from %s (secrets redacted)\n", path)
	case os.IsNotExist(statErr):
		cfg = config.Default()
		fmt.Fprintf(w, "# no config file at %s\n", path)
		fmt.Fprintln(w, "# showing built-in defaults (secrets redacted)")
	default:
		return fmt.Errorf("stat %q: %w", path, statErr)
	}

	data, err := cfg.Redact().Marshal()
	if err != nil {
		return fmt.Errorf("marshal redacted config: %w", err)
	}
	_, err = w.Write(data)
	return err
}

// runConfigValidate loads the config and runs structural validation. On
// success it prints "OK" and returns nil. On failure it returns a
// multi-line error that the caller prints to stderr.
func runConfigValidate(w io.Writer, path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no config file at %s", path)
		}
		return err
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	opts := config.ValidateOptions{
		KnownModules:     search.List(),
		KnownFetchLayers: knownFetchLayers,
	}
	if err := cfg.Validate(opts); err != nil {
		return err
	}

	fmt.Fprintf(w, "OK: %s is valid\n", path)
	return nil
}
