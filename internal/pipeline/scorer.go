package pipeline

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/odradekk/diting/internal/search"
	"gopkg.in/yaml.v3"
)

// ScoredResult wraps a SearchResult with a heuristic relevance score.
type ScoredResult struct {
	search.SearchResult
	Score float64
}

// Scorer computes heuristic relevance scores for search results.
type Scorer interface {
	Score(question string, results []search.SearchResult) []ScoredResult
}

// --- config ------------------------------------------------------------------

//go:embed scorer_config.yaml
var defaultScorerConfigYAML []byte

// ScorerConfig is the user-tunable configuration for HeuristicScorer.
// It can be loaded from the embedded default or from a user-provided YAML file.
type ScorerConfig struct {
	// Weights is the weight assigned to each signal in the final score.
	Weights SignalWeights `yaml:"weights"`

	// SnippetThresholds maps snippet character length to a score. Sorted by
	// chars descending when loaded; first matching threshold wins.
	SnippetThresholds []SnippetThreshold `yaml:"snippet_thresholds"`

	// DefaultDomainScore is returned for hosts not matched by Domains.
	DefaultDomainScore float64 `yaml:"default_domain_score"`

	// DocsSubdomainScore is returned for hosts starting with "docs." that
	// don't have an explicit Domains entry.
	DocsSubdomainScore float64 `yaml:"docs_subdomain_score"`

	// Domains is the domain authority table (host → score).
	Domains map[string]float64 `yaml:"domains"`

	// StopWords is the set of words excluded during tokenization.
	StopWords []string `yaml:"stop_words"`

	// Derived / cached state — populated by prepare().
	stopWordSet map[string]bool `yaml:"-"`
}

// SignalWeights holds the per-signal weights.
type SignalWeights struct {
	Domain  float64 `yaml:"domain"`
	Keyword float64 `yaml:"keyword"`
	Snippet float64 `yaml:"snippet"`
}

// SnippetThreshold is one entry in the snippet length → score map.
type SnippetThreshold struct {
	Chars int     `yaml:"chars"`
	Score float64 `yaml:"score"`
}

// LoadDefaultScorerConfig parses the embedded default scorer config.
func LoadDefaultScorerConfig() (*ScorerConfig, error) {
	return parseScorerConfig(defaultScorerConfigYAML, "embedded default")
}

// LoadScorerConfigFromFile reads and parses a YAML scorer config file.
func LoadScorerConfigFromFile(path string) (*ScorerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scorer: read config %q: %w", path, err)
	}
	return parseScorerConfig(data, path)
}

func parseScorerConfig(data []byte, source string) (*ScorerConfig, error) {
	var cfg ScorerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("scorer: parse %s: %w", source, err)
	}
	cfg.prepare()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("scorer: invalid config %s: %w", source, err)
	}
	return &cfg, nil
}

// prepare computes derived state: sorts thresholds and builds the stop-word set.
func (c *ScorerConfig) prepare() {
	// Sort thresholds by chars descending so iteration finds the highest
	// matching threshold first.
	sort.Slice(c.SnippetThresholds, func(i, j int) bool {
		return c.SnippetThresholds[i].Chars > c.SnippetThresholds[j].Chars
	})

	// Lowercase stop words and build set.
	c.stopWordSet = make(map[string]bool, len(c.StopWords))
	for _, w := range c.StopWords {
		c.stopWordSet[strings.ToLower(strings.TrimSpace(w))] = true
	}

	// Lowercase domain keys (hosts are case-insensitive).
	if len(c.Domains) > 0 {
		lowered := make(map[string]float64, len(c.Domains))
		for k, v := range c.Domains {
			lowered[strings.ToLower(k)] = v
		}
		c.Domains = lowered
	}
}

func (c *ScorerConfig) validate() error {
	w := c.Weights
	if w.Domain == 0 && w.Keyword == 0 && w.Snippet == 0 {
		return fmt.Errorf("all signal weights are zero")
	}
	if w.Domain < 0 || w.Keyword < 0 || w.Snippet < 0 {
		return fmt.Errorf("signal weights must be non-negative")
	}
	if c.DefaultDomainScore < 0 || c.DefaultDomainScore > 1 {
		return fmt.Errorf("default_domain_score out of [0,1]: %v", c.DefaultDomainScore)
	}
	if c.DocsSubdomainScore < 0 || c.DocsSubdomainScore > 1 {
		return fmt.Errorf("docs_subdomain_score out of [0,1]: %v", c.DocsSubdomainScore)
	}
	for host, score := range c.Domains {
		if score < 0 || score > 1 {
			return fmt.Errorf("domain %q score out of [0,1]: %v", host, score)
		}
	}
	return nil
}

// --- HeuristicScorer ---------------------------------------------------------

// HeuristicScorer is the v1 scorer: domain authority + keyword overlap +
// snippet length. Configuration is loaded from a YAML file (see ScorerConfig).
type HeuristicScorer struct {
	cfg *ScorerConfig
}

// NewScorer creates a HeuristicScorer from the given config.
func NewScorer(cfg *ScorerConfig) *HeuristicScorer {
	return &HeuristicScorer{cfg: cfg}
}

// DefaultScorer returns a HeuristicScorer with the embedded default config.
// Panics if the embedded config is malformed — this is a programmer error.
func DefaultScorer() *HeuristicScorer {
	cfg, err := LoadDefaultScorerConfig()
	if err != nil {
		panic(fmt.Sprintf("pipeline: default scorer config invalid: %v", err))
	}
	return NewScorer(cfg)
}

// Score assigns a heuristic score (0.0–1.0) to each result.
func (s *HeuristicScorer) Score(question string, results []search.SearchResult) []ScoredResult {
	qWords := s.tokenize(question)
	scored := make([]ScoredResult, len(results))

	w := s.cfg.Weights

	for i, r := range results {
		domain := s.domainAuthority(r.URL)
		keyword := keywordOverlap(qWords, r.Title, r.Snippet)
		snippet := s.snippetLengthScore(r.Snippet)

		score := w.Domain*domain + w.Keyword*keyword + w.Snippet*snippet

		scored[i] = ScoredResult{
			SearchResult: r,
			Score:        score,
		}
	}

	return scored
}

// --- domain authority --------------------------------------------------------

// domainAuthority returns a 0.0–1.0 score based on the domain's reputation,
// driven by the scorer config.
func (s *HeuristicScorer) domainAuthority(rawURL string) float64 {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return s.cfg.DefaultDomainScore
	}
	host := strings.ToLower(u.Host)

	// Exact match.
	if score, ok := s.cfg.Domains[host]; ok {
		return score
	}

	// Suffix match (e.g., any ".github.com" subdomain).
	for domain, score := range s.cfg.Domains {
		if strings.HasSuffix(host, "."+domain) {
			return score
		}
	}

	// Heuristic: docs.* subdomains get a boost.
	if strings.HasPrefix(host, "docs.") {
		return s.cfg.DocsSubdomainScore
	}

	return s.cfg.DefaultDomainScore
}

// --- keyword overlap ---------------------------------------------------------

func keywordOverlap(qWords []string, title, snippet string) float64 {
	if len(qWords) == 0 {
		return 0
	}
	combined := strings.ToLower(title + " " + snippet)
	hits := 0
	for _, w := range qWords {
		if strings.Contains(combined, w) {
			hits++
		}
	}
	return float64(hits) / float64(len(qWords))
}

// --- snippet length ----------------------------------------------------------

// snippetLengthScore uses the config's threshold table. Thresholds are
// already sorted by chars descending (see prepare()).
func (s *HeuristicScorer) snippetLengthScore(snippet string) float64 {
	n := len(snippet)
	if n == 0 {
		return 0.0
	}
	for _, t := range s.cfg.SnippetThresholds {
		if n >= t.Chars {
			return t.Score
		}
	}
	return 0.0
}

// --- tokenization ------------------------------------------------------------

// tokenize splits text into normalized tokens, filtering stop words and
// very short tokens. Stop words come from the scorer config.
func (s *HeuristicScorer) tokenize(text string) []string {
	return tokenizeWithStopWords(text, s.cfg.stopWordSet)
}

func tokenizeWithStopWords(text string, stopWords map[string]bool) []string {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) >= 2 && !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}
