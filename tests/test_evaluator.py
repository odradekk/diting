"""Tests for diting.pipeline.evaluator — LLM-based quality evaluation."""

from unittest.mock import AsyncMock, MagicMock

from diting.llm.client import LLMError
from diting.models import ScoredResult, SearchResult
from diting.pipeline.evaluator import EvaluationResult, Evaluator


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

QUERY = "best python web frameworks"

SCORED = [
    ScoredResult(url="https://a.com/1", relevance=0.9, quality=0.8, final_score=0.86, reason="good"),
    ScoredResult(url="https://b.com/2", relevance=0.7, quality=0.6, final_score=0.66, reason="ok"),
    ScoredResult(url="https://c.com/3", relevance=0.95, quality=0.9, final_score=0.93, reason="great"),
]

ALL_RESULTS = [
    SearchResult(title="Result A", url="https://a.com/1", snippet="Snippet A"),
    SearchResult(title="Result B", url="https://b.com/2", snippet="Snippet B"),
    SearchResult(title="Result C", url="https://c.com/3", snippet="Snippet C"),
]


def _make_evaluator(chat_json_return=None, chat_json_side_effect=None) -> Evaluator:
    llm = MagicMock()
    llm.chat_json = AsyncMock(
        return_value=chat_json_return,
        side_effect=chat_json_side_effect,
    )
    prompts = MagicMock()
    prompts.load.return_value = "You are an evaluator."
    return Evaluator(llm, prompts)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestEvaluateSufficient:
    async def test_sufficient_result(self):
        response = {
            "sufficient": True,
            "reason": "Good coverage",
            "next_query": "",
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)

        assert isinstance(result, EvaluationResult)
        assert result.sufficient is True
        assert result.reason == "Good coverage"
        assert result.next_query == ""


class TestEvaluateInsufficient:
    async def test_insufficient_with_next_query(self):
        response = {
            "sufficient": False,
            "reason": "Missing async framework coverage",
            "next_query": "python async frameworks comparison",
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)

        assert result.sufficient is False
        assert result.next_query == "python async frameworks comparison"


class TestEvaluateLLMFailure:
    async def test_llm_error_returns_sufficient(self):
        evaluator = _make_evaluator(chat_json_side_effect=LLMError("timeout"))
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)

        assert result.sufficient is True
        assert "failed" in result.reason.lower()


class TestEvaluateMalformedResponse:
    async def test_missing_fields_defaults(self):
        evaluator = _make_evaluator(chat_json_return={})
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)

        # Missing "sufficient" defaults to True
        assert result.sufficient is True
        assert result.next_query == ""

    async def test_null_next_query_treated_as_empty(self):
        response = {
            "sufficient": False,
            "reason": "Need more",
            "next_query": None,
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)
        assert result.next_query == ""

    async def test_non_string_next_query_coerced(self):
        response = {
            "sufficient": False,
            "reason": "Need more",
            "next_query": 123,
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)
        assert result.next_query == "123"

    async def test_whitespace_next_query_stripped(self):
        response = {
            "sufficient": False,
            "reason": "Need more",
            "next_query": "  targeted query  ",
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, ALL_RESULTS, 1, 3)
        assert result.next_query == "targeted query"


class TestComputeStats:
    def test_stats_with_results(self):
        stats = Evaluator._compute_stats(SCORED)
        assert stats["total_results"] == 3
        assert stats["above_0_7"] == 2  # 0.86 and 0.93
        assert stats["unique_domains"] == 3

    def test_stats_empty(self):
        stats = Evaluator._compute_stats([])
        assert stats["total_results"] == 0
        assert stats["average_score"] == 0.0


class TestBuildUserMessage:
    def test_message_format(self):
        stats = Evaluator._compute_stats(SCORED)
        msg = Evaluator._build_user_message(QUERY, stats, SCORED, ALL_RESULTS, 1, 3)
        assert "Query: best python web frameworks" in msg
        assert "Round: 1/3" in msg
        assert "Total results: 3" in msg

    def test_message_includes_current_results(self):
        stats = Evaluator._compute_stats(SCORED)
        msg = Evaluator._build_user_message(QUERY, stats, SCORED, ALL_RESULTS, 1, 3)
        assert "Current results:" in msg
        assert "Result A" in msg
        assert "https://a.com/1" in msg

    def test_message_without_results(self):
        stats = Evaluator._compute_stats([])
        msg = Evaluator._build_user_message(QUERY, stats, [], [], 1, 3)
        assert "Current results:" not in msg
