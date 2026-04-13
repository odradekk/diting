package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/doctor"
)

// --- printDoctorReport ------------------------------------------------------

func TestPrintDoctorReport_GroupsByCategory(t *testing.T) {
	r := &doctor.Report{
		Checks: []doctor.Check{
			{Category: "System", Name: "diting", Status: doctor.StatusOK, Message: "v2.0.0-test"},
			{Category: "LLM", Name: "API key", Status: doctor.StatusOK, Message: "anthropic configured"},
			{Category: "Search", Name: "bing", Status: doctor.StatusOK, Message: "free"},
			{Category: "Search", Name: "brave", Status: doctor.StatusWarn, Message: "BRAVE_API_KEY not set"},
			{Category: "Fetch", Name: "utls", Status: doctor.StatusOK, Message: "pure-Go"},
		},
		OKCount:   4,
		WarnCount: 1,
		FailCount: 0,
	}

	var buf bytes.Buffer
	printDoctorReport(&buf, r)
	out := buf.String()

	// Each category should have a heading.
	for _, cat := range []string{"== System ==", "== LLM ==", "== Search ==", "== Fetch =="} {
		if !strings.Contains(out, cat) {
			t.Errorf("missing heading %q:\n%s", cat, out)
		}
	}

	// Status labels.
	if !strings.Contains(out, "[ OK ] bing:") {
		t.Errorf("missing OK label for bing:\n%s", out)
	}
	if !strings.Contains(out, "[WARN] brave:") {
		t.Errorf("missing WARN label for brave:\n%s", out)
	}

	// Summary line.
	if !strings.Contains(out, "Summary: 4 OK, 1 warn, 0 fail") {
		t.Errorf("missing summary:\n%s", out)
	}

	// No failures → no "Fix the [FAIL]" line.
	if strings.Contains(out, "Fix the [FAIL]") {
		t.Errorf("should not print fix-failures hint when no failures:\n%s", out)
	}
}

func TestPrintDoctorReport_WithFailures(t *testing.T) {
	r := &doctor.Report{
		Checks: []doctor.Check{
			{Category: "LLM", Name: "API key", Status: doctor.StatusFail, Message: "no provider"},
		},
		FailCount: 1,
	}
	var buf bytes.Buffer
	printDoctorReport(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "[FAIL] API key:") {
		t.Errorf("missing FAIL label:\n%s", out)
	}
	if !strings.Contains(out, "Fix the [FAIL]") {
		t.Errorf("missing fix hint when failures present:\n%s", out)
	}
	if !strings.Contains(out, "Summary: 0 OK, 0 warn, 1 fail") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

func TestPrintDoctorReport_PreservesCategoryOrder(t *testing.T) {
	// RunChecks emits System → Config → LLM → Search → Fetch → Cache.
	// The formatter must NOT sort alphabetically (that would put
	// "Cache" before "LLM", reordering the user's priority signals).
	r := &doctor.Report{
		Checks: []doctor.Check{
			{Category: "System", Name: "v", Status: doctor.StatusOK, Message: "x"},
			{Category: "LLM", Name: "k", Status: doctor.StatusOK, Message: "x"},
			{Category: "Cache", Name: "c", Status: doctor.StatusOK, Message: "x"},
		},
	}
	var buf bytes.Buffer
	printDoctorReport(&buf, r)
	out := buf.String()

	sysIdx := strings.Index(out, "System")
	llmIdx := strings.Index(out, "LLM")
	cacheIdx := strings.Index(out, "Cache")
	if !(sysIdx < llmIdx && llmIdx < cacheIdx) {
		t.Errorf("category order wrong: System=%d LLM=%d Cache=%d\n%s",
			sysIdx, llmIdx, cacheIdx, out)
	}
}

func TestPrintDoctorReport_EmptyCategoryFallback(t *testing.T) {
	r := &doctor.Report{
		Checks: []doctor.Check{
			{Name: "orphan", Status: doctor.StatusOK, Message: "no category"},
		},
	}
	var buf bytes.Buffer
	printDoctorReport(&buf, r)
	if !strings.Contains(buf.String(), "== Other ==") {
		t.Errorf("orphan check should land in Other category:\n%s", buf.String())
	}
}

// --- groupByCategory unit test ---------------------------------------------

func TestGroupByCategory(t *testing.T) {
	checks := []doctor.Check{
		{Category: "A", Name: "a1"},
		{Category: "B", Name: "b1"},
		{Category: "A", Name: "a2"},
		{Category: "A", Name: "a3"},
		{Category: "B", Name: "b2"},
	}
	groups := groupByCategory(checks)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].name != "A" || groups[1].name != "B" {
		t.Errorf("order wrong: %v", groups)
	}
	if len(groups[0].checks) != 3 {
		t.Errorf("A should have 3 checks, got %d", len(groups[0].checks))
	}
	if len(groups[1].checks) != 2 {
		t.Errorf("B should have 2 checks, got %d", len(groups[1].checks))
	}
	// Within-group order preserved.
	if groups[0].checks[0].Name != "a1" || groups[0].checks[2].Name != "a3" {
		t.Errorf("within-group order not preserved: %+v", groups[0].checks)
	}
}
