package bench

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Metric weights from architecture §12.3.
const (
	WeightDomainHit       = 0.30
	WeightTermCoverage    = 0.25
	WeightPollution       = 0.15
	WeightSourceDiversity = 0.10
	WeightLatency         = 0.10
	WeightCost            = 0.10
)

// Default scorer settings.
const (
	DefaultTopK          = 5
	DefaultLatencyBudget = 90 * time.Second
	DefaultCostBudget    = 0.50 // USD per query; placeholder, caller can override
)

// Metric-name keys used by AggScore.PerMetric and the report drill-down.
const (
	MetricDomainHit       = "domain_hit"
	MetricTermCoverage    = "term_coverage"
	MetricPollution       = "pollution_suppression"
	MetricSourceDiversity = "source_diversity"
	MetricLatency         = "latency"
	MetricCost            = "cost"
)

// Scorer is the configurable scoring engine.
type Scorer struct {
	TopK          int
	LatencyBudget time.Duration
	CostBudget    float64
}

// NewScorer returns a Scorer with architecture-default values.
func NewScorer() *Scorer {
	return &Scorer{
		TopK:          DefaultTopK,
		LatencyBudget: DefaultLatencyBudget,
		CostBudget:    DefaultCostBudget,
	}
}

// Score computes per-metric + composite score for a single Result.
func (s *Scorer) Score(q Query, r Result) Score {
	topK := r.topK(s.TopK)
	sc := Score{QueryID: q.ID}
	sc.DomainHitRate = domainHitRate(q.GroundTruth.MustContainDomains, topK)
	sc.TermCoverage = termCoverage(q.GroundTruth.MustContainTerms, r.Answer)
	sc.PollutionSuppression = pollutionSuppression(q.GroundTruth.ForbiddenDomains, topK)
	sc.SourceTypeDiversity = sourceTypeDiversity(q.GroundTruth.ExpectedSourceTypes, topK)
	sc.LatencyScore = latencyScore(r.Latency, s.LatencyBudget)
	sc.CostScore = costScore(r.Cost, s.CostBudget)
	sc.Composite = compositeScore(sc)
	return sc
}

// Aggregate rolls up a slice of Scores into an AggScore. Mean / P50 / P95 are
// computed on the Composite field; PerMetric averages each component metric
// across the same sample so the report can show drill-downs.
func (s *Scorer) Aggregate(scores []Score) AggScore {
	agg := AggScore{
		SampleSize: len(scores),
		PerMetric:  map[string]float64{},
	}
	if len(scores) == 0 {
		return agg
	}

	composites := make([]float64, len(scores))
	var sumComposite float64
	var sumDomain, sumTerm, sumPollution, sumDiversity, sumLatency, sumCost float64
	for i, sc := range scores {
		composites[i] = sc.Composite
		sumComposite += sc.Composite
		sumDomain += sc.DomainHitRate
		sumTerm += sc.TermCoverage
		sumPollution += sc.PollutionSuppression
		sumDiversity += sc.SourceTypeDiversity
		sumLatency += sc.LatencyScore
		sumCost += sc.CostScore
	}
	n := float64(len(scores))
	agg.Mean = sumComposite / n
	agg.PerMetric[MetricDomainHit] = sumDomain / n
	agg.PerMetric[MetricTermCoverage] = sumTerm / n
	agg.PerMetric[MetricPollution] = sumPollution / n
	agg.PerMetric[MetricSourceDiversity] = sumDiversity / n
	agg.PerMetric[MetricLatency] = sumLatency / n
	agg.PerMetric[MetricCost] = sumCost / n

	sort.Float64s(composites)
	agg.P50 = percentile(composites, 0.50)
	agg.P95 = percentile(composites, 0.95)
	return agg
}

// percentile returns the value at p in a pre-sorted sample. Uses
// nearest-rank: the smallest sample index i such that (i+1)/n >= p.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// PopulateScores fills in Scores, CategoryAgg, and Overall on r by scoring
// every Result against its corresponding Query in qset. Results whose
// QueryID is not found in qset are silently skipped (the runner only emits
// IDs that came from qset, so this should not happen in practice).
func (r *RunReport) PopulateScores(scorer *Scorer, qset *QuerySet) {
	if r == nil || scorer == nil || qset == nil {
		return
	}
	// index queries by ID for O(1) lookup
	idx := make(map[string]Query, qset.TotalQueries())
	// also track which category each ID belongs to
	catOf := make(map[string]Category, qset.TotalQueries())
	for _, b := range qset.Batches {
		for _, q := range b.Queries {
			idx[q.ID] = q
			catOf[q.ID] = b.Category
		}
	}

	r.Scores = r.Scores[:0]
	scoresByCat := map[Category][]Score{}
	for _, res := range r.Results {
		q, ok := idx[res.QueryID]
		if !ok {
			continue
		}
		sc := scorer.Score(q, res)
		r.Scores = append(r.Scores, sc)
		cat := catOf[res.QueryID]
		scoresByCat[cat] = append(scoresByCat[cat], sc)
	}

	r.CategoryAgg = make(map[Category]AggScore, len(scoresByCat))
	for cat, scs := range scoresByCat {
		r.CategoryAgg[cat] = scorer.Aggregate(scs)
	}
	r.Overall = scorer.Aggregate(r.Scores)
}

// domainSuffixMatch reports whether citationDomain matches pattern using a
// suffix check with a dot boundary: "github.com" matches "github.com" and
// "api.github.com" but NOT "github.com.evil".
func domainSuffixMatch(pattern, citationDomain string) bool {
	if pattern == "" || citationDomain == "" {
		return false
	}
	p := strings.ToLower(pattern)
	c := strings.ToLower(citationDomain)
	return c == p || strings.HasSuffix(c, "."+p)
}

// domainHitRate returns |patterns that match at least one citation domain| /
// |patterns|. Returns 1.0 when gt is empty (nothing required → nothing can
// miss), so this metric is neutral for queries without domain constraints.
// Duplicate patterns are deduped (case-insensitive) so the denominator
// reflects distinct requirements.
func domainHitRate(gt []DomainSpec, topK []Citation) float64 {
	patterns := uniqueDomainPatterns(gt)
	if len(patterns) == 0 {
		return 1.0
	}
	hits := 0
	for _, p := range patterns {
		for _, c := range topK {
			if domainSuffixMatch(p, c.Domain) {
				hits++
				break
			}
		}
	}
	return float64(hits) / float64(len(patterns))
}

// termCoverage returns |required terms found in answer (case-insensitive)| /
// |required|. Returns 1.0 when gt is empty. Returns 0 when answer is empty
// and at least one term is required. Duplicate terms are deduped
// (case-insensitive) so the denominator reflects distinct requirements.
func termCoverage(gt []TermSpec, answer string) float64 {
	terms := uniqueTerms(gt)
	if len(terms) == 0 {
		return 1.0
	}
	if answer == "" {
		return 0
	}
	lower := strings.ToLower(answer)
	hits := 0
	for _, t := range terms {
		if strings.Contains(lower, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}

// pollutionSuppression returns 1 - (|topK citations matching any forbidden
// pattern| / max(len(topK), 1)), clamped to [0, 1]. Returns 1.0 when topK is
// empty (nothing to pollute). Duplicate forbidden patterns are deduped so a
// pattern listed twice is not counted twice.
func pollutionSuppression(gt []DomainSpec, topK []Citation) float64 {
	if len(topK) == 0 {
		return 1.0
	}
	patterns := uniqueDomainPatterns(gt)
	if len(patterns) == 0 {
		return 1.0
	}
	polluted := 0
	for _, c := range topK {
		for _, p := range patterns {
			if domainSuffixMatch(p, c.Domain) {
				polluted++
				break
			}
		}
	}
	ratio := float64(polluted) / float64(len(topK))
	score := 1.0 - ratio
	return clamp01(score)
}

// sourceTypeDiversity returns |distinct source types in topK that appear in
// expected| / |distinct expected|. Returns 1.0 when expected is empty.
// Duplicate expected source types are deduped so the denominator reflects
// distinct requirements.
func sourceTypeDiversity(expected []SourceType, topK []Citation) float64 {
	if len(expected) == 0 {
		return 1.0
	}
	want := make(map[SourceType]struct{}, len(expected))
	for _, st := range expected {
		want[st] = struct{}{}
	}
	if len(want) == 0 {
		return 1.0
	}
	seen := make(map[SourceType]struct{}, len(want))
	for _, c := range topK {
		if _, ok := want[c.SourceType]; ok {
			seen[c.SourceType] = struct{}{}
		}
	}
	return float64(len(seen)) / float64(len(want))
}

// latencyScore returns 1 - min(latency/budget, 1). Returns 1.0 when budget is
// zero (no penalty applied).
func latencyScore(latency, budget time.Duration) float64 {
	if budget <= 0 {
		return 1.0
	}
	if latency <= 0 {
		return 1.0
	}
	ratio := float64(latency) / float64(budget)
	if ratio > 1 {
		ratio = 1
	}
	return 1.0 - ratio
}

// costScore returns 1 - min(cost/budget, 1). Returns 1.0 when budget is zero.
func costScore(cost, budget float64) float64 {
	if budget <= 0 {
		return 1.0
	}
	if cost <= 0 {
		return 1.0
	}
	ratio := cost / budget
	if ratio > 1 {
		ratio = 1
	}
	return 1.0 - ratio
}

// compositeScore is the weighted sum × 100, clamped to [0, 100].
func compositeScore(s Score) float64 {
	sum := WeightDomainHit*s.DomainHitRate +
		WeightTermCoverage*s.TermCoverage +
		WeightPollution*s.PollutionSuppression +
		WeightSourceDiversity*s.SourceTypeDiversity +
		WeightLatency*s.LatencyScore +
		WeightCost*s.CostScore
	score := sum * 100
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// uniqueDomainPatterns returns the deduped, lowercased, trimmed patterns
// from gt, skipping empty entries.
func uniqueDomainPatterns(gt []DomainSpec) []string {
	if len(gt) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(gt))
	out := make([]string, 0, len(gt))
	for _, d := range gt {
		p := strings.ToLower(strings.TrimSpace(d.Pattern))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// uniqueTerms returns the deduped, lowercased, trimmed terms from gt,
// skipping empty entries.
func uniqueTerms(gt []TermSpec) []string {
	if len(gt) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(gt))
	out := make([]string, 0, len(gt))
	for _, t := range gt {
		term := strings.ToLower(strings.TrimSpace(t.Term))
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// topK returns up to k Citations from r sorted by Rank ascending. Citations
// with zero or negative Rank are treated as tied at the end, preserving their
// input order relative to each other.
func (r Result) topK(k int) []Citation {
	if k <= 0 || len(r.Citations) == 0 {
		return nil
	}
	sorted := make([]Citation, len(r.Citations))
	copy(sorted, r.Citations)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Rank, sorted[j].Rank
		// push zero/negative ranks to the end
		aEnd := a <= 0
		bEnd := b <= 0
		if aEnd && !bEnd {
			return false
		}
		if !aEnd && bEnd {
			return true
		}
		if aEnd && bEnd {
			return false
		}
		return a < b
	})
	if k > len(sorted) {
		k = len(sorted)
	}
	return sorted[:k]
}
