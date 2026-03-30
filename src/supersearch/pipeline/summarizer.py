"""LLM-based summarization of top search results with fetched page content."""

from __future__ import annotations

from dataclasses import dataclass, field

from supersearch.fetch.tavily import TavilyFetcher
from supersearch.llm.client import LLMClient, LLMError
from supersearch.llm.prompts import PromptLoader
from supersearch.log import get_logger
from supersearch.models import Source

logger = get_logger("pipeline.summarizer")

_MAX_CONTENT_CHARS = 5000


@dataclass
class SummaryResult:
    """Outcome of a summarization attempt."""

    summary: str
    warnings: list[str] = field(default_factory=list)


class Summarizer:
    """Generate an LLM summary from the fetched page content of top sources.

    Uses :class:`TavilyFetcher` to retrieve page content and
    :class:`LLMClient` to produce a synthesised summary.
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
        top_n: int = 5,
    ) -> SummaryResult:
        """Generate a summary for the top *top_n* sources.

        Steps:
        1. Take the top *top_n* sources (already sorted by score).
        2. Fetch their page content via :meth:`TavilyFetcher.fetch_many`.
        3. For each failed fetch, record a warning.
        4. If NO fetches succeeded, return empty summary with warnings.
        5. Build user message with query + fetched content.
        6. Call LLM for summary generation.
        7. Parse JSON response, extract ``"summary"`` field.

        Degradation:
        - Partial fetch failure: summarise from successful fetches, warn.
        - All fetch failure: empty summary, warnings list all failures.
        - LLM failure: empty summary, warning about LLM failure.
        - Invalid/missing JSON key: empty summary, warning about parse failure.
        """
        if not sources:
            return SummaryResult(summary="")

        top_sources = sources[:top_n]
        warnings: list[str] = []

        # Fetch page content.
        urls = [s.url for s in top_sources]
        fetch_results = await self._fetcher.fetch_many(urls)

        # Separate successes from failures.
        fetched: list[tuple[Source, str]] = []
        for source, result in zip(top_sources, fetch_results):
            if result.success:
                fetched.append((source, result.content))
            else:
                msg = f"Failed to fetch {source.url}: {result.error}"
                logger.warning(msg)
                warnings.append(msg)

        if not fetched:
            return SummaryResult(summary="", warnings=warnings)

        user_message = self._build_user_message(query, fetched)

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("LLM summarization failed: %s", exc)
            warnings.append(f"LLM summarization failed: {exc}")
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
        """Format the user message with query and fetched source content."""
        lines = [f"Query: {query}", "", "Sources:"]
        for i, (source, content) in enumerate(fetched, 1):
            truncated = content[:_MAX_CONTENT_CHARS]
            lines.append(f"{i}. Title: {source.title}")
            lines.append(f"   URL: {source.url}")
            lines.append(f"   Content: {truncated}")
            lines.append("")
        return "\n".join(lines)

    @staticmethod
    def _parse_response(
        data: dict,
        warnings: list[str],
    ) -> SummaryResult:
        """Extract the summary string from the LLM JSON response."""
        summary = data.get("summary")
        if not isinstance(summary, str) or not summary.strip():
            msg = "LLM response missing or empty 'summary' field"
            logger.warning(msg)
            warnings.append(msg)
            return SummaryResult(summary="", warnings=warnings)

        return SummaryResult(summary=summary.strip(), warnings=warnings)
