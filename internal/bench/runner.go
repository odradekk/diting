package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Variant is the single extension point for Phase 5.6 real variants
// (v0-baseline, v2-single, v2-raw). Real variants will live in subpackages
// and wire their existing pipeline code into Run().
//
// Variants receive a RunInput, not a Query — the ground-truth annotation is
// deliberately kept inside the bench package so a variant cannot read the
// answer key it is being scored against.
type Variant interface {
	Name() string
	Run(ctx context.Context, in RunInput) (Result, error)
}

// Runner executes a Variant against a QuerySet with bounded parallelism.
// Concurrency and per-query deadlines are configured via functional options.
type Runner struct {
	variant     Variant
	concurrency int
	perQueryTTL time.Duration
	logger      *slog.Logger
}

// Option is a functional option for Runner. Mirrors fetch.ChainOption.
type Option func(*Runner)

// WithConcurrency caps the number of queries running concurrently. Default
// 4. Values <= 0 are treated as 1.
func WithConcurrency(n int) Option {
	return func(r *Runner) {
		if n <= 0 {
			n = 1
		}
		r.concurrency = n
	}
}

// WithPerQueryTimeout sets the per-query context deadline. Default 300s.
//
// The 300s default was chosen after the Phase 5.7 first-run investigation
// showed successful queries against MiniMax M2.7 HighSpeed averaging 91s
// wall-clock with a p95 near 168s — several queries landed just under
// the previous 180s cutoff, and 6 queries timed out entirely. Reasoning
// models emit large <think> blocks, and under concurrency=4 four answer-
// phase calls can stack up to push the tail past 3 minutes. 300s gives
// enough buffer without being so loose that a genuinely stuck query ties
// up a worker for ages.
//
// Architecture §2.1's original 90s p95 budget was calibrated against
// non-reasoning models (gpt-4.1-mini); it remains the target for the
// "fast" tier but is not achievable with MiniMax M2.7 today.
//
// A value <= 0 disables the per-query deadline, leaving only the parent ctx.
func WithPerQueryTimeout(d time.Duration) Option {
	return func(r *Runner) { r.perQueryTTL = d }
}

// WithLogger replaces the default slog.Logger. Passing nil is a no-op.
func WithLogger(l *slog.Logger) Option {
	return func(r *Runner) {
		if l != nil {
			r.logger = l
		}
	}
}

// NewRunner constructs a Runner with defaults applied, then options. Panics
// when v is nil — a nil Variant is always a caller bug and we surface it
// eagerly rather than letting Run crash mid-goroutine.
func NewRunner(v Variant, opts ...Option) *Runner {
	if v == nil {
		panic("bench: NewRunner called with nil Variant")
	}
	r := &Runner{
		variant:     v,
		concurrency: 4,
		perQueryTTL: 300 * time.Second,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run executes every query in qset under the configured variant. Scoring is
// intentionally not performed here — the caller owns the Scorer
// configuration and the Score/Aggregate decisions. Per-query errors are
// captured into Result.Metadata["error"]=error.Error() rather than aborting
// the whole run. Returns error only if qset is nil.
func (r *Runner) Run(ctx context.Context, qset *QuerySet) (*RunReport, error) {
	if qset == nil {
		return nil, errors.New("bench runner: nil QuerySet")
	}
	start := time.Now()

	// collect all queries into a flat slice, preserving order
	flat := make([]Query, 0, qset.TotalQueries())
	for _, b := range qset.Batches {
		flat = append(flat, b.Queries...)
	}

	results := make([]Result, len(flat))

	// semaphore + WaitGroup pattern (mirrors internal/fetch/chain.go:167-204).
	// No errgroup — per-query failures become Result.Metadata["error"], not
	// a returned error.
	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup

	scheduled := true
	for i := range flat {
		if !scheduled {
			results[i] = errorResult(flat[i].ID, ctx.Err())
			continue
		}
		if err := ctx.Err(); err != nil {
			results[i] = errorResult(flat[i].ID, err)
			scheduled = false
			continue
		}
		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			results[i] = errorResult(flat[i].ID, ctx.Err())
			scheduled = false
			continue
		}
		go func(idx int, q Query) {
			defer wg.Done()
			defer func() { <-sem }()

			qctx := ctx
			var cancel context.CancelFunc
			if r.perQueryTTL > 0 {
				qctx, cancel = context.WithTimeout(ctx, r.perQueryTTL)
			}
			qstart := time.Now()

			var (
				res Result
				err error
			)
			func() {
				// Shield the harness from variant panics: a single bad
				// variant must not kill the whole run. Record the panic in
				// metadata and continue.
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("variant panic: %v", p)
						r.logger.Error("bench variant panicked",
							"query_id", q.ID,
							"variant", r.variant.Name(),
							"panic", p,
						)
					}
				}()
				res, err = r.variant.Run(qctx, q.AsRunInput())
			}()

			if cancel != nil {
				cancel()
			}
			if err != nil {
				if res.Metadata == nil {
					res.Metadata = map[string]any{}
				}
				res.Metadata["error"] = err.Error()
				r.logger.Debug("bench variant error",
					"query_id", q.ID,
					"variant", r.variant.Name(),
					"err", err,
				)
			}
			// enforce invariants the runner owns
			res.QueryID = q.ID
			if res.Latency == 0 {
				res.Latency = time.Since(qstart)
			}
			results[idx] = res
		}(i, flat[i])
	}
	wg.Wait()

	return &RunReport{
		Variant:   r.variant.Name(),
		StartedAt: start,
		Duration:  time.Since(start),
		Results:   results,
	}, nil
}

// errorResult produces a placeholder Result for a query that never ran
// because the parent context was already cancelled.
func errorResult(id string, err error) Result {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Result{
		QueryID:  id,
		Metadata: map[string]any{"error": msg},
	}
}
