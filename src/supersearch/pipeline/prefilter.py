"""Pre-filter layer between dedup and LLM scoring.

Removes low-value results (thin snippets, near-duplicate content)
before they consume LLM tokens.  Domain/path-based filtering is
handled by the unified blacklist module.
"""

from __future__ import annotations

from supersearch.log import get_logger
from supersearch.models import SearchResult

logger = get_logger("pipeline.prefilter")


def prefilter(
    results: list[SearchResult],
    *,
    min_snippet_length: int = 30,
) -> tuple[list[SearchResult], dict[str, int]]:
    """Filter low-value search results before LLM scoring.

    Args:
        results: Deduplicated search results.
        min_snippet_length: Minimum snippet character count.

    Returns:
        ``(filtered_results, stats)`` where *stats* counts removals
        per filter dimension.
    """
    if not results:
        return [], {"total_removed": 0}

    stats = {
        "short_snippet": 0,
        "fuzzy_dedup": 0,
        "total_removed": 0,
    }

    kept: list[SearchResult] = []
    seen_prefixes: set[str] = set()

    for r in results:
        # 1. Snippet length filter.
        if len(r.snippet.strip()) < min_snippet_length:
            stats["short_snippet"] += 1
            logger.debug("Short snippet filtered (%d chars): %s",
                         len(r.snippet.strip()), r.url)
            continue

        # 2. Fuzzy dedup — identical snippet prefix.
        prefix = r.snippet.strip()[:100]
        if prefix in seen_prefixes:
            stats["fuzzy_dedup"] += 1
            logger.debug("Fuzzy dedup filtered: %s", r.url)
            continue
        seen_prefixes.add(prefix)

        kept.append(r)

    stats["total_removed"] = len(results) - len(kept)
    return kept, stats
