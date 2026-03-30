"""Search pipeline orchestrator — single-round and multi-round search."""

from __future__ import annotations

import asyncio
import time
import uuid

from diting.fetch.tavily import TavilyFetcher
from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import get_logger
from diting.models import (
    ModuleOutput,
    ScoredResult,
    SearchMetadata,
    SearchResponse,
    SearchResult,
    Source,
)
from diting.modules.base import BaseSearchModule
from diting.pipeline.blacklist import (
    append_auto_blacklist,
    collect_low_score_domains,
    is_blacklisted,
    load_blacklist,
)
from diting.pipeline.classifier import Classifier
from diting.pipeline.dedup import deduplicate, extract_domain, normalize_url
from diting.pipeline.evaluator import Evaluator
from diting.pipeline.prefilter import prefilter
from diting.pipeline.scorer import Scorer
from diting.pipeline.summarizer import Summarizer

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
        fetcher: TavilyFetcher | None = None,
        categories_path: str | None = None,
        min_snippet_length: int = 30,
        blacklist_file: str = "blacklist.txt",
        auto_blacklist: bool = True,
        auto_blacklist_threshold: float = 0.3,
    ) -> None:
        self._llm = llm
        self._prompts = prompts
        self._modules = modules
        self._max_rounds = max_rounds
        self._global_timeout = global_timeout
        self._score_threshold = score_threshold
        self._min_snippet_length = min_snippet_length
        self._blacklist_file = blacklist_file
        self._auto_bl = auto_blacklist
        self._auto_bl_threshold = auto_blacklist_threshold

        # Load unified blacklist patterns.
        self._blacklist_patterns = load_blacklist(blacklist_file)

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

        logger.info(
            "========== SEARCH START [%s] ==========\n"
            "  query: %s\n"
            "  max_rounds: %d | global_timeout: %ds | score_threshold: %.2f\n"
            "  modules: %s",
            request_id, query, self._max_rounds, self._global_timeout,
            self._score_threshold,
            [m.name for m in self._modules] if self._modules else "(none)",
        )

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
        logger.info("[Step 8] Classifying %d sources via LLM", len(sources))
        categories = await self._classify(sources, warnings)
        logger.info("[Step 8] Classification result: %d categories",
                    len(categories))

        # Summarization.
        logger.info("[Step 9-10] Fetching top sources & generating summary")
        summary = await self._summarize(query, sources, warnings)
        logger.info("[Step 10] Summary generated: %d chars",
                    len(summary))

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

        logger.info(
            "========== SEARCH END [%s] ==========\n"
            "  status: %s | rounds: %d | elapsed: %dms\n"
            "  sources: found=%d dedup=%d filtered=%d\n"
            "  categories: %d | summary_len: %d\n"
            "  warnings: %s\n"
            "  errors: %s",
            request_id, status, rounds_completed, elapsed_ms,
            total_found, total_after_dedup, len(sources),
            len(categories), len(summary),
            warnings or "(none)", errors or "(none)",
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
        rounds_completed = 0

        # Step 1: Generate ranked query queue (strongest first).
        logger.info("[Step 1] Generating ranked search queries from: %s", query)
        ranked_queries = await self._generate_queries(query)
        if not ranked_queries:
            ranked_queries = [query]
        logger.info("[Step 1] Generated %d ranked queries: %s",
                    len(ranked_queries), ranked_queries)

        query_index = 0

        for round_num in range(1, self._max_rounds + 1):
            # Pick the next query from the ranked queue.
            if query_index >= len(ranked_queries):
                logger.info("All ranked queries exhausted after %d rounds", rounds_completed)
                break

            current_query = ranked_queries[query_index]
            query_index += 1

            round_start = time.monotonic()
            logger.info(
                "===== Round %d/%d START (query %d/%d: %s) =====",
                round_num, self._max_rounds,
                query_index, len(ranked_queries), current_query,
            )

            # Step 2: Search with ONE query across all modules.
            logger.info("[Step 2] Searching '%s' across %d modules",
                        current_query, len(self._modules))
            round_results, round_errors = await self._parallel_search([current_query])

            errors.extend(round_errors)
            round_found = sum(len(m.results) for m in round_results)
            total_found += round_found
            if round_errors:
                logger.warning("[Step 2] Module errors: %s", round_errors)
            logger.info("[Step 2] Search returned %d raw results", round_found)

            # Merge all module results into one flat list.
            merged: list[SearchResult] = []
            for m in round_results:
                merged.extend(m.results)

            if not merged:
                if round_num == 1:
                    warnings.append("No results from any search module")
                logger.warning("[Step 2] No results — stopping")
                break

            # Blacklist filter.
            before_bl = len(merged)
            filtered = [r for r in merged if not is_blacklisted(r.url, self._blacklist_patterns)]
            bl_removed = before_bl - len(filtered)
            if bl_removed:
                logger.info("[Step 3] Blacklist: removed %d/%d results", bl_removed, before_bl)

            # Dedup.
            unique, seen_urls = deduplicate(filtered, seen_urls=seen_urls)
            total_after_dedup += len(unique)
            logger.info("[Step 3] After dedup: %d unique (from %d raw)", len(unique), len(merged))

            # Pre-filter: remove thin snippets and near-duplicates.
            unique, filter_stats = prefilter(
                unique,
                min_snippet_length=self._min_snippet_length,
            )
            logger.info(
                "[Step 3.5] Pre-filter: %d remain (removed %d — %s)",
                len(unique), filter_stats["total_removed"], filter_stats,
            )

            if not unique:
                logger.info("Round %d: all results filtered out", round_num)
                break

            # Score.
            logger.info("[Step 4] Scoring %d results via LLM", len(unique))
            scored = await self._scorer.score(query, unique)
            logger.info("[Step 4] Scored %d results", len(scored))
            for s in scored[:5]:
                logger.debug("  %.2f %s — %s", s.final_score, s.url, s.reason[:60])

            # Filter by threshold.
            above = [s for s in scored if s.final_score >= self._score_threshold]
            logger.info("[Step 5] Filter: %d/%d above threshold %.2f",
                        len(above), len(scored), self._score_threshold)

            all_scored.extend(above if above else scored)
            all_results.extend(unique)

            # Auto-blacklist: append low-scoring domains to blacklist file.
            if self._auto_bl and scored:
                bad_domains = collect_low_score_domains(scored, self._auto_bl_threshold)
                if bad_domains:
                    added = append_auto_blacklist(bad_domains, self._blacklist_file)
                    if added:
                        self._blacklist_patterns = load_blacklist(self._blacklist_file)
                        logger.info(
                            "[Step 5.5] Auto-blacklist: added %d domains, reloaded %d patterns",
                            len(added), len(self._blacklist_patterns),
                        )

            rounds_completed = round_num

            round_elapsed = int((time.monotonic() - round_start) * 1000)
            logger.info("===== Round %d/%d END (%dms) =====",
                        round_num, self._max_rounds, round_elapsed)

            # Quality evaluation — should we continue?
            if round_num < self._max_rounds:
                logger.info("[Step 6] Evaluating search quality via LLM")
                evaluation = await self._evaluator.evaluate(
                    query, all_scored, round_num, self._max_rounds,
                )
                logger.info("[Step 6] Evaluation: sufficient=%s — %s",
                            evaluation.sufficient, evaluation.reason)
                if evaluation.sufficient:
                    return {
                        "total_found": total_found,
                        "total_after_dedup": total_after_dedup,
                        "rounds": rounds_completed,
                    }

                # If ranked queries exhausted, append evaluator's supplementary
                # queries as fallback.
                if query_index >= len(ranked_queries) and evaluation.supplementary_queries:
                    ranked_queries.extend(evaluation.supplementary_queries)
                    logger.info(
                        "[Step 6] Appended %d supplementary queries (total queue: %d)",
                        len(evaluation.supplementary_queries), len(ranked_queries),
                    )

        return {
            "total_found": total_found,
            "total_after_dedup": total_after_dedup,
            "rounds": rounds_completed,
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
