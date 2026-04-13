package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/bench/variants"
)

// Default locations for bench artefacts. Paths are resolved relative to
// the current working directory — when `diting bench run` is invoked
// from the repo root, these resolve correctly against the committed
// query set and report directory.
const (
	defaultQuerySetPath = "test/bench/queries.yaml"
	defaultReportsDir   = "test/bench/reports"
)

// runBench dispatches the `diting bench <subcommand>` tree.
//
// Subcommands:
//
//	run     — execute a registered variant, score, write markdown report
//	report  — print the newest report in test/bench/reports/ to stdout
func runBench(args []string) {
	if len(args) == 0 {
		printBenchUsage(os.Stderr)
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "run":
		if err := runBenchRun(os.Stdout, os.Stderr, rest); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "report":
		if err := runBenchReport(os.Stdout, os.Stderr, rest); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown bench subcommand: %s\n\n", sub)
		printBenchUsage(os.Stderr)
		os.Exit(1)
	}
}

func printBenchUsage(w io.Writer) {
	registered := variants.List()
	regText := "(none registered — Phase 5.6 will ship the first variants)"
	if len(registered) > 0 {
		regText = strings.Join(registered, ", ")
	}
	fmt.Fprintf(w, `usage: diting bench <subcommand>

Subcommands:
  run       Execute a benchmark variant and write a markdown report
  report    Print the newest report in %s to stdout

Flags for run:
  --variant <name>    Name of the registered variant (required)
  --query-set <path>  Path to the query YAML (default: %s)
  --reports-dir <dir> Directory to write the report (default: %s)
  --concurrency <n>   Per-variant parallelism (default: 4)
  --per-query-timeout <d>  Timeout per query (default: 300s)

Registered variants: %s
`, defaultReportsDir, defaultQuerySetPath, defaultReportsDir, regText)
}

// --- bench run --------------------------------------------------------------

// runBenchRun loads the query set, resolves the requested variant,
// executes the full runner + scorer + reporter pipeline, and writes
// a markdown report to disk. It returns any error it encountered so
// the caller can print it and exit non-zero.
//
// `out` receives progress / success messages for the user. `errw` is
// reserved for future structured diagnostics (currently unused —
// errors bubble up via the return value).
func runBenchRun(out, _ io.Writer, args []string) error {
	fs := flag.NewFlagSet("bench run", flag.ContinueOnError)
	variantName := fs.String("variant", "", "name of the variant to run (required)")
	querySetPath := fs.String("query-set", defaultQuerySetPath, "path to query YAML")
	reportsDir := fs.String("reports-dir", defaultReportsDir, "directory for the rendered report")
	concurrency := fs.Int("concurrency", 4, "per-variant parallelism")
	perQueryTimeout := fs.Duration("per-query-timeout", 300*time.Second, "timeout per query")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *variantName == "" {
		return fmt.Errorf("--variant is required (registered: %s)",
			formatRegisteredVariants())
	}

	// --- Resolve variant from registry ---
	factory, err := variants.Get(*variantName)
	if err != nil {
		return fmt.Errorf("%w (registered: %s)", err, formatRegisteredVariants())
	}
	v, err := factory()
	if err != nil {
		return fmt.Errorf("construct variant %q: %w", *variantName, err)
	}

	// --- Load query set ---
	qset, err := bench.LoadAndValidate(*querySetPath)
	if err != nil {
		return fmt.Errorf("load query set %s: %w", *querySetPath, err)
	}
	fmt.Fprintf(out, "diting bench: loaded %d queries from %s\n", qset.TotalQueries(), *querySetPath)

	// --- Run the harness ---
	runner := bench.NewRunner(v,
		bench.WithConcurrency(*concurrency),
		bench.WithPerQueryTimeout(*perQueryTimeout),
	)
	fmt.Fprintf(out, "diting bench: running variant %q (concurrency=%d, timeout=%s)\n",
		*variantName, *concurrency, *perQueryTimeout)

	report, err := runner.Run(context.Background(), qset)
	if err != nil {
		return fmt.Errorf("runner.Run: %w", err)
	}

	// --- Score ---
	scorer := bench.NewScorer()
	report.PopulateScores(scorer, qset)

	// --- Render markdown ---
	// Resolve git commit here (NOT inside internal/bench — the library
	// layer deliberately stays pure per architecture §14 Phase 4.10).
	commit := gitCommitHash()
	reporter := &bench.Reporter{CommitHash: commit}
	md, err := reporter.Markdown(report)
	if err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}

	// --- Write to disk ---
	if err := os.MkdirAll(*reportsDir, 0o755); err != nil {
		return fmt.Errorf("create reports dir %s: %w", *reportsDir, err)
	}
	outPath := reportFilename(*reportsDir, *variantName, commit, time.Now())
	if err := os.WriteFile(outPath, md, 0o644); err != nil {
		return fmt.Errorf("write report %s: %w", outPath, err)
	}

	// Also dump the raw RunReport as JSON next to the markdown so per-query
	// Metadata (errors, token counts) survives the run and can be inspected
	// programmatically. The markdown is for humans; the JSON is for grep, jq,
	// and follow-up analysis. Failure to write it is non-fatal — the report
	// markdown is the primary artefact.
	jsonPath := strings.TrimSuffix(outPath, ".md") + ".json"
	if jsonBytes, jerr := json.MarshalIndent(report, "", "  "); jerr == nil {
		_ = os.WriteFile(jsonPath, jsonBytes, 0o644)
	}

	// --- Summary line ---
	fmt.Fprintf(out, "diting bench: wrote report to %s\n", outPath)
	fmt.Fprintf(out, "  variant:   %s\n", report.Variant)
	fmt.Fprintf(out, "  duration:  %s\n", report.Duration.Round(time.Millisecond))
	fmt.Fprintf(out, "  composite: %.1f (p50=%.1f p95=%.1f, n=%d)\n",
		report.Overall.Mean, report.Overall.P50, report.Overall.P95, report.Overall.SampleSize)
	if commit == "" {
		fmt.Fprintln(out, "  commit:    <git not available>")
	} else {
		fmt.Fprintf(out, "  commit:    %s\n", commit)
	}
	return nil
}

// formatRegisteredVariants returns a comma-separated list for error
// messages, or "none" when the registry is empty.
func formatRegisteredVariants() string {
	list := variants.List()
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// reportFilename builds the output path for a bench report:
//
//	<reportsDir>/YYYY-MM-DD-<variant>-<commit>.md
//
// The variant name is included so running multiple variants on the same
// commit doesn't silently overwrite earlier reports. When commit is empty
// (git not available), falls back to a timestamp suffix so repeated runs
// of the same variant on the same day don't collide.
func reportFilename(reportsDir, variant, commit string, now time.Time) string {
	date := now.Format("2006-01-02")
	suffix := commit
	if suffix == "" {
		suffix = now.Format("150405") // HHMMSS fallback
	}
	return filepath.Join(reportsDir, fmt.Sprintf("%s-%s-%s.md", date, variant, suffix))
}

// gitCommitHash returns the short commit hash of the current HEAD,
// or "" if git is unavailable / not in a repository / fails for any
// reason. We never fail the benchmark run over missing git — the
// commit is a nice-to-have for provenance, not a correctness signal.
func gitCommitHash() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// --- bench report -----------------------------------------------------------

// runBenchReport prints the newest report in the reports directory
// to stdout. "Newest" is determined by filename — our filenames start
// with YYYY-MM-DD so lexicographic sort === chronological sort.
func runBenchReport(out, _ io.Writer, args []string) error {
	fs := flag.NewFlagSet("bench report", flag.ContinueOnError)
	reportsDir := fs.String("reports-dir", defaultReportsDir, "directory containing bench reports")
	if err := fs.Parse(args); err != nil {
		return err
	}

	newest, err := newestReport(*reportsDir)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(newest)
	if err != nil {
		return fmt.Errorf("read %s: %w", newest, err)
	}

	fmt.Fprintln(out, string(data))
	fmt.Fprintf(os.Stderr, "\nditing bench report: %s\n", newest)
	return nil
}

// newestReport scans `dir` for *.md files and returns the path to the
// one with the lexicographically-latest name. Returns an error if the
// directory is empty or doesn't exist.
func newestReport(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("reports directory %s does not exist (run `diting bench run` first)", dir)
		}
		return "", fmt.Errorf("read reports dir %s: %w", dir, err)
	}

	var reports []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".md") {
			reports = append(reports, name)
		}
	}
	if len(reports) == 0 {
		return "", fmt.Errorf("no *.md reports in %s (run `diting bench run` first)", dir)
	}

	sort.Strings(reports)
	return filepath.Join(dir, reports[len(reports)-1]), nil
}
