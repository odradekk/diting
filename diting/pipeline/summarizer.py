"""LLM-based analysis of top search results with fetched page content."""

from __future__ import annotations

from dataclasses import dataclass, field

from diting.fetch.tavily import TavilyFetcher
from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import Source

logger = get_logger("pipeline.summarizer")

_MAX_CONTENT_CHARS = 5000


@dataclass
class SummaryResult:
    """Outcome of an analysis attempt."""

    summary: str
    warnings: list[str] = field(default_factory=list)


class Summarizer:
    """Generate a detailed LLM analysis from the fetched page content of top sources.

    Uses :class:`TavilyFetcher` to retrieve page content and
    :class:`LLMClient` to produce a comprehensive markdown analysis
    with inline source citations.
    """

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        fetcher: TavilyFetcher,
    ) -> None:
        self._llm = llm
        self._fetcher = fetcher
        self._system_prompt = prompts.load("summarization")

    async def summarize(
        self,
        query: str,
        sources: list[Source],
        top_n: int = 10,
    ) -> SummaryResult:
        """Generate a detailed analysis for the top *top_n* sources.

        Steps:
        1. Take the top *top_n* sources (already sorted by score).
        2. Fetch their page content via :meth:`TavilyFetcher.fetch_many`.
        3. For each failed fetch, record a warning.
        4. If NO fetches succeeded, return empty analysis with warnings.
        5. Build user message with query + fetched content + source index.
        6. Call LLM for analysis generation.
        7. Parse JSON response, extract ``"analysis"`` field.

        Degradation:
        - Partial fetch failure: analyse from successful fetches, warn.
        - All fetch failure: empty analysis, warnings list all failures.
        - LLM failure: empty analysis, warning about LLM failure.
        - Invalid/missing JSON key: empty analysis, warning about parse failure.
        """
        if not sources:
            return SummaryResult(summary="")

        top_sources = sources[:top_n]
        warnings: list[str] = []

        # Fetch page content.
        urls = [s.url for s in top_sources]
        logger.info("Fetching %d URLs for analysis", len(urls))
        fetch_results = await self._fetcher.fetch_many(urls)

        # Separate successes from failures.
        fetched: list[tuple[Source, str]] = []
        for source, result in zip(top_sources, fetch_results):
            if result.success:
                logger.info("  Fetched %s (%d chars)", source.url, len(result.content))
                fetched.append((source, result.content))
            else:
                msg = f"Failed to fetch {source.url}: {result.error}"
                logger.warning(msg)
                warnings.append(msg)

        logger.info("Fetch results: %d/%d succeeded", len(fetched), len(urls))
        if not fetched:
            return SummaryResult(summary="", warnings=warnings)

        user_message = self._build_user_message(query, fetched)

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("LLM analysis failed: %s", exc)
            warnings.append(f"LLM analysis failed: {exc}")
            return SummaryResult(summary="", warnings=warnings)

        return self._parse_response(data, warnings)

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

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
