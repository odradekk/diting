"""Tests for diting.fetch.archive — Wayback + Archive.today fallback."""

from unittest.mock import AsyncMock, patch

import httpx
import pytest

from diting.fetch.archive import ArchiveFetcher, _is_archivable
from diting.fetch.tavily import FetchError


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_WAYBACK_API = "https://archive.org/wayback/available"


def _response(
    *, json_data: dict | None = None, text: str = "",
    status_code: int = 200, url: str = "https://example.com",
) -> httpx.Response:
    """Build a mock httpx.Response."""
    kwargs: dict = {
        "status_code": status_code,
        "request": httpx.Request("GET", url),
    }
    if json_data is not None:
        kwargs["json"] = json_data
    else:
        kwargs["text"] = text
    return httpx.Response(**kwargs)


def _wayback_payload(
    *, timestamp: str = "20200101000000", status: str = "200",
    available: bool = True,
) -> dict:
    """Build a Wayback availability API JSON payload."""
    return {
        "url": "example.com",
        "archived_snapshots": {
            "closest": {
                "status": status,
                "available": available,
                "url": f"http://web.archive.org/web/{timestamp}/http://example.com",
                "timestamp": timestamp,
            },
        },
    }


def _rich_article_html() -> str:
    """Return an HTML blob that trafilatura will extract cleanly (~300 chars)."""
    body = " ".join(
        f"Paragraph {i} contains meaningful content about a topic that "
        f"readers would find interesting and substantial enough." for i in range(8)
    )
    return f"""
    <!DOCTYPE html>
    <html><head><title>Example Article</title></head><body>
    <article>
      <h1>Example Article</h1>
      <p>{body}</p>
      <p>{body}</p>
    </article>
    </body></html>
    """


# ---------------------------------------------------------------------------
# URL filtering — _is_archivable
# ---------------------------------------------------------------------------


class TestIsArchivable:
    """Static-content URLs pass; search, API, and feed URLs are rejected."""

    @pytest.mark.parametrize("url", [
        "https://example.com/article/123",
        "https://blog.example.org/post/intro-to-async",
        "https://news.site.net/2024/politics/story",
        "http://www.wiki.example/page/Topic",
    ])
    def test_static_urls_pass(self, url):
        assert _is_archivable(url) is True

    @pytest.mark.parametrize("url", [
        "https://google.com/search?q=foo",
        "https://example.com/search/python",
        "https://www.baidu.com/s?wd=hello",
        "https://example.com/page?query=bar",
        "https://example.com/page?keyword=x",
    ])
    def test_search_urls_rejected(self, url):
        assert _is_archivable(url) is False

    @pytest.mark.parametrize("url", [
        "https://api.github.com/repos/foo/bar",
        "https://example.com/api/v1/users",
        "https://example.com/graphql",
    ])
    def test_api_urls_rejected(self, url):
        assert _is_archivable(url) is False

    @pytest.mark.parametrize("url", [
        "https://example.com/feed",
        "https://example.com/feed.xml",
        "https://example.com/rss",
        "https://example.com/.well-known/stuff",
    ])
    def test_feed_and_wellknown_rejected(self, url):
        assert _is_archivable(url) is False

    @pytest.mark.parametrize("url", [
        "ftp://example.com/file",
        "file:///etc/passwd",
        "",
        "not a url",
        "https://",
    ])
    def test_non_http_rejected(self, url):
        assert _is_archivable(url) is False


# ---------------------------------------------------------------------------
# fetch() — non-archivable URLs short-circuit
# ---------------------------------------------------------------------------


class TestNonArchivable:
    @pytest.mark.asyncio
    async def test_search_url_raises_without_network(self):
        fetcher = ArchiveFetcher()
        with patch.object(fetcher._http, "get", new_callable=AsyncMock) as mock_get:
            with pytest.raises(FetchError, match="non-static URL"):
                await fetcher.fetch("https://google.com/search?q=foo")
        mock_get.assert_not_called()
        await fetcher.close()


# ---------------------------------------------------------------------------
# Wayback path
# ---------------------------------------------------------------------------


class TestWaybackHit:
    @pytest.mark.asyncio
    async def test_wayback_hit_returns_extracted_markdown(self):
        fetcher = ArchiveFetcher()
        html = _rich_article_html()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(json_data=_wayback_payload())
            # Snapshot URL fetch
            assert "web.archive.org/web/" in url and "id_" in url
            return _response(text=html)

        with patch.object(fetcher._http, "get", new=fake_get):
            result = await fetcher.fetch("https://example.com/article")

        assert "Example Article" in result or "Paragraph" in result
        assert len(result) >= 200
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_snapshot_url_uses_id_suffix(self):
        """The snapshot URL must use the ``id_`` raw-content modifier."""
        fetcher = ArchiveFetcher()
        captured: dict = {}
        html = _rich_article_html()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(
                    json_data=_wayback_payload(timestamp="20230515120000"),
                )
            captured["snapshot_url"] = url
            return _response(text=html)

        with patch.object(fetcher._http, "get", new=fake_get):
            await fetcher.fetch("https://example.com/article")

        assert captured["snapshot_url"] == (
            "https://web.archive.org/web/20230515120000id_/"
            "https://example.com/article"
        )
        await fetcher.close()


class TestWaybackMiss:
    """Wayback returns no snapshot, so we fall through to archive.today."""

    @pytest.mark.asyncio
    async def test_no_archived_snapshots_falls_through(self):
        fetcher = ArchiveFetcher()
        html = _rich_article_html()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(
                    json_data={"url": "example.com", "archived_snapshots": {}},
                )
            # Archive.today attempt
            assert "archive.ph" in url
            return _response(
                text=html, url="https://archive.ph/abc12/example.com",
            )

        # Replace _http.get with fake; also stub out final URL via monkeypatching
        # Response objects so resp.url reflects the "snapshot" URL.
        with patch.object(fetcher._http, "get", new=fake_get):
            result = await fetcher.fetch("https://example.com/post")

        assert len(result) >= 200
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_non_2xx_status_falls_through(self):
        """Wayback archived a 404 copy — must be skipped."""
        fetcher = ArchiveFetcher()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(json_data=_wayback_payload(status="404"))
            # archive.today redirects to homepage (miss)
            return _response(text="home", url="https://archive.ph/")

        with patch.object(fetcher._http, "get", new=fake_get):
            with pytest.raises(FetchError, match="No archive snapshot"):
                await fetcher.fetch("https://example.com/article")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_snapshot_not_available(self):
        """Wayback returns ``available: false`` — fall through."""
        fetcher = ArchiveFetcher()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(json_data=_wayback_payload(available=False))
            return _response(text="home", url="https://archive.ph/")

        with patch.object(fetcher._http, "get", new=fake_get):
            with pytest.raises(FetchError, match="No archive snapshot"):
                await fetcher.fetch("https://example.com/article")
        await fetcher.close()


class TestWaybackNetworkErrors:
    @pytest.mark.asyncio
    async def test_lookup_timeout_falls_through(self):
        fetcher = ArchiveFetcher()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                raise httpx.TimeoutException("wayback timeout")
            return _response(text="home", url="https://archive.ph/")

        with patch.object(fetcher._http, "get", new=fake_get):
            with pytest.raises(FetchError, match="No archive snapshot"):
                await fetcher.fetch("https://example.com/article")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_lookup_http_error_mentioned_in_final_error(self):
        fetcher = ArchiveFetcher()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(
                    json_data={"error": "boom"}, status_code=503,
                )
            return _response(text="home", url="https://archive.ph/")

        with patch.object(fetcher._http, "get", new=fake_get):
            with pytest.raises(FetchError, match="wayback"):
                await fetcher.fetch("https://example.com/article")
        await fetcher.close()


class TestSnapshotFetchErrors:
    @pytest.mark.asyncio
    async def test_snapshot_http_error_falls_through_to_archive_today(self):
        fetcher = ArchiveFetcher()
        html = _rich_article_html()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(json_data=_wayback_payload())
            if "web.archive.org" in url:
                return _response(text="gone", status_code=503)
            # archive.today hit
            return _response(text=html, url="https://archive.ph/xyz/example.com")

        with patch.object(fetcher._http, "get", new=fake_get):
            result = await fetcher.fetch("https://example.com/article")
        assert len(result) >= 200
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_snapshot_thin_content_falls_through(self):
        fetcher = ArchiveFetcher()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(json_data=_wayback_payload())
            if "web.archive.org" in url:
                return _response(text="<html><body>hi</body></html>")
            return _response(text="home", url="https://archive.ph/")

        with patch.object(fetcher._http, "get", new=fake_get):
            with pytest.raises(FetchError, match="No archive snapshot"):
                await fetcher.fetch("https://example.com/article")
        await fetcher.close()


# ---------------------------------------------------------------------------
# Archive.today path
# ---------------------------------------------------------------------------


class TestArchiveTodayMiss:
    """Archive.today redirects to its homepage on miss."""

    @pytest.mark.asyncio
    async def test_homepage_redirect_treated_as_miss(self):
        fetcher = ArchiveFetcher()

        async def fake_get(url, **_):
            if url == _WAYBACK_API:
                return _response(
                    json_data={"url": "x", "archived_snapshots": {}},
                )
            # Archive.today returns its homepage when no snapshot exists.
            return _response(text="home", url="https://archive.ph")

        with patch.object(fetcher._http, "get", new=fake_get):
            with pytest.raises(FetchError, match="No archive snapshot"):
                await fetcher.fetch("https://example.com/article")
        await fetcher.close()


# ---------------------------------------------------------------------------
# fetch_many()
# ---------------------------------------------------------------------------


class TestFetchMany:
    @pytest.mark.asyncio
    async def test_mixed_results_never_raises(self):
        fetcher = ArchiveFetcher()
        html = _rich_article_html()

        async def fake_get(url, **_):
            # First URL hits wayback successfully; second is not archivable
            # (search URL is filtered before network).
            if url == _WAYBACK_API:
                return _response(json_data=_wayback_payload())
            if "web.archive.org" in url:
                return _response(text=html)
            return _response(text="home", url="https://archive.ph/")

        with patch.object(fetcher._http, "get", new=fake_get):
            results = await fetcher.fetch_many([
                "https://example.com/article",
                "https://google.com/search?q=hello",
            ])

        assert len(results) == 2
        assert results[0].success is True
        assert len(results[0].content) >= 200
        assert results[1].success is False
        assert "non-static" in (results[1].error or "")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_empty_input_returns_empty(self):
        fetcher = ArchiveFetcher()
        assert await fetcher.fetch_many([]) == []
        await fetcher.close()


# ---------------------------------------------------------------------------
# close()
# ---------------------------------------------------------------------------


class TestClose:
    @pytest.mark.asyncio
    async def test_close_closes_http_client(self):
        fetcher = ArchiveFetcher()
        with patch.object(
            fetcher._http, "aclose", new_callable=AsyncMock,
        ) as mock_close:
            await fetcher.close()
        mock_close.assert_awaited_once()
