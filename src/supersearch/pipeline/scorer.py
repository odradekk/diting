"""LLM-based relevance and quality scoring for search results."""

from __future__ import annotations

from supersearch.llm.client import LLMClient, LLMError
from supersearch.llm.prompts import PromptLoader
from supersearch.log import get_logger
from supersearch.models import ScoredResult, SearchResult

logger = get_logger("pipeline.scorer")


class Scorer:
    """Score search results using an LLM for relevance and quality.

    Sends all results in a single batch request and parses the JSON
    response into :class:`ScoredResult` objects.
    """

    def __init__(self, llm: LLMClient, prompts: PromptLoader) -> None:
        self._llm = llm
        self._system_prompt = prompts.load("scoring")

    async def score(
        self,
        query: str,
        results: list[SearchResult],
    ) -> list[ScoredResult]:
        """Score *results* against *query*.

        Returns a list of :class:`ScoredResult`.  On LLM or JSON parse
        failure, returns an empty list (caller should apply degradation).
        """
        if not results:
            return []

        user_message = self._build_user_message(query, results)

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("LLM scoring failed: %s", exc)
            return []

        return self._parse_response(data, results)

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    @staticmethod
    def _build_user_message(query: str, results: list[SearchResult]) -> str:
        lines = [f"Query: {query}", "", "Results:"]
        for i, r in enumerate(results, 1):
            lines.append(f"{i}. title: {r.title}")
            lines.append(f"   url: {r.url}")
            lines.append(f"   snippet: {r.snippet}")
            lines.append("")
        return "\n".join(lines)

    @staticmethod
    def _parse_response(
        data: dict,
        original: list[SearchResult],
    ) -> list[ScoredResult]:
        """Parse the LLM JSON response into ScoredResult objects."""
        raw_list = data.get("scored_results")
        if not isinstance(raw_list, list):
            logger.warning("LLM response missing 'scored_results' list")
            return []

        url_set = {r.url for r in original}
        scored: list[ScoredResult] = []

        for item in raw_list:
            try:
                sr = ScoredResult(
                    url=item["url"],
                    relevance=float(item["relevance"]),
                    quality=float(item["quality"]),
                    final_score=float(item["final_score"]),
                    reason=str(item.get("reason", "")),
                )
            except (KeyError, TypeError, ValueError) as exc:
                logger.debug("Skipping malformed scored item: %s", exc)
                continue

            if sr.url not in url_set:
                logger.debug("Scored URL not in original results: %s", sr.url)
                continue

            scored.append(sr)

        return scored
