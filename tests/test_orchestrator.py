"""Tests for diting.pipeline.orchestrator — search pipeline orchestration."""

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

from diting.fetch.tavily import FetchResult
from diting.llm.client import LLMError
from diting.models import ModuleError, ModuleOutput, SearchResult
from diting.pipeline.health import HealthTracker
from diting.pipeline.orchestrator import Orchestrator


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

QUERY = "best python web frameworks"


def _search_results(n: int = 3, prefix: str = "https://example.com") -> list[SearchResult]:
    return [
        SearchResult(
            title=f"Result {i}",
            url=f"{prefix}/{i}",
            snippet=f"This is a detailed snippet for result {i} with enough content to pass prefilter.",
        )
        for i in range(1, n + 1)
    ]


def _module_output(name: str = "brave", results: list[SearchResult] | None = None) -> ModuleOutput:
    return ModuleOutput(
        module=name,
        results=results or _search_results(),
    )


def _scored_results(results: list[SearchResult], score: float = 0.8) -> dict:
    return {
        "scored_results": [
            {
                "url": r.url,
                "relevance": score,
                "quality": score,
                "final_score": score,
                "reason": "Good",
            }
            for r in results
        ]
    }


def _sufficient_eval() -> dict:
    return {"sufficient": True, "reason": "Good enough", "next_query": ""}


def _insufficient_eval(next_query: str = "more specific query") -> dict:
    return {
        "sufficient": False,
        "reason": "Need more",
        "next_query": next_query,
    }


def _query_gen_response(query: str = "python web frameworks") -> dict:
    return {"query": query}


def _make_orchestrator(
    modules: list | None = None,
    max_rounds: int = 3,
    global_timeout: int = 120,
    score_threshold: float = 0.3,
    blacklist_file: str = "/dev/null/nonexistent",
    fetcher: object | None = None,
    routing_strategy: str = "funnel",
) -> Orchestrator:
    llm = MagicMock()
    prompts = MagicMock()
    prompts.load.return_value = "System prompt"

    if modules is None:
        module = MagicMock()
        module.search = AsyncMock(return_value=_module_output())
        modules = [module]

    return Orchestrator(
        llm=llm,
        prompts=prompts,
        modules=modules,
        max_rounds=max_rounds,
        global_timeout=global_timeout,
        score_threshold=score_threshold,
        blacklist_file=blacklist_file,
        auto_blacklist=False,
        fetcher=fetcher,
        routing_strategy=routing_strategy,
    )


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestSingleRoundSuccess:
    async def test_basic_search_returns_response(self):
        results = _search_results()
        scored = _scored_results(results)

        orch = _make_orchestrator()
        # chat_json calls: query_gen, scoring, evaluation
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            scored,
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)

        assert response.status == "success"
        assert response.metadata.rounds == 1
        assert response.metadata.query == QUERY
        assert len(response.metadata.request_id) == 12

    async def test_sources_sorted_by_score(self):
        results = _search_results(3)
        scored_data = {
            "scored_results": [
                {"url": results[0].url, "relevance": 0.5, "quality": 0.5, "final_score": 0.5, "reason": "Ok"},
                {"url": results[1].url, "relevance": 0.9, "quality": 0.9, "final_score": 0.9, "reason": "Great"},
                {"url": results[2].url, "relevance": 0.7, "quality": 0.7, "final_score": 0.7, "reason": "Good"},
            ]
        }

        orch = _make_orchestrator()
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            scored_data,
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)

        assert response.status == "success"


class TestMultiRound:
    async def test_two_rounds_when_first_insufficient(self):
        """Round 1 uses initial query, round 2 uses evaluator's next_query."""
        results_r1 = _search_results(3, "https://round1.com")
        results_r2 = _search_results(2, "https://round2.com")

        module = MagicMock()
        module.search = AsyncMock(side_effect=[
            _module_output("brave", results_r1),  # round 1
            _module_output("brave", results_r2),  # round 2
        ])

        orch = _make_orchestrator(modules=[module])
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),              # initial query gen
            _scored_results(results_r1),        # round 1 scoring
            _insufficient_eval("django vs flask comparison"),  # round 1 eval — next_query
            _scored_results(results_r2),        # round 2 scoring
            _sufficient_eval(),                 # round 2 eval — sufficient
        ])

        response = await orch.search(QUERY)
        assert response.status == "success"
        assert response.metadata.rounds == 2
        assert module.search.call_count == 2


class TestDegradation:
    async def test_all_modules_fail(self):
        module = MagicMock()
        module.search = AsyncMock(return_value=ModuleOutput(
            module="brave",
            results=[],
            error=ModuleError(code="ERROR", message="API down", retryable=False),
        ))

        orch = _make_orchestrator(modules=[module])
        orch._llm.chat_json = AsyncMock(return_value=_query_gen_response())

        response = await orch.search(QUERY)

        assert response.status == "error"
        assert len(response.errors) > 0

    async def test_scoring_failure_returns_unscored(self):
        results = _search_results()
        module = MagicMock()
        module.search = AsyncMock(return_value=_module_output("brave", results))

        orch = _make_orchestrator(modules=[module])
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            LLMError("scoring failed"),  # scorer fails
            _sufficient_eval(),          # evaluator still called
        ])

        response = await orch.search(QUERY)

        assert response.status == "success"
        assert any("unscored" in w.lower() for w in response.warnings)

    async def test_query_gen_failure_uses_original(self):
        results = _search_results()

        orch = _make_orchestrator()
        # First chat_json call (query gen) fails, rest succeed
        orch._llm.chat_json = AsyncMock(side_effect=[
            LLMError("query gen down"),
            _scored_results(results),
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)
        assert response.status == "success"


class TestGlobalTimeout:
    async def test_global_timeout_returns_partial(self):
        async def slow_search(query):
            await asyncio.sleep(5)
            return _module_output()

        module = MagicMock()
        module.search = slow_search

        orch = _make_orchestrator(modules=[module], global_timeout=1)
        orch._llm.chat_json = AsyncMock(return_value=_query_gen_response())

        response = await orch.search(QUERY)

        assert any("timeout" in w.lower() for w in response.warnings)


class TestBlacklist:
    async def test_blacklisted_domains_filtered(self, tmp_path):
        bl_file = tmp_path / "blacklist.txt"
        bl_file.write_text("^spam\\.org(/|$)\n", encoding="utf-8")

        results = [
            SearchResult(title="Good", url="https://good.com/page", snippet="Useful content that passes the prefilter length check."),
            SearchResult(title="Bad", url="https://spam.org/page", snippet="Spam content that gets blacklisted before prefilter."),
        ]
        module = MagicMock()
        module.search = AsyncMock(return_value=_module_output("brave", results))

        orch = _make_orchestrator(modules=[module], blacklist_file=str(bl_file))
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            {"scored_results": [
                {"url": "https://good.com/page", "relevance": 0.9, "quality": 0.9, "final_score": 0.9, "reason": "Good"},
            ]},
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)
        assert response.status == "success"
        assert response.metadata.sources_after_dedup == 1


class TestNoResults:
    async def test_no_results_status(self):
        module = MagicMock()
        module.search = AsyncMock(return_value=ModuleOutput(module="brave", results=[]))

        orch = _make_orchestrator(modules=[module])
        orch._llm.chat_json = AsyncMock(return_value=_query_gen_response())

        response = await orch.search(QUERY)
        assert response.status == "no_results"


class TestScoreThreshold:
    async def test_low_scores_filtered(self):
        results = _search_results(2)
        scored_data = {
            "scored_results": [
                {"url": results[0].url, "relevance": 0.1, "quality": 0.1, "final_score": 0.1, "reason": "Bad"},
                {"url": results[1].url, "relevance": 0.9, "quality": 0.9, "final_score": 0.9, "reason": "Great"},
            ]
        }

        orch = _make_orchestrator(score_threshold=0.5)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            scored_data,
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)
        assert response.status == "success"
        assert response.metadata.sources_after_filter == 1


class TestMetadata:
    async def test_metadata_populated(self):
        orch = _make_orchestrator()
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)

        assert response.metadata.query == QUERY
        assert response.metadata.elapsed_ms >= 0
        assert response.metadata.rounds >= 1
        assert response.metadata.total_sources_found > 0


class TestParallelSearch:
    async def test_multiple_modules_called(self):
        """Each module is called once per round (one query per round)."""
        module1 = MagicMock()
        module1.search = AsyncMock(return_value=_module_output("brave"))
        module2 = MagicMock()
        module2.search = AsyncMock(return_value=_module_output("serp"))

        orch = _make_orchestrator(modules=[module1, module2])
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response("q1"),
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)

        # One query per round → each module called exactly once.
        assert module1.search.call_count == 1
        assert module2.search.call_count == 1


# ---------------------------------------------------------------------------
# Phase 4 integration: Classification + Summarization
# ---------------------------------------------------------------------------


def _summary_response(text: str = "A comprehensive summary.") -> dict:
    return {"analysis": text}


class TestSummarizationIntegration:
    async def test_summary_populated_with_fetcher(self):
        """When a fetcher is provided, the orchestrator generates a summary."""
        results = _search_results()
        fetcher = MagicMock()
        fetcher.fetch_many = AsyncMock(return_value=[
            FetchResult(url=r.url, content=f"Content for {r.title}", success=True)
            for r in results
        ])

        orch = _make_orchestrator(fetcher=fetcher)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
            _summary_response("Python frameworks compared."),
        ])

        response = await orch.search(QUERY)

        assert response.summary == "Python frameworks compared."
        assert fetcher.fetch_many.call_count == 1

    async def test_no_fetcher_means_no_summary(self):
        """Without a fetcher, summary stays empty."""
        results = _search_results()
        orch = _make_orchestrator()  # no fetcher
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)

        assert response.summary == ""

    async def test_summarization_failure_degrades_gracefully(self):
        """If summarization raises, summary is empty and warning added."""
        results = _search_results()
        fetcher = MagicMock()
        fetcher.fetch_many = AsyncMock(return_value=[])

        orch = _make_orchestrator(fetcher=fetcher)

        with patch.object(orch._summarizer, "summarize", side_effect=Exception("fetch down")):
            orch._llm.chat_json = AsyncMock(side_effect=[
                _query_gen_response(),
                _scored_results(results),
                _sufficient_eval(),
            ])

            response = await orch.search(QUERY)

        assert response.summary == ""
        assert any("summarization failed" in w.lower() for w in response.warnings)

    async def test_fetch_warnings_propagated(self):
        """Warnings from partial fetch failures appear in response."""
        results = _search_results(2)
        fetcher = MagicMock()
        fetcher.fetch_many = AsyncMock(return_value=[
            FetchResult(url=results[0].url, content="Good content", success=True),
            FetchResult(url=results[1].url, content="", success=False, error="Timeout"),
        ])

        orch = _make_orchestrator(fetcher=fetcher)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
            _summary_response("Partial summary."),
        ])

        response = await orch.search(QUERY)

        assert response.summary == "Partial summary."
        assert any("failed to fetch" in w.lower() for w in response.warnings)

    async def test_prefetched_content_reaches_summarizer(self):
        """Orchestrator must schedule top-URL fetches after scoring and
        forward the results as ``prefetched`` to Summarizer so the
        summarizer doesn't re-fetch the same URLs."""
        results = _search_results(3)
        fetcher = MagicMock()
        fetcher.fetch_many = AsyncMock(return_value=[
            FetchResult(url=r.url, content=f"Content for {r.title}", success=True)
            for r in results
        ])

        orch = _make_orchestrator(fetcher=fetcher)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
        ])

        captured: dict = {}

        async def fake_summarize(*_args, **kwargs):
            captured["prefetched"] = kwargs.get("prefetched")
            from diting.pipeline.summarizer import SummaryResult
            return SummaryResult(summary="ok")

        with patch.object(
            orch._summarizer, "summarize", side_effect=fake_summarize,
        ):
            await orch.search(QUERY)

        prefetched = captured["prefetched"]
        assert prefetched is not None
        assert len(prefetched) == 3
        for r in results:
            assert r.url in prefetched
            assert prefetched[r.url].success
            assert prefetched[r.url].content == f"Content for {r.title}"

        # Exactly one batched prefetch call was issued by the orchestrator.
        assert fetcher.fetch_many.call_count == 1

    async def test_below_threshold_urls_not_prefetched(self):
        """Only URLs above the score threshold get prefetched."""
        results = _search_results(3)
        fetcher = MagicMock()
        fetcher.fetch_many = AsyncMock(return_value=[
            FetchResult(url=results[0].url, content="c0", success=True),
            FetchResult(url=results[2].url, content="c2", success=True),
        ])

        # Middle result scores below threshold.
        mixed_scores = {"scored_results": [
            {"url": results[0].url, "relevance": 0.9, "quality": 0.9, "final_score": 0.9, "reason": "a"},
            {"url": results[1].url, "relevance": 0.1, "quality": 0.1, "final_score": 0.1, "reason": "b"},
            {"url": results[2].url, "relevance": 0.8, "quality": 0.8, "final_score": 0.8, "reason": "c"},
        ]}

        orch = _make_orchestrator(fetcher=fetcher, score_threshold=0.5)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            mixed_scores,
            _sufficient_eval(),
        ])

        captured: dict = {}

        async def fake_summarize(*_args, **kwargs):
            captured["prefetched"] = kwargs.get("prefetched")
            from diting.pipeline.summarizer import SummaryResult
            return SummaryResult(summary="ok")

        with patch.object(
            orch._summarizer, "summarize", side_effect=fake_summarize,
        ):
            await orch.search(QUERY)

        prefetched = captured["prefetched"]
        assert set(prefetched) == {results[0].url, results[2].url}

        # Only the above-threshold URLs were included in the batched fetch.
        batch_urls = fetcher.fetch_many.call_args[0][0]
        assert set(batch_urls) == {results[0].url, results[2].url}

    async def test_multi_round_prefetch_deduped(self):
        """URLs scored in both rounds are fetched only once."""
        # Round 1 returns A,B. Round 2 returns B,C (B repeats).
        r1 = [
            SearchResult(title="A", url="https://ex.com/a",
                         snippet="detailed snippet for a passes prefilter easily."),
            SearchResult(title="B", url="https://ex.com/b",
                         snippet="detailed snippet for b passes prefilter easily."),
        ]
        r2 = [
            SearchResult(title="B2", url="https://ex.com/b",
                         snippet="detailed snippet for b repeat passes prefilter."),
            SearchResult(title="C", url="https://ex.com/c",
                         snippet="detailed snippet for c passes prefilter easily."),
        ]

        module = MagicMock()
        module.name = "brave"
        module.search = AsyncMock(side_effect=[
            _module_output("brave", r1),
            _module_output("brave", r2),
        ])

        fetcher = MagicMock()
        # Round 1 batch: [a, b].  Round 2 batch: [c] only (b deduped).
        fetcher.fetch_many = AsyncMock(side_effect=[
            [FetchResult(url="https://ex.com/a", content="A", success=True),
             FetchResult(url="https://ex.com/b", content="B", success=True)],
            [FetchResult(url="https://ex.com/c", content="C", success=True)],
        ])

        orch = _make_orchestrator(modules=[module], fetcher=fetcher, max_rounds=2)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(r1),
            _insufficient_eval("follow-up"),
            _scored_results(r2),
            _sufficient_eval(),
        ])

        captured: dict = {}

        async def fake_summarize(*_args, **kwargs):
            captured["prefetched"] = kwargs.get("prefetched")
            from diting.pipeline.summarizer import SummaryResult
            return SummaryResult(summary="ok")

        with patch.object(
            orch._summarizer, "summarize", side_effect=fake_summarize,
        ):
            await orch.search(QUERY)

        # Two rounds => two batches (one per round).
        assert fetcher.fetch_many.call_count == 2
        # Round-1 batch included b; round-2 batch must NOT include b again.
        round2_urls = fetcher.fetch_many.call_args_list[1][0][0]
        assert "https://ex.com/b" not in round2_urls
        assert "https://ex.com/c" in round2_urls

        prefetched = captured["prefetched"]
        assert set(prefetched) == {
            "https://ex.com/a", "https://ex.com/b", "https://ex.com/c",
        }

    async def test_prefetch_failure_swallowed_summarizer_retries(self):
        """If a prefetch batch raises, the summarizer's own fetch path
        retries the URL from scratch — no crash, no double-report."""
        results = _search_results(2)

        fetcher = MagicMock()
        # First call (prefetch batch) raises; second call (summarizer's
        # fallback fetch_many) returns valid results.
        fetcher.fetch_many = AsyncMock(side_effect=[
            RuntimeError("prefetch network down"),
            [FetchResult(url=r.url, content=f"Retry {r.title}", success=True)
             for r in results],
        ])

        orch = _make_orchestrator(fetcher=fetcher)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
            _summary_response("Recovered."),
        ])

        response = await orch.search(QUERY)

        assert response.summary == "Recovered."
        assert fetcher.fetch_many.call_count == 2

    async def test_multi_engine_snippet_map_reaches_summarizer(self):
        """Orchestrator must build a pre-dedup url→[(engine,snippet)] map
        and forward it to Summarizer as ``url_snippets`` so the aggregation
        fallback can rescue URLs whose fetch failed."""
        shared_url = "https://example.com/shared"
        # Two modules both find the same URL with different snippets.
        module_a = MagicMock()
        module_a.name = "brave"
        module_a.search = AsyncMock(return_value=ModuleOutput(
            module="brave",
            results=[SearchResult(
                title="A", url=shared_url,
                snippet="brave snippet with enough characters to pass prefilter.",
            )],
        ))
        module_b = MagicMock()
        module_b.name = "bing"
        module_b.search = AsyncMock(return_value=ModuleOutput(
            module="bing",
            results=[SearchResult(
                title="B", url=shared_url,
                snippet="bing snippet with enough characters to pass prefilter.",
            )],
        ))
        fetcher = MagicMock()
        fetcher.fetch_many = AsyncMock(return_value=[
            FetchResult(url=shared_url, content="", success=False, error="down"),
        ])
        orch = _make_orchestrator(modules=[module_a, module_b], fetcher=fetcher)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results([SearchResult(title="t", url=shared_url, snippet="s")]),
            _sufficient_eval(),
        ])

        captured: dict = {}

        async def fake_summarize(*_args, **kwargs):
            captured["url_snippets"] = kwargs.get("url_snippets")
            from diting.pipeline.summarizer import SummaryResult
            return SummaryResult(summary="ok")

        with patch.object(
            orch._summarizer, "summarize", side_effect=fake_summarize,
        ):
            await orch.search(QUERY)

        snippet_map = captured["url_snippets"]
        assert snippet_map is not None
        # The shared URL's normalized form should carry entries from BOTH engines.
        from diting.pipeline.dedup import normalize_url
        key = normalize_url(shared_url)
        assert key in snippet_map
        engines = {engine for engine, _ in snippet_map[key]}
        assert engines == {"brave", "bing"}


# ---------------------------------------------------------------------------
# Adaptive query generation (evaluator-driven per-round queries)
# ---------------------------------------------------------------------------


class TestAdaptiveQueryGeneration:
    async def test_stops_when_no_next_query(self):
        """If evaluator says insufficient but provides no next_query, stop."""
        results = _search_results(2)
        module = MagicMock()
        module.search = AsyncMock(return_value=_module_output("brave", results))

        orch = _make_orchestrator(modules=[module], max_rounds=5)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),              # initial query
            _scored_results(results),           # round 1 scoring
            _insufficient_eval(""),             # insufficient but no next_query
        ])

        response = await orch.search(QUERY)
        # No next_query → only 1 round.
        assert response.metadata.rounds == 1
        assert module.search.call_count == 1

    async def test_evaluator_next_query_drives_next_round(self):
        """When evaluator provides next_query, it is used for the next round."""
        results_r1 = _search_results(2, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        module = MagicMock()
        module.search = AsyncMock(side_effect=[
            _module_output("brave", results_r1),  # round 1
            _module_output("brave", results_r2),  # round 2
        ])

        orch = _make_orchestrator(modules=[module], max_rounds=5)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response("initial query"),         # initial query gen
            _scored_results(results_r1),                  # round 1 scoring
            _insufficient_eval("follow-up query"),        # insufficient + next_query
            _scored_results(results_r2),                  # round 2 scoring
            _sufficient_eval(),                           # sufficient
        ])

        response = await orch.search(QUERY)
        assert response.metadata.rounds == 2
        assert module.search.call_count == 2

    async def test_three_adaptive_rounds(self):
        """Each round uses the evaluator's next_query from the previous round."""
        results_r1 = _search_results(2, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")
        results_r3 = _search_results(2, "https://r3.com")

        module = MagicMock()
        module.search = AsyncMock(side_effect=[
            _module_output("brave", results_r1),
            _module_output("brave", results_r2),
            _module_output("brave", results_r3),
        ])

        orch = _make_orchestrator(modules=[module], max_rounds=3)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response("q1"),          # initial query
            _scored_results(results_r1),        # round 1 scoring
            _insufficient_eval("q2"),           # next_query for round 2
            _scored_results(results_r2),        # round 2 scoring
            _insufficient_eval("q3"),           # next_query for round 3
            _scored_results(results_r3),        # round 3 scoring (last round, no eval)
        ])

        response = await orch.search(QUERY)
        assert response.metadata.rounds == 3
        assert module.search.call_count == 3


# ---------------------------------------------------------------------------
# Health tracker integration
# ---------------------------------------------------------------------------


class TestHealthTracker:
    async def test_tripped_module_is_skipped(self):
        """A module with an active trip is never called this round."""
        good_results = _search_results(2, "https://good.com")
        good = MagicMock()
        good.name = "good"
        good.search = AsyncMock(return_value=_module_output("good", good_results))

        bad = MagicMock()
        bad.name = "bad"
        bad.search = AsyncMock(return_value=_module_output("bad", _search_results()))

        tracker = HealthTracker()
        # Pre-trip the "bad" module with 3 consecutive failures.
        for _ in range(3):
            tracker.record_failure("bad")
        assert tracker.should_call("bad") is False

        orch = _make_orchestrator(modules=[good, bad])
        orch._health = tracker  # replace the default tracker
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(good_results),
            _sufficient_eval(),
        ])

        response = await orch.search(QUERY)

        assert response.status == "success"
        good.search.assert_awaited()
        bad.search.assert_not_awaited()

    async def test_module_failure_is_recorded(self):
        """A failing module has its failure recorded on the tracker."""
        module = MagicMock()
        module.name = "brave"
        module.search = AsyncMock(return_value=ModuleOutput(
            module="brave",
            results=[],
            error=ModuleError(code="ERROR", message="boom", retryable=False),
        ))

        tracker = HealthTracker()
        orch = _make_orchestrator(modules=[module], max_rounds=3)
        orch._health = tracker
        orch._llm.chat_json = AsyncMock(return_value=_query_gen_response())

        await orch.search(QUERY)

        # Orchestrator halts after the first empty round, so only one
        # failure is recorded — enough to prove the wiring works.
        snap = tracker.snapshot()
        assert snap["brave"]["consecutive_failures"] == 1
        assert snap["brave"]["window_size"] == 1
        assert snap["brave"]["tripped"] is False

    async def test_module_success_is_recorded(self):
        """A successful call records a window entry and zero consecutive fails."""
        results = _search_results()
        module = MagicMock()
        module.name = "brave"
        module.search = AsyncMock(return_value=_module_output("brave", results))

        tracker = HealthTracker()
        orch = _make_orchestrator(modules=[module])
        orch._health = tracker
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
        ])

        await orch.search(QUERY)

        snap = tracker.snapshot()
        assert snap["brave"]["consecutive_failures"] == 0
        assert snap["brave"]["tripped"] is False
        assert snap["brave"]["window_size"] == 1


# ---------------------------------------------------------------------------
# Routing
# ---------------------------------------------------------------------------


def _routed_module(name: str, result_type: str = "general") -> MagicMock:
    """Build a mock module with a properly typed manifest."""
    from diting.modules.manifest import ModuleManifest

    manifest = ModuleManifest(
        domains=["general"],
        languages=["en"],
        cost_tier="free",
        latency_tier="fast",
        result_type=result_type,
        scope=f"Scope for {name}",
    )
    mod = MagicMock()
    mod.name = name
    mod.manifest = manifest
    mod.search = AsyncMock(return_value=_module_output(name))
    return mod


class TestLLMRouting:
    """Query generation now returns module selection alongside the query."""

    async def test_llm_selects_modules(self):
        """When LLM returns valid modules, only those are called."""
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")
        github = _routed_module("github", "code")

        orch = _make_orchestrator(modules=[bing, arxiv, github])
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "quantum computing", "modules": ["bing", "arxiv"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        await orch.search("quantum computing papers")

        # bing and arxiv should be called, github should NOT
        bing.search.assert_called_once()
        arxiv.search.assert_called_once()
        github.search.assert_not_called()

    async def test_llm_invalid_modules_falls_back(self):
        """When LLM returns invalid module names, all modules are called."""
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")

        orch = _make_orchestrator(modules=[bing, arxiv])
        # Disable embedding router to test pure fallback path
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test query", "modules": ["nonexistent_module"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        await orch.search("test query")

        # Both should be called (fallback to all)
        bing.search.assert_called_once()
        arxiv.search.assert_called_once()

    async def test_llm_no_modules_field_falls_back(self):
        """When LLM omits modules field, all modules are called."""
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")

        orch = _make_orchestrator(modules=[bing, arxiv])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test query"},  # no modules field
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        await orch.search("test query")

        bing.search.assert_called_once()
        arxiv.search.assert_called_once()


class TestModuleCatalog:
    """Module catalog formatting for the LLM prompt."""

    def test_catalog_includes_all_modules(self):
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")

        catalog = Orchestrator._build_module_catalog([bing, arxiv])

        assert "bing" in catalog
        assert "arxiv" in catalog
        assert "general" in catalog
        assert "papers" in catalog

    def test_catalog_handles_no_manifest(self):
        mod = MagicMock()
        mod.name = "custom"
        mod.manifest = None

        catalog = Orchestrator._build_module_catalog([mod])

        assert "custom" in catalog
        assert "unknown" in catalog

    def test_empty_modules_returns_empty(self):
        assert Orchestrator._build_module_catalog([]) == ""


class TestParseModuleSelection:
    """_parse_module_selection validates LLM output against known modules."""

    def test_valid_modules_returned(self):
        orch = _make_orchestrator(modules=[
            _routed_module("bing"), _routed_module("arxiv"),
        ])
        result = orch._parse_module_selection(["bing", "arxiv"])
        assert result == ["bing", "arxiv"]

    def test_unknown_modules_filtered(self):
        orch = _make_orchestrator(modules=[_routed_module("bing")])
        result = orch._parse_module_selection(["bing", "nonexistent"])
        assert result == ["bing"]

    def test_empty_list_returns_none(self):
        orch = _make_orchestrator(modules=[_routed_module("bing")])
        assert orch._parse_module_selection([]) is None

    def test_non_list_returns_none(self):
        orch = _make_orchestrator(modules=[_routed_module("bing")])
        assert orch._parse_module_selection("bing") is None

    def test_deduplicates(self):
        orch = _make_orchestrator(modules=[_routed_module("bing")])
        result = orch._parse_module_selection(["bing", "bing", "BING"])
        assert result == ["bing"]


class TestProgressiveRouting:
    """Round 2 uses evaluator's next_modules for selective module invocation."""

    async def test_round2_uses_evaluator_next_modules(self):
        """Evaluator says 'add arxiv', so Round 2 only invokes arxiv (not github)."""
        results_r1 = _search_results(3, "https://round1.com")
        results_r2 = _search_results(2, "https://round2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),   # round 1
            _module_output("bing", results_r2),   # round 2
        ])
        arxiv = _routed_module("arxiv", "papers")
        arxiv.search = AsyncMock(side_effect=[
            _module_output("arxiv", results_r1),  # round 1
            _module_output("arxiv", results_r2),  # round 2
        ])
        github = _routed_module("github", "code")
        github.search = AsyncMock(return_value=_module_output("github"))

        orch = _make_orchestrator(modules=[bing, arxiv, github])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            # Round 1: LLM routes to all three
            {"query": "transformer architecture", "modules": ["bing", "arxiv", "github"]},
            _scored_results(results_r1),          # round 1 scoring
            # Round 1 eval: not sufficient, next round should use bing + arxiv only
            {
                "sufficient": False,
                "reason": "Need more academic depth",
                "next_query": "transformer attention mechanism paper",
                "next_modules": ["bing", "arxiv"],
            },
            _scored_results(results_r2),          # round 2 scoring
            _sufficient_eval(),                   # round 2 eval
        ])

        response = await orch.search("transformer architecture")

        assert response.status == "success"
        assert response.metadata.rounds == 2
        # bing: called in both rounds
        assert bing.search.call_count == 2
        # arxiv: called in both rounds
        assert arxiv.search.call_count == 2
        # github: called in round 1 only (excluded by evaluator in round 2)
        assert github.search.call_count == 1

    async def test_invalid_next_modules_falls_back_to_all(self):
        """When evaluator returns unknown module names, all modules are invoked."""
        results_r1 = _search_results(3, "https://round1.com")
        results_r2 = _search_results(2, "https://round2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])
        arxiv = _routed_module("arxiv", "papers")
        arxiv.search = AsyncMock(side_effect=[
            _module_output("arxiv", results_r1),
            _module_output("arxiv", results_r2),
        ])

        orch = _make_orchestrator(modules=[bing, arxiv])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test query", "modules": ["bing", "arxiv"]},
            _scored_results(results_r1),
            {
                "sufficient": False,
                "reason": "Need more",
                "next_query": "deeper query",
                "next_modules": ["nonexistent_module"],  # invalid
            },
            _scored_results(results_r2),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        # Both called in both rounds (fallback to all when names invalid)
        assert bing.search.call_count == 2
        assert arxiv.search.call_count == 2


class TestCostGating:
    """Expensive modules are gated from Round 1 and conditionally allowed later."""

    async def test_expensive_module_excluded_from_round1(self):
        """cost_tier=expensive modules are never invoked in Round 1."""
        bing = _routed_module("bing", "general")
        serp = _routed_module("serp", "general")
        # Mark serp as expensive.
        from diting.modules.manifest import ModuleManifest
        serp.manifest = ModuleManifest(
            domains=["general"],
            languages=["en"],
            cost_tier="expensive",
            latency_tier="fast",
            result_type="general",
            scope="Expensive Google-grade search",
        )

        orch = _make_orchestrator(modules=[bing, serp])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            # LLM tries to route to both, but cost gate should block serp
            {"query": "test", "modules": ["bing", "serp"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        await orch.search("test query")

        bing.search.assert_called_once()
        serp.search.assert_not_called()

    async def test_expensive_allowed_when_evaluator_requests(self):
        """Evaluator explicitly requesting an expensive module opens the gate."""
        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])

        from diting.modules.manifest import ModuleManifest
        serp = _routed_module("serp", "general")
        serp.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="expensive", latency_tier="fast",
            result_type="general", scope="Expensive search",
        )
        serp.search = AsyncMock(return_value=_module_output("serp", results_r2))

        orch = _make_orchestrator(modules=[bing, serp])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["bing"]},
            _scored_results(results_r1, score=0.8),    # high score
            {
                "sufficient": False,
                "reason": "Need Google-grade results",
                "next_query": "deeper query",
                "next_modules": ["bing", "serp"],  # evaluator explicitly requests serp
            },
            _scored_results(results_r2),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        assert bing.search.call_count == 2
        # serp allowed in Round 2 because evaluator requested it
        assert serp.search.call_count == 1

    async def test_expensive_allowed_when_avg_score_low(self):
        """Low previous-round avg score opens the gate for expensive modules."""
        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])

        from diting.modules.manifest import ModuleManifest
        serp = _routed_module("serp", "general")
        serp.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="expensive", latency_tier="fast",
            result_type="general", scope="Expensive search",
        )
        serp.search = AsyncMock(return_value=_module_output("serp", results_r2))

        orch = _make_orchestrator(modules=[bing, serp])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["bing"]},
            _scored_results(results_r1, score=0.3),    # LOW score → opens gate
            {
                "sufficient": False,
                "reason": "Results are poor",
                "next_query": "better query",
                "next_modules": [],  # evaluator does NOT request serp
            },
            _scored_results(results_r2),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        assert bing.search.call_count == 2
        # serp allowed in Round 2 because avg score (0.3) < threshold (0.5)
        assert serp.search.call_count == 1

    async def test_expensive_gated_round2_when_score_high(self):
        """High avg score + no evaluator request keeps expensive modules gated."""
        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])

        from diting.modules.manifest import ModuleManifest
        serp = _routed_module("serp", "general")
        serp.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="expensive", latency_tier="fast",
            result_type="general", scope="Expensive search",
        )
        serp.search = AsyncMock(return_value=_module_output("serp"))

        orch = _make_orchestrator(modules=[bing, serp])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["bing"]},
            _scored_results(results_r1, score=0.8),    # HIGH score
            {
                "sufficient": False,
                "reason": "Need slightly more",
                "next_query": "refined query",
                "next_modules": [],  # evaluator does NOT request serp
            },
            _scored_results(results_r2),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        assert bing.search.call_count == 2
        # serp NOT called: high score + no explicit request
        serp.search.assert_not_called()


class TestSafetyNet:
    """Router safety nets prevent zero-result scenarios."""

    async def test_empty_cost_gate_falls_back(self):
        """If cost gate would exclude ALL modules, it returns the original set."""
        from diting.modules.manifest import ModuleManifest

        # All modules are expensive — cost gate Round 1 would remove them all.
        exa = _routed_module("exa", "general")
        exa.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="expensive", latency_tier="fast",
            result_type="general", scope="Semantic search",
        )
        tavily = _routed_module("tavily", "general")
        tavily.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="expensive", latency_tier="fast",
            result_type="general", scope="AI-powered search",
        )

        orch = _make_orchestrator(modules=[exa, tavily])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["exa", "tavily"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.status == "success"
        # Both modules should still be called because the empty-gate guard fires.
        assert exa.search.call_count == 1
        assert tavily.search.call_count == 1

    async def test_zero_results_retries_with_all_modules(self):
        """When routed modules return zero results, retry with all modules."""
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")

        # Round 1: LLM routes only to arxiv, which returns nothing.
        arxiv.search = AsyncMock(return_value=ModuleOutput(module="arxiv", results=[]))
        # On the retry with all modules, bing returns results.
        bing.search = AsyncMock(return_value=_module_output("bing"))

        orch = _make_orchestrator(modules=[bing, arxiv])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["arxiv"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.status == "success"
        # arxiv called once (routed), bing called once (retry with all).
        assert arxiv.search.call_count >= 1
        assert bing.search.call_count >= 1

    async def test_fire_more_on_very_low_scores(self):
        """Very low avg score triggers fire-more-sources in next round."""
        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])
        arxiv = _routed_module("arxiv", "papers")
        arxiv.search = AsyncMock(return_value=_module_output("arxiv", results_r2))

        orch = _make_orchestrator(modules=[bing, arxiv])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["bing"]},
            # Score 0.2 < fire-more threshold 0.3 → should open all modules.
            _scored_results(results_r1, score=0.2),
            {
                "sufficient": False,
                "reason": "Very poor results",
                "next_query": "better query",
                "next_modules": ["bing"],  # evaluator only suggests bing
            },
            _scored_results(results_r2, score=0.7),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        # Fire-more should have overridden evaluator's suggestion of only bing.
        assert arxiv.search.call_count == 1  # arxiv was included in Round 2

    async def test_no_fire_more_when_score_acceptable(self):
        """Scores above fire-more threshold do not trigger escalation."""
        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])
        arxiv = _routed_module("arxiv", "papers")
        arxiv.search = AsyncMock(return_value=_module_output("arxiv", results_r2))

        orch = _make_orchestrator(modules=[bing, arxiv])
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["bing"]},
            # Score 0.5 > fire-more threshold 0.3 → no fire-more.
            _scored_results(results_r1, score=0.5),
            {
                "sufficient": False,
                "reason": "Need slightly more",
                "next_query": "more query",
                "next_modules": ["bing"],  # evaluator only suggests bing
            },
            _scored_results(results_r2, score=0.7),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        # arxiv NOT called because score was above fire-more threshold
        # and evaluator didn't request it.
        arxiv.search.assert_not_called()


class TestRoutingStrategyPresets:
    """ROUTING_STRATEGY controls how the orchestrator selects modules."""

    async def test_fire_all_invokes_all_modules(self):
        """fire_all: all modules called regardless of LLM routing suggestion."""
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")
        github = _routed_module("github", "code")

        orch = _make_orchestrator(
            modules=[bing, arxiv, github], routing_strategy="fire_all",
        )
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            # LLM suggests only bing — should be ignored by fire_all.
            {"query": "python frameworks", "modules": ["bing"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.status == "success"
        bing.search.assert_called_once()
        arxiv.search.assert_called_once()
        github.search.assert_called_once()

    async def test_fire_all_ignores_evaluator_module_selection(self):
        """fire_all: Round 2 still uses all modules despite evaluator suggestion."""
        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])
        arxiv = _routed_module("arxiv", "papers")
        arxiv.search = AsyncMock(side_effect=[
            _module_output("arxiv", results_r1),
            _module_output("arxiv", results_r2),
        ])

        orch = _make_orchestrator(
            modules=[bing, arxiv], routing_strategy="fire_all",
        )
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test"},
            _scored_results(results_r1),
            {
                "sufficient": False,
                "reason": "Need more",
                "next_query": "deeper",
                "next_modules": ["bing"],  # evaluator only suggests bing
            },
            _scored_results(results_r2),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        # Both modules called in both rounds.
        assert bing.search.call_count == 2
        assert arxiv.search.call_count == 2

    async def test_fire_all_skips_cost_gate(self):
        """fire_all: expensive modules are NOT gated in Round 1."""
        from diting.modules.manifest import ModuleManifest

        bing = _routed_module("bing", "general")
        serp = _routed_module("serp", "general")
        serp.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="expensive", latency_tier="fast",
            result_type="general", scope="Expensive search",
        )

        orch = _make_orchestrator(
            modules=[bing, serp], routing_strategy="fire_all",
        )
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test"},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.status == "success"
        bing.search.assert_called_once()
        serp.search.assert_called_once()  # NOT gated

    async def test_cheap_first_excludes_nonfree_round1(self):
        """cheap_first: non-free modules are excluded from Round 1."""
        from diting.modules.manifest import ModuleManifest

        bing = _routed_module("bing", "general")  # cost_tier=free
        brave = _routed_module("brave", "general")
        brave.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="cheap", latency_tier="fast",
            result_type="general", scope="API-key search",
        )

        orch = _make_orchestrator(
            modules=[bing, brave], routing_strategy="cheap_first",
        )
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test"},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.status == "success"
        bing.search.assert_called_once()
        brave.search.assert_not_called()  # non-free, gated in Round 1

    async def test_cheap_first_allows_nonfree_round2(self):
        """cheap_first: non-free modules allowed in Round 2 when evaluator requests."""
        from diting.modules.manifest import ModuleManifest

        results_r1 = _search_results(3, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        bing = _routed_module("bing", "general")
        bing.search = AsyncMock(side_effect=[
            _module_output("bing", results_r1),
            _module_output("bing", results_r2),
        ])

        brave = _routed_module("brave", "general")
        brave.manifest = ModuleManifest(
            domains=["general"], languages=["en"],
            cost_tier="cheap", latency_tier="fast",
            result_type="general", scope="API-key search",
        )
        brave.search = AsyncMock(return_value=_module_output("brave", results_r2))

        orch = _make_orchestrator(
            modules=[bing, brave], routing_strategy="cheap_first",
        )
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test"},
            _scored_results(results_r1, score=0.8),
            {
                "sufficient": False,
                "reason": "Need more diversity",
                "next_query": "broader query",
                "next_modules": ["bing", "brave"],  # evaluator requests brave
            },
            _scored_results(results_r2),
            _sufficient_eval(),
        ])

        response = await orch.search("test query")

        assert response.metadata.rounds == 2
        assert bing.search.call_count == 2
        assert brave.search.call_count == 1  # allowed in Round 2

    async def test_funnel_is_default(self):
        """Default strategy is funnel — LLM routing is applied."""
        bing = _routed_module("bing", "general")
        arxiv = _routed_module("arxiv", "papers")

        orch = _make_orchestrator(modules=[bing, arxiv])
        assert orch._routing_strategy == "funnel"
        orch._embedding_router = None
        orch._llm.chat_json = AsyncMock(side_effect=[
            {"query": "test", "modules": ["bing"]},
            _scored_results(_search_results()),
            _sufficient_eval(),
        ])

        await orch.search("test query")

        # LLM routing honoured: only bing called.
        bing.search.assert_called_once()
        arxiv.search.assert_not_called()
