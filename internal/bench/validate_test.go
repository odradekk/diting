package bench

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// validQuery returns a minimal Query that passes every validation check when
// placed in a Batch whose Category matches its Type.
func validQuery(id string, cat Category) Query {
	return Query{
		ID:         id,
		Type:       cat,
		Query:      "what is " + id + "?",
		Intent:     "test intent",
		Difficulty: DifficultyMedium,
		TechArea:   "testing",
		GroundTruth: GroundTruth{
			MustContainDomains: []DomainSpec{
				{Pattern: "example.com", Rationale: "r1"},
				{Pattern: "example.org", Rationale: "r2"},
			},
			MustContainTerms: []TermSpec{
				{Term: "alpha", Rationale: "r1"},
				{Term: "beta", Rationale: "r2"},
			},
			ForbiddenDomains: []DomainSpec{
				{Pattern: "bad.example", Rationale: "r1"},
			},
			ExpectedSourceTypes: []SourceType{SourceDocs, SourceCommunity},
			CanonicalAnswer:     "the canonical answer",
		},
	}
}

// validQuerySet returns a QuerySet with one valid batch of one valid query.
func validQuerySet() *QuerySet {
	return &QuerySet{
		Batches: []Batch{
			{
				Category: CategoryErrorTroubleshooting,
				Count:    1,
				Queries:  []Query{validQuery("et_001", CategoryErrorTroubleshooting)},
			},
		},
	}
}

func TestValidate_PassesValidFixture(t *testing.T) {
	if err := Validate(validQuerySet()); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
}

func TestValidate_RejectsNilOrEmpty(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Error("Validate(nil) = nil, want error")
	}
	if err := Validate(&QuerySet{}); err == nil {
		t.Error("Validate(empty) = nil, want error")
	}
}

func TestValidate_CatchesCountMismatch(t *testing.T) {
	qs := validQuerySet()
	qs.Batches[0].Count = 99
	err := Validate(qs)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "count=99") {
		t.Errorf("error does not mention count mismatch: %v", err)
	}
}

func TestValidate_CatchesDuplicateIDs(t *testing.T) {
	qs := &QuerySet{
		Batches: []Batch{
			{
				Category: CategoryErrorTroubleshooting,
				Count:    1,
				Queries:  []Query{validQuery("dup_001", CategoryErrorTroubleshooting)},
			},
			{
				Category: CategoryAPIUsage,
				Count:    1,
				Queries:  []Query{validQuery("dup_001", CategoryAPIUsage)},
			},
		},
	}
	err := Validate(qs)
	if err == nil {
		t.Fatal("expected duplicate-id error")
	}
	if !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("error does not mention duplicate id: %v", err)
	}
}

func TestValidate_CatchesMissingCanonicalAnswer(t *testing.T) {
	qs := validQuerySet()
	qs.Batches[0].Queries[0].GroundTruth.CanonicalAnswer = ""
	err := Validate(qs)
	if err == nil || !strings.Contains(err.Error(), "empty canonical_answer") {
		t.Errorf("error = %v, want canonical_answer complaint", err)
	}
}

func TestValidate_CatchesTypeCategoryMismatch(t *testing.T) {
	qs := validQuerySet()
	qs.Batches[0].Queries[0].Type = CategoryAPIUsage
	err := Validate(qs)
	if err == nil || !strings.Contains(err.Error(), "type=") {
		t.Errorf("error = %v, want type/category mismatch complaint", err)
	}
}

func TestValidate_CatchesInvalidEnum(t *testing.T) {
	qs := validQuerySet()
	qs.Batches[0].Queries[0].Difficulty = Difficulty("giga")
	qs.Batches[0].Queries[0].GroundTruth.ExpectedSourceTypes = []SourceType{SourceType("rumour")}
	err := Validate(qs)
	if err == nil {
		t.Fatal("expected enum errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid difficulty") {
		t.Errorf("missing difficulty error: %v", err)
	}
	if !strings.Contains(msg, "invalid source_type") {
		t.Errorf("missing source_type error: %v", err)
	}
}

func TestValidate_CatchesDomainBounds(t *testing.T) {
	cases := []struct {
		name string
		n    int
		bad  bool
	}{
		{"zero", 0, true},
		{"one", 1, true},
		{"two", 2, false},
		{"four", 4, false},
		{"five", 5, true},
		{"six", 6, true},
	}
	for _, tc := range cases {
		qs := validQuerySet()
		domains := make([]DomainSpec, tc.n)
		for i := range domains {
			domains[i] = DomainSpec{Pattern: fmt.Sprintf("d%d.example", i), Rationale: "r"}
		}
		qs.Batches[0].Queries[0].GroundTruth.MustContainDomains = domains
		err := Validate(qs)
		if tc.bad && err == nil {
			t.Errorf("%s: expected error for n=%d", tc.name, tc.n)
		}
		if !tc.bad && err != nil {
			t.Errorf("%s: unexpected error for n=%d: %v", tc.name, tc.n, err)
		}
	}
}

func TestValidate_CollectsMultipleIssues(t *testing.T) {
	qs := validQuerySet()
	qs.Batches[0].Queries[0].GroundTruth.CanonicalAnswer = ""
	qs.Batches[0].Queries[0].Difficulty = Difficulty("")
	qs.Batches[0].Queries[0].Intent = ""
	qs.Batches[0].Queries[0].GroundTruth.MustContainTerms = nil

	err := Validate(qs)
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error is not *ValidationError: %T", err)
	}
	if len(ve.Issues) < 3 {
		t.Errorf("expected ≥3 issues, got %d: %v", len(ve.Issues), ve.Issues)
	}
}

func TestCategoryDifficultySourceTypeValid(t *testing.T) {
	if !Category("error_troubleshooting").Valid() {
		t.Error("error_troubleshooting should be valid")
	}
	if Category("wut").Valid() {
		t.Error("wut should be invalid")
	}
	if !DifficultyEasy.Valid() || DifficultyEasy != "easy" {
		t.Error("easy should be valid")
	}
	if Difficulty("").Valid() {
		t.Error("empty difficulty should be invalid")
	}
	if !SourceDocs.Valid() {
		t.Error("docs should be valid")
	}
	if SourceType("gossip").Valid() {
		t.Error("gossip should be invalid")
	}
}
