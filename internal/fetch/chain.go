package fetch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Chain is a Fetcher that tries a list of layers in order and returns the
// first successful Result.
//
// Concurrent use: Fetch and FetchMany are safe to call from multiple
// goroutines concurrently. Close, however, must not race with any in-flight
// Fetch/FetchMany call — callers are responsible for quiescing the chain
// before closing it (the typical pattern is a CLI-level shutdown phase that
// cancels the root context, waits for all fetch goroutines to return, and
// then invokes Close). This mirrors the contract of net/http.Client,
// database/sql.DB, and google.golang.org/grpc.ClientConn.
type Chain struct {
	layers      []Layer
	concurrency int
	logger      *slog.Logger
}

// ChainOption configures a Chain.
type ChainOption func(*Chain)

// WithConcurrency bounds the parallelism of FetchMany. A value <= 0 means
// "one goroutine per URL, no bound". Default: 4.
func WithConcurrency(n int) ChainOption {
	return func(c *Chain) { c.concurrency = n }
}

// WithLogger overrides the default slog logger. Passing nil is a no-op.
func WithLogger(l *slog.Logger) ChainOption {
	return func(c *Chain) {
		if l != nil {
			c.logger = l
		}
	}
}

// NewChain constructs a Chain from the given layers in order. Disabled layers
// are filtered out at construction time; layers with a nil Fetcher are
// rejected and skipped with a warning.
func NewChain(layers []Layer, opts ...ChainOption) *Chain {
	c := &Chain{
		concurrency: 4,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(c)
	}

	enabled := make([]Layer, 0, len(layers))
	for _, l := range layers {
		if !l.Enabled {
			continue
		}
		if l.Fetcher == nil {
			c.logger.Warn("skipping layer with nil Fetcher", "layer", l.Name)
			continue
		}
		if l.Name == "" {
			l.Name = "unnamed"
		}
		enabled = append(enabled, l)
	}
	c.layers = enabled
	return c
}

// Layers returns the enabled layers in chain order. Callers must not mutate
// the returned slice; it is intended for observability and tests.
func (c *Chain) Layers() []Layer {
	out := make([]Layer, len(c.layers))
	copy(out, c.layers)
	return out
}

// Fetch attempts each enabled layer in order. It returns the first successful
// Result (with LayerUsed set to that layer's name) or a *ChainError containing
// every LayerError along the way.
func (c *Chain) Fetch(ctx context.Context, url string) (*Result, error) {
	if len(c.layers) == 0 {
		return nil, &ChainError{URL: url}
	}

	chainErr := &ChainError{URL: url}
	chainStart := time.Now()

	for _, layer := range c.layers {
		if err := ctx.Err(); err != nil {
			// The parent context was cancelled before we could try this
			// layer. Record the cancellation as Cause rather than appending
			// a phantom LayerError for a layer that was never invoked —
			// attempts must only reflect real fetch attempts.
			chainErr.Cause = err
			return nil, chainErr
		}

		layerCtx := ctx
		var cancel context.CancelFunc
		if layer.Timeout > 0 {
			layerCtx, cancel = context.WithTimeout(ctx, layer.Timeout)
		}

		layerStart := time.Now()
		result, err := layer.Fetcher.Fetch(layerCtx, url)
		if cancel != nil {
			cancel()
		}
		layerLatency := time.Since(layerStart).Milliseconds()

		if err == nil && result != nil {
			result.LayerUsed = layer.Name
			if result.LatencyMs == 0 {
				result.LatencyMs = layerLatency
			}
			c.logger.Debug("fetch ok",
				"url", url,
				"layer", layer.Name,
				"latency_ms", layerLatency,
				"total_ms", time.Since(chainStart).Milliseconds(),
			)
			return result, nil
		}

		// Layer produced no result. Normalise the error for the chain record.
		if err == nil {
			err = errors.New("layer returned nil result and nil error")
		}
		le := asLayerError(layer.Name, url, err)
		chainErr.Attempts = append(chainErr.Attempts, le)
		c.logger.Debug("fetch layer failed",
			"url", url,
			"layer", layer.Name,
			"kind", le.Kind.String(),
			"err", le.Err,
			"latency_ms", layerLatency,
		)
	}

	return nil, chainErr
}

// FetchMany fetches multiple URLs concurrently. Failures do not abort other
// URLs — the returned slice has the same length and order as the input, with
// nil entries where a fetch failed. The returned error is a join of every
// per-URL ChainError, or nil if every URL succeeded.
func (c *Chain) FetchMany(ctx context.Context, urls []string) ([]*Result, error) {
	if len(urls) == 0 {
		return nil, nil
	}

	results := make([]*Result, len(urls))
	errs := make([]error, len(urls))

	bound := c.concurrency
	if bound <= 0 || bound > len(urls) {
		bound = len(urls)
	}
	// chan struct{}: zero memory cost
	sem := make(chan struct{}, bound)

	var wg sync.WaitGroup
	// Once the parent context is cancelled we stop scheduling new work and
	// mark every remaining slot with the cancellation error. Workers already
	// running will observe the same ctx via c.Fetch and bail out naturally.
	scheduled := true
	for i, u := range urls {
		if !scheduled {
			errs[i] = ctx.Err()
			continue
		}
		// Cheap fast-path: skip the select dance if the ctx is already done.
		if err := ctx.Err(); err != nil {
			errs[i] = err
			scheduled = false
			continue
		}
		wg.Add(1)
		// Semaphore acquire must observe ctx cancellation — a plain
		// `sem <- struct{}{}` would wedge the producer goroutine when the
		// slots are full and a slow worker is blocked.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			errs[i] = ctx.Err()
			scheduled = false
			continue
		}
		go func(idx int, u string) {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := c.Fetch(ctx, u)
			results[idx] = r
			errs[idx] = err
		}(i, u)
	}
	wg.Wait()

	var failed []error
	for _, e := range errs {
		if e != nil {
			failed = append(failed, e)
		}
	}
	if len(failed) == 0 {
		return results, nil
	}
	return results, errors.Join(failed...)
}

// Close closes every layer's underlying Fetcher. Errors from individual
// layers are joined; all layers are always attempted. Close must not be
// called while any Fetch or FetchMany call is still in flight — see the
// concurrency contract on the Chain type.
func (c *Chain) Close() error {
	var errs []error
	for _, layer := range c.layers {
		if err := layer.Fetcher.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
