package fetch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// ContentExtractor post-processes a successful fetch result (e.g., HTML →
// clean text). If set on the Chain, it runs after the winning layer and
// before the result is returned. If extraction fails, the chain treats the
// layer as having failed and continues to the next one.
type ContentExtractor interface {
	Extract(ctx context.Context, result *Result) (*Result, error)
}

// ContentCache stores and retrieves post-extraction fetch results. If set
// on the Chain, a cache lookup runs before any layer is attempted. On a
// hit, the cached result is returned immediately with FromCache=true. On a
// miss, the chain proceeds normally and stores successful results after
// extraction. See docs/architecture.md §6.4.
type ContentCache interface {
	Get(ctx context.Context, url string) (*Result, bool, error)
	Put(ctx context.Context, result *Result) error
}

// Chain is a Fetcher that fires all enabled layers in parallel and returns
// the first successful Result.
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
	extractor   ContentExtractor
	cache       ContentCache
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

// WithExtractor sets a ContentExtractor that runs on every successful fetch
// result before it is returned. If extraction fails (e.g., readability
// produces empty output), the chain treats the layer as failed and continues
// to the next one. See docs/adr/0002-universal-content-extraction.md.
func WithExtractor(e ContentExtractor) ChainOption {
	return func(c *Chain) { c.extractor = e }
}

// WithCache enables content caching. Cache is checked before any layer;
// successful results are stored after extraction.
func WithCache(cc ContentCache) ChainOption {
	return func(c *Chain) { c.cache = cc }
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

// Fetch fires all enabled layers in parallel. It returns the first successful
// Result (with LayerUsed set to that layer's name) or a *ChainError containing
// every LayerError along the way. If a cache is configured, it is checked
// first; on a hit the cached result is returned without trying any layer.
func (c *Chain) Fetch(ctx context.Context, url string) (*Result, error) {
	if len(c.layers) == 0 && c.cache == nil {
		return nil, &ChainError{URL: url}
	}

	// Cache pre-check: return immediately on a valid hit.
	if c.cache != nil {
		if cached, hit, err := c.cache.Get(ctx, url); err == nil && hit {
			c.logger.Debug("cache hit", "url", url)
			return cached, nil
		}
	}

	if len(c.layers) == 0 {
		return nil, &ChainError{URL: url}
	}

	if err := ctx.Err(); err != nil {
		return nil, &ChainError{URL: url, Cause: err}
	}

	chainErr := &ChainError{URL: url}
	chainStart := time.Now()
	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()

	type fetchOutcome struct {
		layerName string
		result    *Result
		err       error
		latencyMs int64
		parseErr  bool
	}

	ch := make(chan fetchOutcome, len(c.layers))
	for _, layer := range c.layers {
		go func(l Layer) {
			layerCtx := raceCtx
			var cancel context.CancelFunc
			if l.Timeout > 0 {
				layerCtx, cancel = context.WithTimeout(raceCtx, l.Timeout)
			}

			layerStart := time.Now()
			result, err := l.Fetcher.Fetch(layerCtx, url)
			if cancel != nil {
				cancel()
			}
			layerLatency := time.Since(layerStart).Milliseconds()

			if err == nil && result != nil {
				result.LayerUsed = l.Name
				if result.LatencyMs == 0 {
					result.LatencyMs = layerLatency
				}

				// Run content extraction if configured (ADR 0002). If
				// extraction fails (e.g., readability can't find article
				// text), treat this layer as failed so the chain can fall
				// through to any other in-flight layer.
				if c.extractor != nil {
					result, err = c.extractor.Extract(raceCtx, result)
					if err != nil {
						ch <- fetchOutcome{layerName: l.Name, err: err, latencyMs: layerLatency, parseErr: true}
						return
					}
				}
			}

			if err == nil && result != nil {
				ch <- fetchOutcome{layerName: l.Name, result: result, latencyMs: layerLatency}
				return
			}
			if err == nil {
				err = errors.New("layer returned nil result and nil error")
			}
			ch <- fetchOutcome{layerName: l.Name, err: err, latencyMs: layerLatency}
		}(layer)
	}

	var winner *Result
	for range c.layers {
		outcome := <-ch
		if outcome.result != nil {
			if winner == nil {
				winner = outcome.result
				raceCancel()

				// Store in cache after extraction (post-extraction content
				// only — ADR 0002 §7: "cached content is already extracted").
				if c.cache != nil {
					if putErr := c.cache.Put(ctx, winner); putErr != nil {
						c.logger.Warn("cache put failed", "url", url, "err", putErr)
					}
				}

				c.logger.Debug("fetch ok",
					"url", url,
					"layer", outcome.layerName,
					"latency_ms", outcome.latencyMs,
					"total_ms", time.Since(chainStart).Milliseconds(),
				)
			}
			continue
		}
		if winner != nil {
			continue
		}

		le := asLayerError(outcome.layerName, url, outcome.err)
		if outcome.parseErr {
			le.Kind = ErrParse
			c.logger.Debug("fetch ok but extraction failed",
				"url", url,
				"layer", outcome.layerName,
				"err", outcome.err,
			)
		} else {
			c.logger.Debug("fetch layer failed",
				"url", url,
				"layer", outcome.layerName,
				"kind", le.Kind.String(),
				"err", le.Err,
				"latency_ms", outcome.latencyMs,
			)
		}
		chainErr.Attempts = append(chainErr.Attempts, le)
	}

	if winner != nil {
		return winner, nil
	}
	if err := ctx.Err(); err != nil {
		chainErr.Cause = err
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
