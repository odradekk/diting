"""LLM-based analysis of top search results with fetched page content."""

from __future__ import annotations

from dataclasses import dataclass, field

from diting.fetch.base import Fetcher
from diting.fetch.tavily import FetchResult
from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import Source
from diting.pipeline.dedup import normalize_url
from diting.pipeline.snippet_aggregator import aggregate_snippets

logger = get_logger("pipeline.summarizer")

_MAX_CONTENT_CHARS = 5000


@dataclass
class SummaryResult:
    """Outcome of an analysis attempt."""

    summary: str
    warnings: list[str] = field(default_factory=list)


class Summarizer:
    """Generate a detailed LLM analysis from the fetched page content of top sources.

    Uses a :class:`Fetcher` to retrieve page content and
    :class:`LLMClient` to produce a comprehensive markdown analysis
    with inline source citations.
    """

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        fetcher: Fetcher,
    ) -> None:
        self._llm = llm
        self._fetcher = fetcher
        self._system_prompt = prompts.load("summarization")

    async def summarize(
        self,
        query: str,
        sources: list[Source],
        top_n: int = 10,
        *,
        url_snippets: dict[str, list[tuple[str, str]]] | None = None,
        prefetched: dict[str, FetchResult] | None = None,
    ) -> SummaryResult:
        """Generate a detailed analysis for the top *top_n* sources.

        Steps:
        1. Take the top *top_n* sources (already sorted by score).
        2. Reuse ``prefetched`` results for any URL already fetched; call
           :meth:`Fetcher.fetch_many` only for the URLs still missing.
        3. For each failed fetch, try aggregating snippets from the
           multi-engine map (``url_snippets``) as a last-resort
           pseudo-content.  Aggregation requires 2+ distinct engines.
        4. If NO content (real or aggregated) was obtained, return empty
           analysis with warnings.
        5. Build user message with query + fetched content + source index.
        6. Call LLM for analysis generation.
        7. Parse JSON response, extract ``"analysis"`` field.

        Args:
            query: User's original query.
            sources: Scored sources to analyse, highest-score first.
            top_n: Cap the analysis at this many sources.
            url_snippets: Pre-dedup map ``normalized_url -> [(engine,
                snippet), ...]``.  When provided, used as fallback
                content for URLs whose fetch failed.
            prefetched: URL -> :class:`FetchResult` map populated by the
                orchestrator's interleaved prefetch.  URLs present here
                are never re-fetched; their stored result (success or
                failure) is used directly.

        Degradation:
        - Partial fetch failure: analyse from successful fetches, warn.
        - Fetch failure with aggregation hit: use aggregated snippets,
          tagged so the LLM lowers confidence.
        - All content missing: empty analysis, warnings list all failures.
        - LLM failure: empty analysis, warning about LLM failure.
        - Invalid/missing JSON key: empty analysis, warning about parse failure.
        """
        if not sources:
            return SummaryResult(summary="")

        top_sources = sources[:top_n]
        warnings: list[str] = []

        # Build a url -> FetchResult map starting from the prefetched cache,
        # then fetch only the URLs still missing.
        urls = [s.url for s in top_sources]
        fetch_map: dict[str, FetchResult] = dict(prefetched or {})
        missing = [u for u in urls if u not in fetch_map]

        if missing:
            logger.info(
                "Fetching %d URLs for analysis (%d prefetched)",
                len(missing), len(urls) - len(missing),
            )
            new_results = await self._fetcher.fetch_many(missing)
            for url, result in zip(missing, new_results):
                fetch_map[url] = result
        elif prefetched:
            logger.info(
                "All %d URLs already prefetched — skipping fetch call",
                len(urls),
            )

        # Separate successes from failures; on failure, try snippet aggregation.
        fetched: list[tuple[Source, str]] = []
        aggregated_count = 0
        for source in top_sources:
            result = fetch_map.get(source.url)
            if result is None:
                # Defensive: should not happen given the logic above.
                continue
            if result.success:
                logger.info("  Fetched %s (%d chars)", source.url, len(result.content))
                fetched.append((source, result.content))
                continue

            aggregated = self._try_aggregate(source, url_snippets)
            if aggregated:
                logger.info(
                    "  Aggregated snippets for %s (%d chars)",
                    source.url, len(aggregated),
                )
                fetched.append((source, aggregated))
                aggregated_count += 1
                continue

            msg = f"Failed to fetch {source.url}: {result.error}"
            logger.warning(msg)
            warnings.append(msg)

        logger.info(
            "Fetch results: %d/%d succeeded (%d via snippet aggregation)",
            len(fetched), len(urls), aggregated_count,
        )
        if not fetched:
            return SummaryResult(summary="", warnings=warnings)

        user_message = self._build_user_message(query, fetched)

        try:
            data = await self._llm.chat_json(
                self._system_prompt,
                user_message,
            )
        except LLMError as exc:
            logger.warning("LLM analysis failed: %s", exc)
            warnings.append(f"LLM analysis failed: {exc}")
            return SummaryResult(summary="", warnings=warnings)

        return self._parse_response(data, warnings)

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    @staticmethod
    def _try_aggregate(
        source: Source,
        url_snippets: dict[str, list[tuple[str, str]]] | None,
    ) -> str:
        """Return aggregated pseudo-content for *source* or ``""``.

        The source's :attr:`Source.normalized_url` is used as the lookup
        key — that's the same shape the orchestrator stored under.
        """
        if not url_snippets:
            return ""
        key = source.normalized_url or normalize_url(source.url)
        snippets = url_snippets.get(key)
        if not snippets:
            return ""
        return aggregate_snippets(snippets)

    @staticmethod
    def _build_user_message(
        query: str,
        fetched: list[tuple[Source, str]],
    ) -> str:
        """Format the user message with query, source index, and fetched content."""
        lines = [f"Query: {query}", "", "Sources:"]
        for i, (source, content) in enumerate(fetched, 1):
            truncated = content[:_MAX_CONTENT_CHARS]
            lines.append(f"[{i}] Title: {source.title}")
            lines.append(f"    URL: {source.url}")
            lines.append(f"    Content: {truncated}")
            lines.append("")
        return "\n".join(lines)

    @staticmethod
    def _parse_response(
        data: dict,
        warnings: list[str],
    ) -> SummaryResult:
        """Extract the analysis string from the LLM JSON response."""
        analysis = data.get("analysis")
        if not isinstance(analysis, str) or not analysis.strip():
            msg = "LLM response missing or empty 'analysis' field"
            logger.warning(msg)
            warnings.append(msg)
            return SummaryResult(summary="", warnings=warnings)

        return SummaryResult(summary=analysis.strip(), warnings=warnings)
