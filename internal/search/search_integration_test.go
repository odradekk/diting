//go:build integration

// Integration tests for search modules. These hit real endpoints over the
// network.
//
// Run with: go test ./internal/search/ -tags=integration -v -timeout 120s
//
// BYOK modules (brave, serp) are skipped unless the corresponding env var
// is set: BRAVE_API_KEY, SERP_API_KEY.
// GitHub tests run anonymously by default; set GITHUB_TOKEN for higher limits.
package search_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/odradekk/diting/internal/search"
	"github.com/odradekk/diting/internal/search/arxiv"
	"github.com/odradekk/diting/internal/search/baidu"
	"github.com/odradekk/diting/internal/search/bing"
	"github.com/odradekk/diting/internal/search/brave"
	"github.com/odradekk/diting/internal/search/duckduckgo"
	searchgithub "github.com/odradekk/diting/internal/search/github"
	"github.com/odradekk/diting/internal/search/serp"
	"github.com/odradekk/diting/internal/search/stackexchange"
)

func searchAndLog(t *testing.T, mod search.Module, query string, minResults int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	man := mod.Manifest()
	t.Logf("module=%s type=%s cost=%s", man.Name, man.SourceType, man.CostTier)

	results, err := mod.Search(ctx, query)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}

	t.Logf("results=%d", len(results))
	if len(results) < minResults {
		t.Errorf("got %d results, want >= %d", len(results), minResults)
	}

	for i, r := range results {
		if i >= 3 {
			t.Logf("  ... and %d more", len(results)-3)
			break
		}
		t.Logf("  [%d] %s", i+1, r.Title)
		t.Logf("       %s", r.URL)
		if len(r.Snippet) > 100 {
			t.Logf("       %s...", r.Snippet[:100])
		} else {
			t.Logf("       %s", r.Snippet)
		}

		// Basic field validation.
		if r.Title == "" {
			t.Errorf("[%d] Title is empty", i)
		}
		if r.URL == "" {
			t.Errorf("[%d] URL is empty", i)
		}
	}
}

// --- keyless modules (always run) --------------------------------------------

func TestIntegration_Bing(t *testing.T) {
	mod := bing.New(bing.Options{})
	defer mod.Close()
	searchAndLog(t, mod, "Go programming language concurrency", 3)
}

func TestIntegration_DuckDuckGo(t *testing.T) {
	mod := duckduckgo.New(duckduckgo.Options{})
	defer mod.Close()
	searchAndLog(t, mod, "Go programming language concurrency", 3)
}

func TestIntegration_Baidu(t *testing.T) {
	mod := baidu.New(baidu.Options{})
	defer mod.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := mod.Search(ctx, "Go语言并发编程")
	if err != nil {
		// Baidu frequently blocks datacenter IPs with a verification
		// challenge. This is expected in CI — log and skip, don't fail.
		t.Skipf("Baidu blocked (expected for datacenter IPs): %v", err)
	}
	t.Logf("results=%d", len(results))
	if len(results) < 1 {
		t.Errorf("got %d results, want >= 1", len(results))
	}
	for i, r := range results {
		if i >= 3 {
			break
		}
		t.Logf("  [%d] %s — %s", i+1, r.Title, r.URL)
	}
}

func TestIntegration_Arxiv(t *testing.T) {
	mod := arxiv.New(arxiv.Options{})
	defer mod.Close()
	searchAndLog(t, mod, "transformer attention mechanism", 2)
}

func TestIntegration_GitHub(t *testing.T) {
	mod := searchgithub.New(searchgithub.Options{
		Token: os.Getenv("GITHUB_TOKEN"), // optional
	})
	defer mod.Close()
	searchAndLog(t, mod, "golang web framework", 3)
}

func TestIntegration_StackExchange(t *testing.T) {
	mod := stackexchange.New(stackexchange.Options{})
	defer mod.Close()
	searchAndLog(t, mod, "goroutine leak detection", 1)
}

// --- BYOK modules (skip without env var) -------------------------------------

func TestIntegration_Brave(t *testing.T) {
	key := os.Getenv("BRAVE_API_KEY")
	if key == "" {
		t.Skip("BRAVE_API_KEY not set")
	}
	mod := brave.New(brave.Options{APIKey: key})
	defer mod.Close()
	searchAndLog(t, mod, "Go programming language concurrency", 3)
}

func TestIntegration_Serp(t *testing.T) {
	key := os.Getenv("SERP_API_KEY")
	if key == "" {
		t.Skip("SERP_API_KEY not set")
	}
	mod := serp.New(serp.Options{APIKey: key})
	defer mod.Close()
	searchAndLog(t, mod, "Go programming language concurrency", 3)
}

// --- registry completeness ---------------------------------------------------

func TestIntegration_RegistryHasAllModules(t *testing.T) {
	expected := []string{
		"arxiv", "baidu", "bing", "brave",
		"duckduckgo", "github", "serp", "stackexchange",
	}
	names := search.List()
	for _, e := range expected {
		found := false
		for _, n := range names {
			if n == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("module %q not in registry; got %v", e, names)
		}
	}
	t.Logf("registry: %v", names)
}
