package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/bench/variants"
)

// --- fake variant (package-level so tests can share it) --------------------

// fakeBenchVariant is a test-only bench.Variant that returns a canned
// Result for every query. We use it to exercise the CLI plumbing
// without pulling in real LLM/search/fetch dependencies.
type fakeBenchVariant struct {
	name string
}

func (f *fakeBenchVariant) Name() string { return f.name }
func (f *fakeBenchVariant) Run(_ context.Context, in bench.RunInput) (bench.Result, error) {
	return bench.Result{
		QueryID: in.ID,
		Answer:  "fake answer for " + in.ID,
		// Non-nil citations so the scorer has something to work on —
		// content doesn't matter, we only check the CLI plumbing.
		Citations: []bench.Citation{
			{URL: "https://example.com", Domain: "example.com", Rank: 1},
		},
		Latency: 100 * time.Millisecond,
	}, nil
}

// registerFakeVariant installs a freshly-built fake variant in the
// registry and returns a cleanup that removes it. Multiple tests
// register different names so we never step on each other.
func registerFakeVariant(t *testing.T, name string) {
	t.Helper()
	variants.Register(name, func() (bench.Variant, error) {
		return &fakeBenchVariant{name: name}, nil
	})
}

// tempRepoRoot stages a temporary bench working tree containing the
// real test/bench/queries.yaml (so LoadAndValidate passes) plus an
// empty reports dir. Returns the tempdir path.
func tempRepoRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "test", "bench"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Copy the real query set into place so the loader + validator
	// see a fully-formed 50-query file.
	src, err := os.ReadFile(realQueriesPath())
	if err != nil {
		t.Fatalf("read real query set: %v", err)
	}
	dst := filepath.Join(dir, "test", "bench", "queries.yaml")
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// realQueriesPath returns the filesystem location of the real
// test/bench/queries.yaml committed to the repo. It walks up from
// the current test binary's cwd until it finds the file — this
// makes the test robust to wherever `go test` is invoked from.
func realQueriesPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "test", "bench", "queries.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "test/bench/queries.yaml" // fallback
}

// --- runBenchRun -----------------------------------------------------------

func TestRunBenchRun_EndToEnd(t *testing.T) {
	registerFakeVariant(t, "fake-e2e")

	repo := tempRepoRoot(t)
	reportsDir := filepath.Join(repo, "test", "bench", "reports")

	var stdout, stderr bytes.Buffer
	err := runBenchRun(&stdout, &stderr, []string{
		"--variant", "fake-e2e",
		"--query-set", filepath.Join(repo, "test", "bench", "queries.yaml"),
		"--reports-dir", reportsDir,
		"--concurrency", "8",
		"--per-query-timeout", "5s",
	})
	if err != nil {
		t.Fatalf("runBenchRun: %v\nstdout:\n%s", err, stdout.String())
	}

	out := stdout.String()

	// Progress messages.
	if !strings.Contains(out, "loaded 50 queries") {
		t.Errorf("missing 'loaded 50 queries' in stdout:\n%s", out)
	}
	if !strings.Contains(out, `running variant "fake-e2e"`) {
		t.Errorf("missing running-variant line:\n%s", out)
	}
	if !strings.Contains(out, "wrote report to") {
		t.Errorf("missing 'wrote report to' summary:\n%s", out)
	}
	// Composite summary.
	if !strings.Contains(out, "composite:") {
		t.Errorf("missing composite summary:\n%s", out)
	}

	// Report files should exist with the expected shape: a .md report
	// and a sibling .json dump.
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		t.Fatalf("read reports dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d report files, want 2 (.md + .json)", len(entries))
	}
	var mdName, jsonName string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".md"):
			mdName = e.Name()
		case strings.HasSuffix(e.Name(), ".json"):
			jsonName = e.Name()
		}
	}
	if mdName == "" {
		t.Fatalf("missing .md report among %v", entries)
	}
	if jsonName == "" {
		t.Fatalf("missing .json sibling among %v", entries)
	}
	// Format: YYYY-MM-DD-<variant>-<suffix>.md
	if !dateLikePrefix(mdName) {
		t.Errorf("report filename missing YYYY-MM-DD prefix: %q", mdName)
	}
	if !strings.Contains(mdName, "fake-e2e") {
		t.Errorf("report filename should include variant name: %q", mdName)
	}
	// MD and JSON share the same base.
	if strings.TrimSuffix(mdName, ".md") != strings.TrimSuffix(jsonName, ".json") {
		t.Errorf("md/json bases differ: %q vs %q", mdName, jsonName)
	}

	// MD file should contain markdown — at minimum the variant name.
	mdData, err := os.ReadFile(filepath.Join(reportsDir, mdName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mdData), "fake-e2e") {
		t.Errorf("report markdown missing variant name:\n%s", mdData)
	}

	// JSON dump should be parseable and contain Results for every query.
	jsonData, err := os.ReadFile(filepath.Join(reportsDir, jsonName))
	if err != nil {
		t.Fatal(err)
	}
	var dumped struct {
		Variant string         `json:"Variant"`
		Results []bench.Result `json:"Results"`
	}
	if err := json.Unmarshal(jsonData, &dumped); err != nil {
		t.Fatalf("json dump unparseable: %v", err)
	}
	if dumped.Variant != "fake-e2e" {
		t.Errorf("dumped Variant = %q, want fake-e2e", dumped.Variant)
	}
	if len(dumped.Results) != 50 {
		t.Errorf("dumped Results len = %d, want 50", len(dumped.Results))
	}
}

func TestRunBenchRun_MissingVariantFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runBenchRun(&stdout, &stderr, []string{})
	if err == nil {
		t.Fatal("expected error when --variant is missing")
	}
	if !strings.Contains(err.Error(), "--variant is required") {
		t.Errorf("error: %v", err)
	}
	if !strings.Contains(err.Error(), "registered:") {
		t.Errorf("error should list registered variants: %v", err)
	}
}

func TestRunBenchRun_UnknownVariant(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runBenchRun(&stdout, &stderr, []string{"--variant", "not-a-real-variant"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown variant") {
		t.Errorf("error: %v", err)
	}
	if !strings.Contains(err.Error(), "not-a-real-variant") {
		t.Errorf("error should include the bad name: %v", err)
	}
}

func TestRunBenchRun_MissingQuerySet(t *testing.T) {
	registerFakeVariant(t, "fake-missing-qs")

	var stdout, stderr bytes.Buffer
	err := runBenchRun(&stdout, &stderr, []string{
		"--variant", "fake-missing-qs",
		"--query-set", "/nonexistent/queries.yaml",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "load query set") {
		t.Errorf("error: %v", err)
	}
}

// --- runBenchReport --------------------------------------------------------

func TestRunBenchReport_PrintsNewest(t *testing.T) {
	dir := t.TempDir()
	// Write two reports with lexicographically-distinct names. The one
	// later in the alphabet must win.
	older := filepath.Join(dir, "2024-01-01-aaa.md")
	newer := filepath.Join(dir, "2025-12-31-zzz.md")
	os.WriteFile(older, []byte("# old report\ncontent-old\n"), 0o644)
	os.WriteFile(newer, []byte("# new report\ncontent-new\n"), 0o644)

	var stdout, stderr bytes.Buffer
	if err := runBenchReport(&stdout, &stderr, []string{"--reports-dir", dir}); err != nil {
		t.Fatalf("runBenchReport: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "content-new") {
		t.Errorf("output should contain the newer report body:\n%s", out)
	}
	if strings.Contains(out, "content-old") {
		t.Errorf("output should NOT contain the older report body:\n%s", out)
	}
}

func TestRunBenchReport_NoReports(t *testing.T) {
	dir := t.TempDir() // empty
	var stdout, stderr bytes.Buffer
	err := runBenchReport(&stdout, &stderr, []string{"--reports-dir", dir})
	if err == nil {
		t.Fatal("expected error for empty reports dir")
	}
	if !strings.Contains(err.Error(), "no *.md reports") {
		t.Errorf("error: %v", err)
	}
}

func TestRunBenchReport_MissingDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runBenchReport(&stdout, &stderr, []string{"--reports-dir", "/nonexistent/path"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error: %v", err)
	}
}

func TestRunBenchReport_IgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	// Write a .txt that sorts AFTER the .md — it should NOT be picked.
	os.WriteFile(filepath.Join(dir, "2025-01-01-real.md"), []byte("real markdown"), 0o644)
	os.WriteFile(filepath.Join(dir, "2099-12-31-fake.txt"), []byte("NOT MARKDOWN"), 0o644)

	var stdout, stderr bytes.Buffer
	if err := runBenchReport(&stdout, &stderr, []string{"--reports-dir", dir}); err != nil {
		t.Fatalf("runBenchReport: %v", err)
	}
	if strings.Contains(stdout.String(), "NOT MARKDOWN") {
		t.Errorf("should have ignored .txt file:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "real markdown") {
		t.Errorf("should have picked the real .md:\n%s", stdout.String())
	}
}

// --- reportFilename --------------------------------------------------------

func TestReportFilename_WithCommit(t *testing.T) {
	now := time.Date(2025, 4, 13, 15, 30, 45, 0, time.UTC)
	got := reportFilename("/tmp/reports", "v2-single", "abc1234", now)
	want := "/tmp/reports/2025-04-13-v2-single-abc1234.md"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReportFilename_NoCommit_UsesTimestamp(t *testing.T) {
	now := time.Date(2025, 4, 13, 15, 30, 45, 0, time.UTC)
	got := reportFilename("reports", "v0-baseline", "", now)
	want := "reports/2025-04-13-v0-baseline-153045.md"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReportFilename_DistinctVariantsCoexist guards the original Phase 5.7
// bug: running multiple variants on the same commit silently overwrote
// earlier reports because the filename did not include the variant name.
func TestReportFilename_DistinctVariantsCoexist(t *testing.T) {
	now := time.Date(2025, 4, 13, 15, 30, 45, 0, time.UTC)
	a := reportFilename("/r", "v0-baseline", "abc", now)
	b := reportFilename("/r", "v2-single", "abc", now)
	c := reportFilename("/r", "v2-raw", "abc", now)
	if a == b || b == c || a == c {
		t.Errorf("variant filenames must differ: %q %q %q", a, b, c)
	}
}

// --- newestReport ----------------------------------------------------------

func TestNewestReport_Order(t *testing.T) {
	dir := t.TempDir()
	// Out-of-order insertion to confirm sort, not mtime.
	for _, name := range []string{
		"2025-06-15-bbb.md",
		"2024-01-01-aaa.md",
		"2025-12-31-zzz.md",
		"2025-06-01-ccc.md",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}

	got, err := newestReport(dir)
	if err != nil {
		t.Fatalf("newestReport: %v", err)
	}
	want := filepath.Join(dir, "2025-12-31-zzz.md")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- helpers ---------------------------------------------------------------

// dateLikePrefix returns true if s starts with YYYY-MM-DD-. Used by
// the end-to-end test to verify filename shape without hard-coding
// today's date.
func dateLikePrefix(s string) bool {
	if len(s) < 11 {
		return false
	}
	for i, c := range s[:10] {
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return s[10] == '-'
}
