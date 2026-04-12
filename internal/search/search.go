// Package search defines the Module interface, Manifest type, and registry
// for pluggable search backends. Each backend (bing, duckduckgo, brave, etc.)
// lives in a subpackage and registers itself via Register at init time.
//
// The pipeline asks the registry for the modules listed in the user's config,
// never for "all modules." Unknown module names produce a startup error.
package search

import "context"

// SourceType classifies the origin of search results. The LLM planner uses
// these to decide which modules to invoke for a given query.
type SourceType string

const (
	SourceTypeGeneralWeb SourceType = "general_web"
	SourceTypeAcademic   SourceType = "academic"
	SourceTypeCode       SourceType = "code"
	SourceTypeCommunity  SourceType = "community"
	SourceTypeDocs       SourceType = "docs"
)

// CostTier indicates the cost profile of a module. The planner prefers
// free/cheap modules and falls back to expensive ones only when needed.
type CostTier string

const (
	CostTierFree      CostTier = "free"
	CostTierCheap     CostTier = "cheap"
	CostTierExpensive CostTier = "expensive"
)

// Manifest describes a module's capabilities and constraints. It is static
// for the lifetime of a module instance. The Scope field is written for
// another LLM to understand what queries this module is good at.
type Manifest struct {
	Name       string     // stable identifier, matches registry key
	SourceType SourceType // single primary type — no multi-type modules in v1
	CostTier   CostTier
	Languages  []string // BCP 47 codes, e.g., "en", "zh-Hans"
	Scope      string   // ≤200 chars, human-readable, used by LLM planner
}

// SearchResult is a single search hit returned by a module.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string

	// Populated by the pipeline, not the module:
	Module     string     // module.Name() that produced this
	SourceType SourceType // copied from manifest
	Query      string     // the query string that produced it
}

// Module is the interface every search backend implements.
//
// Contract:
//  1. Must return within ctx deadline or respect cancellation.
//  2. Must return an error for HTTP failures, rate limits, parse failures;
//     empty results is not an error.
//  3. Must not mutate package-level state (modules run concurrently).
//  4. Must not write to disk outside the content cache path.
type Module interface {
	Manifest() Manifest
	Search(ctx context.Context, query string) ([]SearchResult, error)
	Close() error
}

// ModuleConfig is the opaque configuration blob passed to module factories.
// Each module interprets the fields it cares about and ignores the rest.
type ModuleConfig struct {
	// APIKey is the user-provided key for BYOK modules. Empty for keyless modules.
	APIKey string

	// Extra holds module-specific key/value pairs from the user's config.
	Extra map[string]string
}
