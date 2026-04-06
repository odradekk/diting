"""Tests for diting.fetch.composite — CompositeFetcher with primary/fallback strategy."""

from unittest.mock import AsyncMock, MagicMock

import pytest

from diting.fetch.composite import (
    CompositeFetcher,
    FetchLayer,
    LayeredFetcher,
    chain_fetchers,
)
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


# ---------------------------------------------------------------------------
# chain_fetchers() — N-layer fallback builder
# ---------------------------------------------------------------------------


class TestChainFetchers:
    """``chain_fetchers`` folds a list of layers into nested CompositeFetchers."""

    def test_empty_list_raises(self):
        with pytest.raises(ValueError, match="at least one layer"):
            chain_fetchers([])

    def test_single_layer_returned_as_is(self):
        solo = _make_fetcher(fetch_return="solo")
        chain = chain_fetchers([solo])
        # No CompositeFetcher wrapping when there is nothing to fall back to.
        assert chain is solo

    def test_two_layers_builds_layered_fetcher(self):
        a = _make_fetcher(fetch_return="A")
        b = _make_fetcher(fetch_return="B")
        chain = chain_fetchers([a, b])

        assert isinstance(chain, LayeredFetcher)
        assert [layer.fetcher for layer in chain._layers] == [a, b]

    def test_three_layers_stays_flat(self):
        a = _make_fetcher(fetch_return="A")
        b = _make_fetcher(fetch_return="B")
        c = _make_fetcher(fetch_return="C")
        chain = chain_fetchers([a, b, c])

        # Flat structure — no nested composites.
        assert isinstance(chain, LayeredFetcher)
        assert len(chain._layers) == 3
        assert [layer.fetcher for layer in chain._layers] == [a, b, c]

    def test_bare_fetchers_get_default_names(self):
        a = _make_fetcher(fetch_return="A")
        chain = chain_fetchers([a, _make_fetcher()])

        assert isinstance(chain, LayeredFetcher)
        # Default name comes from type(fetcher).__name__.
        assert chain._layers[0].name == type(a).__name__

    def test_mix_of_layers_and_bare_fetchers(self):
        a = _make_fetcher(fetch_return="A")
        b = _make_fetcher(fetch_return="B")
        chain = chain_fetchers([
            FetchLayer(fetcher=a, name="local", timeout=5.0),
            b,  # bare
        ])

        assert isinstance(chain, LayeredFetcher)
        assert chain._layers[0].name == "local"
        assert chain._layers[0].timeout == 5.0
        assert chain._layers[1].fetcher is b
        assert chain._layers[1].timeout is None

    @pytest.mark.asyncio
    async def test_chain_falls_through_to_last_layer(self):
        a = _make_fetcher(fetch_side_effect=FetchError("A down"))
        b = _make_fetcher(fetch_side_effect=FetchError("B down"))
        c = _make_fetcher(fetch_return="C content")
        chain = chain_fetchers([a, b, c])

        result = await chain.fetch("https://example.com")

        assert result == "C content"
        a.fetch.assert_awaited_once()
        b.fetch.assert_awaited_once()
        c.fetch.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_chain_stops_at_first_success(self):
        a = _make_fetcher(fetch_return="A content")
        b = _make_fetcher(fetch_return="should not be called")
        c = _make_fetcher(fetch_return="should not be called")
        chain = chain_fetchers([a, b, c])

        result = await chain.fetch("https://example.com")

        assert result == "A content"
        a.fetch.assert_awaited_once()
        b.fetch.assert_not_awaited()
        c.fetch.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_chain_close_closes_every_layer(self):
        a = _make_fetcher()
        b = _make_fetcher()
        c = _make_fetcher()
        chain = chain_fetchers([a, b, c])

        await chain.close()

        a.close.assert_awaited_once()
        b.close.assert_awaited_once()
        c.close.assert_awaited_once()


# ---------------------------------------------------------------------------
# LayeredFetcher — per-layer timeout, naming, path logging
# ---------------------------------------------------------------------------


class TestLayeredFetcherTimeout:
    """Per-layer timeouts fall through to the next layer when hit."""

    @pytest.mark.asyncio
    async def test_slow_layer_times_out_next_layer_wins(self):
        import asyncio as _asyncio

        async def hangs(_url: str) -> str:
            await _asyncio.sleep(10.0)
            return "never"

        slow = _make_fetcher()
        slow.fetch = AsyncMock(side_effect=hangs)
        fast = _make_fetcher(fetch_return="fast content")

        chain = LayeredFetcher([
            FetchLayer(fetcher=slow, name="slow", timeout=0.05),
            FetchLayer(fetcher=fast, name="fast"),
        ])

        result = await chain.fetch("https://example.com")

        assert result == "fast content"
        slow.fetch.assert_awaited_once()
        fast.fetch.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_all_layers_time_out_raises_last_timeout(self):
        import asyncio as _asyncio

        async def hangs(_url: str) -> str:
            await _asyncio.sleep(10.0)
            return "never"

        a = _make_fetcher()
        a.fetch = AsyncMock(side_effect=hangs)
        b = _make_fetcher()
        b.fetch = AsyncMock(side_effect=hangs)

        chain = LayeredFetcher([
            FetchLayer(fetcher=a, name="a", timeout=0.02),
            FetchLayer(fetcher=b, name="b", timeout=0.02),
        ])

        with pytest.raises(FetchError, match="timed out"):
            await chain.fetch("https://example.com")

    @pytest.mark.asyncio
    async def test_fetch_many_layer_timeout_falls_through(self):
        import asyncio as _asyncio

        async def hangs(_urls: list[str]) -> list[FetchResult]:
            await _asyncio.sleep(10.0)
            return []

        slow = _make_fetcher()
        slow.fetch_many = AsyncMock(side_effect=hangs)
        fast = _make_fetcher(fetch_many_return=[
            _ok("https://x.com", "x-content"),
        ])

        chain = LayeredFetcher([
            FetchLayer(fetcher=slow, name="slow", timeout=0.05),
            FetchLayer(fetcher=fast, name="fast"),
        ])

        results = await chain.fetch_many(["https://x.com"])
        assert len(results) == 1
        assert results[0].success
        assert results[0].content == "x-content"


class TestLayeredFetcherNaming:
    """Layer names appear in log output."""

    @pytest.mark.asyncio
    async def test_success_logs_served_by_name(self, caplog):
        import logging as _logging
        caplog.set_level(_logging.INFO, logger="diting.fetch.composite")

        a = _make_fetcher(fetch_side_effect=FetchError("a down"))
        b = _make_fetcher(fetch_return="b content")

        chain = LayeredFetcher([
            FetchLayer(fetcher=a, name="local"),
            FetchLayer(fetcher=b, name="jina"),
        ])

        await chain.fetch("https://example.com")

        log_text = " ".join(r.message for r in caplog.records)
        assert "served_by=jina" in log_text
        assert "path=local->jina" in log_text

    @pytest.mark.asyncio
    async def test_exhaustion_logs_path(self, caplog):
        import logging as _logging
        caplog.set_level(_logging.WARNING, logger="diting.fetch.composite")

        a = _make_fetcher(fetch_side_effect=FetchError("a down"))
        b = _make_fetcher(fetch_side_effect=FetchError("b down"))

        chain = LayeredFetcher([
            FetchLayer(fetcher=a, name="local"),
            FetchLayer(fetcher=b, name="jina"),
        ])

        with pytest.raises(FetchError):
            await chain.fetch("https://example.com")

        log_text = " ".join(r.message for r in caplog.records)
        assert "exhausted" in log_text
        assert "path=local->jina" in log_text


class TestLayeredFetcherFetchMany:
    """fetch_many narrows pending URLs layer by layer."""

    @pytest.mark.asyncio
    async def test_per_url_fallback_preserves_order(self):
        urls = ["https://a.com", "https://b.com", "https://c.com"]
        # Layer 1 resolves a, fails b+c.
        layer1_results = [
            _ok("https://a.com", "A"),
            _fail("https://b.com", "l1 b"),
            _fail("https://c.com", "l1 c"),
        ]
        # Layer 2 resolves b, fails c.
        layer2_results = [
            _ok("https://b.com", "B"),
            _fail("https://c.com", "l2 c"),
        ]
        # Layer 3 resolves c.
        layer3_results = [_ok("https://c.com", "C")]

        l1 = _make_fetcher(fetch_many_return=layer1_results)
        l2 = _make_fetcher(fetch_many_return=layer2_results)
        l3 = _make_fetcher(fetch_many_return=layer3_results)

        chain = LayeredFetcher([
            FetchLayer(fetcher=l1, name="l1"),
            FetchLayer(fetcher=l2, name="l2"),
            FetchLayer(fetcher=l3, name="l3"),
        ])

        results = await chain.fetch_many(urls)

        assert [r.content for r in results] == ["A", "B", "C"]
        # l2 was only called for b+c; l3 only for c.
        l2.fetch_many.assert_awaited_once_with(["https://b.com", "https://c.com"])
        l3.fetch_many.assert_awaited_once_with(["https://c.com"])

    @pytest.mark.asyncio
    async def test_all_layers_fail_preserves_last_error(self):
        urls = ["https://a.com"]
        l1 = _make_fetcher(fetch_many_return=[_fail("https://a.com", "first error")])
        l2 = _make_fetcher(fetch_many_return=[_fail("https://a.com", "second error")])

        chain = LayeredFetcher([
            FetchLayer(fetcher=l1, name="l1"),
            FetchLayer(fetcher=l2, name="l2"),
        ])

        results = await chain.fetch_many(urls)
        assert len(results) == 1
        assert results[0].success is False
        # The most recent failure's error is preserved.
        assert results[0].error == "second error"

    @pytest.mark.asyncio
    async def test_empty_urls_returns_empty(self):
        l1 = _make_fetcher()
        chain = LayeredFetcher([FetchLayer(fetcher=l1, name="l1")])

        results = await chain.fetch_many([])
        assert results == []
        l1.fetch_many.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_first_layer_all_succeed_short_circuits(self):
        urls = ["https://a.com", "https://b.com"]
        l1 = _make_fetcher(fetch_many_return=[
            _ok("https://a.com", "A"),
            _ok("https://b.com", "B"),
        ])
        l2 = _make_fetcher()

        chain = LayeredFetcher([
            FetchLayer(fetcher=l1, name="l1"),
            FetchLayer(fetcher=l2, name="l2"),
        ])

        await chain.fetch_many(urls)
        l2.fetch_many.assert_not_awaited()


class TestLayeredFetcherClose:
    @pytest.mark.asyncio
    async def test_closes_every_layer_even_if_some_raise(self):
        a = _make_fetcher()
        a.close = AsyncMock(side_effect=RuntimeError("a close failed"))
        b = _make_fetcher()
        c = _make_fetcher()

        chain = LayeredFetcher([
            FetchLayer(fetcher=a, name="a"),
            FetchLayer(fetcher=b, name="b"),
            FetchLayer(fetcher=c, name="c"),
        ])

        with pytest.raises(RuntimeError, match="a close failed"):
            await chain.close()

        # All layers must be closed, even after a raises.
        a.close.assert_awaited_once()
        b.close.assert_awaited_once()
        c.close.assert_awaited_once()

    def test_empty_layers_rejected(self):
        with pytest.raises(ValueError, match="at least one layer"):
            LayeredFetcher([])
