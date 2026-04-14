package jina

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// newFakeJina starts a local HTTP server that mimics the jina reader API.
// The handler receives the encoded target URL as the path and returns
// markdown content.
func newFakeJina(t *testing.T, handler http.Handler) (*httptest.Server, *Fetcher) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	f := New(Options{
		Timeout:      3 * time.Second,
		MaxBodyBytes: 64 * 1024,
		BaseURL:      srv.URL,
	})
	t.Cleanup(func() { _ = f.Close() })
	return srv, f
}

func TestFetch_Success(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/markdown" {
			t.Errorf("Accept = %q, want text/markdown", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = io.WriteString(w, "# Test Page\n\nHello from jina reader.\n")
	})
	_, f := newFakeJina(t, handler)

	r, err := f.Fetch(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(r.Content, "Hello from jina reader") {
		t.Errorf("Content = %q, want contains 'Hello from jina reader'", r.Content)
	}
	if r.Title != "Test Page" {
		t.Errorf("Title = %q, want 'Test Page'", r.Title)
	}
	if r.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", r.ContentType)
	}
	if r.URL != "https://example.com/page" {
		t.Errorf("URL = %q", r.URL)
	}
	// LatencyMs is populated from wall-clock elapsed ms. On fast machines
	// (and in CI under lightly loaded runners) the mocked local HTTP call
	// can round to 0 ms, so we only guard against a negative sign error
	// here rather than requiring a strictly positive value.
	if r.LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >= 0", r.LatencyMs)
	}
}

func TestFetch_APIKeyHeader(t *testing.T) {
	var gotAuth string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "# OK\n\ncontent\n")
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:      3 * time.Second,
		MaxBodyBytes: 64 * 1024,
		BaseURL:      srv.URL,
		APIKey:       "test-key-123",
	})
	defer f.Close()

	if _, err := f.Fetch(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer test-key-123" {
		t.Errorf("Authorization = %q, want 'Bearer test-key-123'", gotAuth)
	}
}

func TestFetch_NoAPIKey_NoAuthHeader(t *testing.T) {
	var gotAuth string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "# OK\n\ncontent\n")
	})
	_, f := newFakeJina(t, handler)

	if _, err := f.Fetch(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (no API key configured)", gotAuth)
	}
}

func TestFetch_URLEncoding(t *testing.T) {
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "# OK\n\ncontent\n")
	})
	_, f := newFakeJina(t, handler)

	target := "https://example.com/path?q=hello world&lang=en"
	if _, err := f.Fetch(context.Background(), target); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The target URL should be path-escaped in the jina request URL.
	if !strings.Contains(gotPath, "example.com") {
		t.Errorf("path = %q, want contains 'example.com'", gotPath)
	}
}

func TestFetch_EmptyContentReturnsError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "   \n\n  ") // whitespace only
	})
	_, f := newFakeJina(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/empty")
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrParse {
		t.Errorf("Kind = %v, want ErrParse", le.Kind)
	}
}

func TestFetch_Status403_Blocked(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	_, f := newFakeJina(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrBlocked {
		t.Errorf("Kind = %v, want ErrBlocked", le.Kind)
	}
	if le.Layer != LayerName {
		t.Errorf("Layer = %q, want %q", le.Layer, LayerName)
	}
}

func TestFetch_Status429_RateLimit(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
	})
	_, f := newFakeJina(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrBlocked {
		t.Errorf("Kind = %v, want ErrBlocked (429 = rate limit)", le.Kind)
	}
}

func TestFetch_Status404(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, f := newFakeJina(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/missing")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrNotFound {
		t.Errorf("Kind = %v, want ErrNotFound", le.Kind)
	}
}

func TestFetch_Status500(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, f := newFakeJina(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport", le.Kind)
	}
}

func TestFetch_BodyCap(t *testing.T) {
	bigContent := "# Big\n\n" + strings.Repeat("x", 10*1024)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, bigContent)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:      3 * time.Second,
		MaxBodyBytes: 1024,
		BaseURL:      srv.URL,
	})
	defer f.Close()

	r, err := f.Fetch(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(r.Content) > 1024 {
		t.Errorf("len(Content) = %d, want <= 1024", len(r.Content))
	}
}

func TestFetch_ContextCanceled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "# OK\n\ncontent\n")
	})
	_, f := newFakeJina(t, handler)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Fetch(ctx, "https://example.com/")
	if err == nil {
		t.Fatal("expected error from cancelled fetch, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrCanceled {
		t.Errorf("Kind = %v, want ErrCanceled", le.Kind)
	}
}

func TestFetch_Timeout(t *testing.T) {
	done := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { close(done); srv.Close() })

	f := New(Options{
		Timeout:      100 * time.Millisecond,
		MaxBodyBytes: 1024,
		BaseURL:      srv.URL,
	})
	defer f.Close()

	start := time.Now()
	_, err := f.Fetch(context.Background(), "https://example.com/")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Fetch took %v, want < 3s", elapsed)
	}
}

func TestFetch_TitleExtraction(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"h1 heading", "# My Title\n\nBody text.", "My Title"},
		{"no heading", "Just some text.\nNo heading here.", ""},
		{"heading after blank lines", "\n\n# Late Title\n\nBody.", "Late Title"},
		{"h2 not extracted", "## Subtitle\n\nBody.", ""}, // only # is extracted
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractMarkdownTitle(c.content)
			if got != c.want {
				t.Errorf("extractMarkdownTitle = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFetchMany_SerialLoop(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "# Page\n\ngot "+r.URL.Path+"\n")
	})
	_, f := newFakeJina(t, handler)

	urls := []string{"https://a.com", "https://b.com"}
	results, err := f.FetchMany(context.Background(), urls)
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("results[%d] is nil", i)
		}
	}
}

func TestFetchMany_CtxCancelStops(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "# OK\n\ncontent\n")
	})
	_, f := newFakeJina(t, handler)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	urls := []string{"https://a.com", "https://b.com", "https://c.com"}
	_, err := f.FetchMany(ctx, urls)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrCanceled {
		t.Errorf("Kind = %v, want ErrCanceled", le.Kind)
	}
}

func TestClose_NoOp(t *testing.T) {
	f := New(Options{})
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
		wantK   fetchpkg.ErrKind
	}{
		{200, false, 0},
		{204, false, 0},
		{403, true, fetchpkg.ErrBlocked},
		{429, true, fetchpkg.ErrBlocked},
		{404, true, fetchpkg.ErrNotFound},
		{500, true, fetchpkg.ErrTransport},
	}
	for _, c := range cases {
		k, isErr := classifyStatus(c.status)
		if isErr != c.wantErr {
			t.Errorf("classifyStatus(%d) isErr = %v, want %v", c.status, isErr, c.wantErr)
		}
		if isErr && k != c.wantK {
			t.Errorf("classifyStatus(%d) = %v, want %v", c.status, k, c.wantK)
		}
	}
}
