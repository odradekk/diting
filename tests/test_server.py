"""Tests for server.py — FastMCP tool registration and invocation."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import pytest

from diting.fetch.tavily import FetchError
from diting.models import Category, SearchMetadata, SearchResponse, Source


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@pytest.fixture(autouse=True)
def _env(monkeypatch):
    """Set required env vars so Settings() succeeds without a .env file."""
    monkeypatch.setenv("LLM_BASE_URL", "http://localhost:8080/v1")
    monkeypatch.setenv("LLM_MODEL", "test-model")
    monkeypatch.setenv("LLM_API_KEY", "sk-test")
    monkeypatch.setenv("TAVILY_API_KEY", "tvly-test")


def _make_search_response(query: str = "test") -> SearchResponse:
    return SearchResponse(
        status="success",
        summary="Test summary",
        categories=[
            Category(
                name="General",
                sources=[
                    Source(
                        title="Example",
                        url="https://example.com",
                        normalized_url="https://example.com",
                        snippet="Example snippet",
                        score=0.9,
                        source_module="brave",
                        domain="example.com",
                    )
                ],
            )
        ],
        metadata=SearchMetadata(
            request_id="abc123",
            query=query,
            rounds=1,
            total_sources_found=1,
            sources_after_dedup=1,
            sources_after_filter=1,
            elapsed_ms=500,
        ),
        warnings=[],
        errors=[],
    )


# ---------------------------------------------------------------------------
# Tool registration
# ---------------------------------------------------------------------------


class TestToolRegistration:
    """The MCP server exposes the expected tools."""

    async def test_server_has_search_and_fetch_tools(self):
        from fastmcp import Client
        from server import mcp

        async with Client(mcp) as client:
            tools = await client.list_tools()

        names = {t.name for t in tools}
        assert "search" in names
        assert "fetch" in names

    async def test_search_tool_has_query_param(self):
        from fastmcp import Client
        from server import mcp

        async with Client(mcp) as client:
            tools = await client.list_tools()

        search_tool = next(t for t in tools if t.name == "search")
        assert "query" in search_tool.inputSchema["properties"]

    async def test_fetch_tool_has_url_param(self):
        from fastmcp import Client
        from server import mcp

        async with Client(mcp) as client:
            tools = await client.list_tools()

        fetch_tool = next(t for t in tools if t.name == "fetch")
        assert "url" in fetch_tool.inputSchema["properties"]


# ---------------------------------------------------------------------------
# search tool
# ---------------------------------------------------------------------------


class TestSearchTool:
    """The search tool delegates to the orchestrator and returns structured data."""

    async def test_search_returns_structured_response(self):
        from fastmcp import Client
        from server import mcp
        from diting.pipeline.orchestrator import Orchestrator

        mock_resp = _make_search_response("python frameworks")
        with patch.object(
            Orchestrator, "search", new_callable=AsyncMock, return_value=mock_resp
        ):
            async with Client(mcp) as client:
                result = await client.call_tool(
                    "search", {"query": "python frameworks"}
                )

        # FastMCP returns structured content for Pydantic models.
        assert result is not None

    async def test_search_passes_query_to_orchestrator(self):
        from fastmcp import Client
        from server import mcp
        from diting.pipeline.orchestrator import Orchestrator

        mock_resp = _make_search_response("specific query")
        with patch.object(
            Orchestrator, "search", new_callable=AsyncMock, return_value=mock_resp
        ) as mock_search:
            async with Client(mcp) as client:
                await client.call_tool("search", {"query": "specific query"})

        mock_search.assert_called_once_with("specific query")


# ---------------------------------------------------------------------------
# fetch tool
# ---------------------------------------------------------------------------


class TestFetchTool:
    """The fetch tool delegates to the CompositeFetcher."""

    async def test_fetch_returns_content(self):
        from fastmcp import Client
        from server import mcp
        from diting.fetch.composite import CompositeFetcher

        with patch.object(
            CompositeFetcher,
            "fetch",
            new_callable=AsyncMock,
            return_value="Page content here",
        ):
            async with Client(mcp) as client:
                result = await client.call_tool(
                    "fetch", {"url": "https://example.com"}
                )

        # TextContent from string return.
        assert any("Page content here" in c.text for c in result.content)

    async def test_fetch_passes_url_to_fetcher(self):
        from fastmcp import Client
        from server import mcp
        from diting.fetch.composite import CompositeFetcher

        with patch.object(
            CompositeFetcher,
            "fetch",
            new_callable=AsyncMock,
            return_value="content",
        ) as mock_fetch:
            async with Client(mcp) as client:
                await client.call_tool(
                    "fetch", {"url": "https://example.com/page"}
                )

        mock_fetch.assert_called_once_with("https://example.com/page")

    async def test_fetch_error_returns_error_string(self):
        from fastmcp import Client
        from server import mcp
        from diting.fetch.composite import CompositeFetcher

        with patch.object(
            CompositeFetcher,
            "fetch",
            new_callable=AsyncMock,
            side_effect=FetchError("Timeout fetching https://bad.com"),
        ):
            async with Client(mcp) as client:
                result = await client.call_tool(
                    "fetch",
                    {"url": "https://bad.com"},
                    raise_on_error=False,
                )

        assert any("Timeout" in c.text for c in result.content)
