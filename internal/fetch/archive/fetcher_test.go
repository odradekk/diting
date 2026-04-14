package archive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

// fakeWayback serves both the availability API and the snapshot content.
// The availability handler is at /wayback/available, the snapshot at /raw.
func fakeWayback(t *testing.T, snap *snapshot, snapshotBody string) (*httptest.Server, *Fetcher) {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		ar := availabilityResponse{}
		if snap != nil {
			ar.ArchivedSnapshots.Closest = snap
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ar)
	})

	mux.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		if snapshotBody == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, snapshotBody)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:         3 * time.Second,
		MaxBodyBytes:    64 * 1024,
		AvailabilityURL: srv.URL + "/wayback/available",
		SnapshotBaseURL: srv.URL,
	})
	t.Cleanup(func() { _ = f.Close() })
	return srv, f
}

func TestFetch_Success(t *testing.T) {
	snap := &snapshot{
		Status:    "200",
		Available: true,
		URL:       "https://web.archive.org/web/20240601120000/https://example.com",
		Timestamp: "20240601120000",
	}
	_, f := fakeWayback(t, snap, "<html><body>Archived content</body></html>")

	r, err := f.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(r.Content, "Archived content") {
		t.Errorf("Content = %q, want contains 'Archived content'", r.Content)
	}
	if r.ContentType != "text/html" {
		t.Errorf("ContentType = %q, want text/html", r.ContentType)
	}
	if r.FinalURL != snap.URL {
		t.Errorf("FinalURL = %q, want %q", r.FinalURL, snap.URL)
	}
	// LatencyMs is populated from wall-clock elapsed ms. On fast machines
	// (and in CI under lightly loaded runners) the mocked local HTTP call
	// can round to 0 ms, so we only guard against a negative sign error
	// here rather than requiring a strictly positive value.
	if r.LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >= 0", r.LatencyMs)
	}
}

func TestFetch_NoSnapshot(t *testing.T) {
	_, f := fakeWayback(t, nil, "")

	_, err := f.Fetch(context.Background(), "https://example.com/never-archived")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrNotFound {
		t.Errorf("Kind = %v, want ErrNotFound (no snapshot)", le.Kind)
	}
	if le.Layer != LayerName {
		t.Errorf("Layer = %q, want %q", le.Layer, LayerName)
	}
}

func TestFetch_SnapshotNotAvailable(t *testing.T) {
	snap := &snapshot{
		Status:    "200",
		Available: false,
		URL:       "",
		Timestamp: "",
	}
	_, f := fakeWayback(t, snap, "")

	_, err := f.Fetch(context.Background(), "https://example.com/unavail")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrNotFound {
		t.Errorf("Kind = %v, want ErrNotFound", le.Kind)
	}
}

func TestFetch_EmptySnapshotContent(t *testing.T) {
	snap := &snapshot{
		Status:    "200",
		Available: true,
		URL:       "https://web.archive.org/web/20240601120000/https://example.com",
		Timestamp: "20240601120000",
	}
	_, f := fakeWayback(t, snap, "   \n\n  ")

	_, err := f.Fetch(context.Background(), "https://example.com")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrParse {
		t.Errorf("Kind = %v, want ErrParse (empty snapshot)", le.Kind)
	}
}

func TestFetch_AvailabilityAPI500(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:         3 * time.Second,
		AvailabilityURL: srv.URL + "/wayback/available",
	})
	defer f.Close()

	_, err := f.Fetch(context.Background(), "https://example.com")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrTransport {
		t.Errorf("Kind = %v, want ErrTransport (API 500)", le.Kind)
	}
}

func TestFetch_Snapshot403(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		ar := availabilityResponse{}
		ar.ArchivedSnapshots.Closest = &snapshot{
			Status: "200", Available: true,
			URL:       "https://web.archive.org/web/20240601120000/https://example.com",
			Timestamp: "20240601120000",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ar)
	})
	mux.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:         3 * time.Second,
		AvailabilityURL: srv.URL + "/wayback/available",
		SnapshotBaseURL: srv.URL,
	})
	defer f.Close()

	_, err := f.Fetch(context.Background(), "https://example.com")
	var le *fetchpkg.LayerError
	if !errors.As(err, &le) {
		t.Fatalf("error is not *LayerError: %v", err)
	}
	if le.Kind != fetchpkg.ErrBlocked {
		t.Errorf("Kind = %v, want ErrBlocked (snapshot 403)", le.Kind)
	}
}

func TestFetch_BodyCap(t *testing.T) {
	snap := &snapshot{
		Status: "200", Available: true,
		URL:       "https://web.archive.org/web/20240601120000/https://example.com",
		Timestamp: "20240601120000",
	}
	bigBody := "<html>" + strings.Repeat("x", 10*1024) + "</html>"

	mux := http.NewServeMux()
	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		ar := availabilityResponse{}
		ar.ArchivedSnapshots.Closest = snap
		_ = json.NewEncoder(w).Encode(ar)
	})
	mux.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, bigBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := New(Options{
		Timeout:         3 * time.Second,
		MaxBodyBytes:    1024,
		AvailabilityURL: srv.URL + "/wayback/available",
		SnapshotBaseURL: srv.URL,
	})
	defer f.Close()

	r, err := f.Fetch(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(r.Content) > 1024 {
		t.Errorf("len(Content) = %d, want <= 1024", len(r.Content))
	}
}

func TestFetch_ContextCanceled(t *testing.T) {
	_, f := fakeWayback(t, nil, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Fetch(ctx, "https://example.com")
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
	mux := http.NewServeMux()
	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		<-done
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { close(done); srv.Close() })

	f := New(Options{
		Timeout:         100 * time.Millisecond,
		AvailabilityURL: srv.URL + "/wayback/available",
	})
	defer f.Close()

	_, err := f.Fetch(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestFetchMany_SerialAndCancel(t *testing.T) {
	snap := &snapshot{
		Status: "200", Available: true,
		URL:       "https://web.archive.org/web/20240601120000/https://example.com",
		Timestamp: "20240601120000",
	}
	_, f := fakeWayback(t, snap, "<html><body>archived</body></html>")

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

	// Cancel variant.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.FetchMany(ctx, urls)
	if err == nil {
		t.Fatal("expected error from cancelled FetchMany")
	}
}

func TestClose_NoOp(t *testing.T) {
	f := New(Options{})
	if err := f.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
}

func TestToRawURL(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			"normal snapshot",
			"https://web.archive.org/web/20240601120000/https://example.com",
			"https://web.archive.org/web/20240601120000id_/https://example.com",
		},
		{
			"already raw",
			"https://web.archive.org/web/20240601120000id_/https://example.com",
			"https://web.archive.org/web/20240601120000id_/https://example.com",
		},
		{
			"with path",
			"http://web.archive.org/web/20230101/https://example.com/page?q=1",
			"http://web.archive.org/web/20230101id_/https://example.com/page?q=1",
		},
		{
			"unparseable (no /web/)",
			"https://other.archive.org/something",
			"https://other.archive.org/something",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toRawURL(c.input)
			if got != c.want {
				t.Errorf("toRawURL(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
		wantK   fetchpkg.ErrKind
	}{
		{200, false, 0},
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
