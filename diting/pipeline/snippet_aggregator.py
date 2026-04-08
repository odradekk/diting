"""Snippet aggregation — last-resort pseudo-content when fetch fails.

When every fetch layer (curl_cffi → r.jina.ai → archive → Tavily)
fails for a URL, we can still compose best-effort content from the
snippets multiple search engines already returned for that same URL.

Single-engine snippets are not aggregated — they rarely carry enough
independent signal.  Two or more engines converging on the same URL,
however, is itself a weak authority signal and the concatenated
snippets often capture enough of the page to help the summariser.

The aggregated content is explicitly tagged so downstream consumers
(summariser, LLM) can flag it with lower confidence in the final
analysis.
"""

from __future__ import annotations

AGGREGATED_SOURCE_TAG = "aggregated_snippets"
_MIN_ENGINES = 2


def aggregate_snippets(
    snippets: list[tuple[str, str]],
    *,
    min_engines: int = _MIN_ENGINES,
) -> str:
    """Build pseudo-content by joining snippets from multiple engines.

    Args:
        snippets: List of ``(engine_name, snippet)`` tuples for a single
            URL.  A given engine may appear more than once (same URL
            found across rounds) — only the first snippet per engine is
            retained, and repeats do not inflate the engine count.
        min_engines: Minimum distinct engines required to emit content.

    Returns:
        Formatted pseudo-content prefixed with ``[source=aggregated_snippets]``
        and a per-engine block, or ``""`` when fewer than
        ``min_engines`` distinct engines contributed a non-empty
        snippet.
    """
    by_engine: dict[str, str] = {}
    for engine, snippet in snippets:
        if not engine:
            continue
        cleaned = (snippet or "").strip()
        if not cleaned:
            continue
        by_engine.setdefault(engine, cleaned)

    if len(by_engine) < min_engines:
        return ""

    header = (
        f"[source={AGGREGATED_SOURCE_TAG}] "
        f"Aggregated from {len(by_engine)} engines:"
    )
    # Sort by engine name for stable output — tests (and cache keys)
    # should not depend on dict-insertion order.
    blocks = [f"- [{engine}] {by_engine[engine]}" for engine in sorted(by_engine)]
    return "\n".join([header, *blocks])


__all__ = ["AGGREGATED_SOURCE_TAG", "aggregate_snippets"]
