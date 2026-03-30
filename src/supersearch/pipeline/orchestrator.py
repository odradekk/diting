"""Search pipeline orchestrator — single-round and multi-round search."""

from __future__ import annotations

import asyncio
import time
import uuid

from supersearch.fetch.tavily import TavilyFetcher
from supersearch.llm.client import LLMClient, LLMError
from supersearch.llm.prompts import PromptLoader
from supersearch.log import get_logger
from supersearch.models import (
    ModuleOutput,
    ScoredResult,
    SearchMetadata,
    SearchResponse,
    SearchResult,
    Source,
)
from supersearch.modules.base import BaseSearchModule
from supersearch.pipeline.classifier import Classifier
from supersearch.pipeline.dedup import deduplicate, extract_domain, normalize_url
from supersearch.pipeline.evaluator import Evaluator
from supersearch.pipeline.scorer import Scorer
from supersearch.pipeline.summarizer import Summarizer

logger = get_logger("pipeline.orchestrator")


class Orchestrator:
    """Multi-round search pipeline orchestrator.

    Coordinates query generation, parallel module search, deduplication,
    scoring, quality evaluation, and result merging with global timeout
    and degradation strategies.
    """

    def __init__(
        self,
        llm: LLMClient,
        prompts: PromptLoader,
        modules: list[BaseSearchModule],
        *,
        max_rounds: int = 3,
        global_timeout: int = 120,
        score_threshold: float = 0.3,
        blacklist: list[str] | None = None,
        fetcher: TavilyFetcher | None = None,
        categories_path: str | None = None,
    ) -> None:
        self._llm = llm
        self._prompts = prompts
        self._modules = modules
        self._max_rounds = max_rounds
        self._global_timeout = global_timeout
        self._score_threshold = score_threshold
        self._blacklist = blacklist or []

        self._scorer = Scorer(llm, prompts)
        self._evaluator = Evaluator(llm, prompts)
        self._classifier = Classifier(llm, prompts, categories_path=categories_path)
        self._summarizer: Summarizer | None = (
            Summarizer(llm, prompts, fetcher) if fetcher else None
        )
        self._query_system_prompt = prompts.load("query_generation")

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def search(self, query: str) -> SearchResponse:
        """Execute the full multi-round search pipeline.

        Returns a :class:`SearchResponse` regardless of failures —
        degradation strategies produce partial results.
        """
        request_id = uuid.uuid4().hex[:12]
        start = time.monotonic()
        warnings: list[str] = []
        errors: list[str] = []

        all_scored: list[ScoredResult] = []
        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()
        total_found = 0
        total_after_dedup = 0
        rounds_completed = 0

        try:
            result = await asyncio.wait_for(
                self._run_rounds(
                    query, all_scored, all_results, seen_urls,
                    warnings, errors,
                ),
                timeout=self._global_timeout,
            )
            total_found = result["total_found"]
            total_after_dedup = result["total_after_dedup"]
            rounds_completed = result["rounds"]
        except asyncio.TimeoutError:
            logger.warning("Global timeout (%ds) reached", self._global_timeout)
            warnings.append(
                f"Global timeout ({self._global_timeout}s) reached — "
                "returning partial results"
            )

        # Determine status.
        if not all_scored and not all_results:
            if errors:
                status = "error"
            else:
                status = "no_results"
        else:
            status = "success"

        # Build scored URL → ScoredResult lookup for enrichment.
        score_map = {s.url: s for s in all_scored}

        # Build enriched Source list from original results that were scored.
        sources: list[Source] = []
        for r in all_results:
            sr = score_map.get(r.url)
            if sr and sr.final_score >= self._score_threshold:
                sources.append(Source(
                    title=r.title,
                    url=r.url,
                    normalized_url=normalize_url(r.url),
                    snippet=r.snippet,
                    score=sr.final_score,
                    source_module="",
                    domain=extract_domain(r.url),
                ))

        # Sort by score descending.
        sources.sort(key=lambda s: s.score, reverse=True)

        # If scoring failed entirely (no scored results), fall back to raw
        # results in original order — the degradation strategy.
        if not sources and all_results:
            warnings.append("LLM scoring unavailable — returning unscored results")
            for r in all_results:
                sources.append(Source(
                    title=r.title,
                    url=r.url,
                    normalized_url=normalize_url(r.url),
                    snippet=r.snippet,
                    score=0.0,
                    source_module="",
                    domain=extract_domain(r.url),
                ))

        # --- Post-processing (outside global timeout) ---

        # Classification.
        categories = await self._classify(sources, warnings)

        # Summarization.
        summary = await self._summarize(query, sources, warnings)

        elapsed_ms = int((time.monotonic() - start) * 1000)

        metadata = SearchMetadata(
            request_id=request_id,
            query=query,
            rounds=rounds_completed,
            total_sources_found=total_found,
            sources_after_dedup=total_after_dedup,
            sources_after_filter=len(sources),
            elapsed_ms=elapsed_ms,
        )

        return SearchResponse(
            status=status,
            summary=summary,
            categories=categories,
            metadata=metadata,
            warnings=warnings,
            errors=errors,
        )

    # ------------------------------------------------------------------
    # Post-processing
    # ------------------------------------------------------------------

    async def _classify(
        self,
        sources: list[Source],
        warnings: list[str],
    ) -> list:
        """Classify sources into categories. Returns [] on failure."""
        if not sources:
            return []
        try:
            return await self._classifier.classify(sources)
        except Exception as exc:
            logger.warning("Classification failed: %s", exc)
            warnings.append(f"Classification failed: {exc}")
            return []

    async def _summarize(
        self,
        query: str,
        sources: list[Source],
        warnings: list[str],
    ) -> str:
        """Summarize top sources. Returns "" on failure or if no fetcher."""
        if not self._summarizer or not sources:
            return ""
        try:
            result = await self._summarizer.summarize(query, sources)
            warnings.extend(result.warnings)
            return result.summary
        except Exception as exc:
            logger.warning("Summarization failed: %s", exc)
            warnings.append(f"Summarization failed: {exc}")
            return ""

    # ------------------------------------------------------------------
    # Multi-round loop
    # ------------------------------------------------------------------

    async def _run_rounds(
        self,
        query: str,
        all_scored: list[ScoredResult],
        all_results: list[SearchResult],
        seen_urls: set[str],
        warnings: list[str],
        errors: list[str],
    ) -> dict:
        total_found = 0
        total_after_dedup = 0
        round_num = 0
        queries = await self._generate_queries(query)
        if not queries:
            queries = [query]

        for round_num in range(1, self._max_rounds + 1):
            logger.info("Starting round %d/%d", round_num, self._max_rounds)

            round_results, round_errors = await self._parallel_search(queries)

            errors.extend(round_errors)
            total_found += sum(len(m.results) for m in round_results)

            # Merge all module results into one flat list.
            merged: list[SearchResult] = []
            for m in round_results:
                merged.extend(m.results)

            if not merged:
                if round_num == 1:
                    warnings.append("No results from any search module")
                break

            # Dedup + blacklist.
            unique, seen_urls = deduplicate(
                merged, seen_urls=seen_urls, blacklist=self._blacklist,
            )
            total_after_dedup += len(unique)

            if not unique:
                logger.info("Round %d: all results duplicate or blacklisted", round_num)
                break

            # Score.
            scored = await self._scorer.score(query, unique)

            # Filter by threshold.
            above = [s for s in scored if s.final_score >= self._score_threshold]

            all_scored.extend(above if above else scored)
            all_results.extend(unique)

            # Quality evaluation — should we continue?
            if round_num < self._max_rounds:
                evaluation = await self._evaluator.evaluate(
                    query, all_scored, round_num, self._max_rounds,
                )
                if evaluation.sufficient:
                    logger.info(
                        "Round %d: quality sufficient — %s",
                        round_num, evaluation.reason,
                    )
                    return {
                        "total_found": total_found,
                        "total_after_dedup": total_after_dedup,
                        "rounds": round_num,
                    }

                # Use supplementary queries for the next round.
                if evaluation.supplementary_queries:
                    queries = evaluation.supplementary_queries
                else:
                    queries = [query]

        return {
            "total_found": total_found,
            "total_after_dedup": total_after_dedup,
            "rounds": round_num,
        }

    # ------------------------------------------------------------------
    # Query generation
    # ------------------------------------------------------------------

    async def _generate_queries(self, query: str) -> list[str]:
        """Use LLM to generate structured search queries."""
        try:
            data = await self._llm.chat_json(
                self._query_system_prompt, query,
            )
        except LLMError as exc:
            logger.warning("Query generation failed: %s", exc)
            return [query]

        raw = data.get("queries", [])
        if isinstance(raw, list):
            return [q for q in raw if isinstance(q, str) and q.strip()] or [query]
        return [query]

    # ------------------------------------------------------------------
    # Parallel module search
    # ------------------------------------------------------------------

    async def _parallel_search(
        self, queries: list[str],
    ) -> tuple[list[ModuleOutput], list[str]]:
        """Run all modules against all queries concurrently.

        Returns (results, error_messages).
        """
        tasks = []
        for module in self._modules:
            for q in queries:
                tasks.append(module.search(q))

        outputs: list[ModuleOutput] = await asyncio.gather(*tasks)

        results: list[ModuleOutput] = []
        error_msgs: list[str] = []

        for out in outputs:
            if out.error:
                error_msgs.append(f"[{out.module}] {out.error.message}")
            if out.results:
                results.append(out)

        return results, error_msgs
