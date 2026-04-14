package chromedp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// skipIfNoBrowser skips the test if a real Chrome / Chromium browser
// is not available. chromedp needs an installed browser that can
// actually launch, not just one whose binary appears in PATH.
//
// In `go test -short` mode we always skip: GitHub-hosted runners have
// chromium-browser on PATH but Chromium's zygote fails to initialize
// inside the default sandbox (missing /sys/devices/system/cpu cpufreq
// files plus no SUID helper), so the test would crash with SIGABRT.
// Running the full non-short suite locally is the way to exercise
// these tests — they need an actual working browser environment.
func skipIfNoBrowser(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skipping chromedp tests (require a working browser environment)")
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	t.Skip("no Chrome / Chromium binary found in PATH — skipping chromedp tests")
}

// newTestFetcher creates a Fetcher with short timeout for fast tests.
func newTestFetcher(t *testing.T) *Fetcher {
	t.Helper()
	skipIfNoBrowser(t)
	f, err := New(Options{
		Timeout:      10 * time.Second,
		MaxBodyBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// newHTTPServer starts a plain HTTP server (not HTTPS). chromedp handles
// TLS internally; for local testing HTTP is simpler and avoids cert issues.
func newHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch_BasicHTML(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Hello Test</title></head><body><h1>Hello chromedp</h1></body></html>`)
	})
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	r, err := f.Fetch(context.Background(), srv.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(r.Content, "Hello chromedp") {
		t.Errorf("Content does not contain 'Hello chromedp': %s", r.Content[:min(200, len(r.Content))])
	}
	if r.Title != "Hello Test" {
		t.Errorf("Title = %q, want 'Hello Test'", r.Title)
	}
	if r.ContentType != "text/html" {
		t.Errorf("ContentType = %q, want text/html", r.ContentType)
	}
	// LatencyMs is populated from wall-clock elapsed ms. On fast machines
	// the headless-browser request can complete in under 1 ms wall-clock
	// once the browser is warm; only guard against a negative sign error.
	if r.LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >= 0", r.LatencyMs)
	}
}

func TestFetch_JSRenderedContent(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>SPA</title></head>
<body><div id="app"></div>
<script>document.getElementById('app').textContent = 'JS rendered';</script>
</body></html>`)
	})
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(r.Content, "JS rendered") {
		t.Errorf("JS-rendered content not found in body: %s", r.Content[:min(300, len(r.Content))])
	}
}

func TestFetch_Redirect(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `<html><head><title>End</title></head><body>final</body></html>`)
	})
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	r, err := f.Fetch(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(r.Content, "final") {
		t.Errorf("Content does not contain 'final'")
	}
	if !strings.HasSuffix(r.FinalURL, "/end") {
		t.Errorf("FinalURL = %q, want suffix /end", r.FinalURL)
	}
}

func TestFetch_Status403(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	_, err := f.Fetch(context.Background(), srv.URL+"/")
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

func TestFetch_Status404(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	_, err := f.Fetch(context.Background(), srv.URL+"/missing")
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
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport", le.Kind)
	}
}

func TestFetch_BodyCap(t *testing.T) {
	bigBody := strings.Repeat("x", 10*1024)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<html><body>%s</body></html>`, bigBody)
	})
	srv := newHTTPServer(t, handler)

	skipIfNoBrowser(t)
	f, err := New(Options{
		Timeout:      10 * time.Second,
		MaxBodyBytes: 1024, // 1 KiB cap
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	r, ferr := f.Fetch(context.Background(), srv.URL+"/")
	if ferr != nil {
		t.Fatalf("Fetch: %v", ferr)
	}
	if len(r.Content) > 1024 {
		t.Errorf("len(Content) = %d, want <= 1024", len(r.Content))
	}
}

func TestFetch_ContextCanceled(t *testing.T) {
	srv := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html><body>ok</body></html>")
	}))
	f := newTestFetcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Fetch(ctx, srv.URL+"/")
	if err == nil {
		t.Fatal("expected error from cancelled fetch, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrCanceled && le.Kind != fetchpkg.ErrTimeout {
		t.Errorf("Kind = %v, want ErrCanceled or ErrTimeout", le.Kind)
	}
}

func TestFetch_Timeout(t *testing.T) {
	done := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done // block until test cleanup
	})
	srv := newHTTPServer(t, handler)
	t.Cleanup(func() { close(done) })

	skipIfNoBrowser(t)
	f, err := New(Options{
		Timeout:      500 * time.Millisecond,
		MaxBodyBytes: 1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	start := time.Now()
	_, ferr := f.Fetch(context.Background(), srv.URL+"/")
	elapsed := time.Since(start)
	if ferr == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Fetch took %v, want < 5s", elapsed)
	}
}

func TestFetchMany_SerialLoop(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<html><body>got %s</body></html>`, r.URL.Path)
	})
	srv := newHTTPServer(t, handler)
	f := newTestFetcher(t)

	urls := []string{srv.URL + "/a", srv.URL + "/b"}
	results, err := f.FetchMany(context.Background(), urls)
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("results[%d] is nil", i)
			continue
		}
		wantPath := "/" + string(rune('a'+i))
		if !strings.Contains(r.Content, "got "+wantPath) {
			t.Errorf("results[%d].Content does not contain 'got %s'", i, wantPath)
		}
	}
}

func TestClose_TerminatesBrowser(t *testing.T) {
	skipIfNoBrowser(t)
	f, err := New(Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Second close should not panic.
	_ = f.Close()
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
		wantK   fetchpkg.ErrKind
	}{
		{200, false, 0},
		{0, false, 0}, // no status captured
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
