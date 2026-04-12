package bench

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testDataDir returns the repo root so tests can reach docs/bench/final and
// test/bench/testdata without depending on the caller's cwd.
func testDataDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/bench/ → repo root
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func realQueriesPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(testDataDir(t), "docs", "bench", "final", "queries.yaml")
}

func TestLoad_ReadsRealQueriesYAML(t *testing.T) {
	qs, err := Load(realQueriesPath(t))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if qs == nil {
		t.Fatal("Load returned nil QuerySet")
	}
	if len(qs.Batches) != 7 {
		t.Errorf("len(Batches) = %d, want 7", len(qs.Batches))
	}
	if got := qs.TotalQueries(); got != 50 {
		t.Errorf("TotalQueries = %d, want 50", got)
	}
}

func TestLoad_ReturnsLoadErrorOnMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error on missing file, got nil")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LoadError: %T %v", err, err)
	}
	if le.Path == "" {
		t.Errorf("LoadError.Path empty")
	}
	if le.Unwrap() == nil {
		t.Errorf("LoadError.Unwrap() is nil")
	}
}

func TestLoad_ReturnsLoadErrorOnMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	// unterminated quote, invalid YAML
	if err := os.WriteFile(path, []byte("batches: [ { category: \"oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LoadError: %T %v", err, err)
	}
}

func TestLoadAndValidate_PassesOnRealDataset(t *testing.T) {
	qs, err := LoadAndValidate(realQueriesPath(t))
	if err != nil {
		t.Fatalf("LoadAndValidate: %v", err)
	}
	if qs.TotalQueries() != 50 {
		t.Errorf("TotalQueries = %d, want 50", qs.TotalQueries())
	}
}

func TestQuerySet_FindByID(t *testing.T) {
	qs, err := Load(realQueriesPath(t))
	if err != nil {
		t.Fatal(err)
	}
	q, bi := qs.FindByID("et_001")
	if q == nil {
		t.Fatal("et_001 not found")
	}
	if bi != 0 {
		t.Errorf("batch index = %d, want 0 (error_troubleshooting is first)", bi)
	}
	if q.Type != CategoryErrorTroubleshooting {
		t.Errorf("et_001.Type = %q, want %q", q.Type, CategoryErrorTroubleshooting)
	}

	if q2, bi2 := qs.FindByID("no_such_id"); q2 != nil || bi2 != -1 {
		t.Errorf("FindByID(unknown) = (%v, %d), want (nil, -1)", q2, bi2)
	}
}
