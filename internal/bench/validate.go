package bench

import (
	"fmt"
	"strings"
)

// Validate walks every batch and query in qs and collects schema / enum /
// uniqueness / bound issues into a single *ValidationError. It returns nil
// when qs is fully valid.
func Validate(qs *QuerySet) error {
	var issues []string
	seenIDs := map[string]string{} // id -> first-seen location

	if qs == nil || len(qs.Batches) == 0 {
		issues = append(issues, "schema: no batches in query set")
		return &ValidationError{Issues: issues}
	}

	for bi, batch := range qs.Batches {
		loc := fmt.Sprintf("batches[%d] (%s)", bi, batch.Category)
		if !batch.Category.Valid() {
			issues = append(issues, fmt.Sprintf("%s: invalid category %q", loc, batch.Category))
		}
		if batch.Count != len(batch.Queries) {
			issues = append(issues, fmt.Sprintf("%s: count=%d but len(queries)=%d", loc, batch.Count, len(batch.Queries)))
		}
		for qi, q := range batch.Queries {
			qloc := fmt.Sprintf("%s.queries[%d] (id=%s)", loc, qi, q.ID)
			if q.ID == "" {
				issues = append(issues, fmt.Sprintf("%s: empty id", qloc))
			}
			if q.Query == "" {
				issues = append(issues, fmt.Sprintf("%s: empty query", qloc))
			}
			if q.Intent == "" {
				issues = append(issues, fmt.Sprintf("%s: empty intent", qloc))
			}
			if q.Type != batch.Category {
				issues = append(issues, fmt.Sprintf("%s: type=%q but batch.category=%q", qloc, q.Type, batch.Category))
			}
			if !q.Difficulty.Valid() {
				issues = append(issues, fmt.Sprintf("%s: invalid difficulty %q", qloc, q.Difficulty))
			}
			if q.TechArea == "" {
				issues = append(issues, fmt.Sprintf("%s: empty tech_area", qloc))
			}
			if prev, ok := seenIDs[q.ID]; ok {
				issues = append(issues, fmt.Sprintf("%s: duplicate id %q (first seen at %s)", qloc, q.ID, prev))
			} else if q.ID != "" {
				seenIDs[q.ID] = qloc
			}

			gt := q.GroundTruth
			if gt.CanonicalAnswer == "" {
				issues = append(issues, fmt.Sprintf("%s: empty canonical_answer", qloc))
			}
			if n := len(gt.MustContainDomains); n < 2 || n > 4 {
				issues = append(issues, fmt.Sprintf("%s: must_contain_domains count=%d (want 2-4)", qloc, n))
			}
			if n := len(gt.MustContainTerms); n < 2 || n > 5 {
				issues = append(issues, fmt.Sprintf("%s: must_contain_terms count=%d (want 2-5)", qloc, n))
			}
			if n := len(gt.ForbiddenDomains); n > 4 {
				issues = append(issues, fmt.Sprintf("%s: forbidden_domains count=%d (want 0-4)", qloc, n))
			}
			if len(gt.ExpectedSourceTypes) < 1 {
				issues = append(issues, fmt.Sprintf("%s: expected_source_types empty", qloc))
			}
			for _, st := range gt.ExpectedSourceTypes {
				if !st.Valid() {
					issues = append(issues, fmt.Sprintf("%s: invalid source_type %q", qloc, st))
				}
			}
			// Ground-truth lists are set-shaped; reject duplicates so the
			// scorer's denominators stay meaningful.
			seenDomain := map[string]struct{}{}
			for di, d := range gt.MustContainDomains {
				if d.Pattern == "" {
					issues = append(issues, fmt.Sprintf("%s: must_contain_domains[%d]: empty pattern", qloc, di))
					continue
				}
				key := strings.ToLower(strings.TrimSpace(d.Pattern))
				if _, ok := seenDomain[key]; ok {
					issues = append(issues, fmt.Sprintf("%s: must_contain_domains[%d]: duplicate pattern %q", qloc, di, d.Pattern))
				}
				seenDomain[key] = struct{}{}
			}
			seenForbidden := map[string]struct{}{}
			for di, d := range gt.ForbiddenDomains {
				if d.Pattern == "" {
					issues = append(issues, fmt.Sprintf("%s: forbidden_domains[%d]: empty pattern", qloc, di))
					continue
				}
				key := strings.ToLower(strings.TrimSpace(d.Pattern))
				if _, ok := seenForbidden[key]; ok {
					issues = append(issues, fmt.Sprintf("%s: forbidden_domains[%d]: duplicate pattern %q", qloc, di, d.Pattern))
				}
				seenForbidden[key] = struct{}{}
			}
			seenTerm := map[string]struct{}{}
			for ti, t := range gt.MustContainTerms {
				if t.Term == "" {
					issues = append(issues, fmt.Sprintf("%s: must_contain_terms[%d]: empty term", qloc, ti))
					continue
				}
				key := strings.ToLower(strings.TrimSpace(t.Term))
				if _, ok := seenTerm[key]; ok {
					issues = append(issues, fmt.Sprintf("%s: must_contain_terms[%d]: duplicate term %q", qloc, ti, t.Term))
				}
				seenTerm[key] = struct{}{}
			}
			seenSourceType := map[SourceType]struct{}{}
			for si, st := range gt.ExpectedSourceTypes {
				if _, ok := seenSourceType[st]; ok {
					issues = append(issues, fmt.Sprintf("%s: expected_source_types[%d]: duplicate %q", qloc, si, st))
				}
				seenSourceType[st] = struct{}{}
			}
		}
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
