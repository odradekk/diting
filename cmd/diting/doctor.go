package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/odradekk/diting/internal/config"
	"github.com/odradekk/diting/internal/doctor"
	"github.com/odradekk/diting/internal/search"
)

// ditingVersion is the human-readable version string printed by
// `diting doctor` and `diting version`. Kept in a single place so
// future release builds can override it via -ldflags "-X main.ditingVersion=v2.0.0".
var ditingVersion = "v2.0.0-dev"

// runDoctor implements the `diting doctor` subcommand. It loads the
// config (if one exists), runs every health check, prints a grouped
// report, and exits non-zero if any check FAILed.
//
// Flags:
//
//	--config <path>   Override the config file path (env: DITING_CONFIG)
//
// Intentional design: we NEVER load secrets and NEVER print the values
// of environment variables. The doctor only reports presence/absence.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml (env: DITING_CONFIG)")
	fs.Parse(args)

	path, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	env := doctor.DefaultEnvironment()
	env.Version = ditingVersion
	env.ConfigPath = path
	env.AvailableModules = search.List()

	// Best-effort config load. A missing file is not an error here —
	// the config-file check will surface the absence as a WARN.
	if _, statErr := os.Stat(path); statErr == nil {
		cfg, loadErr := config.Load(path)
		if loadErr != nil {
			env.ConfigLoadErr = loadErr
		} else {
			env.Config = cfg
		}
	}

	report := doctor.RunChecks(env)
	printDoctorReport(os.Stdout, report)

	if report.HasFailures() {
		os.Exit(1)
	}
}

// printDoctorReport renders a Report to the given writer. The output
// is plain ASCII (no ANSI colors) so it's safe to redirect or pipe
// into logs/CI.
//
// Layout:
//
//	== System ==
//	[ OK ] diting version: v2.0.0-dev (linux/amd64, Go 1.23)
//
//	== LLM ==
//	[ OK ] API key: anthropic configured
//
//	== Search ==
//	[ OK ] bing: free, no key required
//	[WARN] brave: BRAVE_API_KEY not set — module will be skipped
//
//	Summary: 8 OK, 3 warn, 0 fail
//
// The function is a method-less helper so tests can call it with a
// bytes.Buffer.
func printDoctorReport(w io.Writer, r *doctor.Report) {
	// Group checks by category while preserving insertion order.
	// RunChecks emits categories in a deterministic sequence, so we
	// just iterate and break on category changes.
	groups := groupByCategory(r.Checks)
	for _, g := range groups {
		fmt.Fprintf(w, "== %s ==\n", g.name)
		for _, c := range g.checks {
			fmt.Fprintf(w, "%s %s: %s\n", c.Status, c.Name, c.Message)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Summary: %d OK, %d warn, %d fail\n",
		r.OKCount, r.WarnCount, r.FailCount)
	if r.HasFailures() {
		fmt.Fprintln(w, "\nFix the [FAIL] items above before running `diting search`.")
	}
}

// categoryGroup is an ordered bucket of checks sharing a category name.
type categoryGroup struct {
	name   string
	checks []doctor.Check
}

// groupByCategory splits a flat slice of checks into per-category
// buckets while preserving the original ordering of both categories
// and checks within each category.
func groupByCategory(checks []doctor.Check) []categoryGroup {
	var groups []categoryGroup
	index := map[string]int{}
	for _, c := range checks {
		cat := c.Category
		if cat == "" {
			cat = "Other"
		}
		if i, ok := index[cat]; ok {
			groups[i].checks = append(groups[i].checks, c)
			continue
		}
		index[cat] = len(groups)
		groups = append(groups, categoryGroup{name: cat, checks: []doctor.Check{c}})
	}
	return groups
}

// sortCategoryNames is unused in production — retained here to document
// that we deliberately do NOT sort categories alphabetically. The
// category order in the report follows RunChecks's emission order
// (System → Config → LLM → Search → Fetch → Cache) so the user sees
// the highest-priority issues first.
var _ = sort.Strings
var _ = strings.Join
