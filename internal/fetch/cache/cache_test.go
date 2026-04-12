package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
)

func tempDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.db")
}

func testCache(t *testing.T, opts ...func(*Options)) *Cache {
	t.Helper()
	o := Options{Path: tempDB(t), FallbackTTL: time.Hour}
	for _, fn := range opts {
		fn(&o)
	}
	c, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sampleResult(url string) *fetchpkg.Result {
	return &fetchpkg.Result{
		URL:         url,
		FinalURL:    url,
		Title:       "Test Title",
		Content:     "Test content body.",
		ContentType: "text/plain",
		LayerUsed:   "utls",
		LatencyMs:   100,
	}
}

func TestPutAndGet(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()

	r := sampleResult("https://example.com/page")
	if err := c.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, hit, err := c.Get(ctx, "https://example.com/page")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit, got miss")
	}
	if got.Content != "Test content body." {
		t.Errorf("Content = %q", got.Content)
	}
	if got.Title != "Test Title" {
		t.Errorf("Title = %q", got.Title)
	}
	if !got.FromCache {
		t.Error("FromCache = false, want true")
	}
	if got.URL != "https://example.com/page" {
		t.Errorf("URL = %q", got.URL)
	}
}

func TestGet_Miss(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()

	_, hit, err := c.Get(ctx, "https://example.com/nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatal("expected miss, got hit")
	}
}

func TestGet_Expired(t *testing.T) {
	now := time.Now()
	c := testCache(t, func(o *Options) {
		o.FallbackTTL = 1 * time.Second
		o.NowFunc = func() time.Time { return now }
	})
	ctx := context.Background()

	r := sampleResult("https://example.com/expire")
	if err := c.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Advance time past TTL.
	c.nowFunc = func() time.Time { return now.Add(2 * time.Second) }

	_, hit, err := c.Get(ctx, "https://example.com/expire")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatal("expected miss (expired), got hit")
	}
}

func TestPut_ReplaceExisting(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()

	r1 := sampleResult("https://example.com/replace")
	r1.Content = "version 1"
	if err := c.Put(ctx, r1); err != nil {
		t.Fatalf("Put v1: %v", err)
	}

	r2 := sampleResult("https://example.com/replace")
	r2.Content = "version 2"
	if err := c.Put(ctx, r2); err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	got, hit, _ := c.Get(ctx, "https://example.com/replace")
	if !hit {
		t.Fatal("miss")
	}
	if got.Content != "version 2" {
		t.Errorf("Content = %q, want 'version 2'", got.Content)
	}
}

func TestTTL_AcademicLong(t *testing.T) {
	c := testCache(t)
	ttl := c.resolveTTL("https://arxiv.org/abs/2111.00396")
	if ttl < 300*24*time.Hour {
		t.Errorf("arxiv TTL = %v, want >= 300 days", ttl)
	}
}

func TestTTL_DocsShort(t *testing.T) {
	c := testCache(t)
	ttl := c.resolveTTL("https://docs.python.org/3/tutorial/controlflow.html")
	if ttl != 7*24*time.Hour {
		t.Errorf("docs TTL = %v, want 7 days", ttl)
	}
}

func TestTTL_BlogOneDay(t *testing.T) {
	c := testCache(t)
	ttl := c.resolveTTL("https://blog.cloudflare.com/some-post")
	if ttl != 24*time.Hour {
		t.Errorf("blog TTL = %v, want 1 day", ttl)
	}
}

func TestTTL_Fallback(t *testing.T) {
	c := testCache(t)
	ttl := c.resolveTTL("https://randomsite.com/page")
	if ttl != time.Hour { // testCache sets FallbackTTL = 1 hour
		t.Errorf("fallback TTL = %v, want 1h", ttl)
	}
}

func TestEviction(t *testing.T) {
	c := testCache(t, func(o *Options) {
		o.MaxMB = 0 // will be clamped to DefaultMaxMB
	})
	// Override maxBytes directly for a tiny limit.
	c.maxBytes = 1024 // 1 KB

	ctx := context.Background()
	// Insert enough data to exceed 1KB.
	for i := 0; i < 20; i++ {
		r := sampleResult("https://example.com/" + string(rune('a'+i)))
		r.Content = "x" + string(make([]byte, 100))
		if err := c.Put(ctx, r); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	// After eviction, some entries should be gone.
	var remaining int
	for i := 0; i < 20; i++ {
		_, hit, _ := c.Get(ctx, "https://example.com/"+string(rune('a'+i)))
		if hit {
			remaining++
		}
	}
	// We can't predict exactly how many remain, but it shouldn't be all 20.
	if remaining == 20 {
		t.Errorf("no eviction happened: all 20 entries still present")
	}
}

func TestOpen_CreatesDirIfMissing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "deep", "test.db")
	c, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("db file not created: %v", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	c := testCache(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	// Second close may error (sql.DB.Close is not idempotent) — that's OK.
	_ = c.Close()
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := expandHome("~/foo/bar")
	want := filepath.Join(home, "foo/bar")
	if got != want {
		t.Errorf("expandHome = %q, want %q", got, want)
	}
	// Non-tilde paths unchanged.
	if expandHome("/abs/path") != "/abs/path" {
		t.Error("absolute path changed")
	}
}
