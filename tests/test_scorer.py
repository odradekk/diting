"""Tests for diting.pipeline.scorer — LLM-based result scoring."""

from unittest.mock import AsyncMock, MagicMock

from diting.llm.client import LLMError
from diting.models import ScoredResult, SearchResult
from diting.pipeline.scorer import Scorer


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

QUERY = "best python web frameworks"

RESULTS = [
    SearchResult(title="Django docs", url="https://djangoproject.com", snippet="The web framework for perfectionists"),
    SearchResult(title="Flask intro", url="https://flask.palletsprojects.com", snippet="Lightweight WSGI web application framework"),
    SearchResult(title="FastAPI", url="https://fastapi.tiangolo.com", snippet="Modern, fast web framework for APIs"),
]

GOOD_LLM_RESPONSE = {
    "scored_results": [
        {"url": "https://djangoproject.com", "relevance": 0.9, "quality": 0.85, "final_score": 0.88, "reason": "Official docs"},
        {"url": "https://flask.palletsprojects.com", "relevance": 0.8, "quality": 0.7, "final_score": 0.76, "reason": "Good tutorial"},
        {"url": "https://fastapi.tiangolo.com", "relevance": 0.95, "quality": 0.9, "final_score": 0.93, "reason": "Modern framework"},
    ]
}


def _make_scorer(chat_json_return=None, chat_json_side_effect=None) -> Scorer:
    llm = MagicMock()
    llm.chat_json = AsyncMock(
        return_value=chat_json_return,
        side_effect=chat_json_side_effect,
    )
    prompts = MagicMock()
    prompts.load.return_value = "You are a scorer."
    return Scorer(llm, prompts)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestScoreSuccess:
    async def test_returns_scored_results(self):
        scorer = _make_scorer(chat_json_return=GOOD_LLM_RESPONSE)
        scored = await scorer.score(QUERY, RESULTS)

        assert len(scored) == 3
        assert all(isinstance(s, ScoredResult) for s in scored)
        assert scored[0].url == "https://djangoproject.com"
        assert scored[0].final_score == 0.88

    async def test_preserves_all_fields(self):
        scorer = _make_scorer(chat_json_return=GOOD_LLM_RESPONSE)
        scored = await scorer.score(QUERY, RESULTS)

        fastapi = next(s for s in scored if "fastapi" in s.url)
        assert fastapi.relevance == 0.95
        assert fastapi.quality == 0.9
        assert fastapi.reason == "Modern framework"


class TestScoreEmpty:
    async def test_empty_results_returns_empty(self):
        scorer = _make_scorer()
        scored = await scorer.score(QUERY, [])
        assert scored == []


class TestScoreLLMFailure:
    async def test_llm_error_returns_empty(self):
        scorer = _make_scorer(chat_json_side_effect=LLMError("timeout"))
        scored = await scorer.score(QUERY, RESULTS)
        assert scored == []


class TestScoreMalformedResponse:
    async def test_missing_scored_results_key(self):
        scorer = _make_scorer(chat_json_return={"wrong_key": []})
        scored = await scorer.score(QUERY, RESULTS)
        assert scored == []

    async def test_non_list_scored_results(self):
        scorer = _make_scorer(chat_json_return={"scored_results": "not a list"})
        scored = await scorer.score(QUERY, RESULTS)
        assert scored == []

    async def test_partial_malformed_items(self):
        response = {
            "scored_results": [
                {"url": "https://djangoproject.com", "relevance": 0.9, "quality": 0.85, "final_score": 0.88, "reason": "Good"},
                {"url": "missing_fields"},  # missing required fields
                {"url": "https://fastapi.tiangolo.com", "relevance": 0.95, "quality": 0.9, "final_score": 0.93, "reason": "Great"},
            ]
        }
        scorer = _make_scorer(chat_json_return=response)
        scored = await scorer.score(QUERY, RESULTS)
        assert len(scored) == 2

    async def test_url_not_in_original_results(self):
        response = {
            "scored_results": [
                {"url": "https://unknown.com", "relevance": 0.5, "quality": 0.5, "final_score": 0.5, "reason": "Unknown"},
            ]
        }
        scorer = _make_scorer(chat_json_return=response)
        scored = await scorer.score(QUERY, RESULTS)
        assert scored == []


class TestScoreOutOfRange:
    async def test_score_values_clamped_by_pydantic(self):
        response = {
            "scored_results": [
                {"url": "https://djangoproject.com", "relevance": 1.5, "quality": 0.8, "final_score": 0.9, "reason": "Over"},
            ]
        }
        scorer = _make_scorer(chat_json_return=response)
        scored = await scorer.score(QUERY, RESULTS)
        # Pydantic raises ValidationError for values > 1, so item is skipped
        assert scored == []


class TestBuildUserMessage:
    def test_message_contains_query_and_results(self):
        msg = Scorer._build_user_message(QUERY, RESULTS)
        assert QUERY in msg
        assert "https://djangoproject.com" in msg
        assert "Django docs" in msg
        assert "snippet:" in msg
