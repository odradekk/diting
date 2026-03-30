"""LLM-based relevance and quality scoring for search results."""

from __future__ import annotations

import asyncio

from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import ScoredResult, SearchResult

logger = get_logger("pipeline.scorer")

_BATCH_SIZE = 10


class Scorer:
    """Score search results using an LLM for relevance and quality.

    Results are scored in batches of ``_BATCH_SIZE`` to avoid LLM
    timeouts on large result sets.  Batches run concurrently.
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
        failure for a batch, that batch is skipped (degradation).
        """
        if not results:
            return []

        # Split into batches.
        batches = [
            results[i : i + _BATCH_SIZE]
            for i in range(0, len(results), _BATCH_SIZE)
        ]
        logger.info(
            "Scoring %d results in %d batch(es) of ≤%d",
            len(results), len(batches), _BATCH_SIZE,
        )

        tasks = [self._score_batch(query, batch, idx + 1) for idx, batch in enumerate(batches)]
        batch_results = await asyncio.gather(*tasks)

        all_scored: list[ScoredResult] = []
        for scored in batch_results:
            all_scored.extend(scored)

        logger.info("Scored %d/%d results successfully", len(all_scored), len(results))
        return all_scored

    async def _score_batch(
        self,
        query: str,
        results: list[SearchResult],
        batch_num: int,
    ) -> list[ScoredResult]:
        """Score a single batch of results."""
        user_message = self._build_user_message(query, results)
        logger.debug("Batch %d: scoring %d results", batch_num, len(results))

        try:
            data = await self._llm.chat_json(self._system_prompt, user_message)
        except LLMError as exc:
            logger.warning("Batch %d: LLM scoring failed: %s", batch_num, exc)
            return []

        scored = self._parse_response(data, results)
        logger.debug("Batch %d: scored %d/%d", batch_num, len(scored), len(results))
        return scored

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
