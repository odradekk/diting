"""Tests for diting.fetch.composite — CompositeFetcher with primary/fallback strategy."""

from unittest.mock import AsyncMock, MagicMock

import pytest

from diting.fetch.composite import CompositeFetcher
from diting.fetch.tavily import FetchError, FetchResult


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_fetcher(
    *,
    fetch_return: str | None = None,
    fetch_side_effect: Exception | None = None,
    fetch_many_return: list[FetchResult] | None = None,
) -> MagicMock:
    """Build a mock fetcher satisfying the Fetcher protocol."""
    mock = MagicMock()
    mock.fetch = AsyncMock(
        return_value=fetch_return,
        side_effect=fetch_side_effect,
    )
    mock.fetch_many = AsyncMock(return_value=fetch_many_return or [])
    mock.close = AsyncMock()
    return mock


def _ok(url: str, content: str = "ok") -> FetchResult:
    """Shorthand for a successful FetchResult."""
    return FetchResult(url=url, content=content, success=True)


def _fail(url: str, error: str = "failed") -> FetchResult:
    """Shorthand for a failed FetchResult."""
    return FetchResult(url=url, content="", success=False, error=error)


# ---------------------------------------------------------------------------
# fetch() -- single URL
# ---------------------------------------------------------------------------


class TestFetchPrimarySucceeds:
    """Primary fetcher succeeds; fallback must not be called."""

    @pytest.mark.asyncio
    async def test_returns_primary_content(self):
        primary = _make_fetcher(fetch_return="content")
        fallback = _make_fetcher(fetch_return="fallback content")
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        result = await composite.fetch("https://example.com")

        assert result == "content"
        primary.fetch.assert_awaited_once_with("https://example.com")
        fallback.fetch.assert_not_awaited()


class TestFetchPrimaryFailsFallbackSucceeds:
    """Primary raises FetchError; fallback returns content."""

    @pytest.mark.asyncio
    async def test_returns_fallback_content(self):
        primary = _make_fetcher(fetch_side_effect=FetchError("primary down"))
        fallback = _make_fetcher(fetch_return="fallback content")
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        result = await composite.fetch("https://example.com")

        assert result == "fallback content"
        primary.fetch.assert_awaited_once_with("https://example.com")
        fallback.fetch.assert_awaited_once_with("https://example.com")


class TestFetchBothFail:
    """Both fetchers raise FetchError; the fallback error propagates."""

    @pytest.mark.asyncio
    async def test_raises_fallback_fetch_error(self):
        primary = _make_fetcher(fetch_side_effect=FetchError("primary down"))
        fallback = _make_fetcher(fetch_side_effect=FetchError("fallback down"))
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        with pytest.raises(FetchError, match="fallback down"):
            await composite.fetch("https://example.com")


class TestFetchNonFetchErrorPropagates:
    """Non-FetchError exceptions bypass fallback and propagate directly."""

    @pytest.mark.asyncio
    async def test_runtime_error_does_not_trigger_fallback(self):
        primary = _make_fetcher(fetch_side_effect=RuntimeError("unexpected"))
        fallback = _make_fetcher(fetch_return="should not be called")
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        with pytest.raises(RuntimeError, match="unexpected"):
            await composite.fetch("https://example.com")

        fallback.fetch.assert_not_awaited()


# ---------------------------------------------------------------------------
# fetch_many() -- multiple URLs
# ---------------------------------------------------------------------------


class TestFetchManyAllPrimarySucceed:
    """All primary results succeed; fallback must not be called."""

    @pytest.mark.asyncio
    async def test_returns_all_primary_results(self):
        urls = ["https://a.com", "https://b.com", "https://c.com"]
        primary_results = [_ok(u, f"content-{i}") for i, u in enumerate(urls)]

        primary = _make_fetcher(fetch_many_return=primary_results)
        fallback = _make_fetcher()
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        results = await composite.fetch_many(urls)

        assert len(results) == 3
        assert all(r.success for r in results)
        primary.fetch_many.assert_awaited_once_with(urls)
        fallback.fetch_many.assert_not_awaited()


class TestFetchManyPartialFallback:
    """One URL fails on primary; fallback retries only that URL."""

    @pytest.mark.asyncio
    async def test_merges_fallback_into_correct_position(self):
        urls = ["https://a.com", "https://b.com", "https://c.com"]
        primary_results = [
            _ok("https://a.com", "A"),
            _fail("https://b.com"),
            _ok("https://c.com", "C"),
        ]
        fallback_results = [_ok("https://b.com", "B-fallback")]

        primary = _make_fetcher(fetch_many_return=primary_results)
        fallback = _make_fetcher(fetch_many_return=fallback_results)
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        results = await composite.fetch_many(urls)

        assert len(results) == 3
        assert all(r.success for r in results)
        assert results[0].content == "A"
        assert results[1].content == "B-fallback"
        assert results[2].content == "C"
        fallback.fetch_many.assert_awaited_once_with(["https://b.com"])


class TestFetchManyAllPrimaryFail:
    """All primary results fail; fallback returns a mix of success/failure."""

    @pytest.mark.asyncio
    async def test_merges_mixed_fallback_results(self):
        urls = ["https://a.com", "https://b.com", "https://c.com"]
        primary_results = [_fail(u) for u in urls]
        fallback_results = [
            _ok("https://a.com", "A-fb"),
            _ok("https://b.com", "B-fb"),
            _fail("https://c.com", "still broken"),
        ]

        primary = _make_fetcher(fetch_many_return=primary_results)
        fallback = _make_fetcher(fetch_many_return=fallback_results)
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        results = await composite.fetch_many(urls)

        assert len(results) == 3
        assert results[0].success is True
        assert results[0].content == "A-fb"
        assert results[1].success is True
        assert results[1].content == "B-fb"
        assert results[2].success is False
        assert results[2].error == "still broken"
        fallback.fetch_many.assert_awaited_once_with(urls)


class TestFetchManyDuplicateUrls:
    """Duplicate URLs should preserve per-occurrence fallback semantics."""

    @pytest.mark.asyncio
    async def test_duplicate_urls_get_individual_fallback_results(self):
        urls = ["https://a.com", "https://a.com"]
        primary_results = [
            _fail("https://a.com", "err1"),
            _fail("https://a.com", "err2"),
        ]
        # Fallback returns two results — one per occurrence.
        fallback_results = [
            _ok("https://a.com", "A-fb-1"),
            _ok("https://a.com", "A-fb-2"),
        ]

        primary = _make_fetcher(fetch_many_return=primary_results)
        fallback = _make_fetcher(fetch_many_return=fallback_results)
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        results = await composite.fetch_many(urls)

        # Desired: each position gets its own fallback result.
        assert len(results) == 2
        assert results[0].content == "A-fb-1"
        assert results[1].content == "A-fb-2"


# ---------------------------------------------------------------------------
# close()
# ---------------------------------------------------------------------------


class TestClose:
    """close() must await close on both inner fetchers."""

    @pytest.mark.asyncio
    async def test_closes_both_fetchers(self):
        primary = _make_fetcher()
        fallback = _make_fetcher()
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        await composite.close()

        primary.close.assert_awaited_once()
        fallback.close.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_fallback_closed_even_when_primary_close_raises(self):
        """Desired: fallback.close() is still awaited when primary.close() raises."""
        primary = _make_fetcher()
        primary.close = AsyncMock(side_effect=RuntimeError("primary close failed"))
        fallback = _make_fetcher()
        composite = CompositeFetcher(primary=primary, fallback=fallback)

        with pytest.raises(RuntimeError, match="primary close failed"):
            await composite.close()

        # Desired behavior: fallback should still be closed.
        fallback.close.assert_awaited_once()
