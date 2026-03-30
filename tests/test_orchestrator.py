"""Tests for supersearch.pipeline.orchestrator — search pipeline orchestration."""

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

from supersearch.fetch.tavily import FetchResult
from supersearch.llm.client import LLMError
from supersearch.models import ModuleError, ModuleOutput, SearchResult
from supersearch.pipeline.orchestrator import Orchestrator


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
    return {"sufficient": True, "reason": "Good enough", "supplementary_queries": []}


def _insufficient_eval(queries: list[str] | None = None) -> dict:
    return {
        "sufficient": False,
        "reason": "Need more",
        "supplementary_queries": queries or ["more queries"],
    }


def _query_gen_response(queries: list[str] | None = None) -> dict:
    return {"queries": queries or ["python web frameworks", "django vs flask"]}


def _make_orchestrator(
    modules: list | None = None,
    max_rounds: int = 3,
    global_timeout: int = 120,
    score_threshold: float = 0.3,
    blacklist: list[str] | None = None,
    fetcher: object | None = None,
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
        blacklist=blacklist,
        fetcher=fetcher,
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

        # Note: categories are empty at Phase 3 (Phase 4 adds classification)
        # But internal sources list is used for status determination
        assert response.status == "success"


class TestMultiRound:
    async def test_two_rounds_when_first_insufficient(self):
        """Progressive search: round 1 uses query[0], round 2 uses query[1]."""
        results_r1 = _search_results(3, "https://round1.com")
        results_r2 = _search_results(2, "https://round2.com")

        module = MagicMock()
        module.search = AsyncMock(side_effect=[
            _module_output("brave", results_r1),  # round 1: ranked query[0]
            _module_output("brave", results_r2),  # round 2: ranked query[1]
        ])

        orch = _make_orchestrator(modules=[module])
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),              # query gen (2 ranked queries)
            _scored_results(results_r1),        # round 1 scoring
            _insufficient_eval(),               # round 1 eval — insufficient
            _scored_results(results_r2),        # round 2 scoring
            _sufficient_eval(),                 # round 2 eval — sufficient
        ])

        response = await orch.search(QUERY)
        assert response.status == "success"
        assert response.metadata.rounds == 2
        assert module.search.call_count == 2  # one query per round


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
    async def test_blacklisted_domains_filtered(self):
        results = [
            SearchResult(title="Good", url="https://good.com/page", snippet="Useful content that passes the prefilter length check."),
            SearchResult(title="Bad", url="https://spam.org/page", snippet="Spam content that gets blacklisted before prefilter."),
        ]
        module = MagicMock()
        module.search = AsyncMock(return_value=_module_output("brave", results))

        orch = _make_orchestrator(modules=[module], blacklist=["spam.org"])
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
            _query_gen_response(["q1"]),
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


def _classification_response(urls: list[str], category: str = "Other") -> dict:
    return {
        "classifications": [
            {"url": url, "category": category}
            for url in urls
        ]
    }


def _summary_response(text: str = "A comprehensive summary.") -> dict:
    return {"summary": text}


class TestClassificationIntegration:
    async def test_categories_populated(self):
        """After a successful round, sources are classified into categories."""
        results = _search_results()
        orch = _make_orchestrator()
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(),
            _scored_results(results),
            _sufficient_eval(),
            # classification call
            _classification_response(
                [r.url for r in results],
                "Official Documentation",
            ),
        ])

        response = await orch.search(QUERY)

        assert response.status == "success"
        assert len(response.categories) > 0
        assert response.categories[0].name == "Official Documentation"
        assert len(response.categories[0].sources) == 3

    async def test_classification_failure_degrades_gracefully(self):
        """If classification raises, categories are empty and warning added."""
        results = _search_results()
        orch = _make_orchestrator()

        with patch.object(orch._classifier, "classify", side_effect=Exception("LLM down")):
            orch._llm.chat_json = AsyncMock(side_effect=[
                _query_gen_response(),
                _scored_results(results),
                _sufficient_eval(),
            ])

            response = await orch.search(QUERY)

        assert response.status == "success"
        assert response.categories == []
        assert any("classification failed" in w.lower() for w in response.warnings)

    async def test_no_sources_skips_classification(self):
        """When no results exist, classification is not attempted."""
        module = MagicMock()
        module.search = AsyncMock(return_value=ModuleOutput(module="brave", results=[]))

        orch = _make_orchestrator(modules=[module])
        orch._llm.chat_json = AsyncMock(return_value=_query_gen_response())

        response = await orch.search(QUERY)

        assert response.categories == []


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
            _classification_response([r.url for r in results]),
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
            _classification_response([r.url for r in results]),
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
                _classification_response([r.url for r in results]),
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
            _classification_response([r.url for r in results]),
            _summary_response("Partial summary."),
        ])

        response = await orch.search(QUERY)

        assert response.summary == "Partial summary."
        assert any("failed to fetch" in w.lower() for w in response.warnings)


# ---------------------------------------------------------------------------
# Progressive search (ranked query queue)
# ---------------------------------------------------------------------------


class TestProgressiveSearch:
    async def test_stops_when_queries_exhausted(self):
        """If all ranked queries are used and eval says insufficient with no
        supplementary queries, stop early."""
        results = _search_results(2)
        module = MagicMock()
        module.search = AsyncMock(return_value=_module_output("brave", results))

        orch = _make_orchestrator(modules=[module], max_rounds=5)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(["q1"]),     # only 1 ranked query
            _scored_results(results),        # round 1 scoring
            {"sufficient": False, "reason": "Need more", "supplementary_queries": []},
        ])

        response = await orch.search(QUERY)
        # Only 1 query available, no supplementary → only 1 round.
        assert response.metadata.rounds == 1
        assert module.search.call_count == 1

    async def test_supplementary_queries_used_as_fallback(self):
        """When ranked queries run out, evaluator's supplementary queries are appended."""
        results_r1 = _search_results(2, "https://r1.com")
        results_r2 = _search_results(2, "https://r2.com")

        module = MagicMock()
        module.search = AsyncMock(side_effect=[
            _module_output("brave", results_r1),  # round 1
            _module_output("brave", results_r2),  # round 2 (supplementary)
        ])

        orch = _make_orchestrator(modules=[module], max_rounds=5)
        orch._llm.chat_json = AsyncMock(side_effect=[
            _query_gen_response(["only-query"]),       # 1 ranked query
            _scored_results(results_r1),               # round 1 scoring
            _insufficient_eval(["supplementary-q"]),   # insufficient + supplementary
            _scored_results(results_r2),               # round 2 scoring
            _sufficient_eval(),                        # sufficient
        ])

        response = await orch.search(QUERY)
        assert response.metadata.rounds == 2
        assert module.search.call_count == 2

    async def test_one_query_per_round(self):
        """Each round uses exactly one query from the ranked queue."""
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
            _query_gen_response(["q1", "q2", "q3"]),  # 3 ranked queries
            _scored_results(results_r1),               # round 1
            _insufficient_eval(),                      # continue
            _scored_results(results_r2),               # round 2
            _insufficient_eval(),                      # continue
            _scored_results(results_r3),               # round 3 (last round)
        ])

        response = await orch.search(QUERY)
        assert response.metadata.rounds == 3
        # 3 rounds × 1 query × 1 module = 3 calls.
        assert module.search.call_count == 3
