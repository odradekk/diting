"""Search pipeline orchestrator — single-round and multi-round search."""

from __future__ import annotations

import asyncio
import pathlib
import time
import uuid

from diting.fetch.base import Fetcher
from diting.llm.client import LLMClient, LLMError
from diting.llm.prompts import PromptLoader
from diting.log import ContextLogger, get_logger
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
from diting.pipeline.dedup import deduplicate, extract_domain, normalize_url
from diting.pipeline.evaluator import Evaluator
from diting.pipeline.health import HealthTracker
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
        fetcher: Fetcher | None = None,
        min_snippet_length: int = 30,
        blacklist_file: str = "",
        auto_blacklist: bool = True,
        auto_blacklist_threshold: float = 0.3,
        relevance_weight: float = 0.5,
        quality_weight: float = 0.5,
        max_concurrency: int = 5,
        health_tracker: HealthTracker | None = None,
    ) -> None:
        self._llm = llm
        self._prompts = prompts
        self._modules = modules
        self._max_rounds = max_rounds
        self._global_timeout = global_timeout
        self._score_threshold = score_threshold
        self._min_snippet_length = min_snippet_length
        self._semaphore = asyncio.Semaphore(max_concurrency)
        if not blacklist_file:
            blacklist_file = str(
                pathlib.Path(__file__).resolve().parent.parent / "data" / "blacklist.txt"
            )
        self._blacklist_file = blacklist_file
        self._auto_bl = auto_blacklist
        self._auto_bl_threshold = auto_blacklist_threshold

        # Load unified blacklist patterns.
        self._blacklist_patterns = load_blacklist(blacklist_file)

        self._health = health_tracker or HealthTracker()
        self._scorer = Scorer(llm, prompts, relevance_weight=relevance_weight, quality_weight=quality_weight)
        self._evaluator = Evaluator(llm, prompts)
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

        ctx = ContextLogger(logger, {"query_id": request_id})
        engine_names = [m.name for m in self._modules]
        ctx.info(
            "========== SEARCH START [%s] ==========\n"
            "  query: %s\n"
            "  max_rounds: %d | global_timeout: %ds | score_threshold: %.2f\n"
            "  modules: %s",
            request_id, query, self._max_rounds, self._global_timeout,
            self._score_threshold,
            engine_names or "(none)",
            extra={
                "phase": "search_start",
                "query": query,
                "engines": engine_names,
                "max_rounds": self._max_rounds,
                "global_timeout": self._global_timeout,
                "score_threshold": self._score_threshold,
            },
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
                    warnings, errors, ctx,
                ),
                timeout=self._global_timeout,
            )
            total_found = result["total_found"]
            total_after_dedup = result["total_after_dedup"]
            rounds_completed = result["rounds"]
        except asyncio.TimeoutError:
            ctx.warning(
                "Global timeout (%ds) reached", self._global_timeout,
                extra={"phase": "global_timeout", "timeout_s": self._global_timeout},
            )
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
                    source_module=r.source_module,
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
                    source_module=r.source_module,
                    domain=extract_domain(r.url),
                ))

        # --- Post-processing (outside global timeout) ---

        # Summarization.
        summarize_start = time.monotonic()
        ctx.info(
            "[Step 9-10] Fetching top sources & generating summary",
            extra={"phase": "summarization_start", "sources_count": len(sources)},
        )
        summary = await self._summarize(query, sources, warnings)
        summarize_ms = int((time.monotonic() - summarize_start) * 1000)
        ctx.info(
            "[Step 10] Summary generated: %d chars", len(summary),
            extra={
                "phase": "summarization_end",
                "success": bool(summary),
                "chars": len(summary),
                "latency_ms": summarize_ms,
            },
        )

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

        ctx.info(
            "========== SEARCH END [%s] ==========\n"
            "  status: %s | rounds: %d | elapsed: %dms\n"
            "  sources: found=%d dedup=%d filtered=%d\n"
            "  summary_len: %d\n"
            "  warnings: %s\n"
            "  errors: %s",
            request_id, status, rounds_completed, elapsed_ms,
            total_found, total_after_dedup, len(sources),
            len(summary),
            warnings or "(none)", errors or "(none)",
            extra={
                "phase": "search_end",
                "status": status,
                "rounds": rounds_completed,
                "elapsed_ms": elapsed_ms,
                "sources_found": total_found,
                "sources_dedup": total_after_dedup,
                "sources_filtered": len(sources),
                "summary_chars": len(summary),
                "warnings_count": len(warnings),
                "errors_count": len(errors),
            },
        )

        return SearchResponse(
            status=status,
            summary=summary,
            sources=sources,
            metadata=metadata,
            warnings=warnings,
            errors=errors,
        )

    # ------------------------------------------------------------------
    # Post-processing
    # ------------------------------------------------------------------

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
        ctx: ContextLogger,
    ) -> dict:
        total_found = 0
        total_after_dedup = 0
        rounds_completed = 0

        # Step 1: Generate a single initial search query.
        qgen_start = time.monotonic()
        ctx.info("[Step 1] Generating initial search query from: %s", query)
        current_query = await self._generate_initial_query(query)
        qgen_ms = int((time.monotonic() - qgen_start) * 1000)
        ctx.info(
            "[Step 1] Initial query: %s", current_query,
            extra={
                "phase": "query_generation",
                "initial_query": current_query,
                "latency_ms": qgen_ms,
            },
        )

        for round_num in range(1, self._max_rounds + 1):
            round_ctx = ctx.with_context(round=round_num)
            round_start = time.monotonic()
            round_ctx.info(
                "===== Round %d/%d START (query: %s) =====",
                round_num, self._max_rounds, current_query,
                extra={"phase": "round_start", "query": current_query},
            )

            # Step 2: Search with ONE query across all modules.
            search_start = time.monotonic()
            round_ctx.info("[Step 2] Searching '%s' across %d modules",
                           current_query, len(self._modules))
            round_results, round_errors = await self._parallel_search([current_query])
            search_ms = int((time.monotonic() - search_start) * 1000)

            errors.extend(round_errors)
            round_found = sum(len(m.results) for m in round_results)
            total_found += round_found
            if round_errors:
                round_ctx.warning(
                    "[Step 2] Module errors: %s", round_errors,
                    extra={"phase": "module_errors", "errors": round_errors},
                )
            round_ctx.info(
                "[Step 2] Search returned %d raw results", round_found,
                extra={
                    "phase": "parallel_search",
                    "raw_count": round_found,
                    "engines_with_results": len(round_results),
                    "error_count": len(round_errors),
                    "latency_ms": search_ms,
                },
            )

            # Merge all module results into one flat list, tagging each
            # result with the originating module name.
            merged: list[SearchResult] = []
            for m in round_results:
                for r in m.results:
                    r.source_module = m.module
                merged.extend(m.results)

            if not merged:
                if round_num == 1:
                    warnings.append("No results from any search module")
                round_ctx.warning(
                    "[Step 2] No results — stopping",
                    extra={"phase": "round_abort", "reason": "no_results"},
                )
                break

            # Blacklist filter.
            before_bl = len(merged)
            filtered = [r for r in merged if not is_blacklisted(r.url, self._blacklist_patterns)]
            bl_removed = before_bl - len(filtered)
            if bl_removed:
                round_ctx.info(
                    "[Step 3] Blacklist: removed %d/%d results", bl_removed, before_bl,
                    extra={
                        "phase": "blacklist",
                        "removed": bl_removed,
                        "total": before_bl,
                    },
                )

            # Dedup.
            unique, seen_urls = deduplicate(filtered, seen_urls=seen_urls)
            total_after_dedup += len(unique)
            round_ctx.info(
                "[Step 3] After dedup: %d unique (from %d raw)", len(unique), len(merged),
                extra={
                    "phase": "dedup",
                    "unique_count": len(unique),
                    "raw_count": len(merged),
                },
            )

            # Pre-filter: remove thin snippets and near-duplicates.
            unique, filter_stats = prefilter(
                unique,
                min_snippet_length=self._min_snippet_length,
            )
            round_ctx.info(
                "[Step 3.5] Pre-filter: %d remain (removed %d — %s)",
                len(unique), filter_stats["total_removed"], filter_stats,
                extra={
                    "phase": "prefilter",
                    "remaining": len(unique),
                    "removed": filter_stats["total_removed"],
                    "stats": filter_stats,
                },
            )

            if not unique:
                round_ctx.info(
                    "Round %d: all results filtered out", round_num,
                    extra={"phase": "round_abort", "reason": "all_filtered"},
                )
                break

            # Score.
            score_start = time.monotonic()
            round_ctx.info("[Step 4] Scoring %d results via LLM", len(unique))
            scored = await self._scorer.score(query, unique)
            score_ms = int((time.monotonic() - score_start) * 1000)
            round_ctx.info(
                "[Step 4] Scored %d results", len(scored),
                extra={
                    "phase": "scoring",
                    "scored_count": len(scored),
                    "latency_ms": score_ms,
                },
            )
            for s in scored[:5]:
                round_ctx.debug("  %.2f %s — %s", s.final_score, s.url, s.reason[:60])

            # Filter by threshold.
            above = [s for s in scored if s.final_score >= self._score_threshold]
            round_ctx.info(
                "[Step 5] Filter: %d/%d above threshold %.2f",
                len(above), len(scored), self._score_threshold,
                extra={
                    "phase": "filter_threshold",
                    "above_count": len(above),
                    "scored_count": len(scored),
                    "threshold": self._score_threshold,
                },
            )

            all_scored.extend(above if above else scored)
            all_results.extend(unique)

            # Auto-blacklist: append low-scoring domains to blacklist file.
            if self._auto_bl and scored:
                bad_domains = collect_low_score_domains(scored, self._auto_bl_threshold)
                if bad_domains:
                    added = append_auto_blacklist(bad_domains, self._blacklist_file)
                    if added:
                        self._blacklist_patterns = load_blacklist(self._blacklist_file)
                        round_ctx.info(
                            "[Step 5.5] Auto-blacklist: added %d domains, reloaded %d patterns",
                            len(added), len(self._blacklist_patterns),
                            extra={
                                "phase": "auto_blacklist",
                                "added_count": len(added),
                                "patterns_count": len(self._blacklist_patterns),
                            },
                        )

            rounds_completed = round_num

            round_elapsed = int((time.monotonic() - round_start) * 1000)
            round_ctx.info(
                "===== Round %d/%d END (%dms) =====",
                round_num, self._max_rounds, round_elapsed,
                extra={"phase": "round_end", "latency_ms": round_elapsed},
            )

            # Quality evaluation — should we continue?
            if round_num < self._max_rounds:
                eval_start = time.monotonic()
                round_ctx.info("[Step 6] Evaluating search quality via LLM")
                evaluation = await self._evaluator.evaluate(
                    query, all_scored, all_results, round_num, self._max_rounds,
                )
                eval_ms = int((time.monotonic() - eval_start) * 1000)
                round_ctx.info(
                    "[Step 6] Evaluation: sufficient=%s — %s",
                    evaluation.sufficient, evaluation.reason,
                    extra={
                        "phase": "evaluation",
                        "sufficient": evaluation.sufficient,
                        "reason": evaluation.reason,
                        "next_query": evaluation.next_query or "",
                        "latency_ms": eval_ms,
                    },
                )
                if evaluation.sufficient:
                    return {
                        "total_found": total_found,
                        "total_after_dedup": total_after_dedup,
                        "rounds": rounds_completed,
                    }

                # Use the evaluator's next_query for the next round.
                if evaluation.next_query:
                    current_query = evaluation.next_query
                    round_ctx.info("[Step 6] Next query: %s", current_query)
                else:
                    round_ctx.info(
                        "[Step 6] No next_query provided — stopping",
                        extra={"phase": "round_abort", "reason": "no_next_query"},
                    )
                    break

        return {
            "total_found": total_found,
            "total_after_dedup": total_after_dedup,
            "rounds": rounds_completed,
        }

    # ------------------------------------------------------------------
    # Query generation
    # ------------------------------------------------------------------

    async def _generate_initial_query(self, query: str) -> str:
        """Use LLM to generate a single optimal search query."""
        try:
            data = await self._llm.chat_json(
                self._query_system_prompt, query,
            )
        except LLMError as exc:
            logger.warning("Query generation failed: %s", exc)
            return query

        raw = data.get("query", "")
        if isinstance(raw, str) and raw.strip():
            return raw.strip()
        return query

    # ------------------------------------------------------------------
    # Parallel module search
    # ------------------------------------------------------------------

    async def _parallel_search(
        self, queries: list[str],
    ) -> tuple[list[ModuleOutput], list[str]]:
        """Run all callable modules against all queries concurrently.

        Concurrency is bounded by ``self._semaphore`` to avoid resource
        exhaustion when many modules run at once.  Modules whose circuit
        breaker is currently tripped are skipped entirely — they contribute
        no task, no error message, and no log noise beyond a single debug
        line.

        Each call's outcome is recorded on the health tracker so future
        rounds can react to failure clusters.

        Returns (results, error_messages).
        """

        async def _run(module: BaseSearchModule, q: str) -> ModuleOutput:
            async with self._semaphore:
                output = await module.search(q)
            if output.error:
                self._health.record_failure(module.name)
            else:
                self._health.record_success(module.name)
            return output

        tasks: list = []
        skipped: list[str] = []
        for module in self._modules:
            if not self._health.should_call(module.name):
                skipped.append(module.name)
                continue
            for q in queries:
                tasks.append(_run(module, q))

        if skipped:
            logger.debug("Skipping tripped modules: %s", skipped)

        outputs: list[ModuleOutput] = await asyncio.gather(*tasks)

        results: list[ModuleOutput] = []
        error_msgs: list[str] = []

        for out in outputs:
            if out.error:
                error_msgs.append(f"[{out.module}] {out.error.message}")
            if out.results:
                results.append(out)

        return results, error_msgs
