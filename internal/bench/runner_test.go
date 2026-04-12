package bench

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedVariant is a test-only Variant that runs a user-supplied function
// per query. It records every call and is safe for concurrent use.
type scriptedVariant struct {
	name    string
	fn      func(ctx context.Context, in RunInput) (Result, error)
	mu      sync.Mutex
	calls   int
	callIDs []string
}

func (s *scriptedVariant) Name() string { return s.name }

func (s *scriptedVariant) Run(ctx context.Context, in RunInput) (Result, error) {
	s.mu.Lock()
	s.calls++
	s.callIDs = append(s.callIDs, in.ID)
	s.mu.Unlock()
	return s.fn(ctx, in)
}

func (s *scriptedVariant) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// smallQuerySet returns a QuerySet with 3 queries across 2 batches.
func smallQuerySet() *QuerySet {
	return &QuerySet{
		Batches: []Batch{
			{
				Category: CategoryErrorTroubleshooting,
				Count:    2,
				Queries: []Query{
					validQuery("et_001", CategoryErrorTroubleshooting),
					validQuery("et_002", CategoryErrorTroubleshooting),
				},
			},
			{
				Category: CategoryAPIUsage,
				Count:    1,
				Queries: []Query{
					validQuery("api_001", CategoryAPIUsage),
				},
			},
		},
	}
}

func TestRunner_CompletesAllQueriesUnderVariant(t *testing.T) {
	v := &scriptedVariant{
		name: "canned",
		fn: func(_ context.Context, in RunInput) (Result, error) {
			return Result{
				Answer:  "answer for " + in.ID,
				Latency: 5 * time.Millisecond,
			}, nil
		},
	}
	runner := NewRunner(v, WithConcurrency(2), WithPerQueryTimeout(time.Second))
	report, err := runner.Run(context.Background(), smallQuerySet())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Variant != "canned" {
		t.Errorf("report.Variant = %q", report.Variant)
	}
	if len(report.Results) != 3 {
		t.Fatalf("len(Results) = %d, want 3", len(report.Results))
	}
	wantOrder := []string{"et_001", "et_002", "api_001"}
	for i, id := range wantOrder {
		if report.Results[i].QueryID != id {
			t.Errorf("Results[%d].QueryID = %q, want %q", i, report.Results[i].QueryID, id)
		}
		if report.Results[i].Answer != "answer for "+id {
			t.Errorf("Results[%d].Answer mismatch", i)
		}
		if _, isErr := report.Results[i].Metadata["error"]; isErr {
			t.Errorf("Results[%d] has error metadata: %v", i, report.Results[i].Metadata)
		}
	}
	if v.Calls() != 3 {
		t.Errorf("variant calls = %d, want 3", v.Calls())
	}
}

func TestRunner_CapturesPerQueryErrorsInMetadata(t *testing.T) {
	sentinel := errors.New("et_002 boom")
	v := &scriptedVariant{
		name: "flaky",
		fn: func(_ context.Context, in RunInput) (Result, error) {
			if in.ID == "et_002" {
				return Result{}, sentinel
			}
			return Result{Answer: "ok"}, nil
		},
	}
	runner := NewRunner(v, WithConcurrency(1))
	report, err := runner.Run(context.Background(), smallQuerySet())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Results) != 3 {
		t.Fatalf("len(Results) = %d, want 3", len(report.Results))
	}
	got, ok := report.Results[1].Metadata["error"]
	if !ok {
		t.Fatalf("Results[1].Metadata[error] missing; metadata = %v", report.Results[1].Metadata)
	}
	if s, _ := got.(string); !strings.Contains(s, "et_002 boom") {
		t.Errorf("error metadata = %v, want substring 'et_002 boom'", got)
	}
	// Other queries must have succeeded.
	if _, bad := report.Results[0].Metadata["error"]; bad {
		t.Errorf("Results[0] unexpectedly errored: %v", report.Results[0].Metadata)
	}
}

func TestRunner_RespectsConcurrency(t *testing.T) {
	var inflight atomic.Int32
	var peak atomic.Int32
	v := &scriptedVariant{
		name: "slow",
		fn: func(ctx context.Context, _ RunInput) (Result, error) {
			n := inflight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			defer inflight.Add(-1)
			select {
			case <-time.After(15 * time.Millisecond):
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
			return Result{Answer: "ok"}, nil
		},
	}

	// Build a larger set so concurrency can saturate.
	qs := &QuerySet{Batches: []Batch{{Category: CategoryErrorTroubleshooting, Count: 8}}}
	for i := 0; i < 8; i++ {
		qs.Batches[0].Queries = append(qs.Batches[0].Queries,
			validQuery("et_"+string(rune('a'+i)), CategoryErrorTroubleshooting))
	}

	runner := NewRunner(v, WithConcurrency(2), WithPerQueryTimeout(time.Second))
	if _, err := runner.Run(context.Background(), qs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := peak.Load(); got > 2 {
		t.Errorf("peak concurrency = %d, want <= 2", got)
	}
}

func TestRunner_HonoursPerQueryTimeout(t *testing.T) {
	v := &scriptedVariant{
		name: "blocking",
		fn: func(ctx context.Context, _ RunInput) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	}
	runner := NewRunner(v, WithConcurrency(2), WithPerQueryTimeout(10*time.Millisecond))

	qs := &QuerySet{Batches: []Batch{{
		Category: CategoryErrorTroubleshooting,
		Count:    2,
		Queries: []Query{
			validQuery("et_001", CategoryErrorTroubleshooting),
			validQuery("et_002", CategoryErrorTroubleshooting),
		},
	}}}

	start := time.Now()
	report, err := runner.Run(context.Background(), qs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("runner took %v, expected quick timeout", elapsed)
	}
	for i, r := range report.Results {
		errStr, _ := r.Metadata["error"].(string)
		if !strings.Contains(errStr, "deadline exceeded") {
			t.Errorf("Results[%d] error = %q, want 'deadline exceeded'", i, errStr)
		}
	}
}

func TestRunner_AbortsOnParentCancel(t *testing.T) {
	v := &scriptedVariant{
		name: "never-called",
		fn: func(_ context.Context, _ RunInput) (Result, error) {
			return Result{}, errors.New("should not run")
		},
	}
	runner := NewRunner(v, WithConcurrency(1), WithPerQueryTimeout(time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	report, err := runner.Run(ctx, smallQuerySet())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Results) != 3 {
		t.Fatalf("len(Results) = %d, want 3", len(report.Results))
	}
	for i, r := range report.Results {
		s, _ := r.Metadata["error"].(string)
		if !strings.Contains(s, "canceled") {
			t.Errorf("Results[%d] metadata error = %q, want 'canceled'", i, s)
		}
	}
}

func TestRunner_NilQuerySetReturnsError(t *testing.T) {
	runner := NewRunner(&scriptedVariant{name: "x", fn: func(context.Context, RunInput) (Result, error) {
		return Result{}, nil
	}})
	if _, err := runner.Run(context.Background(), nil); err == nil {
		t.Error("expected error on nil QuerySet")
	}
}

func TestWithConcurrency_ClampsNonPositive(t *testing.T) {
	r := NewRunner(&scriptedVariant{name: "x"}, WithConcurrency(-5))
	if r.concurrency != 1 {
		t.Errorf("concurrency = %d, want 1", r.concurrency)
	}
}
