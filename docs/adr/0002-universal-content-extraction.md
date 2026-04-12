# ADR 0002 — Universal content extraction as a post-chain step

**Status**: Accepted
**Date**: 2026-04-12
**Decider**: Manual observation of Phase 1.2 utls output (raw HTML with nav, script, style, ads), discussion of two extraction architectures
**Supersedes**: N/A

## Context

Phase 1.2 delivered the utls HTTP fetcher (`internal/fetch/utls/`). A manual smoke test of the layer against `https://en.wikipedia.org/wiki/Metasearch_engine` (via `test/debug/main.go`) revealed that `Result.Content` contains the raw HTML body — full of `<nav>`, `<script>`, `<style>`, sidebar navigation, footer links, and other non-article content.

This raw HTML is unusable as LLM input. diting's pipeline sends fetched content to an LLM in the "answer" phase (see `docs/architecture.md` §8). Every byte of irrelevant HTML translates directly to wasted tokens and degraded answer quality. The architecture's `Result.Content` field is documented as "extracted main content (markdown)" — the current implementation does not deliver on that contract.

The question is **where** content extraction should happen: inside individual fetch layers (so only the layers that produce HTML do the work), or as a universal post-chain step (so every layer's output passes through the same extractor).

## Decision

Content extraction is a **universal post-chain step** that every successful fetch result passes through before being returned to the caller. It is implemented as a ContentType-dispatched `Extractor` interface in `internal/fetch/extract/`.

Specifically:

1. **Position in the pipeline**: after the `Chain` obtains a successful `*Result` from any layer, and before the result is cached or returned to the caller. The extractor is wired as a post-processor in `Chain.Fetch`, not as a decorator inside individual layers.

2. **ContentType dispatch**:

   | ContentType | Strategy | Dependencies |
   |---|---|---|
   | `text/html` | `go-readability` → markdown, then `goquery` removal of residual nav/script/style/footer | `go-shiori/go-readability`, `PuerkitoBio/goquery` |
   | `text/markdown` / content that is already markdown | Light sanitize: collapse excessive whitespace, normalize link formats, strip residual HTML tags, truncate to token budget | None (stdlib only) |
   | `text/plain` | Pass-through + truncate to token budget | None |
   | `application/pdf` | Not handled in Phase 1; deferred to future extension | — |

3. **Interface**:

   ```go
   // Package: internal/fetch/extract
   type Extractor interface {
       Extract(ctx context.Context, result *fetch.Result) (*fetch.Result, error)
   }
   ```

4. **Token budget truncation**: every code path (HTML, markdown, plain) applies a configurable character-level truncation as the last step, so downstream consumers (the LLM) never receive more than `max_content_chars` (default: 32 000 characters, ~8 000 tokens) per page.

## Alternatives considered

| Alternative | Verdict | Reason |
|---|---|---|
| Per-layer extraction (only utls and chromedp do HTML cleanup) | Rejected | Couples extraction logic to individual layers; forces each HTML-producing layer to independently implement or call readability. Layers like jina and tavily *usually* return clean content, but not always — jina has been observed to include nav/footer residue, and tavily occasionally returns ad text. Without a universal extractor, these quality regressions silently propagate to LLM input. |
| No extraction at all (pass raw content to LLM, let the model handle it) | Rejected | Wastes tokens at scale. A typical Wikipedia page is ~250 KB of HTML; the article text is ~15 KB. Sending 17× more tokens per page directly degrades cost and latency. LLMs are also worse at extracting signal from noisy HTML than structured markdown. |
| Extraction inside each layer's `Fetch` method | Rejected | Same as per-layer but even more tightly coupled. Would require each layer to import `go-readability` and decide whether its output needs extraction. Violates single-responsibility: a layer's job is to *fetch*, not to *extract*. |
| Extraction as a separate `Fetcher` decorator (wraps the Chain) | Deferred | Architecturally clean (Extractor wraps Chain, both implement Fetcher), but adds one more layer of indirection for debugging. For Phase 1, wiring extraction inside `Chain.Fetch` after the winning layer is simpler and adequate. If the extractor grows complex or if we need to run extraction in a different goroutine, refactoring to a decorator is straightforward and backwards-compatible. |

## Evidence

### Observation

`test/debug/main.go` fetches `https://en.wikipedia.org/wiki/Metasearch_engine` via the Phase 1.2 utls layer:

- **Raw HTML size**: ~220 KB
- **Visible article text** (manually estimated): ~12 KB
- **Ratio**: ~5.5 % of the raw fetch is useful content; the remaining 94.5 % is HTML structure, navigation, scripts, styles, and metadata.

This single observation confirms the need for extraction. The exact ratio will vary by site, but the pattern (article content is a small fraction of the raw HTML body) is universal across the web.

### Prior art

- **Python v1** used `trafilatura` for HTML-to-text extraction. It was applied universally to all fetched pages, regardless of source.
- **go-readability** (`github.com/go-shiori/go-readability`) is the Go port of Mozilla's Readability.js, the same algorithm used by Firefox Reader View. It is well-maintained and handles the common case (news articles, blog posts, documentation pages) well.
- **jina reader** (`r.jina.ai`) applies server-side extraction and returns markdown. However, its output quality is not guaranteed — it occasionally includes navigation elements, cookie banners, and footer links. A universal extractor provides defense-in-depth.
- **tavily extract** returns pre-cleaned content, but its definition of "clean" may not match diting's token-density requirements (e.g., it may retain sidebar content that we would strip).

### Why ContentType dispatch (not "always run readability")

Running `go-readability` on content that is already markdown (e.g., jina output) would corrupt the formatting — readability expects HTML and would treat markdown syntax as plain text, stripping headings and links. ContentType dispatch avoids this: HTML gets the full readability pipeline, non-HTML gets light sanitization only.

## Consequences

### Positive

- **Token efficiency**: 10–20× reduction in content size for HTML pages, directly reducing LLM cost and improving answer quality.
- **Consistent output contract**: `Result.Content` always contains cleaned markdown after extraction, regardless of which layer produced it. Downstream code (scoring, LLM prompt building) can rely on this invariant.
- **Single test surface**: one `internal/fetch/extract/` package to test and tune, rather than extraction logic scattered across N layers.
- **Defense-in-depth**: catches quality regressions in API sources (jina, tavily) that would otherwise silently reach the LLM.

### Negative

- **Added latency**: go-readability adds ~5 ms per HTML page. For the typical diting query (10–30 fetched pages), this is 50–150 ms total — within the architecture's 60–90 s soft cliff.
- **New dependencies**: `go-shiori/go-readability` and `PuerkitoBio/goquery` (transitive via readability). These are well-maintained but add to the module graph.
- **Possible over-extraction**: readability may strip content that a human would consider relevant (tables, code blocks in unusual HTML structures). This needs to be tested against the benchmark query set and tuned.

### Follow-up required

- **Phase 1.7**: implement `internal/fetch/extract/` per this ADR.
- **Phase 1.9**: integration tests must verify that extraction does not regress the chain's success rate (a page that fetched successfully but extracts to empty content should be treated as a fetch failure for fallback purposes).
- **Phase 5 benchmark**: include "content quality" as a dimension — compare extracted content against ground-truth article text for precision/recall.

### Trigger conditions for re-evaluation

Re-open this ADR or write a new one if **any** of the following is observed:

- `go-readability` extraction quality drops below 70 % on the benchmark's content-precision metric (e.g., after a library version bump that changes heuristics).
- A new fetch layer produces output that requires a fundamentally different extraction strategy not covered by the ContentType dispatch table (e.g., a JSON API that returns structured data, not HTML or markdown).
- The ~5 ms per-page extraction latency becomes a bottleneck (unlikely unless we scale to 100+ pages per query).

## Implementation notes

1. **Package**: `internal/fetch/extract/`. One file per extraction strategy (`html.go`, `markdown.go`, `text.go`) plus `extractor.go` for the dispatcher.
2. **HTML extraction**: `go-readability` first for article extraction, then `goquery` for post-processing (remove any residual `<nav>`, `<script>`, `<style>`, `<footer>`, `<aside>` elements that readability missed). Convert the cleaned HTML to markdown (either via readability's built-in text output or a lightweight HTML-to-markdown converter).
3. **Title extraction**: from `<title>` tag for HTML; from the first `# heading` for markdown; empty for plain text.
4. **Token budget truncation**: apply *after* extraction, not before. Truncating raw HTML before extraction would corrupt the HTML structure and break readability.
5. **Empty-content guard**: if extraction produces an empty `Content` string (readability could not find article text), the extractor should return an error so the chain can fall through to the next layer. This prevents "successful fetch of a nav-only page" from consuming the chain's success slot.
6. **Wire into Chain**: add an optional `Extractor` field to `Chain`. If set, `Chain.Fetch` calls `Extractor.Extract(ctx, result)` after the winning layer returns and before caching/returning. If extraction fails, the chain treats it as a layer failure and continues to the next layer.
7. **Do NOT extract cached results**: cached content is already extracted (it was extracted before caching). The cache stores post-extraction content.

## References

- Manual observation: `test/debug/main.go` fetching Wikipedia via utls layer
- [go-readability](https://github.com/go-shiori/go-readability) — Go port of Mozilla Readability.js
- [goquery](https://github.com/PuerkitoBio/goquery) — jQuery-style HTML manipulation for Go
- Python v1's `trafilatura` extraction layer (predecessor)
- Architecture: `docs/architecture.md` §6.2, §6.5, Phase 1.7
- ADR writing guide: `docs/adr/README.md`
