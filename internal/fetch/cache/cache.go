// Package cache implements a SQLite-backed content cache for the fetch
// pipeline. It stores post-extraction content so repeated fetches of the
// same URL skip the full chain + extraction cycle. See docs/architecture.md
// §6.4 for the schema and TTL policy.
//
// The cache stores extracted content (not raw HTML). ADR 0002 mandates:
// "Do NOT extract cached results — cached content is already extracted."
//
// Concurrency: the cache uses WAL mode for concurrent read access. Writes
// are serialized by SQLite's internal locking. Safe for use from multiple
// goroutines.
package cache

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	fetchpkg "github.com/odradekk/diting/internal/fetch"
	_ "modernc.org/sqlite"
)

// DefaultPath is the default cache database location.
const DefaultPath = "~/.cache/diting/content.db"

// Default configuration values.
const (
	DefaultMaxMB      = 256
	DefaultFallbackTTL = 3 * 24 * time.Hour // 3 days
)

// Options configures the cache.
type Options struct {
	// Path to the SQLite database file. Supports ~ for home directory.
	// Empty means DefaultPath.
	Path string

	// MaxMB is the approximate maximum database size in megabytes.
	// When exceeded, the oldest entries are evicted. Zero means DefaultMaxMB.
	MaxMB int

	// FallbackTTL is the default TTL for domains not matched by any rule.
	// Zero means DefaultFallbackTTL.
	FallbackTTL time.Duration

	// TTLRules overrides the domain→TTL mapping. If nil, defaultTTLRules
	// is used.
	TTLRules []TTLRule

	// NowFunc overrides time.Now for testing.
	NowFunc func() time.Time
}

// TTLRule maps a domain pattern to a TTL duration.
type TTLRule struct {
	// Contains is matched against the URL's hostname. If the hostname
	// contains this substring, the rule applies. First match wins.
	Contains string
	TTL      time.Duration
}

// Cache is a SQLite-backed content cache.
type Cache struct {
	db          *sql.DB
	maxBytes    int64
	fallbackTTL time.Duration
	ttlRules    []TTLRule
	nowFunc     func() time.Time
}

// Open creates or opens a cache database at the configured path.
func Open(opts Options) (*Cache, error) {
	path := opts.Path
	if path == "" {
		path = DefaultPath
	}
	path = expandHome(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("cache: create dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cache: open db: %w", err)
	}

	// Enable WAL for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cache: enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cache: set synchronous: %w", err)
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	maxMB := opts.MaxMB
	if maxMB <= 0 {
		maxMB = DefaultMaxMB
	}
	fallback := opts.FallbackTTL
	if fallback <= 0 {
		fallback = DefaultFallbackTTL
	}
	rules := opts.TTLRules
	if rules == nil {
		rules = defaultTTLRules
	}
	nowFn := opts.NowFunc
	if nowFn == nil {
		nowFn = time.Now
	}

	return &Cache{
		db:          db,
		maxBytes:    int64(maxMB) * 1024 * 1024,
		fallbackTTL: fallback,
		ttlRules:    rules,
		nowFunc:     nowFn,
	}, nil
}

func createSchema(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS content (
    url TEXT PRIMARY KEY,
    final_url TEXT,
    title TEXT,
    content TEXT NOT NULL,
    content_type TEXT,
    layer_used TEXT,
    fetched_at INTEGER NOT NULL,
    ttl_seconds INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fetched_at ON content(fetched_at);
`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("cache: create schema: %w", err)
	}
	return nil
}

// Get retrieves a cached result for the given URL. Returns (result, true, nil)
// on a valid cache hit, (nil, false, nil) on miss or expired entry, or
// (nil, false, err) on database error.
func (c *Cache) Get(ctx context.Context, url string) (*fetchpkg.Result, bool, error) {
	now := c.nowFunc().Unix()

	row := c.db.QueryRowContext(ctx,
		`SELECT final_url, title, content, content_type, layer_used, fetched_at, ttl_seconds
		 FROM content WHERE url = ? AND (fetched_at + ttl_seconds) > ?`,
		url, now)

	var r fetchpkg.Result
	var fetchedAt, ttlSec int64
	err := row.Scan(&r.FinalURL, &r.Title, &r.Content, &r.ContentType,
		&r.LayerUsed, &fetchedAt, &ttlSec)

	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache get: %w", err)
	}

	r.URL = url
	r.FromCache = true
	return &r, true, nil
}

// Put stores a fetch result in the cache. The TTL is determined by the
// URL's domain using the configured TTL rules.
func (c *Cache) Put(ctx context.Context, result *fetchpkg.Result) error {
	ttl := c.resolveTTL(result.URL)
	now := c.nowFunc().Unix()

	_, err := c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO content
		 (url, final_url, title, content, content_type, layer_used, fetched_at, ttl_seconds)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		result.URL, result.FinalURL, result.Title, result.Content,
		result.ContentType, result.LayerUsed, now, int64(ttl.Seconds()))

	if err != nil {
		return fmt.Errorf("cache put: %w", err)
	}

	// Best-effort eviction if over size limit.
	_ = c.evictIfNeeded(ctx)
	return nil
}

// Close closes the database connection.
func (c *Cache) Close() error {
	return c.db.Close()
}

// --- TTL resolution ---------------------------------------------------------

var defaultTTLRules = []TTLRule{
	// Academic — effectively permanent.
	{Contains: "arxiv.org", TTL: 365 * 24 * time.Hour},
	{Contains: "openalex.org", TTL: 365 * 24 * time.Hour},
	{Contains: "pubmed", TTL: 365 * 24 * time.Hour},
	{Contains: "jmlr.org", TTL: 365 * 24 * time.Hour},
	{Contains: "aclanthology.org", TTL: 365 * 24 * time.Hour},

	// Documentation — 7 days.
	{Contains: "docs.", TTL: 7 * 24 * time.Hour},
	{Contains: "developer.", TTL: 7 * 24 * time.Hour},
	{Contains: "go.dev", TTL: 7 * 24 * time.Hour},
	{Contains: "python.org", TTL: 7 * 24 * time.Hour},
	{Contains: "postgresql.org", TTL: 7 * 24 * time.Hour},
	{Contains: "react.dev", TTL: 7 * 24 * time.Hour},
	{Contains: "nextjs.org/docs", TTL: 7 * 24 * time.Hour},

	// News / time-sensitive — 1 day.
	{Contains: "news.", TTL: 24 * time.Hour},
	{Contains: "blog.", TTL: 24 * time.Hour},
	{Contains: "/blog/", TTL: 24 * time.Hour},

	// Tech articles / Q&A — 30 days.
	{Contains: "stackoverflow.com", TTL: 30 * 24 * time.Hour},
	{Contains: "github.com", TTL: 30 * 24 * time.Hour},
	{Contains: "wikipedia.org", TTL: 30 * 24 * time.Hour},

	// Fallback is handled separately via c.fallbackTTL.
}

func (c *Cache) resolveTTL(rawURL string) time.Duration {
	u, err := url.Parse(rawURL)
	if err != nil {
		return c.fallbackTTL
	}
	// Match against hostname + path for rules that include path fragments.
	target := strings.ToLower(u.Host + u.Path)

	for _, rule := range c.ttlRules {
		if strings.Contains(target, strings.ToLower(rule.Contains)) {
			return rule.TTL
		}
	}
	return c.fallbackTTL
}

// --- eviction ---------------------------------------------------------------

func (c *Cache) evictIfNeeded(ctx context.Context) error {
	var pageCount, pageSize int64
	row := c.db.QueryRowContext(ctx, "PRAGMA page_count")
	if err := row.Scan(&pageCount); err != nil {
		return err
	}
	row = c.db.QueryRowContext(ctx, "PRAGMA page_size")
	if err := row.Scan(&pageSize); err != nil {
		return err
	}

	dbSize := pageCount * pageSize
	if dbSize <= c.maxBytes {
		return nil
	}

	// Delete oldest 10% of entries by fetched_at.
	_, err := c.db.ExecContext(ctx,
		`DELETE FROM content WHERE url IN (
			SELECT url FROM content ORDER BY fetched_at ASC
			LIMIT (SELECT MAX(1, COUNT(*)/10) FROM content)
		)`)
	return err
}

// --- helpers ----------------------------------------------------------------

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
