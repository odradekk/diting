package bench

import (
	"math"
	"testing"
	"time"
)

func approxEqual(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

func TestDomainSuffixMatch_ExactAndSubdomain(t *testing.T) {
	if !domainSuffixMatch("github.com", "github.com") {
		t.Error("exact match should hit")
	}
	if !domainSuffixMatch("github.com", "api.github.com") {
		t.Error("subdomain should hit")
	}
	if !domainSuffixMatch("GITHUB.com", "api.github.COM") {
		t.Error("case-insensitive match should hit")
	}
}

func TestDomainSuffixMatch_RejectsSuffixConfusion(t *testing.T) {
	if domainSuffixMatch("github.com", "github.com.evil") {
		t.Error("github.com.evil must not match github.com")
	}
	if domainSuffixMatch("github.com", "notgithub.com") {
		t.Error("notgithub.com must not match github.com")
	}
	if domainSuffixMatch("github.com", "") {
		t.Error("empty domain must not match")
	}
	if domainSuffixMatch("", "github.com") {
		t.Error("empty pattern must not match")
	}
}

func TestDomainHitRate_PerfectHit(t *testing.T) {
	gt := []DomainSpec{{Pattern: "a.com"}, {Pattern: "b.com"}}
	topK := []Citation{
		{Domain: "a.com", Rank: 1},
		{Domain: "api.b.com", Rank: 2},
	}
	if got := domainHitRate(gt, topK); got != 1.0 {
		t.Errorf("domainHitRate = %v, want 1.0", got)
	}
}

func TestDomainHitRate_PartialHit(t *testing.T) {
	gt := []DomainSpec{{Pattern: "a.com"}, {Pattern: "b.com"}, {Pattern: "c.com"}}
	topK := []Citation{{Domain: "a.com", Rank: 1}}
	if got := domainHitRate(gt, topK); !approxEqual(got, 1.0/3.0, 1e-9) {
		t.Errorf("domainHitRate = %v, want ~0.333", got)
	}
}

func TestDomainHitRate_NoHit(t *testing.T) {
	gt := []DomainSpec{{Pattern: "a.com"}}
	topK := []Citation{{Domain: "x.com", Rank: 1}}
	if got := domainHitRate(gt, topK); got != 0 {
		t.Errorf("domainHitRate = %v, want 0", got)
	}
}

func TestDomainHitRate_EmptyGroundTruthReturnsOne(t *testing.T) {
	if got := domainHitRate(nil, nil); got != 1.0 {
		t.Errorf("domainHitRate(nil, nil) = %v, want 1.0", got)
	}
}

func TestTermCoverage_PerfectPartialNone(t *testing.T) {
	gt := []TermSpec{{Term: "alpha"}, {Term: "Beta"}, {Term: "gamma"}}
	if got := termCoverage(gt, "ALPHA BETA GAMMA"); got != 1.0 {
		t.Errorf("perfect = %v", got)
	}
	if got := termCoverage(gt, "alpha only"); !approxEqual(got, 1.0/3.0, 1e-9) {
		t.Errorf("partial = %v", got)
	}
	if got := termCoverage(gt, "nothing here"); got != 0 {
		t.Errorf("none = %v", got)
	}
}

func TestTermCoverage_EmptyAnswerOrGT(t *testing.T) {
	if got := termCoverage(nil, ""); got != 1.0 {
		t.Errorf("empty gt = %v, want 1.0", got)
	}
	gt := []TermSpec{{Term: "x"}}
	if got := termCoverage(gt, ""); got != 0 {
		t.Errorf("empty answer = %v, want 0", got)
	}
}

func TestPollutionSuppression_CleanAndDirty(t *testing.T) {
	forbid := []DomainSpec{{Pattern: "bad.example"}}
	clean := []Citation{{Domain: "good.example", Rank: 1}, {Domain: "good2.example", Rank: 2}}
	if got := pollutionSuppression(forbid, clean); got != 1.0 {
		t.Errorf("clean = %v, want 1.0", got)
	}
	half := []Citation{{Domain: "bad.example", Rank: 1}, {Domain: "good.example", Rank: 2}}
	if got := pollutionSuppression(forbid, half); !approxEqual(got, 0.5, 1e-9) {
		t.Errorf("half = %v, want 0.5", got)
	}
	all := []Citation{{Domain: "sub.bad.example", Rank: 1}}
	if got := pollutionSuppression(forbid, all); got != 0 {
		t.Errorf("all polluted = %v, want 0", got)
	}
}

func TestPollutionSuppression_EmptyTopK(t *testing.T) {
	forbid := []DomainSpec{{Pattern: "bad.example"}}
	if got := pollutionSuppression(forbid, nil); got != 1.0 {
		t.Errorf("empty topK = %v, want 1.0", got)
	}
}

func TestSourceTypeDiversity(t *testing.T) {
	expected := []SourceType{SourceDocs, SourceCommunity, SourceCode}
	topK := []Citation{
		{SourceType: SourceDocs, Rank: 1},
		{SourceType: SourceCommunity, Rank: 2},
	}
	if got := sourceTypeDiversity(expected, topK); !approxEqual(got, 2.0/3.0, 1e-9) {
		t.Errorf("diversity = %v, want 0.667", got)
	}
	full := []Citation{
		{SourceType: SourceDocs, Rank: 1},
		{SourceType: SourceCommunity, Rank: 2},
		{SourceType: SourceCode, Rank: 3},
	}
	if got := sourceTypeDiversity(expected, full); got != 1.0 {
		t.Errorf("full = %v", got)
	}
	if got := sourceTypeDiversity(nil, nil); got != 1.0 {
		t.Errorf("empty expected = %v, want 1.0", got)
	}
}

func TestLatencyScore(t *testing.T) {
	budget := 10 * time.Second
	if got := latencyScore(0, budget); got != 1.0 {
		t.Errorf("zero latency = %v", got)
	}
	if got := latencyScore(5*time.Second, budget); !approxEqual(got, 0.5, 1e-9) {
		t.Errorf("half latency = %v", got)
	}
	if got := latencyScore(20*time.Second, budget); got != 0 {
		t.Errorf("over-budget = %v", got)
	}
	if got := latencyScore(5*time.Second, 0); got != 1.0 {
		t.Errorf("zero budget = %v", got)
	}
}

func TestCostScore(t *testing.T) {
	if got := costScore(0, 0.5); got != 1.0 {
		t.Errorf("zero cost = %v", got)
	}
	if got := costScore(0.25, 0.5); !approxEqual(got, 0.5, 1e-9) {
		t.Errorf("half cost = %v", got)
	}
	if got := costScore(2.0, 0.5); got != 0 {
		t.Errorf("over budget = %v", got)
	}
	if got := costScore(0.1, 0); got != 1.0 {
		t.Errorf("zero budget = %v", got)
	}
}

func TestScorer_Score_Composite(t *testing.T) {
	// All 1.0 across the board => composite = 100.
	s := Score{
		DomainHitRate:        1.0,
		TermCoverage:         1.0,
		PollutionSuppression: 1.0,
		SourceTypeDiversity:  1.0,
		LatencyScore:         1.0,
		CostScore:            1.0,
	}
	if got := compositeScore(s); !approxEqual(got, 100.0, 1e-9) {
		t.Errorf("perfect composite = %v, want 100", got)
	}

	// Mixed: verify weighted sum is what we expect.
	s2 := Score{
		DomainHitRate:        1.0, // 0.30
		TermCoverage:         0.5, // 0.125
		PollutionSuppression: 1.0, // 0.15
		SourceTypeDiversity:  0.0, // 0
		LatencyScore:         0.8, // 0.08
		CostScore:            1.0, // 0.10
	}
	want := (0.30 + 0.125 + 0.15 + 0 + 0.08 + 0.10) * 100
	if got := compositeScore(s2); !approxEqual(got, want, 1e-9) {
		t.Errorf("mixed composite = %v, want %v", got, want)
	}
}

func TestScorer_Score_EndToEnd(t *testing.T) {
	scorer := NewScorer()
	q := Query{
		ID: "t1",
		GroundTruth: GroundTruth{
			MustContainDomains:  []DomainSpec{{Pattern: "a.com"}, {Pattern: "b.com"}},
			MustContainTerms:    []TermSpec{{Term: "foo"}, {Term: "bar"}},
			ForbiddenDomains:    []DomainSpec{{Pattern: "bad.example"}},
			ExpectedSourceTypes: []SourceType{SourceDocs, SourceCommunity},
		},
	}
	r := Result{
		QueryID: "t1",
		Citations: []Citation{
			{Domain: "a.com", SourceType: SourceDocs, Rank: 1},
			{Domain: "b.com", SourceType: SourceCommunity, Rank: 2},
		},
		Answer:  "foo and bar",
		Latency: 0, // no latency penalty
		Cost:    0,
	}
	sc := scorer.Score(q, r)
	if sc.DomainHitRate != 1.0 {
		t.Errorf("domain hit = %v", sc.DomainHitRate)
	}
	if sc.TermCoverage != 1.0 {
		t.Errorf("term cov = %v", sc.TermCoverage)
	}
	if sc.PollutionSuppression != 1.0 {
		t.Errorf("pollution = %v", sc.PollutionSuppression)
	}
	if sc.Composite < 99.0 {
		t.Errorf("composite = %v, want ~100", sc.Composite)
	}
}

func TestResult_TopK_OrdersByRank(t *testing.T) {
	r := Result{Citations: []Citation{
		{Domain: "c", Rank: 3},
		{Domain: "a", Rank: 1},
		{Domain: "b", Rank: 2},
	}}
	top := r.topK(2)
	if len(top) != 2 {
		t.Fatalf("len = %d, want 2", len(top))
	}
	if top[0].Domain != "a" || top[1].Domain != "b" {
		t.Errorf("order = %v, want a,b", []string{top[0].Domain, top[1].Domain})
	}
}

func TestResult_TopK_ZeroRankGoesLast(t *testing.T) {
	r := Result{Citations: []Citation{
		{Domain: "zero", Rank: 0},
		{Domain: "one", Rank: 1},
		{Domain: "two", Rank: 2},
	}}
	top := r.topK(5)
	if top[0].Domain != "one" || top[1].Domain != "two" || top[2].Domain != "zero" {
		t.Errorf("order = %v", []string{top[0].Domain, top[1].Domain, top[2].Domain})
	}
}

func TestAggregate_PercentilesAndMetricMeans(t *testing.T) {
	// 5 scores with known composites 10, 20, 30, 40, 50
	scores := []Score{
		{QueryID: "a", Composite: 50, DomainHitRate: 1.0, TermCoverage: 1.0},
		{QueryID: "b", Composite: 10, DomainHitRate: 0.0, TermCoverage: 0.0},
		{QueryID: "c", Composite: 30, DomainHitRate: 0.5, TermCoverage: 0.5},
		{QueryID: "d", Composite: 20, DomainHitRate: 0.25, TermCoverage: 0.25},
		{QueryID: "e", Composite: 40, DomainHitRate: 0.75, TermCoverage: 0.75},
	}
	scorer := NewScorer()
	agg := scorer.Aggregate(scores)
	if agg.SampleSize != 5 {
		t.Errorf("n = %d, want 5", agg.SampleSize)
	}
	if !approxEqual(agg.Mean, 30.0, 1e-9) {
		t.Errorf("mean = %v, want 30", agg.Mean)
	}
	// Nearest-rank: P50 at idx ceil(0.5*5)-1 = 2 → 30; P95 at idx ceil(0.95*5)-1 = 4 → 50.
	if !approxEqual(agg.P50, 30, 1e-9) {
		t.Errorf("p50 = %v, want 30", agg.P50)
	}
	if !approxEqual(agg.P95, 50, 1e-9) {
		t.Errorf("p95 = %v, want 50", agg.P95)
	}
	if !approxEqual(agg.PerMetric[MetricDomainHit], 0.5, 1e-9) {
		t.Errorf("PerMetric domain_hit = %v, want 0.5", agg.PerMetric[MetricDomainHit])
	}
	if !approxEqual(agg.PerMetric[MetricTermCoverage], 0.5, 1e-9) {
		t.Errorf("PerMetric term_coverage = %v, want 0.5", agg.PerMetric[MetricTermCoverage])
	}
}

func TestAggregate_EmptyInput(t *testing.T) {
	scorer := NewScorer()
	agg := scorer.Aggregate(nil)
	if agg.SampleSize != 0 || agg.Mean != 0 {
		t.Errorf("empty agg = %+v", agg)
	}
}
