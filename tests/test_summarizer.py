"""Tests for diting.pipeline.summarizer — LLM-based result summarization."""

from unittest.mock import AsyncMock, MagicMock

from diting.fetch.tavily import FetchResult
from diting.llm.client import LLMError
from diting.models import Source
from diting.pipeline.summarizer import SummaryResult, Summarizer, _MAX_CONTENT_CHARS


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

QUERY = "best python web frameworks"


def _make_source(title: str, url: str, snippet: str = "snippet") -> Source:
    return Source(
        title=title,
        url=url,
        normalized_url=url.lower(),
        snippet=snippet,
        score=0.9,
        source_module="brave",
        domain=url.split("/")[2],
    )


SOURCES = [
    _make_source("Django docs", "https://djangoproject.com"),
    _make_source("Flask intro", "https://flask.palletsprojects.com"),
    _make_source("FastAPI", "https://fastapi.tiangolo.com"),
]


def _success_fetch(url: str, content: str = "Page content here.") -> FetchResult:
    return FetchResult(url=url, content=content, success=True)


def _failed_fetch(url: str, error: str = "Timeout") -> FetchResult:
    return FetchResult(url=url, content="", success=False, error=error)


def _make_summarizer(
    chat_json_return=None,
    chat_json_side_effect=None,
    fetch_many_return=None,
) -> Summarizer:
    llm = MagicMock()
    llm.chat_json = AsyncMock(
        return_value=chat_json_return,
        side_effect=chat_json_side_effect,
    )
    prompts = MagicMock()
    prompts.load.return_value = "You are a summarizer."

    fetcher = MagicMock()
    fetcher.fetch_many = AsyncMock(return_value=fetch_many_return or [])

    return Summarizer(llm, prompts, fetcher)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestSummarizeHappyPath:
    async def test_returns_summary(self):
        fetch_returns = [_success_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Django, Flask, and FastAPI are top choices."},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert isinstance(result, SummaryResult)
        assert result.summary == "Django, Flask, and FastAPI are top choices."
        assert result.warnings == []

    async def test_llm_receives_correct_prompt(self):
        fetch_returns = [_success_fetch(s.url, f"Content for {s.title}") for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "A summary."},
            fetch_many_return=fetch_returns,
        )
        await summarizer.summarize(QUERY, SOURCES)

        # Verify chat_json was called with system prompt and user message
        call_args = summarizer._llm.chat_json.call_args
        system_prompt = call_args[0][0]
        user_message = call_args[0][1]

        assert system_prompt == "You are a summarizer."
        assert QUERY in user_message
        assert "Django docs" in user_message
        assert "Content for Django docs" in user_message


class TestSummarizeEmptySources:
    async def test_returns_empty_summary(self):
        summarizer = _make_summarizer()
        result = await summarizer.summarize(QUERY, [])

        assert result.summary == ""
        assert result.warnings == []

    async def test_does_not_call_fetcher(self):
        summarizer = _make_summarizer()
        await summarizer.summarize(QUERY, [])

        summarizer._fetcher.fetch_many.assert_not_called()


class TestSummarizePartialFetchFailure:
    async def test_summarizes_successful_fetches(self):
        fetch_returns = [
            _success_fetch(SOURCES[0].url),
            _failed_fetch(SOURCES[1].url, "Timeout"),
            _success_fetch(SOURCES[2].url),
        ]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Django and FastAPI are great."},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == "Django and FastAPI are great."
        assert len(result.warnings) == 1
        assert "flask.palletsprojects.com" in result.warnings[0]
        assert "Timeout" in result.warnings[0]

    async def test_user_message_excludes_failed_sources(self):
        fetch_returns = [
            _success_fetch(SOURCES[0].url, "Django content"),
            _failed_fetch(SOURCES[1].url, "Timeout"),
            _success_fetch(SOURCES[2].url, "FastAPI content"),
        ]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Summary."},
            fetch_many_return=fetch_returns,
        )
        await summarizer.summarize(QUERY, SOURCES)

        user_message = summarizer._llm.chat_json.call_args[0][1]
        assert "Django content" in user_message
        assert "FastAPI content" in user_message
        # Flask content should not appear (it failed)
        assert "Flask intro" not in user_message


class TestSummarizeAllFetchFailure:
    async def test_returns_empty_summary(self):
        fetch_returns = [_failed_fetch(s.url, "Timeout") for s in SOURCES]
        summarizer = _make_summarizer(fetch_many_return=fetch_returns)
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == ""
        assert len(result.warnings) == 3
        for w in result.warnings:
            assert "Failed to fetch" in w

    async def test_does_not_call_llm(self):
        fetch_returns = [_failed_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(fetch_many_return=fetch_returns)
        await summarizer.summarize(QUERY, SOURCES)

        summarizer._llm.chat_json.assert_not_called()


class TestSummarizeLLMError:
    async def test_returns_empty_summary_with_warning(self):
        fetch_returns = [_success_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_side_effect=LLMError("timeout"),
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == ""
        assert len(result.warnings) == 1
        assert "LLM summarization failed" in result.warnings[0]


class TestSummarizeInvalidJSON:
    async def test_missing_summary_key(self):
        fetch_returns = [_success_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_return={"wrong_key": "some value"},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == ""
        assert len(result.warnings) == 1
        assert "missing" in result.warnings[0].lower()

    async def test_empty_summary_string(self):
        fetch_returns = [_success_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_return={"summary": ""},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == ""
        assert len(result.warnings) == 1

    async def test_whitespace_only_summary(self):
        fetch_returns = [_success_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "   \n  "},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == ""
        assert len(result.warnings) == 1

    async def test_non_string_summary_value(self):
        fetch_returns = [_success_fetch(s.url) for s in SOURCES]
        summarizer = _make_summarizer(
            chat_json_return={"summary": 42},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, SOURCES)

        assert result.summary == ""
        assert len(result.warnings) == 1


class TestSummarizeTopNLimitsSources:
    async def test_only_fetches_top_n(self):
        many_sources = [
            _make_source(f"Source {i}", f"https://example{i}.com")
            for i in range(10)
        ]
        fetch_returns = [_success_fetch(s.url) for s in many_sources[:3]]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Summary of top 3."},
            fetch_many_return=fetch_returns,
        )
        result = await summarizer.summarize(QUERY, many_sources, top_n=3)

        # fetch_many should be called with only the first 3 URLs
        call_args = summarizer._fetcher.fetch_many.call_args[0][0]
        assert len(call_args) == 3
        assert call_args[0] == "https://example0.com"
        assert call_args[2] == "https://example2.com"
        assert result.summary == "Summary of top 3."

    async def test_default_top_n_is_five(self):
        many_sources = [
            _make_source(f"Source {i}", f"https://example{i}.com")
            for i in range(10)
        ]
        fetch_returns = [_success_fetch(s.url) for s in many_sources[:5]]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Summary."},
            fetch_many_return=fetch_returns,
        )
        await summarizer.summarize(QUERY, many_sources)

        call_args = summarizer._fetcher.fetch_many.call_args[0][0]
        assert len(call_args) == 5


class TestSummarizeContentTruncation:
    async def test_long_content_is_truncated(self):
        long_content = "x" * (_MAX_CONTENT_CHARS + 5000)
        fetch_returns = [
            _success_fetch(SOURCES[0].url, long_content),
        ]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Summary of truncated content."},
            fetch_many_return=fetch_returns,
        )
        await summarizer.summarize(QUERY, SOURCES[:1])

        user_message = summarizer._llm.chat_json.call_args[0][1]
        # Content in user message should be truncated to _MAX_CONTENT_CHARS
        # The full long_content should NOT appear
        assert long_content not in user_message
        # But the truncated version should
        assert "x" * _MAX_CONTENT_CHARS in user_message

    async def test_short_content_is_not_truncated(self):
        short_content = "Short page content."
        fetch_returns = [
            _success_fetch(SOURCES[0].url, short_content),
        ]
        summarizer = _make_summarizer(
            chat_json_return={"summary": "Summary."},
            fetch_many_return=fetch_returns,
        )
        await summarizer.summarize(QUERY, SOURCES[:1])

        user_message = summarizer._llm.chat_json.call_args[0][1]
        assert short_content in user_message


class TestBuildUserMessage:
    def test_message_contains_query_and_sources(self):
        fetched = [
            (SOURCES[0], "Django page content"),
            (SOURCES[1], "Flask page content"),
        ]
        msg = Summarizer._build_user_message(QUERY, fetched)

        assert f"Query: {QUERY}" in msg
        assert "Django docs" in msg
        assert "https://djangoproject.com" in msg
        assert "Django page content" in msg
        assert "Flask intro" in msg
        assert "Flask page content" in msg

    def test_message_numbers_sources(self):
        fetched = [
            (SOURCES[0], "Content A"),
            (SOURCES[1], "Content B"),
        ]
        msg = Summarizer._build_user_message(QUERY, fetched)
        assert "1. Title: Django docs" in msg
        assert "2. Title: Flask intro" in msg


class TestParseResponse:
    def test_valid_summary(self):
        result = Summarizer._parse_response({"summary": "A good summary."}, [])
        assert result.summary == "A good summary."
        assert result.warnings == []

    def test_strips_whitespace(self):
        result = Summarizer._parse_response({"summary": "  Padded summary.  "}, [])
        assert result.summary == "Padded summary."

    def test_preserves_existing_warnings(self):
        existing = ["fetch warning"]
        result = Summarizer._parse_response(
            {"summary": "Summary."},
            existing,
        )
        assert result.warnings == ["fetch warning"]
