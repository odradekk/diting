package fetch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFetcher is a scriptable Fetcher used by chain tests. It records every
// call and optionally delays before returning.
type fakeFetcher struct {
	name     string
	delay    time.Duration
	onFetch  func(ctx context.Context, url string) (*Result, error)
	closeErr error

	mu     sync.Mutex
	calls  int
	closed int
}

func (f *fakeFetcher) Fetch(ctx context.Context, url string) (*Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.onFetch(ctx, url)
}

func (f *fakeFetcher) FetchMany(ctx context.Context, urls []string) ([]*Result, error) {
	out := make([]*Result, len(urls))
	for i, u := range urls {
		r, err := f.Fetch(ctx, u)
		if err != nil {
			return out, err
		}
		out[i] = r
	}
	return out, nil
}

func (f *fakeFetcher) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return f.closeErr
}

func (f *fakeFetcher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeFetcher) Closed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// succeed builds a fetcher that always returns a Result tagged with its name
// in the Content field.
func succeed(name string) *fakeFetcher {
	f := &fakeFetcher{name: name}
	f.onFetch = func(ctx context.Context, url string) (*Result, error) {
		return &Result{URL: url, FinalURL: url, Content: "from " + name}, nil
	}
	return f
}

// fail builds a fetcher that always returns a classified LayerError.
func fail(name string, kind ErrKind) *fakeFetcher {
	f := &fakeFetcher{name: name}
	f.onFetch = func(ctx context.Context, url string) (*Result, error) {
		return nil, &LayerError{Layer: name, URL: url, Kind: kind, Err: errors.New(name + " failed")}
	}
	return f
}

func enabled(name string, f Fetcher, timeout time.Duration) Layer {
	return Layer{Name: name, Fetcher: f, Timeout: timeout, Enabled: true}
}

func TestChain_FirstLayerSucceeds(t *testing.T) {
	l1 := succeed("utls")
	l2 := succeed("chromedp")
	c := NewChain([]Layer{
		enabled("utls", l1, 0),
		enabled("chromedp", l2, 0),
	})

	r, err := c.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.LayerUsed != "utls" {
		t.Errorf("LayerUsed = %q, want utls", r.LayerUsed)
	}
	if l1.Calls() != 1 {
		t.Errorf("utls Calls = %d, want 1", l1.Calls())
	}
	if l2.Calls() != 0 {
		t.Errorf("chromedp Calls = %d, want 0 (should not be tried)", l2.Calls())
	}
}

func TestChain_FallsThroughOnFailure(t *testing.T) {
	l1 := fail("utls", ErrBlocked)
	l2 := fail("chromedp", ErrTransport)
	l3 := succeed("jina")
	c := NewChain([]Layer{
		enabled("utls", l1, 0),
		enabled("chromedp", l2, 0),
		enabled("jina", l3, 0),
	})

	r, err := c.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.LayerUsed != "jina" {
		t.Errorf("LayerUsed = %q, want jina", r.LayerUsed)
	}
	if l1.Calls() != 1 || l2.Calls() != 1 || l3.Calls() != 1 {
		t.Errorf("calls utls=%d chromedp=%d jina=%d; want 1/1/1", l1.Calls(), l2.Calls(), l3.Calls())
	}
}

func TestChain_AllLayersFail(t *testing.T) {
	l1 := fail("utls", ErrBlocked)
	l2 := fail("chromedp", ErrTransport)
	c := NewChain([]Layer{
		enabled("utls", l1, 0),
		enabled("chromedp", l2, 0),
	})

	_, err := c.Fetch(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected ChainError, got nil")
	}
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *ChainError: %T", err)
	}
	if len(ce.Attempts) != 2 {
		t.Errorf("Attempts = %d, want 2", len(ce.Attempts))
	}
	if ce.Attempts[0].Kind != ErrBlocked || ce.Attempts[1].Kind != ErrTransport {
		t.Errorf("kinds = %v/%v, want blocked/transport",
			ce.Attempts[0].Kind, ce.Attempts[1].Kind)
	}

	// errors.As should walk into individual LayerErrors via Unwrap() []error.
	var le *LayerError
	if !errors.As(err, &le) {
		t.Errorf("errors.As(err, *LayerError) failed; Unwrap chain broken")
	}
}

func TestChain_DisabledLayersFilteredOut(t *testing.T) {
	l1 := succeed("utls")
	l2 := succeed("chromedp")
	c := NewChain([]Layer{
		{Name: "utls", Fetcher: l1, Enabled: false},
		{Name: "chromedp", Fetcher: l2, Enabled: true},
	})

	layers := c.Layers()
	if len(layers) != 1 || layers[0].Name != "chromedp" {
		t.Fatalf("expected only chromedp enabled, got %v", layers)
	}

	r, err := c.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.LayerUsed != "chromedp" {
		t.Errorf("LayerUsed = %q, want chromedp", r.LayerUsed)
	}
}

func TestChain_EmptyChainReturnsChainError(t *testing.T) {
	c := NewChain(nil)
	_, err := c.Fetch(context.Background(), "https://example.com")
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *ChainError: %v", err)
	}
	if len(ce.Attempts) != 0 {
		t.Errorf("Attempts = %d, want 0", len(ce.Attempts))
	}
}

func TestChain_NilFetcherSkipped(t *testing.T) {
	l2 := succeed("chromedp")
	c := NewChain([]Layer{
		{Name: "broken", Fetcher: nil, Enabled: true},
		{Name: "chromedp", Fetcher: l2, Enabled: true},
	})
	if got := len(c.Layers()); got != 1 {
		t.Fatalf("enabled layers = %d, want 1", got)
	}
	r, err := c.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.LayerUsed != "chromedp" {
		t.Errorf("LayerUsed = %q", r.LayerUsed)
	}
}

func TestChain_ContextCancelStopsFallthrough(t *testing.T) {
	l1 := fail("utls", ErrBlocked)
	l2 := succeed("chromedp")
	c := NewChain([]Layer{
		enabled("utls", l1, 0),
		enabled("chromedp", l2, 0),
	})

	// Wrap l1 to cancel ctx mid-fetch so l2 is skipped.
	ctx, cancel := context.WithCancel(context.Background())
	l1.onFetch = func(_ context.Context, url string) (*Result, error) {
		cancel()
		return nil, &LayerError{Layer: "utls", URL: url, Kind: ErrBlocked, Err: errors.New("blocked")}
	}

	_, err := c.Fetch(ctx, "https://example.com")
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *ChainError: %v", err)
	}
	if l2.Calls() != 0 {
		t.Errorf("chromedp Calls = %d, want 0 (should be skipped after ctx cancel)", l2.Calls())
	}
	// l1 was actually attempted, so there should be exactly one real attempt.
	if len(ce.Attempts) != 1 {
		t.Errorf("Attempts = %d, want 1 (no phantom entry for skipped l2)", len(ce.Attempts))
	}
	if ce.Cause == nil || !errors.Is(ce.Cause, context.Canceled) {
		t.Errorf("Cause = %v, want context.Canceled", ce.Cause)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; Unwrap chain broken")
	}
}

func TestChain_PrecancelledContextNoPhantomAttempt(t *testing.T) {
	l1 := succeed("utls")
	l2 := succeed("chromedp")
	c := NewChain([]Layer{
		enabled("utls", l1, 0),
		enabled("chromedp", l2, 0),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Fetch(ctx, "https://example.com")
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *ChainError: %v", err)
	}
	if len(ce.Attempts) != 0 {
		t.Errorf("Attempts = %d, want 0 (no layer was actually tried)", len(ce.Attempts))
	}
	if ce.Cause == nil || !errors.Is(ce.Cause, context.Canceled) {
		t.Errorf("Cause = %v, want context.Canceled", ce.Cause)
	}
	if l1.Calls() != 0 || l2.Calls() != 0 {
		t.Errorf("calls = %d/%d, want 0/0", l1.Calls(), l2.Calls())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false")
	}
}

func TestChain_LayerTimeoutDoesNotKillChainCtx(t *testing.T) {
	slow := &fakeFetcher{
		name:  "slow",
		delay: 50 * time.Millisecond,
		onFetch: func(ctx context.Context, url string) (*Result, error) {
			return &Result{URL: url}, nil
		},
	}
	fast := succeed("fast")

	// slow has a 5ms timeout so its per-layer ctx expires before it returns.
	c := NewChain([]Layer{
		enabled("slow", slow, 5*time.Millisecond),
		enabled("fast", fast, 0),
	})

	r, err := c.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.LayerUsed != "fast" {
		t.Errorf("LayerUsed = %q, want fast", r.LayerUsed)
	}
}

func TestChain_FetchMany_AllSucceed(t *testing.T) {
	l1 := succeed("utls")
	c := NewChain([]Layer{enabled("utls", l1, 0)})

	urls := []string{"https://a", "https://b", "https://c"}
	results, err := c.FetchMany(context.Background(), urls)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("results[%d] is nil", i)
			continue
		}
		if r.URL != urls[i] {
			t.Errorf("results[%d].URL = %q, want %q", i, r.URL, urls[i])
		}
		if r.LayerUsed != "utls" {
			t.Errorf("results[%d].LayerUsed = %q, want utls", i, r.LayerUsed)
		}
	}
}

func TestChain_FetchMany_PartialFailure(t *testing.T) {
	var counter atomic.Int32
	flaky := &fakeFetcher{name: "flaky"}
	flaky.onFetch = func(_ context.Context, url string) (*Result, error) {
		n := counter.Add(1)
		if n%2 == 0 {
			return nil, &LayerError{Layer: "flaky", URL: url, Kind: ErrBlocked, Err: errors.New("nope")}
		}
		return &Result{URL: url, Content: url}, nil
	}
	c := NewChain([]Layer{enabled("flaky", flaky, 0)})

	urls := []string{"https://a", "https://b", "https://c", "https://d"}
	results, err := c.FetchMany(context.Background(), urls)
	if err == nil {
		t.Fatal("expected joined error for failing URLs, got nil")
	}
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4", len(results))
	}

	gotNil := 0
	for _, r := range results {
		if r == nil {
			gotNil++
		}
	}
	if gotNil != 2 {
		t.Errorf("nil results = %d, want 2", gotNil)
	}
}

func TestChain_FetchMany_RespectsConcurrency(t *testing.T) {
	var inflight atomic.Int32
	var peak atomic.Int32

	slow := &fakeFetcher{name: "slow"}
	slow.onFetch = func(ctx context.Context, url string) (*Result, error) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inflight.Add(-1)
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &Result{URL: url}, nil
	}

	c := NewChain([]Layer{enabled("slow", slow, 0)}, WithConcurrency(2))

	urls := make([]string, 8)
	for i := range urls {
		urls[i] = "https://example.com/" + string(rune('a'+i))
	}
	_, err := c.FetchMany(context.Background(), urls)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := peak.Load(); got > 2 {
		t.Errorf("peak concurrency = %d, want <= 2", got)
	}
}

func TestChain_Close(t *testing.T) {
	l1 := succeed("utls")
	l2 := succeed("chromedp")
	c := NewChain([]Layer{
		enabled("utls", l1, 0),
		enabled("chromedp", l2, 0),
	})
	if err := c.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l1.Closed() != 1 || l2.Closed() != 1 {
		t.Errorf("closed counts = %d/%d, want 1/1", l1.Closed(), l2.Closed())
	}
}

func TestChain_CloseJoinsErrors(t *testing.T) {
	boom1 := errors.New("boom1")
	boom2 := errors.New("boom2")
	l1 := &fakeFetcher{name: "a", closeErr: boom1, onFetch: func(ctx context.Context, u string) (*Result, error) {
		return &Result{}, nil
	}}
	l2 := &fakeFetcher{name: "b", closeErr: boom2, onFetch: func(ctx context.Context, u string) (*Result, error) {
		return &Result{}, nil
	}}
	c := NewChain([]Layer{
		enabled("a", l1, 0),
		enabled("b", l2, 0),
	})
	err := c.Close()
	if err == nil {
		t.Fatal("expected joined error, got nil")
	}
	if !errors.Is(err, boom1) || !errors.Is(err, boom2) {
		t.Errorf("joined error missing one of the causes: %v", err)
	}
}

func TestLayerError_ErrorsAs(t *testing.T) {
	inner := errors.New("boom")
	le := &LayerError{Layer: "utls", URL: "https://x", Kind: ErrBlocked, Err: inner}
	if !errors.Is(le, inner) {
		t.Errorf("errors.Is(le, inner) = false, want true")
	}
}

func TestAsLayerError_PreservesExistingKind(t *testing.T) {
	orig := &LayerError{Layer: "utls", URL: "https://x", Kind: ErrBlocked, Err: errors.New("nope")}
	got := asLayerError("outer", "https://y", orig)
	if got != orig {
		t.Errorf("asLayerError should return the original LayerError unchanged")
	}
}

func TestAsLayerError_ClassifiesContextDeadline(t *testing.T) {
	got := asLayerError("utls", "https://x", context.DeadlineExceeded)
	if got.Kind != ErrTimeout {
		t.Errorf("Kind = %v, want ErrTimeout", got.Kind)
	}
}

func TestAsLayerError_ClassifiesContextCanceled(t *testing.T) {
	got := asLayerError("utls", "https://x", context.Canceled)
	if got.Kind != ErrCanceled {
		t.Errorf("Kind = %v, want ErrCanceled (must not collapse to ErrTimeout)", got.Kind)
	}
	if !errors.Is(got, context.Canceled) {
		t.Errorf("errors.Is(got, context.Canceled) = false")
	}
}

// TestChain_FetchMany_SemaphoreRespectsContextCancel verifies that a producer
// goroutine blocked on a full semaphore observes ctx cancellation instead of
// waiting indefinitely for a wedged slow worker to release its slot.
func TestChain_FetchMany_SemaphoreRespectsContextCancel(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	slow := &fakeFetcher{name: "slow"}
	slow.onFetch = func(ctx context.Context, url string) (*Result, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return &Result{URL: url}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	c := NewChain([]Layer{enabled("slow", slow, 0)}, WithConcurrency(1))

	ctx, cancel := context.WithCancel(context.Background())
	urls := []string{"https://a", "https://b", "https://c"}

	done := make(chan struct {
		results []*Result
		err     error
	}, 1)
	go func() {
		r, err := c.FetchMany(ctx, urls)
		done <- struct {
			results []*Result
			err     error
		}{r, err}
	}()

	// Wait for the first worker to enter the blocking path. The producer
	// goroutine is now wedged on `sem <- struct{}{}` trying to schedule URL 2.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("worker never started")
	}

	cancel() // producer must unblock from its semaphore wait

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("expected error from cancelled FetchMany, got nil")
		}
		if !errors.Is(res.err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false: %v", res.err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("FetchMany did not observe ctx cancellation — producer wedged on semaphore")
	}

	close(release) // let the in-flight worker exit cleanly (nothing asserted about it)
}
