package bench

import (
	"fmt"
	"strings"
	"time"
)

// QuerySet is the top-level bench document (mirrors the `batches:` YAML key
// in docs/bench/final/queries.yaml).
type QuerySet struct {
	Batches []Batch `yaml:"batches"`
}

// Batch groups queries by category with shared metadata.
type Batch struct {
	Category    Category  `yaml:"category"`
	Count       int       `yaml:"count"`
	Generator   string    `yaml:"generator"`
	GeneratedAt time.Time `yaml:"generated_at"`
	Notes       string    `yaml:"notes"`
	Queries     []Query   `yaml:"queries"`
}

// Query is one benchmark item with its ground-truth annotation.
type Query struct {
	ID            string      `yaml:"id"`
	Type          Category    `yaml:"type"`
	Query         string      `yaml:"query"`
	Intent        string      `yaml:"intent"`
	Difficulty    Difficulty  `yaml:"difficulty"`
	TechArea      string      `yaml:"tech_area"`
	GroundTruth   GroundTruth `yaml:"ground_truth"`
	ReviewerNotes string      `yaml:"reviewer_notes,omitempty"`
}

// GroundTruth is the machine-checkable annotation used by the scorer.
type GroundTruth struct {
	MustContainDomains  []DomainSpec `yaml:"must_contain_domains"`
	MustContainTerms    []TermSpec   `yaml:"must_contain_terms"`
	ForbiddenDomains    []DomainSpec `yaml:"forbidden_domains"`
	ExpectedSourceTypes []SourceType `yaml:"expected_source_types"`
	CanonicalAnswer     string       `yaml:"canonical_answer"`
}

// DomainSpec is one entry in must_contain_domains or forbidden_domains.
type DomainSpec struct {
	Pattern   string `yaml:"pattern"`
	Rationale string `yaml:"rationale"`
}

// TermSpec is one entry in must_contain_terms.
type TermSpec struct {
	Term      string `yaml:"term"`
	Rationale string `yaml:"rationale"`
}

// Category is the query-type enum (matches batch.category values).
type Category string

const (
	CategoryErrorTroubleshooting Category = "error_troubleshooting"
	CategoryAPIUsage             Category = "api_usage"
	CategoryVersionCompatibility Category = "version_compatibility"
	CategoryConceptExplanation   Category = "concept_explanation"
	CategoryComparison           Category = "comparison"
	CategoryFuzzyRecall          Category = "fuzzy_recall"
	CategoryTimeSensitive        Category = "time_sensitive"
)

// AllCategories returns the full legal set, in the order from architecture
// §12.1.
func AllCategories() []Category {
	return []Category{
		CategoryErrorTroubleshooting,
		CategoryAPIUsage,
		CategoryVersionCompatibility,
		CategoryConceptExplanation,
		CategoryComparison,
		CategoryFuzzyRecall,
		CategoryTimeSensitive,
	}
}

// Valid reports whether c is one of the legal Category values.
func (c Category) Valid() bool {
	switch c {
	case CategoryErrorTroubleshooting,
		CategoryAPIUsage,
		CategoryVersionCompatibility,
		CategoryConceptExplanation,
		CategoryComparison,
		CategoryFuzzyRecall,
		CategoryTimeSensitive:
		return true
	default:
		return false
	}
}

// Difficulty is the difficulty enum.
type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

// Valid reports whether d is one of the legal Difficulty values.
func (d Difficulty) Valid() bool {
	switch d {
	case DifficultyEasy, DifficultyMedium, DifficultyHard:
		return true
	default:
		return false
	}
}

// SourceType is the fetched-source-type enum (matches architecture §3 Search
// Module Layer's source_type values; the bench harness uses them to score
// source-type diversity).
type SourceType string

const (
	SourceGeneralWeb SourceType = "general_web"
	SourceAcademic   SourceType = "academic"
	SourceCode       SourceType = "code"
	SourceCommunity  SourceType = "community"
	SourceDocs       SourceType = "docs"
)

// Valid reports whether s is one of the legal SourceType values.
func (s SourceType) Valid() bool {
	switch s {
	case SourceGeneralWeb, SourceAcademic, SourceCode, SourceCommunity, SourceDocs:
		return true
	default:
		return false
	}
}

// RunInput is the subset of Query handed to a Variant. It intentionally
// excludes GroundTruth so a benchmarked variant cannot read the answer key
// or scoring hints. Phase 5.6 variants receive a RunInput, not a Query.
type RunInput struct {
	ID         string
	Query      string
	Intent     string
	Type       Category
	Difficulty Difficulty
	TechArea   string
}

// AsRunInput returns a RunInput snapshot of q suitable for passing to a
// Variant. GroundTruth and ReviewerNotes are deliberately stripped.
func (q Query) AsRunInput() RunInput {
	return RunInput{
		ID:         q.ID,
		Query:      q.Query,
		Intent:     q.Intent,
		Type:       q.Type,
		Difficulty: q.Difficulty,
		TechArea:   q.TechArea,
	}
}

// Result is what a Variant returns for one query.
type Result struct {
	QueryID   string         `json:"query_id"`
	Citations []Citation     `json:"citations"`
	Answer    string         `json:"answer"`
	Latency   time.Duration  `json:"latency"`
	Cost      float64        `json:"cost"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Citation is one fetched URL in a Result.
type Citation struct {
	URL        string     `json:"url"`
	Domain     string     `json:"domain"`
	SourceType SourceType `json:"source_type"`
	Rank       int        `json:"rank"`
}

// Score is the per-query metric breakdown + composite.
type Score struct {
	QueryID              string
	DomainHitRate        float64
	TermCoverage         float64
	PollutionSuppression float64
	SourceTypeDiversity  float64
	LatencyScore         float64
	CostScore            float64
	Composite            float64
}

// RunReport is the full output of one benchmark run.
type RunReport struct {
	Variant     string
	StartedAt   time.Time
	Duration    time.Duration
	Results     []Result
	Scores      []Score
	CategoryAgg map[Category]AggScore
	Overall     AggScore
}

// AggScore is an aggregated score summary across a set of queries.
type AggScore struct {
	Mean       float64
	P50        float64
	P95        float64
	SampleSize int
	// PerMetric rolls up each metric's mean across the same sample. Keyed by
	// short metric name (e.g. "domain_hit", "term_coverage"). Used by the
	// report template for drill-down tables.
	PerMetric map[string]float64
}

// LoadError is returned by Load on I/O or YAML parse failure.
type LoadError struct {
	Path string
	Err  error
}

func (e *LoadError) Error() string {
	return fmt.Sprintf("bench load %q: %v", e.Path, e.Err)
}

func (e *LoadError) Unwrap() error { return e.Err }

// ValidationError collects every validation issue from a single Validate
// call. Callers get a readable multi-issue error instead of a stream of
// single-issue errors — matches the rubric in
// docs/bench/audit_queries_prompt.md Step 1.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "bench validation: no issues"
	}
	if len(e.Issues) == 1 {
		return "bench validation: " + e.Issues[0]
	}
	return fmt.Sprintf("bench validation: %d issues:\n  - %s",
		len(e.Issues), strings.Join(e.Issues, "\n  - "))
}
