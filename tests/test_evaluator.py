"""Tests for diting.pipeline.evaluator — LLM-based quality evaluation."""

from unittest.mock import AsyncMock, MagicMock

from diting.llm.client import LLMError
from diting.models import ScoredResult
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
            "supplementary_queries": [],
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, 1, 3)

        assert isinstance(result, EvaluationResult)
        assert result.sufficient is True
        assert result.reason == "Good coverage"
        assert result.supplementary_queries == []


class TestEvaluateInsufficient:
    async def test_insufficient_with_queries(self):
        response = {
            "sufficient": False,
            "reason": "Missing async framework coverage",
            "supplementary_queries": ["python async frameworks", "aiohttp vs tornado"],
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, 1, 3)

        assert result.sufficient is False
        assert len(result.supplementary_queries) == 2
        assert "async" in result.supplementary_queries[0]


class TestEvaluateLLMFailure:
    async def test_llm_error_returns_sufficient(self):
        evaluator = _make_evaluator(chat_json_side_effect=LLMError("timeout"))
        result = await evaluator.evaluate(QUERY, SCORED, 1, 3)

        assert result.sufficient is True
        assert "failed" in result.reason.lower()


class TestEvaluateMalformedResponse:
    async def test_missing_fields_defaults(self):
        evaluator = _make_evaluator(chat_json_return={})
        result = await evaluator.evaluate(QUERY, SCORED, 1, 3)

        # Missing "sufficient" defaults to True
        assert result.sufficient is True
        assert result.supplementary_queries == []

    async def test_non_list_queries_ignored(self):
        response = {
            "sufficient": False,
            "reason": "Need more",
            "supplementary_queries": "not a list",
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, 1, 3)
        assert result.supplementary_queries == []

    async def test_empty_string_queries_filtered(self):
        response = {
            "sufficient": False,
            "reason": "Need more",
            "supplementary_queries": ["valid query", "", "  ", "another valid"],
        }
        evaluator = _make_evaluator(chat_json_return=response)
        result = await evaluator.evaluate(QUERY, SCORED, 1, 3)
        assert result.supplementary_queries == ["valid query", "another valid"]


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
        msg = Evaluator._build_user_message(QUERY, stats, 1, 3)
        assert "Query: best python web frameworks" in msg
        assert "Round: 1/3" in msg
        assert "Total results: 3" in msg
