package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/odradekk/diting/internal/search"
)

// ExecuteConfig controls the execute phase behavior.
type ExecuteConfig struct {
	// MaxSourcesPerType caps results per source type in final selection.
	// Zero means 5.
	MaxSourcesPerType int

	// MaxFetchedTotal caps the total number of selected sources.
	// Zero means 15.
	MaxFetchedTotal int

	// Concurrency limits how many search modules run in parallel.
	// Zero means 4.
	Concurrency int

	// Logger is the structured logger for per-task events (module
	// successes, failures, dedup/selection stats). Nil means
	// slog.Default() — which in --debug mode is the CLI's JSON handler.
	Logger *slog.Logger
}

func (c ExecuteConfig) maxPerType() int {
	if c.MaxSourcesPerType > 0 {
		return c.MaxSourcesPerType
	}
	return 5
}

// maxTotal returns the global cap on selected (= fetched) sources.
//
// Default 25 (was 15) was chosen in Phase 5.7 Round 3.4. After Round 2.2's
// citation merge and Round 3.2's TopK=10, the scorer's window can hold
// up to 10 citations per query. The previous 15-source ceiling left
// only 5 fetched sources beyond the typical LLM-cited 4-5, which often
// missed authoritative domains the planner found but the scorer didn't
// see. Bumping to 25 lets the merge populate more of the topK window
// with high-quality fetched sources.
//
// Cost impact: the fetch chain handles 10 extra URLs per query, mostly
// served from cache after the first run. Wall-clock impact is small
// because fetching is parallel within concurrency=4.
func (c ExecuteConfig) maxTotal() int {
	if c.MaxFetchedTotal > 0 {
		return c.MaxFetchedTotal
	}
	return 25
}

func (c ExecuteConfig) concurrency() int {
	if c.Concurrency > 0 {
		return c.Concurrency
	}
	return 4
}

func (c ExecuteConfig) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// ExecuteResult holds the output of the execute phase.
type ExecuteResult struct {
	// AllResults is every result from all modules before dedup/scoring.
	AllResults []search.SearchResult
	// Selected is the top-K results after dedup, scoring, and selection.
	Selected []ScoredResult
}

// searchTask is one (module, query) pair to execute.
type searchTask struct {
	module search.Module
	query  string
}

// RunExecutePhase performs parallel search, dedup, scoring, and top-K selection.
func RunExecutePhase(
	ctx context.Context,
	plan Plan,
	modules []search.Module,
	scorer Scorer,
	question string,
	cfg ExecuteConfig,
) (*ExecuteResult, error) {
	logger := cfg.logger()

	// 1. Build task list: match plan's source types to available modules.
	tasks := buildSearchTasks(plan, modules)
	if len(tasks) == 0 {
		return nil, fmt.Errorf("execute: no search tasks (no modules match plan source types)")
	}
	logger.Debug("execute: task list built", "tasks", len(tasks), "modules", len(modules))

	// 2. Parallel search.
	raw := parallelSearch(ctx, tasks, cfg.concurrency(), logger)

	// 3. Dedup by URL.
	dedupped := dedupByURL(raw)
	logger.Debug("execute: dedup complete", "before", len(raw), "after", len(dedupped))

	// 4. Score.
	scored := scorer.Score(question, dedupped)

	// 5. Select top sources with per-source-type guarantee.
	selected := selectTopSources(scored, cfg.maxPerType(), cfg.maxTotal())
	logger.Debug("execute: selection complete",
		"scored", len(scored),
		"selected", len(selected),
		"max_per_type", cfg.maxPerType(),
		"max_total", cfg.maxTotal(),
	)

	return &ExecuteResult{
		AllResults: raw,
		Selected:   selected,
	}, nil
}

// buildSearchTasks maps plan queries to modules by source type.
func buildSearchTasks(plan Plan, modules []search.Module) []searchTask {
	// Index modules by source type.
	byType := make(map[search.SourceType][]search.Module)
	for _, m := range modules {
		st := m.Manifest().SourceType
		byType[st] = append(byType[st], m)
	}

	var tasks []searchTask
	for st, queries := range plan.QueriesBySourceType {
		mods, ok := byType[st]
		if !ok || len(mods) == 0 {
			continue
		}
		for _, q := range queries {
			if q == "" {
				continue
			}
			// Fan out to all modules of this source type.
			for _, m := range mods {
				tasks = append(tasks, searchTask{module: m, query: q})
			}
		}
	}
	return tasks
}

// parallelSearch executes search tasks with bounded concurrency.
// It collects all results, annotating each with Module/SourceType/Query.
// Errors from individual tasks are logged and skipped (partial success).
func parallelSearch(ctx context.Context, tasks []searchTask, concurrency int, logger *slog.Logger) []search.SearchResult {
	var (
		mu      sync.Mutex
		results []search.SearchResult
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
	)

	for _, task := range tasks {
		wg.Add(1)
		go func(t searchTask) {
			defer wg.Done()

			// Acquire semaphore (respect context cancellation).
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			manifest := t.module.Manifest()
			rs, err := t.module.Search(ctx, t.query)
			if err != nil {
				// Partial success: one bad module shouldn't fail the
				// whole phase. Surface the error at debug level so
				// --debug reveals which modules are misbehaving.
				logger.Debug("execute: module search failed",
					"module", manifest.Name,
					"source_type", manifest.SourceType,
					"query", t.query,
					"error", err,
				)
				return
			}

			logger.Debug("execute: module search success",
				"module", manifest.Name,
				"source_type", manifest.SourceType,
				"query", t.query,
				"results", len(rs),
			)

			mu.Lock()
			for _, r := range rs {
				r.Module = manifest.Name
				r.SourceType = manifest.SourceType
				r.Query = t.query
				results = append(results, r)
			}
			mu.Unlock()
		}(task)
	}

	wg.Wait()
	return results
}

// dedupByURL removes duplicate results by URL, keeping the first occurrence.
func dedupByURL(results []search.SearchResult) []search.SearchResult {
	seen := make(map[string]bool, len(results))
	out := make([]search.SearchResult, 0, len(results))

	for _, r := range results {
		key := normalizeURL(r.URL)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// trackingParams are URL query parameters that don't affect content identity.
var trackingParams = map[string]bool{
	"utm_source": true, "utm_medium": true, "utm_campaign": true,
	"utm_content": true, "utm_term": true, "utm_id": true,
	"fbclid": true, "gclid": true, "msclkid": true, "dclid": true,
	"mc_cid": true, "mc_eid": true,
	"ref": true, "referrer": true, "source": true,
	"_ga": true, "_gl": true,
	"spm": true, // Alibaba/Taobao tracking
}

// normalizeURL normalizes a URL for dedup comparison. It:
//   - lowercases scheme and host
//   - strips "www." prefix
//   - removes fragment (#...)
//   - removes tracking query parameters (utm_*, fbclid, etc.)
//   - sorts remaining query parameters alphabetically
//   - strips trailing slash from path (except root "/")
func normalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Fallback: best-effort lowercase + trim slash.
		return strings.ToLower(strings.TrimRight(rawURL, "/"))
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimPrefix(u.Host, "www.")
	u.Fragment = ""
	u.RawFragment = ""

	// Filter tracking params and sort remaining.
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if trackingParams[strings.ToLower(k)] {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode() // Encode() sorts keys alphabetically
	}

	// Strip trailing slash except for root path.
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}

	return u.String()
}

// selectTopSources picks the top results with per-source-type guarantee.
// It first ensures each source type gets up to maxPerType results (by score),
// then fills remaining slots from the global pool up to maxTotal.
func selectTopSources(scored []ScoredResult, maxPerType, maxTotal int) []ScoredResult {
	// Sort all by score descending.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	// Phase 1: guarantee per-source-type representation.
	typeCounts := make(map[search.SourceType]int)
	selected := make(map[int]bool)

	for i, r := range scored {
		st := r.SourceType
		if typeCounts[st] < maxPerType {
			selected[i] = true
			typeCounts[st]++
		}
		if len(selected) >= maxTotal {
			break
		}
	}

	// Phase 2: fill remaining slots from global top (if any room left).
	for i := range scored {
		if len(selected) >= maxTotal {
			break
		}
		if !selected[i] {
			selected[i] = true
		}
	}

	// Collect in score order.
	out := make([]ScoredResult, 0, len(selected))
	for i, r := range scored {
		if selected[i] {
			out = append(out, r)
		}
	}
	return out
}
