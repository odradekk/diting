// Package fetch defines the Fetcher interface and the chain orchestrator that
// tries multiple fetch layers in order. Individual layers (utls, chromedp, jina,
// archive, tavily) live in subpackages and are composed into a Chain by the
// caller. The Chain itself implements Fetcher so it can be substituted anywhere
// a single Fetcher is expected.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Result is the outcome of a successful fetch. The Chain sets LayerUsed to the
// name of the layer that produced it; layers should not set this field
// themselves.
type Result struct {
	URL         string
	FinalURL    string // after redirects
	Title       string
	Content     string // extracted main content (markdown after Phase 1.7)
	ContentType string // "text/html", "application/pdf", ...
	LayerUsed   string // set by Chain — "utls" | "chromedp" | "jina" | "archive" | "tavily" | "cache"
	LatencyMs   int64
	FromCache   bool
}

// Fetcher is the top-level fetch interface. Both Chain and individual layers
// implement it. Layers typically implement FetchMany as a serial loop (the
// Chain provides parallelism on top).
type Fetcher interface {
	Fetch(ctx context.Context, url string) (*Result, error)
	FetchMany(ctx context.Context, urls []string) ([]*Result, error)
	Close() error
}

// Layer is one element of the fallback chain. Disabled layers are filtered out
// when the Chain is constructed.
type Layer struct {
	Name    string
	Fetcher Fetcher
	Timeout time.Duration
	Enabled bool
}

// ErrKind categorises fetch failures so future layers or callers can decide
// whether the error is worth retrying with a different technique.
type ErrKind int

const (
	ErrUnknown   ErrKind = iota
	ErrBlocked           // HTTP 403, captcha, bot-protection page
	ErrNotFound          // HTTP 404 / 410
	ErrTimeout           // layer-level context deadline exceeded
	ErrCanceled          // caller cancelled the parent context
	ErrTransport         // TCP/TLS/DNS failure
	ErrParse             // response received but body could not be decoded
	ErrDisabled          // layer disabled via config (rare at runtime)
)

func (k ErrKind) String() string {
	switch k {
	case ErrBlocked:
		return "blocked"
	case ErrNotFound:
		return "not_found"
	case ErrTimeout:
		return "timeout"
	case ErrCanceled:
		return "canceled"
	case ErrTransport:
		return "transport"
	case ErrParse:
		return "parse"
	case ErrDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// LayerError is the structured error each layer returns. Layers should wrap
// their root cause with a LayerError so the Chain can classify failures
// uniformly.
type LayerError struct {
	Layer string
	URL   string
	Kind  ErrKind
	Err   error
}

func (e *LayerError) Error() string {
	return fmt.Sprintf("fetch %s via %s (%s): %v", e.URL, e.Layer, e.Kind, e.Err)
}

func (e *LayerError) Unwrap() error { return e.Err }

// ChainError is returned by Chain.Fetch when every layer has been attempted
// and none produced a result, or when the chain aborted early due to a
// parent-context cancellation. Attempts records only layers that were actually
// invoked; Cause is set (non-nil) when the chain stopped without trying every
// layer — typically because the parent context was cancelled between layers.
type ChainError struct {
	URL      string
	Attempts []*LayerError
	Cause    error // non-nil when the chain aborted without exhausting all layers
}

func (e *ChainError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("fetch %s: chain aborted after %d layer(s): %v", e.URL, len(e.Attempts), e.Cause)
	}
	if len(e.Attempts) == 0 {
		return fmt.Sprintf("fetch %s: no layers attempted", e.URL)
	}
	last := e.Attempts[len(e.Attempts)-1]
	return fmt.Sprintf("fetch %s: all %d layers failed; last: %v", e.URL, len(e.Attempts), last)
}

// Unwrap returns every underlying LayerError (and the Cause, if any) so
// errors.Is / errors.As can walk into the original causes.
func (e *ChainError) Unwrap() []error {
	errs := make([]error, 0, len(e.Attempts)+1)
	for _, a := range e.Attempts {
		errs = append(errs, a)
	}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	return errs
}

// asLayerError normalises an arbitrary error into a *LayerError. It preserves
// classification information when the underlying error is already a
// LayerError, classifies context deadlines as ErrTimeout and caller-initiated
// cancellations as ErrCanceled, and otherwise defaults to ErrUnknown.
func asLayerError(layer, url string, err error) *LayerError {
	if err == nil {
		return nil
	}
	var le *LayerError
	if errors.As(err, &le) {
		return le
	}
	kind := ErrUnknown
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = ErrTimeout
	case errors.Is(err, context.Canceled):
		kind = ErrCanceled
	}
	return &LayerError{Layer: layer, URL: url, Kind: kind, Err: err}
}
