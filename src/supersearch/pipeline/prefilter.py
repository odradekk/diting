"""Pre-filter layer between dedup and LLM scoring.

Removes low-value results (video pages, search aggregator pages,
thin snippets, near-duplicate content) before they consume LLM tokens.
"""

from __future__ import annotations

from supersearch.log import get_logger
from supersearch.models import SearchResult
from supersearch.pipeline.dedup import extract_domain

logger = get_logger("pipeline.prefilter")

# -- Default video/media domains (used when no override is provided) --------

DEFAULT_VIDEO_DOMAINS: list[str] = [
    "youtube.com",
    "youtu.be",
    "bilibili.com",
    "douyin.com",
    "tiktok.com",
    "vimeo.com",
    "dailymotion.com",
    "twitch.tv",
    "v.qq.com",
    "ixigua.com",
]

# -- Built-in path patterns that indicate video/media content ---------------

_VIDEO_PATH_PATTERNS: list[str] = [
    "/video/",
    "/watch?",
    "/shorts/",
    "/live/",
    "/clip/",
]

# -- Paths that are text content even on video domains ----------------------

_TEXT_PATH_EXCEPTIONS: list[str] = [
    "/read/",
    "/article/",
    "/opus/",
    "/column/",
]

# -- Search aggregator page patterns (domain prefix + path) -----------------

_SEARCH_PAGE_PATTERNS: list[str] = [
    "douyin.com/search/",
    "baidu.com/s?",
    "google.com/search",
    "bing.com/search",
    "sogou.com/web?",
    "so.com/s?",
]


def prefilter(
    results: list[SearchResult],
    *,
    video_domains: list[str] | None = None,
    min_snippet_length: int = 30,
    filter_search_pages: bool = True,
) -> tuple[list[SearchResult], dict[str, int]]:
    """Filter low-value search results before LLM scoring.

    Args:
        results: Deduplicated search results.
        video_domains: Video/media domains to block. ``None`` uses
            :data:`DEFAULT_VIDEO_DOMAINS`.
        min_snippet_length: Minimum snippet character count.
        filter_search_pages: Whether to remove search aggregator pages.

    Returns:
        ``(filtered_results, stats)`` where *stats* counts removals
        per filter dimension.
    """
    if not results:
        return [], {"total_removed": 0}

    domains = video_domains if video_domains is not None else DEFAULT_VIDEO_DOMAINS
    domain_set = {d.lower() for d in domains}

    stats = {
        "video_domain": 0,
        "video_path": 0,
        "search_page": 0,
        "short_snippet": 0,
        "fuzzy_dedup": 0,
        "total_removed": 0,
    }

    kept: list[SearchResult] = []
    seen_prefixes: set[str] = set()

    for r in results:
        url_lower = r.url.lower()
        domain = extract_domain(r.url).lower()

        # 1. Video domain filter.
        if _is_video_domain(domain, domain_set, url_lower):
            stats["video_domain"] += 1
            logger.debug("Video domain filtered: %s", r.url)
            continue

        # 2. Video path filter (any domain).
        if _has_video_path(url_lower):
            stats["video_path"] += 1
            logger.debug("Video path filtered: %s", r.url)
            continue

        # 3. Search aggregator page filter.
        if filter_search_pages and _is_search_page(url_lower):
            stats["search_page"] += 1
            logger.debug("Search page filtered: %s", r.url)
            continue

        # 4. Snippet length filter.
        if len(r.snippet.strip()) < min_snippet_length:
            stats["short_snippet"] += 1
            logger.debug("Short snippet filtered (%d chars): %s",
                         len(r.snippet.strip()), r.url)
            continue

        # 5. Fuzzy dedup — identical snippet prefix.
        prefix = r.snippet.strip()[:100]
        if prefix in seen_prefixes:
            stats["fuzzy_dedup"] += 1
            logger.debug("Fuzzy dedup filtered: %s", r.url)
            continue
        seen_prefixes.add(prefix)

        kept.append(r)

    stats["total_removed"] = len(results) - len(kept)
    return kept, stats


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _is_video_domain(
    domain: str,
    domain_set: set[str],
    url_lower: str,
) -> bool:
    """Check if the domain is a known video/media platform.

    Returns False for text-content paths on video domains
    (e.g. bilibili.com/read/).
    """
    if domain not in domain_set:
        return False
    # Allow text-content paths even on video domains.
    for exc in _TEXT_PATH_EXCEPTIONS:
        if exc in url_lower:
            return False
    return True


def _has_video_path(url_lower: str) -> bool:
    """Check if the URL path indicates video/media content."""
    return any(p in url_lower for p in _VIDEO_PATH_PATTERNS)


def _is_search_page(url_lower: str) -> bool:
    """Check if the URL is a search engine results page."""
    return any(p in url_lower for p in _SEARCH_PAGE_PATTERNS)
