package utls

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// newTestFetcher returns a Fetcher with InsecureSkipVerify enabled (so it
// can talk to httptest's self-signed cert) and a short timeout so individual
// tests run fast.
func newTestFetcher(t *testing.T) *Fetcher {
	t.Helper()
	return New(Options{
		Timeout:            3 * time.Second,
		MaxBodyBytes:       64 * 1024,
		MaxRedirects:       3,
		InsecureSkipVerify: true,
	})
}

// newTLSServer starts an HTTPS server. If enableH2 is true, the server
// advertises "h2, http/1.1" in ALPN and handles h2 via net/http's built-in
// h2 support.
func newTLSServer(t *testing.T, handler http.Handler, enableH2 bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = enableH2
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch_H1_Success(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello h1")
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "hello h1" {
		t.Errorf("Content = %q, want %q", r.Content, "hello h1")
	}
	if r.FinalURL != srv.URL+"/page" {
		t.Errorf("FinalURL = %q, want %q", r.FinalURL, srv.URL+"/page")
	}
	if !strings.HasPrefix(r.ContentType, "text/plain") {
		t.Errorf("ContentType = %q, want text/plain...", r.ContentType)
	}
	if r.LatencyMs <= 0 {
		t.Errorf("LatencyMs = %d, want > 0", r.LatencyMs)
	}
}

func TestFetch_H2_Success(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("server saw protocol %q, want HTTP/2", r.Proto)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html>hello h2</html>")
	})
	srv := newTLSServer(t, handler, true)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "<html>hello h2</html>" {
		t.Errorf("Content = %q", r.Content)
	}
	if !strings.Contains(r.ContentType, "text/html") {
		t.Errorf("ContentType = %q", r.ContentType)
	}
}

func TestFetch_H1_Headers_SentAsChrome(t *testing.T) {
	var receivedUA, receivedSecChUa string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		receivedSecChUa = r.Header.Get("Sec-Ch-Ua")
		_, _ = io.WriteString(w, "ok")
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	if _, err := f.Fetch(context.Background(), srv.URL+"/"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(receivedUA, "Chrome") {
		t.Errorf("User-Agent = %q, want contains Chrome", receivedUA)
	}
	if !strings.Contains(receivedSecChUa, "Google Chrome") {
		t.Errorf("Sec-Ch-Ua = %q, want contains Google Chrome", receivedSecChUa)
	}
}

func TestFetch_Redirect_Followed(t *testing.T) {
	var visited atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visited.Add(1)
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		if r.URL.Path == "/end" {
			_, _ = io.WriteString(w, "final")
			return
		}
		http.NotFound(w, r)
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "final" {
		t.Errorf("Content = %q, want final", r.Content)
	}
	if !strings.HasSuffix(r.FinalURL, "/end") {
		t.Errorf("FinalURL = %q, want ...end", r.FinalURL)
	}
	if visited.Load() != 2 {
		t.Errorf("server visits = %d, want 2", visited.Load())
	}
}

func TestFetch_TooManyRedirects(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unconditional redirect loop back to self.
		http.Redirect(w, r, "/", http.StatusFound)
	})
	srv := newTLSServer(t, handler, false)
	f := New(Options{
		Timeout:            3 * time.Second,
		MaxBodyBytes:       1024,
		MaxRedirects:       2,
		InsecureSkipVerify: true,
	})
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("expected too-many-redirects error, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %T", err)
	}
	if le.Kind != fetchpkg.ErrUnknown {
		// We classify too-many-redirects as ErrUnknown (a client-side
		// policy violation, not a server-side block). Record the kind so
		// future classification tweaks surface here.
		t.Errorf("Kind = %v, want ErrUnknown", le.Kind)
	}
	if !strings.Contains(le.Err.Error(), "too many redirects") {
		t.Errorf("error text = %q", le.Err.Error())
	}
}

func TestFetch_BodyCap(t *testing.T) {
	payload := strings.Repeat("x", 10*1024) // 10 KiB
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	})
	srv := newTLSServer(t, handler, false)
	f := New(Options{
		Timeout:            3 * time.Second,
		MaxBodyBytes:       1024, // 1 KiB cap
		MaxRedirects:       3,
		InsecureSkipVerify: true,
	})
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(r.Content) != 1024 {
		t.Errorf("len(Content) = %d, want 1024 (capped)", len(r.Content))
	}
}

func TestFetch_Status403_Blocked(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "go away", http.StatusForbidden)
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

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

func TestFetch_Status404_NotFound(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(http.NotFound), false)
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/missing")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrNotFound {
		t.Errorf("Kind = %v, want ErrNotFound", le.Kind)
	}
}

func TestFetch_Status429_Blocked(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrBlocked {
		t.Errorf("Kind = %v, want ErrBlocked (429)", le.Kind)
	}
}

func TestFetch_Status500_Transport(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport", le.Kind)
	}
}

func TestFetch_NonHTTPS_Rejected(t *testing.T) {
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), "http://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if !strings.Contains(le.Err.Error(), "unsupported scheme") {
		t.Errorf("error = %v, want 'unsupported scheme'", le.Err)
	}
}

func TestFetch_InvalidURL(t *testing.T) {
	f := newTestFetcher(t)
	defer f.Close()

	// http.NewRequest does tolerant URL parsing, so we need something that
	// actually fails url.Parse. A control character is a reliable trigger.
	_, err := f.Fetch(context.Background(), "https://example.com/\x7f")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrParse {
		t.Errorf("Kind = %v, want ErrParse", le.Kind)
	}
}

func TestFetch_ContextCanceled(t *testing.T) {
	// Pre-cancelled ctx: net.Dialer.DialContext returns immediately with
	// an error that wraps context.Canceled. No server involvement needed
	// (and on localhost the dial is so fast that mid-flight cancellation
	// would be too racy to test reliably).
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), false)
	f := newTestFetcher(t)
	defer f.Close()

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
	if le.Kind != fetchpkg.ErrCanceled {
		t.Errorf("Kind = %v, want ErrCanceled", le.Kind)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false")
	}
}

func TestFetch_ContextDeadline(t *testing.T) {
	// Handler blocks until the client's ctx deadline fires. We use a
	// server-side done channel rather than a fixed Sleep so that the
	// httptest Server.Close() call at teardown does not have to wait out
	// a long Sleep — Close blocks on in-flight handlers.
	done := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	})
	srv := newTLSServer(t, handler, false)
	t.Cleanup(func() { close(done) })

	f := newTestFetcher(t)
	defer f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := f.Fetch(ctx, srv.URL+"/")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("Fetch took %v, want < 1.5s (should respect short ctx deadline)", elapsed)
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTimeout {
		t.Errorf("Kind = %v, want ErrTimeout", le.Kind)
	}
}

func TestFetchMany_SerialLoop(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "got %s", r.URL.Path)
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	urls := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}
	results, err := f.FetchMany(context.Background(), urls)
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("results[%d] is nil", i)
			continue
		}
		wantSuffix := strings.TrimPrefix(urls[i], srv.URL)
		if !strings.HasSuffix(r.Content, wantSuffix) {
			t.Errorf("results[%d].Content = %q, want suffix %q", i, r.Content, wantSuffix)
		}
	}
}

func TestFetchMany_PartialFailure(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "nope", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	urls := []string{srv.URL + "/good", srv.URL + "/bad", srv.URL + "/good"}
	results, err := f.FetchMany(context.Background(), urls)
	if err == nil {
		t.Fatal("expected partial error, got nil")
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	if results[0] == nil || results[2] == nil {
		t.Errorf("expected results[0] and [2] non-nil, got %v / %v", results[0], results[2])
	}
	if results[1] != nil {
		t.Errorf("results[1] = %v, want nil (the 403)", results[1])
	}
}

func TestFetch_Close_NoOp(t *testing.T) {
	f := newTestFetcher(t)
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
	// Second close also a no-op.
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
		{299, false, 0},
		{401, true, fetchpkg.ErrBlocked},
		{403, true, fetchpkg.ErrBlocked},
		{429, true, fetchpkg.ErrBlocked},
		{404, true, fetchpkg.ErrNotFound},
		{410, true, fetchpkg.ErrNotFound},
		{500, true, fetchpkg.ErrTransport},
		{502, true, fetchpkg.ErrTransport},
		{418, true, fetchpkg.ErrUnknown},
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

// --- content-encoding / body-integrity tests ------------------------------

func TestFetch_GzipDecompression(t *testing.T) {
	plaintext := "hello gzip world " + strings.Repeat("x", 500)
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", gzBuf.Len()))
		_, _ = w.Write(gzBuf.Bytes())
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != plaintext {
		t.Errorf("decompressed content mismatch: got %q (%d bytes), want %q (%d bytes)",
			r.Content[:min(32, len(r.Content))], len(r.Content),
			plaintext[:min(32, len(plaintext))], len(plaintext))
	}
}

// TestFetch_DeflateZlibWrapped verifies the RFC-7230-compliant case: HTTP
// Content-Encoding: deflate means zlib-wrapped DEFLATE (RFC 1950). This is
// what real servers send; raw DEFLATE is a historical compatibility quirk
// covered by the next test.
func TestFetch_DeflateZlibWrapped(t *testing.T) {
	plaintext := "deflate content " + strings.Repeat("a", 300)
	var zBuf bytes.Buffer
	zw := zlib.NewWriter(&zBuf)
	if _, err := zw.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "deflate")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", zBuf.Len()))
		_, _ = w.Write(zBuf.Bytes())
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != plaintext {
		t.Errorf("Content = %q, want %q", r.Content[:min(32, len(r.Content))], plaintext[:min(32, len(plaintext))])
	}
}

// TestFetch_DeflateRawFallback verifies the historical-compat case: some
// old servers send raw DEFLATE (RFC 1951) under Content-Encoding: deflate.
// decompressor routes the stream through isZlibHeader (CM/CINFO/FCHECK
// validation) — raw DEFLATE prefixes fail that check ~999/1000 of the
// time and fall through to flate.NewReader. This test exercises that
// fallback branch; the defensive t.Skipf below handles the ~1/1000
// coincidence where raw output happens to form a valid zlib header
// (fundamentally ambiguous to any header-peek heuristic).
func TestFetch_DeflateRawFallback(t *testing.T) {
	plaintext := "raw deflate content"
	var rawBuf bytes.Buffer
	fw, err := flate.NewWriter(&rawBuf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	if isZlibHeader(rawBuf.Bytes()) {
		t.Skipf("raw DEFLATE output coincidentally satisfies the RFC 1950 zlib header check (on the order of 1/1000) — any header-peek heuristic is fundamentally ambiguous here; first two bytes: % x", rawBuf.Bytes()[:2])
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "deflate")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", rawBuf.Len()))
		_, _ = w.Write(rawBuf.Bytes())
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != plaintext {
		t.Errorf("Content = %q, want %q", r.Content, plaintext)
	}
}

func TestFetch_BrotliDecompression(t *testing.T) {
	plaintext := "brotli payload " + strings.Repeat("y", 200)
	var brBuf bytes.Buffer
	bw := brotli.NewWriter(&brBuf)
	if _, err := bw.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", brBuf.Len()))
		_, _ = w.Write(brBuf.Bytes())
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != plaintext {
		t.Errorf("Content = %q, want %q", r.Content, plaintext)
	}
}

func TestFetch_ZstdDecompression(t *testing.T) {
	plaintext := "zstd payload " + strings.Repeat("z", 400)
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := zw.EncodeAll([]byte(plaintext), nil)
	zw.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "zstd")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(compressed)))
		_, _ = w.Write(compressed)
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != plaintext {
		t.Errorf("Content = %q, want %q", r.Content[:min(32, len(r.Content))], plaintext[:min(32, len(plaintext))])
	}
}

func TestFetch_AcceptEncodingHeaderMatchesChrome(t *testing.T) {
	var accept string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept-Encoding")
		_, _ = io.WriteString(w, "ok")
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	if _, err := f.Fetch(context.Background(), srv.URL+"/"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Chrome sends gzip, deflate, br, zstd.
	for _, enc := range []string{"gzip", "br", "zstd"} {
		if !strings.Contains(accept, enc) {
			t.Errorf("Accept-Encoding = %q, missing %q", accept, enc)
		}
	}
	if strings.Contains(accept, "identity") {
		t.Errorf("Accept-Encoding = %q, must not send 'identity' (non-Chrome fingerprint)", accept)
	}
}

// TestFetch_TruncatedContentLength verifies that a response which announces
// Content-Length: N but delivers fewer than N bytes is surfaced as an error
// rather than silently treated as a successful short body. Covers Codex's
// round-1 🔴 issue about blanket ErrUnexpectedEOF tolerance.
func TestFetch_TruncatedContentLength(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack not supported on this test server")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		// Advertise 100 bytes, deliver 50, then close abruptly.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n")
		_, _ = buf.WriteString(strings.Repeat("x", 50))
		_ = buf.Flush()
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("expected truncation error, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	// Truncation in the transport is classified as ErrTransport.
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport", le.Kind)
	}
	if !strings.Contains(le.Err.Error(), "truncated body") {
		t.Errorf("error = %v, want contains 'truncated body'", le.Err)
	}
}

// TestFetch_TruncatedGzip_WithContentLength verifies that a gzip body cut
// mid-stream with a declared Content-Length is surfaced as an error — the
// decoder, not Content-Length comparisons, is authoritative for compressed
// stream completeness (round-2 🔴 fix).
func TestFetch_TruncatedGzip_WithContentLength(t *testing.T) {
	plaintext := strings.Repeat("gzip payload ", 200)
	var full bytes.Buffer
	gz := gzip.NewWriter(&full)
	if _, err := gz.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if full.Len() < 40 {
		t.Fatalf("gzip output too short for truncation test: %d bytes", full.Len())
	}
	truncated := full.Bytes()[:full.Len()/2] // chop in half

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		// Advertise the full length but deliver only the truncated half.
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", full.Len())
		_, _ = buf.Write(truncated)
		_ = buf.Flush()
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("expected error for truncated gzip, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport", le.Kind)
	}
	if !strings.Contains(le.Err.Error(), "decode compressed body") {
		t.Errorf("error = %v, want contains 'decode compressed body'", le.Err)
	}
}

// TestFetch_TruncatedGzip_CloseDelimited verifies that a gzip body truncated
// mid-stream and close-delimited (no Content-Length) still surfaces as an
// error. This is the case the old tri-clause policy would have silently
// forgiven (contentLength < 0 → tolerate) — we must rely on the gzip
// decoder's view of completeness, not on the wire-length hint.
func TestFetch_TruncatedGzip_CloseDelimited(t *testing.T) {
	plaintext := strings.Repeat("gzip payload ", 200)
	var full bytes.Buffer
	gz := gzip.NewWriter(&full)
	if _, err := gz.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	truncated := full.Bytes()[:full.Len()/2]

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		// No Content-Length — close-delimited.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nConnection: close\r\n\r\n")
		_, _ = buf.Write(truncated)
		_ = buf.Flush()
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	_, err := f.Fetch(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("expected error for truncated close-delimited gzip, got nil")
	}
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport (decoder must reject truncated stream even without Content-Length)", le.Kind)
	}
}

// TestFetch_CloseDelimitedNoLengthTolerated verifies that a close-delimited
// (no Content-Length, no chunked) response still succeeds even though the
// underlying body reader surfaces ErrUnexpectedEOF on the close. This is
// the benign ADR §8.5 case we must continue to tolerate.
func TestFetch_CloseDelimitedNoLengthTolerated(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack not supported")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n")
		_, _ = buf.WriteString("hello close-delimited")
		_ = buf.Flush()
	})
	srv := newTLSServer(t, handler, false)
	f := newTestFetcher(t)
	defer f.Close()

	r, err := f.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "hello close-delimited" {
		t.Errorf("Content = %q", r.Content)
	}
}

// --- header-map isolation --------------------------------------------------

// TestNew_ClonesHeaderMap verifies that Options.Headers is copied at New
// time, so mutations to either the caller's map or DefaultHeaders do not
// affect the fetcher. Covers Codex's round-1 🟡 issue about shared map
// references.
func TestNew_ClonesHeaderMap(t *testing.T) {
	caller := map[string]string{
		"User-Agent": "MyAgent/1.0",
		"X-Custom":   "one",
	}
	f := New(Options{Headers: caller, InsecureSkipVerify: true})

	// Mutate the caller map after construction.
	caller["X-Custom"] = "two"
	delete(caller, "User-Agent")

	// Verify the fetcher retains the snapshot it was given.
	if got := f.opts.Headers["X-Custom"]; got != "one" {
		t.Errorf("fetcher X-Custom = %q, want 'one' (caller mutation leaked in)", got)
	}
	if got := f.opts.Headers["User-Agent"]; got != "MyAgent/1.0" {
		t.Errorf("fetcher User-Agent = %q, want 'MyAgent/1.0' (caller delete leaked in)", got)
	}

	// Two independent fetchers constructed from DefaultHeaders must not
	// share state through it.
	f1 := New(Options{InsecureSkipVerify: true})
	f2 := New(Options{InsecureSkipVerify: true})
	f1.opts.Headers["User-Agent"] = "fetcher1/custom"
	if f2.opts.Headers["User-Agent"] == "fetcher1/custom" {
		t.Error("f1 mutation leaked into f2 — DefaultHeaders clone is shared")
	}
	if DefaultHeaders["User-Agent"] == "fetcher1/custom" {
		t.Error("f1 mutation leaked into DefaultHeaders — clone is shallow-referencing the global")
	}
}

// --- FetchMany ctx-cancel wrapping -----------------------------------------

// TestFetchMany_CtxCancelWrappedAsLayerError verifies that when ctx is
// cancelled mid-batch, FetchMany stops and surfaces a single wrapped
// LayerError rather than N bare ctx.Err() values. Covers Codex's round-1
// 🔵 issue about error-shape inconsistency with single-URL Fetch.
func TestFetchMany_CtxCancelWrappedAsLayerError(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), false)
	f := newTestFetcher(t)
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	urls := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}
	results, err := f.FetchMany(ctx, urls)
	if err == nil {
		t.Fatal("expected error from cancelled FetchMany, got nil")
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for i, r := range results {
		if r != nil {
			t.Errorf("results[%d] = %v, want nil", i, r)
		}
	}

	// There must be exactly one LayerError in the joined error chain
	// (stop-on-first-cancel), and it must be classified as ErrCanceled.
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrCanceled {
		t.Errorf("Kind = %v, want ErrCanceled", le.Kind)
	}
	// Count layer errors by walking Unwrap() []error.
	count := countLayerErrors(err)
	if count != 1 {
		t.Errorf("LayerError count in joined err = %d, want 1 (stop-on-first-cancel)", count)
	}
}

func countLayerErrors(err error) int {
	if err == nil {
		return 0
	}
	var le *fetchpkg.LayerError
	if errors.As(err, &le) {
		// errors.As only walks until the first match; for a joined error
		// we need manual traversal.
	}
	n := 0
	var visit func(e error)
	visit = func(e error) {
		if e == nil {
			return
		}
		if le, ok := e.(*fetchpkg.LayerError); ok {
			n++
			_ = le
			return
		}
		// Go 1.20+ joined errors implement Unwrap() []error.
		if u, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range u.Unwrap() {
				visit(child)
			}
			return
		}
		if u, ok := e.(interface{ Unwrap() error }); ok {
			visit(u.Unwrap())
		}
	}
	visit(err)
	return n
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestIsZlibHeader(t *testing.T) {
	// Build a real zlib-wrapped stream and confirm its first two bytes
	// satisfy the check.
	var zBuf bytes.Buffer
	zw := zlib.NewWriter(&zBuf)
	if _, err := zw.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if !isZlibHeader(zBuf.Bytes()) {
		t.Errorf("real zlib output first 2 bytes (% x) failed isZlibHeader", zBuf.Bytes()[:2])
	}

	// Short inputs must reject cleanly.
	if isZlibHeader(nil) || isZlibHeader([]byte{0x78}) {
		t.Error("short input accepted")
	}

	// CM != 8 must reject.
	if isZlibHeader([]byte{0x77, 0x01}) {
		t.Error("CM != 8 accepted")
	}

	// CM == 8 but FCHECK violated (random FLG) must reject in most cases.
	// 0x78 0x00 → (0x7800 + 0x00) % 31 == 0x7800 % 31 = 8 → invalid.
	if isZlibHeader([]byte{0x78, 0x00}) {
		t.Error("0x78 0x00 accepted (FCHECK should fail)")
	}

	// 0x78 0x9C → standard zlib default-compression header,
	// 0x789C % 31 == 0 → valid.
	if !isZlibHeader([]byte{0x78, 0x9C}) {
		t.Error("0x78 0x9C rejected (should be a standard zlib header)")
	}

	// 0x88 0x1C → CM == 8 (valid) and (0x881C % 31) == 0 (FCHECK pass),
	// but CINFO == 8 which is explicitly disallowed by RFC 1950 §2.2.
	// Must reject — otherwise we'd route raw-DEFLATE prefixes with this
	// coincidence to zlib.NewReader and hard-fail instead of the flate
	// fallback.
	if isZlibHeader([]byte{0x88, 0x1C}) {
		t.Error("0x88 0x1C accepted (CINFO > 7 should fail)")
	}
}

func TestClassifyTransportError(t *testing.T) {
	if k := classifyTransportError(context.DeadlineExceeded); k != fetchpkg.ErrTimeout {
		t.Errorf("DeadlineExceeded → %v, want ErrTimeout", k)
	}
	if k := classifyTransportError(context.Canceled); k != fetchpkg.ErrCanceled {
		t.Errorf("Canceled → %v, want ErrCanceled", k)
	}
	if k := classifyTransportError(errors.New("random")); k != fetchpkg.ErrTransport {
		t.Errorf("random → %v, want ErrTransport", k)
	}
}
