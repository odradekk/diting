package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odradekk/diting/internal/search"
)

// --- HeuristicScorer tests ---------------------------------------------------

func TestScorer_DomainAuthority(t *testing.T) {
	s := DefaultScorer()
	results := []search.SearchResult{
		{Title: "Go Docs", URL: "https://go.dev/doc/effective_go", Snippet: "effective go"},
		{Title: "Random", URL: "https://random-site.com/page", Snippet: "some content"},
		{Title: "Low Quality", URL: "https://jb51.net/article/123", Snippet: "copy paste"},
	}

	scored := s.Score("golang", results)
	if scored[0].Score <= scored[1].Score {
		t.Errorf("go.dev (%f) should score higher than random (%f)", scored[0].Score, scored[1].Score)
	}
	if scored[1].Score <= scored[2].Score {
		t.Errorf("random (%f) should score higher than jb51 (%f)", scored[1].Score, scored[2].Score)
	}
}

func TestScorer_KeywordOverlap(t *testing.T) {
	s := DefaultScorer()
	results := []search.SearchResult{
		{Title: "Go Concurrency Patterns", URL: "https://a.com", Snippet: "Goroutines and channels in Go for concurrent programming"},
		{Title: "Cooking Recipes", URL: "https://b.com", Snippet: "Best pasta recipes for dinner tonight"},
	}

	scored := s.Score("Go concurrency goroutines", results)
	if scored[0].Score <= scored[1].Score {
		t.Errorf("relevant (%f) should score higher than irrelevant (%f)", scored[0].Score, scored[1].Score)
	}
}

func TestScorer_SnippetLength(t *testing.T) {
	// Snippet-only weights to isolate the signal.
	cfg, err := LoadDefaultScorerConfig()
	if err != nil {
		t.Fatalf("LoadDefaultScorerConfig: %v", err)
	}
	cfg.Weights = SignalWeights{Domain: 0, Keyword: 0, Snippet: 1.0}
	s := NewScorer(cfg)

	results := []search.SearchResult{
		{Title: "Long", URL: "https://a.com", Snippet: "This is a very long snippet that contains more than two hundred characters of useful content about the topic at hand. It provides detailed information that helps the user understand the subject matter thoroughly and completely."},
		{Title: "Short", URL: "https://b.com", Snippet: "Brief."},
		{Title: "Empty", URL: "https://c.com", Snippet: ""},
	}

	scored := s.Score("test", results)
	if scored[0].Score <= scored[1].Score {
		t.Errorf("long (%f) should score higher than short (%f)", scored[0].Score, scored[1].Score)
	}
	if scored[1].Score <= scored[2].Score {
		t.Errorf("short (%f) should score higher than empty (%f)", scored[1].Score, scored[2].Score)
	}
}

// --- domainAuthority tests ---------------------------------------------------

func TestDomainAuthority_KnownDomains(t *testing.T) {
	s := DefaultScorer()
	tests := []struct {
		url  string
		want float64
	}{
		{"https://go.dev/doc", 1.0},
		{"https://arxiv.org/abs/1234", 0.95},
		{"https://github.com/user/repo", 0.90},
		{"https://stackoverflow.com/q/123", 0.85},
		{"https://jb51.net/article", 0.15},
		{"https://blog.csdn.net/user/article", 0.2},
		{"https://unknown-site.com/page", 0.5},
	}
	for _, tt := range tests {
		got := s.domainAuthority(tt.url)
		if got != tt.want {
			t.Errorf("domainAuthority(%q) = %f, want %f", tt.url, got, tt.want)
		}
	}
}

func TestDomainAuthority_DocsSubdomain(t *testing.T) {
	s := DefaultScorer()
	score := s.domainAuthority("https://docs.someproject.io/guide")
	if score != 0.85 {
		t.Errorf("docs.* subdomain = %f, want 0.85", score)
	}
}

// --- tokenize tests ----------------------------------------------------------

func TestTokenize(t *testing.T) {
	s := DefaultScorer()
	words := s.tokenize("How does Go concurrency work?")
	// "how", "does" are stop words; "go", "concurrency", "work" remain
	expected := map[string]bool{"go": true, "concurrency": true, "work": true}
	for _, w := range words {
		if !expected[w] {
			t.Errorf("unexpected token: %q", w)
		}
	}
	if len(words) != 3 {
		t.Errorf("len = %d, want 3", len(words))
	}
}

func TestTokenize_Empty(t *testing.T) {
	s := DefaultScorer()
	words := s.tokenize("")
	if len(words) != 0 {
		t.Errorf("len = %d, want 0", len(words))
	}
}

// --- config loading tests ----------------------------------------------------

func TestLoadDefaultScorerConfig(t *testing.T) {
	cfg, err := LoadDefaultScorerConfig()
	if err != nil {
		t.Fatalf("LoadDefaultScorerConfig: %v", err)
	}
	// Sanity: weights are set.
	if cfg.Weights.Domain == 0 || cfg.Weights.Keyword == 0 || cfg.Weights.Snippet == 0 {
		t.Errorf("zero weights: %+v", cfg.Weights)
	}
	// Sanity: some well-known domains are present.
	if cfg.Domains["go.dev"] != 1.0 {
		t.Errorf("go.dev = %v, want 1.0", cfg.Domains["go.dev"])
	}
	if cfg.Domains["csdn.net"] != 0.2 {
		t.Errorf("csdn.net = %v, want 0.2", cfg.Domains["csdn.net"])
	}
	// Thresholds sorted descending.
	for i := 1; i < len(cfg.SnippetThresholds); i++ {
		if cfg.SnippetThresholds[i-1].Chars <= cfg.SnippetThresholds[i].Chars {
			t.Errorf("thresholds not sorted descending: %+v", cfg.SnippetThresholds)
			break
		}
	}
	// Stop words set built.
	if !cfg.stopWordSet["the"] {
		t.Error("stopWordSet missing 'the'")
	}
}

func TestLoadScorerConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scorer.yaml")
	content := `
weights:
  domain: 0.5
  keyword: 0.3
  snippet: 0.2
snippet_thresholds:
  - chars: 100
    score: 1.0
  - chars: 10
    score: 0.5
default_domain_score: 0.4
docs_subdomain_score: 0.75
domains:
  example.com: 0.9
  badsite.com: 0.1
stop_words:
  - foo
  - bar
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadScorerConfigFromFile(path)
	if err != nil {
		t.Fatalf("LoadScorerConfigFromFile: %v", err)
	}

	if cfg.Weights.Domain != 0.5 {
		t.Errorf("Weights.Domain = %v", cfg.Weights.Domain)
	}
	if cfg.DefaultDomainScore != 0.4 {
		t.Errorf("DefaultDomainScore = %v", cfg.DefaultDomainScore)
	}
	if cfg.DocsSubdomainScore != 0.75 {
		t.Errorf("DocsSubdomainScore = %v", cfg.DocsSubdomainScore)
	}
	if cfg.Domains["example.com"] != 0.9 {
		t.Errorf("example.com = %v, want 0.9", cfg.Domains["example.com"])
	}
	if !cfg.stopWordSet["foo"] {
		t.Error("stopWordSet missing 'foo'")
	}

	// Use the loaded config.
	scorer := NewScorer(cfg)
	if scorer.domainAuthority("https://example.com/page") != 0.9 {
		t.Error("custom domain score not applied")
	}
	if scorer.domainAuthority("https://random.org/page") != 0.4 {
		t.Error("custom default score not applied")
	}
}

func TestLoadScorerConfigFromFile_NotFound(t *testing.T) {
	_, err := LoadScorerConfigFromFile("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestScorerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ScorerConfig
		wantErr bool
	}{
		{
			name:    "all zero weights",
			cfg:     ScorerConfig{},
			wantErr: true,
		},
		{
			name: "negative weight",
			cfg: ScorerConfig{
				Weights: SignalWeights{Domain: -0.1, Keyword: 0.5, Snippet: 0.5},
			},
			wantErr: true,
		},
		{
			name: "domain score out of range",
			cfg: ScorerConfig{
				Weights: SignalWeights{Domain: 0.5, Keyword: 0.5, Snippet: 0.5},
				Domains: map[string]float64{"bad.com": 1.5},
			},
			wantErr: true,
		},
		{
			name: "valid",
			cfg: ScorerConfig{
				Weights:            SignalWeights{Domain: 0.5, Keyword: 0.5, Snippet: 0.5},
				DefaultDomainScore: 0.5,
				DocsSubdomainScore: 0.8,
				Domains:            map[string]float64{"ok.com": 0.9},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestSnippetLengthScore_FromConfig(t *testing.T) {
	s := DefaultScorer()
	tests := []struct {
		name    string
		snippet string
		min     float64
	}{
		{"long (>=200)", string(make([]byte, 250)), 0.9},
		{"medium (>=100)", string(make([]byte, 150)), 0.7},
		{"short (>=50)", string(make([]byte, 75)), 0.4},
		{"tiny", "x", 0.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.snippetLengthScore(tt.snippet)
			if got < tt.min {
				t.Errorf("snippetLengthScore(%d chars) = %f, want >= %f", len(tt.snippet), got, tt.min)
			}
		})
	}
}

func TestSnippetLengthScore_Empty(t *testing.T) {
	s := DefaultScorer()
	if s.snippetLengthScore("") != 0.0 {
		t.Error("empty snippet should score 0")
	}
}
