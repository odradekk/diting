"""Search pipeline orchestrator — single-round and multi-round search."""

from __future__ import annotations

import asyncio
import pathlib
import time
import uuid
from collections.abc import Awaitable

from diting.fetch.base import Fetcher
from diting.fetch.tavily import FetchResult
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
from diting.pipeline.semantic_dedup import SemanticDeduplicator
from diting.pipeline.summarizer import Summarizer
from diting.routing.decision_log import RoutingDecision, RoutingDecisionLog
from diting.routing.embedding_router import EmbeddingRouter
from diting.routing.strategy import RoutingStrategy

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
        fast_llm: LLMClient | None = None,
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
        scorer_backend: str = "llm",
        reranker_model: str = "",
        reranker_cache_dir: str = "",
        domain_authority_path: str = "",
        semantic_dedup: bool = False,
        semantic_dedup_threshold: float = 0.9,
        routing_strategy: RoutingStrategy = "funnel",
    ) -> None:
        self._llm = llm
        self._prompts = prompts
        self._routing_strategy: RoutingStrategy = routing_strategy
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
        self._fetcher = fetcher
        self._scorer = Scorer(
            llm,
            prompts,
            relevance_weight=relevance_weight,
            quality_weight=quality_weight,
            backend=scorer_backend,
            reranker_model_id=reranker_model,
            reranker_cache_dir=reranker_cache_dir,
            domain_authority_path=domain_authority_path,
        )
        # Semantic dedup (optional — requires `pip install diting[rerank]`).
        self._semantic_dedup: SemanticDeduplicator | None = None
        if semantic_dedup:
            try:
                from diting.rerank.embedder import BGEEmbedder

                self._semantic_dedup = SemanticDeduplicator(
                    BGEEmbedder(), threshold=semantic_dedup_threshold,
                )
            except Exception as exc:
                logger.warning("Semantic dedup unavailable: %s", exc)

        # Build module catalog (shared by query gen prompt and evaluator).
        self._module_names = {m.name for m in modules}
        self._expensive_modules: set[str] = {
            m.name for m in modules
            if m.manifest is not None and m.manifest.cost_tier == "expensive"
        }
        self._nonfree_modules: set[str] = {
            m.name for m in modules
            if m.manifest is not None and m.manifest.cost_tier != "free"
        }
        catalog = self._build_module_catalog(modules)

        self._evaluator = Evaluator(
            fast_llm or llm, prompts, module_catalog=catalog,
        )
        self._summarizer: Summarizer | None = (
            Summarizer(llm, prompts, fetcher) if fetcher else None
        )

        base_prompt = prompts.load("query_generation")
        self._query_system_prompt = (
            f"{base_prompt}\n\n## Available Search Modules\n\n{catalog}"
            if catalog else base_prompt
        )

        # Embedding router (Layer 1 fallback for LLM routing).
        self._embedding_router: EmbeddingRouter | None = None
        try:
            from diting.rerank.embedder import BGEEmbedder

            # Reuse the same embedder instance if semantic dedup created one.
            embedder = (
                self._semantic_dedup._embedder
                if self._semantic_dedup is not None
                else BGEEmbedder()
            )
            self._embedding_router = EmbeddingRouter(
                modules, embedder, top_k=5,
            )
        except Exception as exc:
            logger.info("Embedding router unavailable: %s", exc)

        self._decision_log = RoutingDecisionLog()

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
        # Pre-dedup snippet map keyed by normalized URL.  Feeds the
        # snippet-aggregation fallback in Summarizer when fetch fails.
        url_snippet_map: dict[str, list[tuple[str, str]]] = {}
        # Interleaved prefetch state.  Each round schedules one
        # fetch_many batch for the new top URLs; tasks run concurrently
        # with subsequent search rounds and are collected before
        # summarization.
        prefetch_batches: list[
            tuple[list[str], asyncio.Task[list[FetchResult]]]
        ] = []
        prefetch_scheduled: set[str] = set()
        total_found = 0
        total_after_dedup = 0
        rounds_completed = 0

        try:
            result = await asyncio.wait_for(
                self._run_rounds(
                    query, all_scored, all_results, seen_urls,
                    url_snippet_map, prefetch_batches, prefetch_scheduled,
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

        # Collect interleaved prefetch results before summarization.  Any
        # task still running will be awaited here; failed batches are
        # logged and omitted from the returned map so Summarizer can
        # perform its normal fetch/fallback path for those URLs.
        prefetched = await self._collect_prefetch(prefetch_batches, ctx)

        # Summarization.
        summarize_start = time.monotonic()
        ctx.info(
            "[Step 9-10] Fetching top sources & generating summary",
            extra={"phase": "summarization_start", "sources_count": len(sources)},
        )
        summary = await self._summarize(
            query, sources, warnings, url_snippet_map, prefetched,
        )
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
        url_snippet_map: dict[str, list[tuple[str, str]]] | None = None,
        prefetched: dict[str, FetchResult] | None = None,
    ) -> str:
        """Summarize top sources. Returns "" on failure or if no fetcher."""
        if not self._summarizer or not sources:
            return ""
        try:
            result = await self._summarizer.summarize(
                query, sources,
                url_snippets=url_snippet_map,
                prefetched=prefetched,
            )
            warnings.extend(result.warnings)
            return result.summary
        except Exception as exc:
            logger.warning("Summarization failed: %s", exc)
            warnings.append(f"Summarization failed: {exc}")
            return ""

    # ------------------------------------------------------------------
    # Interleaved prefetch
    # ------------------------------------------------------------------

    def _schedule_prefetch(
        self,
        scored: list[ScoredResult],
        prefetch_batches: list[
            tuple[list[str], asyncio.Task[list[FetchResult]]]
        ],
        prefetch_scheduled: set[str],
        ctx: ContextLogger,
        *,
        top_k: int = 10,
    ) -> None:
        """Kick off a background ``fetch_many`` for the round's new top URLs.

        URLs already scheduled in a previous round are skipped, so
        repeated calls never duplicate work.  A single task is created
        per round — batching lets the fetcher exploit connection reuse,
        and the task runs concurrently with the next round's search.
        """
        if self._fetcher is None or not scored:
            return

        # Top-K by final_score among entries above threshold.
        candidates = [s for s in scored if s.final_score >= self._score_threshold]
        candidates.sort(key=lambda s: s.final_score, reverse=True)

        new_urls: list[str] = []
        for s in candidates[:top_k]:
            if s.url in prefetch_scheduled:
                continue
            prefetch_scheduled.add(s.url)
            new_urls.append(s.url)

        if not new_urls:
            return

        fetcher = self._fetcher
        task = asyncio.create_task(fetcher.fetch_many(new_urls))
        prefetch_batches.append((new_urls, task))

        ctx.info(
            "[Step 5.7] Scheduled prefetch batch: %d URLs (total scheduled: %d)",
            len(new_urls), len(prefetch_scheduled),
            extra={
                "phase": "prefetch_schedule",
                "batch_size": len(new_urls),
                "total_scheduled": len(prefetch_scheduled),
            },
        )

    async def _collect_prefetch(
        self,
        prefetch_batches: list[
            tuple[list[str], asyncio.Task[list[FetchResult]]]
        ],
        ctx: ContextLogger,
    ) -> dict[str, FetchResult]:
        """Await every in-flight prefetch batch and return a url→result map.

        A batch that raises is logged and dropped — the summarizer's
        normal fetch path will retry any URLs missing from the map.
        """
        if not prefetch_batches:
            return {}

        prefetched: dict[str, FetchResult] = {}
        successes = 0
        total = 0
        for urls, task in prefetch_batches:
            total += len(urls)
            try:
                results = await task
            except Exception as exc:
                ctx.warning(
                    "Prefetch batch failed (%d URLs): %s", len(urls), exc,
                    extra={
                        "phase": "prefetch_collect",
                        "batch_size": len(urls),
                        "success": False,
                    },
                )
                continue
            for url, result in zip(urls, results):
                prefetched[url] = result
                if result.success:
                    successes += 1

        ctx.info(
            "[Step 8] Prefetch collected: %d/%d succeeded",
            successes, total,
            extra={
                "phase": "prefetch_collect",
                "total": total,
                "succeeded": successes,
                "batches": len(prefetch_batches),
            },
        )
        return prefetched

    # ------------------------------------------------------------------
    # Multi-round loop
    # ------------------------------------------------------------------

    async def _run_rounds(
        self,
        query: str,
        all_scored: list[ScoredResult],
        all_results: list[SearchResult],
        seen_urls: set[str],
        url_snippet_map: dict[str, list[tuple[str, str]]],
        prefetch_batches: list[
            tuple[list[str], asyncio.Task[list[FetchResult]]]
        ],
        prefetch_scheduled: set[str],
        warnings: list[str],
        errors: list[str],
        ctx: ContextLogger,
    ) -> dict:
        total_found = 0
        total_after_dedup = 0
        rounds_completed = 0

        # Step 1: Generate a single initial search query + module routing.
        strategy = self._routing_strategy
        qgen_start = time.monotonic()
        ctx.info(
            "[Step 1] Generating initial search query (strategy=%s): %s",
            strategy, query,
        )

        if strategy == "fire_all":
            # fire_all: no routing — use raw LLM query, all modules.
            current_query, _ = await self._generate_initial_query(query)
            active_modules: set[str] | None = None
            routing_source = "all"
        elif strategy == "cheap_first":
            # cheap_first: skip LLM routing call, use embedding or free-only.
            current_query, _ = await self._generate_initial_query(query)
            active_modules = self._resolve_routing(None, current_query, ctx)
            # Aggressively gate ALL non-free modules in Round 1.
            active_modules = self._apply_cost_gate(
                active_modules, round_num=1, prev_avg_score=None,
                evaluator_modules=None, ctx=ctx,
                free_only=True,
            )
            routing_source = (
                "embedding" if active_modules is not None else "all"
            )
        else:
            # funnel (default): full LLM → embedding → all cascade.
            current_query, llm_modules = await self._generate_initial_query(query)
            active_modules = self._resolve_routing(
                llm_modules, current_query, ctx,
            )
            active_modules = self._apply_cost_gate(
                active_modules, round_num=1, prev_avg_score=None,
                evaluator_modules=None, ctx=ctx,
            )
            routing_source = (
                "llm" if llm_modules is not None
                else "embedding" if active_modules is not None
                else "all"
            )

        qgen_ms = int((time.monotonic() - qgen_start) * 1000)

        ctx.info(
            "[Step 1] Initial query: %s, routed modules: %s", current_query,
            sorted(active_modules) if active_modules else "all",
            extra={
                "phase": "query_generation",
                "initial_query": current_query,
                "routed_modules": sorted(active_modules) if active_modules else [],
                "routing_source": routing_source,
                "latency_ms": qgen_ms,
            },
        )

        # Record routing decision for Round 1.
        self._record_routing_decision(
            ctx, round_num=1, query=current_query,
            routing_source=routing_source,
            active_modules=active_modules,
        )

        for round_num in range(1, self._max_rounds + 1):
            round_ctx = ctx.with_context(round=round_num)
            round_start = time.monotonic()
            round_ctx.info(
                "===== Round %d/%d START (query: %s) =====",
                round_num, self._max_rounds, current_query,
                extra={"phase": "round_start", "query": current_query},
            )

            # Step 2: Search with ONE query across routed modules.
            search_start = time.monotonic()
            n_active = len(active_modules) if active_modules else len(self._modules)
            round_ctx.info("[Step 2] Searching '%s' across %d modules",
                           current_query, n_active)
            round_results, round_errors = await self._parallel_search(
                [current_query], round_ctx,
                active_modules=active_modules,
            )
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

            # Track per-URL multi-engine snippets before dedup strips
            # the duplicate-URL hits.  The aggregation fallback uses this
            # map when a URL's fetch fails and ≥2 engines contributed.
            for r in merged:
                key = normalize_url(r.url)
                if key:
                    url_snippet_map.setdefault(key, []).append(
                        (r.source_module, r.snippet),
                    )

            if not merged:
                # Safety net: if routing excluded modules, retry with all.
                if active_modules is not None:
                    round_ctx.warning(
                        "[Step 2] No results with routed modules %s "
                        "— retrying with ALL modules",
                        sorted(active_modules),
                        extra={
                            "phase": "safety_net_fire_all",
                            "routed_modules": sorted(active_modules),
                        },
                    )
                    active_modules = None
                    round_results, round_errors_retry = await self._parallel_search(
                        [current_query], round_ctx, active_modules=None,
                    )
                    errors.extend(round_errors_retry)
                    for m in round_results:
                        for r in m.results:
                            r.source_module = m.module
                        merged.extend(m.results)

                    self._record_routing_decision(
                        round_ctx, round_num=round_num,
                        query=current_query,
                        routing_source="safety_net",
                        active_modules=None,
                    )

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

            # Semantic dedup (optional).
            if self._semantic_dedup is not None:
                sd_start = time.monotonic()
                unique, sd_stats = self._semantic_dedup.deduplicate(unique)
                sd_ms = int((time.monotonic() - sd_start) * 1000)
                round_ctx.info(
                    "[Step 3.75] Semantic dedup: removed %d (%.0fms)",
                    sd_stats["semantic_removed"], sd_ms,
                    extra={
                        "phase": "semantic_dedup",
                        "removed": sd_stats["semantic_removed"],
                        "remaining": len(unique),
                        "latency_ms": sd_ms,
                    },
                )

            # Score.
            score_start = time.monotonic()
            round_ctx.info(
                "[Step 4] Scoring %d results via %s",
                len(unique),
                self._scorer.backend_name,
            )
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

            # Start fetching top-scored URLs in the background so their
            # content is ready by the time summarization runs — overlaps
            # with the next round's search and evaluation.
            self._schedule_prefetch(
                above or scored, prefetch_batches, prefetch_scheduled, round_ctx,
            )

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
                        "next_modules": evaluation.next_modules,
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

                    if strategy == "fire_all":
                        # fire_all: ignore module selection, keep all.
                        active_modules = None
                        routing_src = "all"
                    else:
                        # Validate evaluator module names against known modules.
                        validated = [
                            m for m in evaluation.next_modules
                            if m in self._module_names
                        ]
                        active_modules = set(validated) if validated else None

                        # Apply cost gate for Round 2+.
                        round_scores = [s.final_score for s in all_scored]
                        prev_avg = (
                            sum(round_scores) / len(round_scores)
                            if round_scores else None
                        )
                        active_modules = self._apply_cost_gate(
                            active_modules, round_num=round_num + 1,
                            prev_avg_score=prev_avg,
                            evaluator_modules=evaluation.next_modules,
                            ctx=round_ctx,
                        )

                        # Safety net: fire-more escalation when quality is very low.
                        fire_more = (
                            prev_avg is not None
                            and prev_avg < self._FIRE_MORE_SCORE_THRESHOLD
                            and active_modules is not None
                        )
                        if fire_more:
                            round_ctx.warning(
                                "[Step 6] Low avg score (%.2f < %.2f) — "
                                "escalating to ALL modules for next round",
                                prev_avg, self._FIRE_MORE_SCORE_THRESHOLD,
                                extra={
                                    "phase": "safety_net_fire_more",
                                    "prev_avg_score": prev_avg,
                                    "threshold": self._FIRE_MORE_SCORE_THRESHOLD,
                                },
                            )
                            active_modules = None

                        routing_src = (
                            "safety_net" if fire_more
                            else "evaluator" if validated
                            else "all"
                        )

                    round_ctx.info(
                        "[Step 6] Next query: %s, next modules: %s",
                        current_query,
                        sorted(active_modules) if active_modules else "all",
                    )

                    # Record routing decision for the upcoming round.
                    self._record_routing_decision(
                        round_ctx, round_num=round_num + 1,
                        query=current_query,
                        routing_source=routing_src,
                        active_modules=active_modules,
                    )
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

    async def _generate_initial_query(
        self, query: str,
    ) -> tuple[str, list[str] | None]:
        """Use LLM to generate a search query and select modules.

        Returns ``(optimised_query, selected_module_names)``.  The module
        list is ``None`` when the LLM did not produce a valid selection
        (caller should fall back to the embedding router or all modules).
        """
        try:
            data = await self._llm.chat_json(
                self._query_system_prompt,
                query,
            )
        except LLMError as exc:
            logger.warning("Query generation failed: %s", exc)
            return query, None

        raw = data.get("query", "")
        optimised = raw.strip() if isinstance(raw, str) and raw.strip() else query

        # Parse module selection.
        selected = self._parse_module_selection(data.get("modules"))
        if selected is not None:
            skip_reason = data.get("skip_reason")
            if isinstance(skip_reason, dict) and skip_reason:
                logger.debug("LLM skip reasons: %s", skip_reason)

        return optimised, selected

    def _parse_module_selection(
        self, raw_modules: object,
    ) -> list[str] | None:
        """Validate LLM-returned module list against known module names.

        Returns ``None`` when the output is invalid or empty (triggering
        fallback to the embedding router).
        """
        if not isinstance(raw_modules, list) or not raw_modules:
            return None

        valid: list[str] = []
        seen: set[str] = set()
        for item in raw_modules:
            if not isinstance(item, str):
                continue
            name = item.strip().lower()
            if name and name in self._module_names and name not in seen:
                seen.add(name)
                valid.append(name)

        return valid if valid else None

    def _resolve_routing(
        self,
        llm_modules: list[str] | None,
        query: str,
        ctx: ContextLogger,
    ) -> set[str] | None:
        """Resolve final module set: LLM → embedding fallback → all.

        Returns ``None`` to invoke all modules (no filtering).
        """
        if llm_modules is not None:
            ctx.debug(
                "LLM router selected modules: %s", llm_modules,
                extra={"phase": "routing", "source": "llm"},
            )
            return set(llm_modules)

        if self._embedding_router is not None:
            try:
                emb_modules = self._embedding_router.route(query)
                # If the embedding router returns all modules, treat as None.
                if set(emb_modules) == self._module_names:
                    ctx.debug(
                        "Embedding router returned all modules — no filtering",
                        extra={"phase": "routing", "source": "embedding_all"},
                    )
                    return None
                ctx.info(
                    "Embedding router selected modules: %s", emb_modules,
                    extra={"phase": "routing", "source": "embedding"},
                )
                return set(emb_modules)
            except Exception:
                ctx.warning(
                    "Embedding router failed — using all modules",
                    exc_info=True,
                    extra={"phase": "routing", "source": "fallback"},
                )

        return None

    # Cost gate threshold: if the previous round's average score is below
    # this value, expensive modules are allowed in subsequent rounds even
    # when the evaluator did not explicitly request them.
    _COST_GATE_SCORE_THRESHOLD: float = 0.5

    # Fire-more threshold: if the round's average score is below this
    # value, the next round expands to ALL modules regardless of what
    # the evaluator suggested — an emergency "fire more sources" mode.
    _FIRE_MORE_SCORE_THRESHOLD: float = 0.3

    def _apply_cost_gate(
        self,
        active_modules: set[str] | None,
        round_num: int,
        prev_avg_score: float | None,
        evaluator_modules: list[str] | None,
        ctx: ContextLogger,
        *,
        free_only: bool = False,
    ) -> set[str] | None:
        """Filter expensive modules based on round and quality signals.

        Round 1: expensive modules are always excluded.
        Round 2+: expensive modules are allowed only when:
          - The evaluator explicitly requested them, OR
          - The previous round's average score is below the threshold.

        When *free_only* is True (``cheap_first`` strategy), ALL non-free
        modules are gated in Round 1 — not just expensive ones.
        """
        gate_set = self._nonfree_modules if free_only else self._expensive_modules
        if not gate_set:
            return active_modules

        if round_num == 1:
            # Round 1: unconditionally gate.
            gated = gate_set
        else:
            # Round 2+: check if any gate-opening condition is met.
            evaluator_requested = (
                set(evaluator_modules) & gate_set
                if evaluator_modules else set()
            )
            low_quality = (
                prev_avg_score is not None
                and prev_avg_score < self._COST_GATE_SCORE_THRESHOLD
            )

            if evaluator_requested:
                # Only allow the explicitly requested gated modules.
                gated = gate_set - evaluator_requested
            elif low_quality:
                # Low quality: open the gate for all gated modules.
                gated = set()
            else:
                # Quality is acceptable and evaluator didn't ask: keep gated.
                gated = gate_set

        if not gated:
            return active_modules

        if active_modules is not None:
            filtered = active_modules - gated
        else:
            filtered = self._module_names - gated

        # Safety net: never produce an empty module set.
        if not filtered:
            ctx.warning(
                "Cost gate would exclude ALL modules — disabling gate",
                extra={
                    "phase": "cost_gate_safety",
                    "gated_modules": sorted(gated),
                    "round": round_num,
                },
            )
            return active_modules

        if gated:
            ctx.info(
                "Cost gate: excluded expensive modules %s (round=%d, prev_avg=%.2f)",
                sorted(gated), round_num,
                prev_avg_score if prev_avg_score is not None else 0.0,
                extra={
                    "phase": "cost_gate",
                    "gated_modules": sorted(gated),
                    "round": round_num,
                },
            )

        return filtered if filtered != self._module_names else None

    def _record_routing_decision(
        self,
        ctx: ContextLogger,
        *,
        round_num: int,
        query: str,
        routing_source: str,
        active_modules: set[str] | None,
    ) -> None:
        """Persist a routing decision to the JSONL log."""
        try:
            all_names = {str(n) for n in self._module_names}
            active = {str(n) for n in active_modules} if active_modules else all_names
            included = sorted(active)
            excluded = sorted(all_names - active)
            cost_gated = sorted(
                {str(n) for n in self._expensive_modules} & set(excluded)
            )

            query_id = ctx.extra.get("query_id", "") if ctx.extra else ""
            self._decision_log.record(RoutingDecision(
                query_id=query_id,
                round=round_num,
                query=query,
                routing_source=routing_source,
                included_modules=included,
                excluded_modules=excluded,
                cost_gated=cost_gated,
            ))
        except Exception:
            logger.debug("Failed to record routing decision", exc_info=True)

    @staticmethod
    def _build_module_catalog(modules: list[BaseSearchModule]) -> str:
        """Format module manifests as a compact table for the LLM prompt."""
        lines = ["| Module | Type | Scope |", "|--------|------|-------|"]
        for mod in modules:
            manifest = mod.manifest
            if manifest is None:
                lines.append(f"| {mod.name} | unknown | (no manifest) |")
                continue
            scope = manifest.scope.replace("\n", " ").strip()
            if len(scope) > 120:
                scope = scope[:117] + "..."
            lines.append(
                f"| {mod.name} | {manifest.result_type} | {scope} |"
            )
        return "\n".join(lines) if len(lines) > 2 else ""

    # ------------------------------------------------------------------
    # Parallel module search
    # ------------------------------------------------------------------

    async def _parallel_search(
        self,
        queries: list[str],
        ctx: ContextLogger | None = None,
        *,
        active_modules: set[str] | None = None,
    ) -> tuple[list[ModuleOutput], list[str]]:
        """Run callable modules against all queries concurrently.

        Parameters
        ----------
        active_modules:
            When provided, only modules whose name is in this set are
            invoked.  ``None`` means all enabled modules (current behaviour).

        Concurrency is bounded by ``self._semaphore`` to avoid resource
        exhaustion when many modules run at once.  Modules whose circuit
        breaker is currently tripped are skipped entirely.

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

        tasks: list[Awaitable[ModuleOutput]] = []
        skipped: list[str] = []
        routed_out: list[str] = []
        for module in self._modules:
            if active_modules is not None and module.name not in active_modules:
                routed_out.append(module.name)
                continue
            if not self._health.should_call(module.name):
                skipped.append(module.name)
                continue
            for q in queries:
                tasks.append(_run(module, q))

        log = ctx if ctx is not None else logger
        if routed_out:
            log.debug(
                "Routed out modules: %s", routed_out,
                extra={"phase": "module_routed_out", "routed_out": routed_out},
            )
        if skipped:
            log.debug(
                "Skipping tripped modules: %s", skipped,
                extra={"phase": "module_skipped", "skipped_modules": skipped},
            )

        outputs: list[ModuleOutput] = await asyncio.gather(*tasks)

        results: list[ModuleOutput] = []
        error_msgs: list[str] = []

        for out in outputs:
            if out.error:
                error_msgs.append(f"[{out.module}] {out.error.message}")
            if out.results:
                results.append(out)

        return results, error_msgs
