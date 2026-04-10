"""Tests for diting.modules.arxiv -- ArxivSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import httpx

from diting.models import SearchResult
from diting.modules.arxiv import ArxivSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

ARXIV_URL = "https://export.arxiv.org/api/query"


def _make_feed(*entries: str) -> str:
    """Wrap entry XML fragments into a valid Atom feed."""
    body = "\n".join(entries)
    return (
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<feed xmlns="http://www.w3.org/2005/Atom">\n'
        f"{body}\n"
        "</feed>"
    )


def _make_entry(
    arxiv_id: str = "http://arxiv.org/abs/2301.12345v1",
    title: str = "A Study of Large Language Models",
    summary: str = "We present a comprehensive study of LLMs...",
) -> str:
    """Build a single Atom <entry> XML fragment."""
    return (
        "<entry>"
        f"<id>{arxiv_id}</id>"
        f"<title>{title}</title>"
        f"<summary>{summary}</summary>"
        "</entry>"
    )


def _make_response(xml_text: str, status_code: int = 200) -> httpx.Response:
    """Build a mock httpx.Response with XML text body."""
    return httpx.Response(
        status_code=status_code,
        text=xml_text,
        request=httpx.Request("GET", ARXIV_URL),
    )


# ---------------------------------------------------------------------------
# Basic search
# ---------------------------------------------------------------------------


class TestBasicSearch:
    """Successful API response returns parsed SearchResult objects."""

    async def test_basic_search(self) -> None:
        module = ArxivSearchModule()
        xml = _make_feed(
            _make_entry(
                arxiv_id="http://arxiv.org/abs/2301.12345v1",
                title="A Study of Large Language Models",
                summary="We present a comprehensive study of LLMs...",
            ),
            _make_entry(
                arxiv_id="http://arxiv.org/abs/2302.67890v2",
                title="Attention Is All You Need",
                summary="We propose a new architecture based solely on attention...",
            ),
        )
        mock_response = _make_response(xml)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("large language models")

        assert len(results) == 2
        assert all(isinstance(r, SearchResult) for r in results)

        assert results[0].title == "A Study of Large Language Models"
        assert results[0].url == "http://arxiv.org/abs/2301.12345v1"
        assert results[0].snippet == "We present a comprehensive study of LLMs..."

        assert results[1].title == "Attention Is All You Need"
        assert results[1].url == "http://arxiv.org/abs/2302.67890v2"
        assert results[1].snippet == "We propose a new architecture based solely on attention..."

        await module.close()


# ---------------------------------------------------------------------------
# Title whitespace collapsing
# ---------------------------------------------------------------------------


class TestTitleWhitespace:
    """Titles with newlines and extra spaces are collapsed to single spaces."""

    async def test_title_whitespace_collapsed(self) -> None:
        module = ArxivSearchModule()
        xml = _make_feed(
            _make_entry(
                title="A Study\n  of   Large\nLanguage Models",
            ),
        )
        mock_response = _make_response(xml)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("test")

        assert len(results) == 1
        assert results[0].title == "A Study of Large Language Models"
        await module.close()


# ---------------------------------------------------------------------------
# Empty feed
# ---------------------------------------------------------------------------


class TestEmptyFeed:
    """Empty Atom feed returns an empty list."""

    async def test_empty_feed(self) -> None:
        module = ArxivSearchModule()
        xml = _make_feed()  # no entries
        mock_response = _make_response(xml)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("nonexistent topic")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# max_results limiting
# ---------------------------------------------------------------------------


class TestMaxResults:
    """Output is capped at max_results even if more entries are returned."""

    async def test_max_results_respected(self) -> None:
        module = ArxivSearchModule(max_results=5)

        entries = [
            _make_entry(
                arxiv_id=f"http://arxiv.org/abs/2301.{i:05d}v1",
                title=f"Paper {i}",
                summary=f"Summary of paper {i}.",
            )
            for i in range(20)
        ]
        xml = _make_feed(*entries)
        mock_response = _make_response(xml)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("papers")

        assert len(results) == 5
        await module.close()


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    """close() delegates to the underlying httpx client."""

    async def test_close(self) -> None:
        module = ArxivSearchModule()

        with patch.object(module._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await module.close()

        mock_aclose.assert_awaited_once()
