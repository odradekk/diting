"""Search result scoring backends: LLM or local hybrid reranker scorer."""

from __future__ import annotations

import asyncio
from typing import Protocol

from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import ScoredResult, SearchResult
from diting.pipeline.quality import HeuristicQualityScorer
from diting.rerank.bge import (
    DEFAULT_BGE_MODEL,
    DEFAULT_BGE_MODEL_DIR,
    BGEReranker,
    RerankerError,
)

logger = get_logger("pipeline.scorer")

_BATCH_SIZE = 10
_RERANKER_RELEVANCE_WEIGHT = 0.7
_RERANKER_QUALITY_WEIGHT = 0.3


class ScorerBackend(Protocol):
    """Abstract scoring backend."""

    async def score(self, query: str, results: list[SearchResult]) -> list[ScoredResult]:
        """Score *results* against *query*."""


class LLMScorerBackend:
    """LLM-based relevance and quality scoring."""

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        *,
        relevance_weight: float = 0.5,
        quality_weight: float = 0.5,
    ) -> None:
        self._llm = llm
        self._system_prompt = prompts.load("scoring")
        self._relevance_weight = relevance_weight
        self._quality_weight = quality_weight

    async def score(
        self,
        query: str,
        results: list[SearchResult],
    ) -> list[ScoredResult]:
        if not results:
            return []

        batches = [
            results[i : i + _BATCH_SIZE]
            for i in range(0, len(results), _BATCH_SIZE)
        ]
        logger.info(
            "Scoring %d results in %d batch(es) of ≤%d via llm",
            len(results), len(batches), _BATCH_SIZE,
        )

        tasks = [self._score_batch(query, batch, idx + 1) for idx, batch in enumerate(batches)]
        batch_results = await asyncio.gather(*tasks)

        all_scored: list[ScoredResult] = []
        for scored in batch_results:
            all_scored.extend(scored)

        logger.info("Scored %d/%d results successfully via llm", len(all_scored), len(results))
        return all_scored

    async def _score_batch(
        self,
        query: str,
        results: list[SearchResult],
        batch_num: int,
    ) -> list[ScoredResult]:
        user_message = Scorer._build_user_message(query, results)
        logger.debug("Batch %d: scoring %d results via llm", batch_num, len(results))

        try:
            data = await self._llm.chat_json(
                self._system_prompt,
                user_message,
            )
        except LLMError as exc:
            logger.warning("Batch %d: LLM scoring failed: %s", batch_num, exc)
            return []

        scored = Scorer._parse_response_static(
            data,
            results,
            relevance_weight=self._relevance_weight,
            quality_weight=self._quality_weight,
        )
        logger.debug("Batch %d: scored %d/%d via llm", batch_num, len(scored), len(results))
        return scored


class RerankerScorerBackend:
    """Local hybrid scorer: reranker relevance + heuristic quality."""

    def __init__(
        self,
        *,
        reranker: BGEReranker,
        quality: HeuristicQualityScorer,
        relevance_weight: float = _RERANKER_RELEVANCE_WEIGHT,
        quality_weight: float = _RERANKER_QUALITY_WEIGHT,
    ) -> None:
        self._reranker = reranker
        self._quality = quality
        self._relevance_weight = relevance_weight
        self._quality_weight = quality_weight

    async def score(
        self,
        query: str,
        results: list[SearchResult],
    ) -> list[ScoredResult]:
        if not results:
            return []

        docs = [self._build_document(result) for result in results]
        logger.info("Scoring %d results via hybrid scorer (reranker + heuristics)", len(results))
        try:
            relevance_scores = await asyncio.to_thread(self._reranker.rerank, query, docs)
            quality_scores = self._quality.score_results(results)
        except RerankerError:
            raise
        except Exception as exc:
            raise RerankerError(f"Reranker backend failed: {exc}") from exc

        if len(relevance_scores) != len(results):
            raise RerankerError(
                f"Reranker returned {len(relevance_scores)} scores for {len(results)} docs"
            )

        scored: list[ScoredResult] = []
        for result, relevance in zip(results, relevance_scores):
            quality = quality_scores.get(result.url, 0.0)
            final_score = (
                self._relevance_weight * float(relevance)
                + self._quality_weight * float(quality)
            )
            final_score = max(0.0, min(1.0, final_score))
            scored.append(ScoredResult(
                url=result.url,
                relevance=float(relevance),
                quality=float(quality),
                final_score=final_score,
                reason=(
                    f"reranker={float(relevance):.3f}; heuristic={float(quality):.3f}"
                ),
            ))

        return scored

    @staticmethod
    def _build_document(result: SearchResult) -> str:
        return f"Title: {result.title}\nSnippet: {result.snippet}"


class Scorer:
    """Facade over selectable scoring backends.

    Constructor default remains ``llm`` for predictable unit tests and direct
    library use. The runtime default is selected in configuration.

    Backend names:
    - ``llm``: LLM relevance + quality scoring
    - ``hybrid``: local reranker + heuristic quality scorer
    - ``reranker``: legacy alias of ``hybrid``
    """

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        relevance_weight: float = 0.5,
        quality_weight: float = 0.5,
        *,
        backend: str = "llm",
        reranker: BGEReranker | None = None,
        quality_scorer: HeuristicQualityScorer | None = None,
        reranker_model_id: str = "",
        reranker_cache_dir: str = "",
        domain_authority_path: str = "",
    ) -> None:
        self._relevance_weight = relevance_weight
        self._quality_weight = quality_weight
        self._backend_name = (backend or "llm").strip().lower()
        self._llm_backend = LLMScorerBackend(
            llm,
            prompts,
            relevance_weight=relevance_weight,
            quality_weight=quality_weight,
        )

        resolved_reranker = reranker or BGEReranker(
            model_id=reranker_model_id or DEFAULT_BGE_MODEL,
            model_dir=reranker_cache_dir or DEFAULT_BGE_MODEL_DIR,
        )
        resolved_quality = quality_scorer or HeuristicQualityScorer(
            authority_path=domain_authority_path,
        )
        self._reranker_backend = RerankerScorerBackend(
            reranker=resolved_reranker,
            quality=resolved_quality,
        )

    @property
    def backend_name(self) -> str:
        return self._backend_name

    async def score(
        self,
        query: str,
        results: list[SearchResult],
    ) -> list[ScoredResult]:
        if self._backend_name == "llm":
            return await self._llm_backend.score(query, results)
        if self._backend_name not in {"hybrid", "reranker"}:
            logger.warning("Unknown scorer backend %r — falling back to llm", self._backend_name)
            return await self._llm_backend.score(query, results)

        try:
            return await self._reranker_backend.score(query, results)
        except RerankerError as exc:
            logger.warning("Hybrid scorer backend unavailable: %s — falling back to llm scorer", exc)
            return await self._llm_backend.score(query, results)

    # ------------------------------------------------------------------
    # LLM response helpers kept on the facade for test stability
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

    def _parse_response(
        self,
        data: dict,
        original: list[SearchResult],
    ) -> list[ScoredResult]:
        return self._parse_response_static(
            data,
            original,
            relevance_weight=self._relevance_weight,
            quality_weight=self._quality_weight,
        )

    @staticmethod
    def _parse_response_static(
        data: dict,
        original: list[SearchResult],
        *,
        relevance_weight: float,
        quality_weight: float,
    ) -> list[ScoredResult]:
        raw_list = data.get("scored_results")
        if not isinstance(raw_list, list):
            logger.warning("LLM response missing 'scored_results' list")
            return []

        url_set = {r.url for r in original}
        scored: list[ScoredResult] = []

        for item in raw_list:
            try:
                relevance = float(item["relevance"])
                quality = float(item["quality"])
                final_score = relevance_weight * relevance + quality_weight * quality
                final_score = max(0.0, min(1.0, final_score))
                sr = ScoredResult(
                    url=item["url"],
                    relevance=relevance,
                    quality=quality,
                    final_score=final_score,
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
