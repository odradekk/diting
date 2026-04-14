package tavily

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// newFakeTavily starts a local HTTP server that mimics the Tavily Extract
// API at /extract and returns a Fetcher pointed at it.
func newFakeTavily(t *testing.T, handler http.Handler) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	f := New(Options{
		Timeout:      3 * time.Second,
		MaxBodyBytes: 64 * 1024,
		APIKey:       "test-key",
		BaseURL:      srv.URL,
	})
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// extractHandler returns a handler that serves a successful extract response
// with the given content for any URL.
func extractHandler(content string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method and headers.
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "want application/json", http.StatusBadRequest)
			return
		}

		var req extractRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.URLs) == 0 {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}

		resp := extractResponse{
			Results: []extractResult{{
				URL:        req.URLs[0],
				RawContent: content,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func TestFetch_Success(t *testing.T) {
	f := newFakeTavily(t, extractHandler("Extracted content from tavily"))

	r, err := f.Fetch(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "Extracted content from tavily" {
		t.Errorf("Content = %q", r.Content)
	}
	if r.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", r.ContentType)
	}
	if r.URL != "https://example.com/page" {
		t.Errorf("URL = %q", r.URL)
	}
	// LatencyMs is populated from wall-clock elapsed ms. On fast machines
	// the mocked local HTTP call can round to 0 ms; only guard against a
	// negative sign error rather than requiring a strictly positive value.
	if r.LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >= 0", r.LatencyMs)
	}
}

func TestFetch_AuthHeader(t *testing.T) {
	var gotAuth string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp := extractResponse{Results: []extractResult{{RawContent: "ok"}}}
		_ = json.NewEncoder(w).Encode(resp)
	})
	f := newFakeTavily(t, handler)

	if _, err := f.Fetch(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want 'Bearer test-key'", gotAuth)
	}
}

func TestFetch_NoAPIKey_Disabled(t *testing.T) {
	f := New(Options{}) // no key
	defer f.Close()

	_, err := f.Fetch(context.Background(), "https://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrDisabled {
		t.Errorf("Kind = %v, want ErrDisabled", le.Kind)
	}
	if le.Layer != LayerName {
		t.Errorf("Layer = %q, want %q", le.Layer, LayerName)
	}
}

func TestFetch_ExtractionFailed(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req extractRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := extractResponse{
			FailedResults: []failedResult{{
				URL:   req.URLs[0],
				Error: "could not reach site",
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	f := newFakeTavily(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/fail")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport", le.Kind)
	}
	if !strings.Contains(le.Err.Error(), "could not reach site") {
		t.Errorf("error = %v, want contains 'could not reach site'", le.Err)
	}
}

func TestFetch_EmptyContent(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req extractRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := extractResponse{
			Results: []extractResult{{URL: req.URLs[0], RawContent: "", Content: ""}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	f := newFakeTavily(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/empty")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrParse {
		t.Errorf("Kind = %v, want ErrParse (empty content)", le.Kind)
	}
}

func TestFetch_URLNotInResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := extractResponse{
			Results: []extractResult{{URL: "https://other.com", RawContent: "wrong url"}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	f := newFakeTavily(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/mine")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrParse {
		t.Errorf("Kind = %v, want ErrParse (url not in response)", le.Kind)
	}
}

func TestFetch_FallbackToContentField(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req extractRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := extractResponse{
			Results: []extractResult{{
				URL:        req.URLs[0],
				RawContent: "",          // empty raw
				Content:    "fallback!", // non-empty content
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	f := newFakeTavily(t, handler)

	r, err := f.Fetch(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Content != "fallback!" {
		t.Errorf("Content = %q, want 'fallback!'", r.Content)
	}
}

func TestFetch_Status401_Blocked(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	f := newFakeTavily(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrBlocked {
		t.Errorf("Kind = %v, want ErrBlocked (401)", le.Kind)
	}
}

func TestFetch_Status429_RateLimit(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
	})
	f := newFakeTavily(t, handler)

	_, err := f.Fetch(context.Background(), "https://example.com/")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrBlocked {
		t.Errorf("Kind = %v, want ErrBlocked (429)", le.Kind)
	}
}

func TestFetch_Status500(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	f := newFakeTavily(t, handler)

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
	bigContent := strings.Repeat("x", 10*1024)
	srv := httptest.NewServer(extractHandler(bigContent))
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:      3 * time.Second,
		MaxBodyBytes: 1024,
		APIKey:       "key",
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
	f := newFakeTavily(t, extractHandler("ok"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Fetch(ctx, "https://example.com/")
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
		APIKey:       "key",
		BaseURL:      srv.URL,
	})
	defer f.Close()

	_, err := f.Fetch(context.Background(), "https://example.com/")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestFetchMany_SerialAndCancel(t *testing.T) {
	f := newFakeTavily(t, extractHandler("content"))

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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.FetchMany(ctx, urls)
	if err == nil {
		t.Fatal("expected error from cancelled FetchMany")
	}
}

func TestClose_NoOp(t *testing.T) {
	f := New(Options{APIKey: "key"})
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
		wantK   fetchpkg.ErrKind
	}{
		{200, false, 0},
		{401, true, fetchpkg.ErrBlocked},
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
