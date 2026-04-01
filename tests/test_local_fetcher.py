"""Tests for diting.fetch.local — LocalFetcher with HTTP + browser escalation."""

from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from curl_cffi.requests.exceptions import RequestException
from playwright.async_api import Browser

from diting.fetch.local import (
    LocalFetcher,
    _detect_blockers,
    _MIN_BROWSER_FALLBACK_LENGTH,
    _MIN_HTML_LENGTH,
)
from diting.fetch.tavily import FetchError, FetchResult


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

TEST_URL = "https://example.com/page"
GOOD_HTML = "<html><body><p>Some meaningful page content here.</p></body></html>"
GOOD_MARKDOWN = "# Example\n\nSome meaningful page content here." + " Extra." * 50
SHORT_MARKDOWN = "thin"


def _make_fetcher(browser: Browser | None = None) -> LocalFetcher:
    """Create a LocalFetcher with a mocked HTTP session."""
    fetcher = LocalFetcher(browser=browser)
    fetcher._session = MagicMock()
    return fetcher


def _mock_http_response(
    fetcher: LocalFetcher,
    *,
    url: str = TEST_URL,
    html: str = GOOD_HTML,
    raise_exc: Exception | None = None,
) -> AsyncMock:
    """Patch fetcher._session.get to return a mock response or raise."""
    mock_get = AsyncMock()
    if raise_exc:
        mock_get.side_effect = raise_exc
    else:
        mock_resp = MagicMock()
        mock_resp.url = url
        mock_resp.text = html
        mock_resp.raise_for_status = MagicMock()
        mock_get.return_value = mock_resp
    fetcher._session.get = mock_get
    return mock_get


def _make_browser_mock() -> tuple[MagicMock, AsyncMock, AsyncMock]:
    """Create a MagicMock(spec=Browser) with a full new_context chain."""
    browser = MagicMock(spec=Browser)
    page = AsyncMock()
    page.url = TEST_URL
    page.content = AsyncMock(return_value=GOOD_HTML)
    page.close = AsyncMock()
    page.wait_for_timeout = AsyncMock()

    context = AsyncMock()
    context.new_page = AsyncMock(return_value=page)
    context.close = AsyncMock()

    browser.new_context = AsyncMock(return_value=context)

    return browser, context, page


# ---------------------------------------------------------------------------
# TestFetchHTTPSuccess
# ---------------------------------------------------------------------------


class TestFetchHTTPSuccess:
    """HTTP fetch returns markdown content without browser escalation."""

    async def test_fetch_returns_markdown(self):
        fetcher = _make_fetcher(browser=None)
        _mock_http_response(fetcher, html=GOOD_HTML)

        with patch(
            "diting.fetch.local._extract_markdown",
            return_value=("Example", GOOD_MARKDOWN, []),
        ):
            result = await fetcher.fetch(TEST_URL)

        assert result == GOOD_MARKDOWN

    async def test_browser_not_called_on_http_success(self):
        browser, context, page = _make_browser_mock()
        fetcher = _make_fetcher(browser=browser)
        _mock_http_response(fetcher, html=GOOD_HTML)

        with patch(
            "diting.fetch.local._extract_markdown",
            return_value=("Example", GOOD_MARKDOWN, []),
        ):
            await fetcher.fetch(TEST_URL)

        browser.new_context.assert_not_awaited()


# ---------------------------------------------------------------------------
# TestFetchHTTPFailureBrowserFallback
# ---------------------------------------------------------------------------


class TestFetchHTTPFailureBrowserFallback:
    """HTTP failure triggers browser fallback when browser is available."""

    async def test_browser_fallback_on_http_error(self):
        browser, context, page = _make_browser_mock()
        page.content = AsyncMock(return_value=GOOD_HTML)
        page.url = TEST_URL

        fetcher = _make_fetcher(browser=browser)
        _mock_http_response(fetcher, raise_exc=RequestException("connection refused"))

        with patch(
            "diting.fetch.local._extract_markdown",
            return_value=("Example", GOOD_MARKDOWN, []),
        ):
            result = await fetcher.fetch(TEST_URL)

        assert result == GOOD_MARKDOWN
        browser.new_context.assert_awaited_once()


# ---------------------------------------------------------------------------
# TestFetchHTTPFailureNoBrowser
# ---------------------------------------------------------------------------


class TestFetchHTTPFailureNoBrowser:
    """HTTP failure without a browser raises FetchError."""

    async def test_raises_fetch_error_when_no_browser(self):
        fetcher = _make_fetcher(browser=None)
        _mock_http_response(fetcher, raise_exc=RequestException("connection refused"))

        with pytest.raises(FetchError, match="Empty content"):
            await fetcher.fetch(TEST_URL)


# ---------------------------------------------------------------------------
# TestFetchAutoEscalation
# ---------------------------------------------------------------------------


class TestFetchAutoEscalation:
    """Thin or JS-shell HTTP content triggers automatic browser escalation."""

    async def test_thin_content_triggers_browser_escalation(self):
        browser, context, page = _make_browser_mock()
        page.content = AsyncMock(return_value="<html><body><p>Rich JS content</p></body></html>")
        page.url = TEST_URL

        fetcher = _make_fetcher(browser=browser)
        _mock_http_response(fetcher, html=GOOD_HTML)

        # First call returns thin markdown (HTTP), second returns good markdown (browser)
        with patch(
            "diting.fetch.local._extract_markdown",
            side_effect=[
                ("Title", SHORT_MARKDOWN, []),
                ("Title", GOOD_MARKDOWN, []),
            ],
        ):
            result = await fetcher.fetch(TEST_URL)

        assert result == GOOD_MARKDOWN
        browser.new_context.assert_awaited_once()

    async def test_js_shell_triggers_browser_escalation_even_with_long_content(self):
        """js_shell warning causes escalation regardless of markdown length."""
        browser, context, page = _make_browser_mock()
        browser_html = "<html><body><p>Full rendered content</p></body></html>"
        page.content = AsyncMock(return_value=browser_html)
        page.url = TEST_URL

        fetcher = _make_fetcher(browser=browser)
        # HTTP returns HTML containing "enable javascript" (triggers js_shell)
        js_shell_html = (
            "<html><body>You need to enable javascript</body></html>"
        )
        _mock_http_response(fetcher, html=js_shell_html)

        # First extraction has enough length but js_shell warning triggers escalation
        long_enough = "x" * (_MIN_BROWSER_FALLBACK_LENGTH + 50)
        with patch(
            "diting.fetch.local._extract_markdown",
            side_effect=[
                ("Title", long_enough, []),
                ("Title", GOOD_MARKDOWN, []),
            ],
        ):
            result = await fetcher.fetch(TEST_URL)

        assert result == GOOD_MARKDOWN
        browser.new_context.assert_awaited_once()


# ---------------------------------------------------------------------------
# TestFetchBrowserFallbackFailure
# ---------------------------------------------------------------------------


class TestFetchBrowserFallbackFailure:
    """Browser fallback failure propagates correctly through fetch/fetch_many."""

    async def test_fetch_raises_when_browser_fails_after_http_error(self):
        """fetch() propagates browser exception when HTTP failed first."""
        browser, context, page = _make_browser_mock()
        page.goto = AsyncMock(side_effect=Exception("browser crashed"))

        fetcher = _make_fetcher(browser=browser)
        _mock_http_response(fetcher, raise_exc=RequestException("connection refused"))

        with pytest.raises(Exception, match="browser crashed"):
            await fetcher.fetch(TEST_URL)

    async def test_fetch_many_captures_browser_failure(self):
        """fetch_many() captures browser exception as FetchResult(success=False)."""
        browser, context, page = _make_browser_mock()
        page.goto = AsyncMock(side_effect=Exception("browser crashed"))

        fetcher = _make_fetcher(browser=browser)
        _mock_http_response(fetcher, raise_exc=RequestException("connection refused"))

        results = await fetcher.fetch_many([TEST_URL])

        assert len(results) == 1
        assert results[0].success is False
        assert "browser crashed" in results[0].error


# ---------------------------------------------------------------------------
# TestFetchHardBlockers
# ---------------------------------------------------------------------------


class TestFetchHardBlockers:
    """Hard blockers (captcha, login_wall) prevent browser escalation."""

    async def test_captcha_raises_fetch_error(self):
        browser, context, page = _make_browser_mock()
        fetcher = _make_fetcher(browser=browser)
        captcha_html = "<html><body>Please solve this captcha to continue</body></html>"
        _mock_http_response(fetcher, html=captcha_html)

        with pytest.raises(FetchError, match="Empty content"):
            await fetcher.fetch(TEST_URL)

        browser.new_context.assert_not_awaited()

    async def test_login_wall_raises_fetch_error(self):
        browser, context, page = _make_browser_mock()
        fetcher = _make_fetcher(browser=browser)
        login_html = "<html><body>Please sign in to continue</body></html>"
        _mock_http_response(fetcher, html=login_html)

        with pytest.raises(FetchError, match="Empty content"):
            await fetcher.fetch(TEST_URL)

        browser.new_context.assert_not_awaited()


# ---------------------------------------------------------------------------
# TestFetchMany
# ---------------------------------------------------------------------------


class TestFetchMany:
    """fetch_many returns FetchResults in order, never raises."""

    async def test_returns_mixed_results_in_order(self):
        fetcher = _make_fetcher(browser=None)
        urls = ["https://a.com", "https://b.com", "https://c.com"]

        from diting.fetch.local import _FetchOutcome

        # URL-keyed outcomes to avoid coupling to coroutine scheduling order
        outcomes_by_url = {
            "https://a.com": _FetchOutcome(
                url="https://a.com", title="A", content="Content A",
                method="http", quality="high",
            ),
            "https://b.com": _FetchOutcome(
                url="https://b.com", title="B", content="Content B",
                method="http", quality="medium",
            ),
            "https://c.com": _FetchOutcome(
                url="https://c.com", title="C", content="",
                method="http", quality="low", warnings=["http_error:ConnectionError"],
            ),
        }

        async def side_effect(url):
            return outcomes_by_url[url]

        with patch.object(fetcher, "_fetch_single", side_effect=side_effect):
            results = await fetcher.fetch_many(urls)

        assert len(results) == 3
        assert results[0] == FetchResult(url="https://a.com", content="Content A", success=True)
        assert results[1] == FetchResult(url="https://b.com", content="Content B", success=True)
        assert results[2].success is False
        assert results[2].url == "https://c.com"
        assert results[2].content == ""


# ---------------------------------------------------------------------------
# TestFetchManyErrorCapture
# ---------------------------------------------------------------------------


class TestFetchManyErrorCapture:
    """fetch_many captures unexpected exceptions without affecting other URLs."""

    async def test_exception_captured_as_failed_result(self):
        fetcher = _make_fetcher(browser=None)
        urls = ["https://ok.com", "https://bad.com"]

        from diting.fetch.local import _FetchOutcome

        good_outcome = _FetchOutcome(
            url="https://ok.com", title="OK", content="Good content",
            method="http", quality="high",
        )

        async def side_effect(url):
            if "bad" in url:
                raise RuntimeError("unexpected crash")
            return good_outcome

        with patch.object(fetcher, "_fetch_single", side_effect=side_effect):
            results = await fetcher.fetch_many(urls)

        assert len(results) == 2
        assert results[0].success is True
        assert results[0].content == "Good content"
        assert results[1].success is False
        assert "unexpected crash" in results[1].error


# ---------------------------------------------------------------------------
# TestBrowserResourceCleanup
# ---------------------------------------------------------------------------


class TestBrowserResourceCleanup:
    """Browser page and context are closed even when goto raises."""

    async def test_page_and_context_closed_on_goto_error(self):
        browser, context, page = _make_browser_mock()
        page.goto = AsyncMock(side_effect=Exception("navigation timeout"))

        fetcher = _make_fetcher(browser=browser)

        with pytest.raises(Exception, match="navigation timeout"):
            await fetcher._fetch_via_browser(TEST_URL)

        page.close.assert_awaited_once()
        context.close.assert_awaited_once()


# ---------------------------------------------------------------------------
# TestDetectBlockers (unit test the module-level function)
# ---------------------------------------------------------------------------


class TestDetectBlockers:
    """Unit tests for _detect_blockers()."""

    def test_captcha_detected(self):
        html = "<html><body>Please complete this captcha</body></html>"
        warnings = _detect_blockers(html, "https://example.com")
        assert "captcha" in warnings

    def test_showcaptcha_detected(self):
        html = "<html><body><div id='showCaptcha'></div></body></html>"
        warnings = _detect_blockers(html, "https://example.com")
        assert "captcha" in warnings

    def test_js_shell_enable_javascript(self):
        html = "<html><body>Please enable javascript to continue</body></html>"
        warnings = _detect_blockers(html, "https://example.com")
        assert "js_shell" in warnings

    def test_no_js_shell_without_marker_phrases(self):
        """Long HTML without any configured js_shell marker does not trigger js_shell."""
        html = "<html><body><div>Normal content with scripts</div></body></html>"
        html = html + "<p>" + "x" * _MIN_HTML_LENGTH + "</p>"
        warnings = _detect_blockers(html, "https://example.com")
        assert "js_shell" not in warnings

    def test_short_html_detected(self):
        html = "<html><body>Short</body></html>"
        assert len(html) < _MIN_HTML_LENGTH  # precondition
        warnings = _detect_blockers(html, "https://example.com")
        assert "short_html" in warnings

    def test_long_html_no_short_warning(self):
        html = "<html><body>" + "x" * _MIN_HTML_LENGTH + "</body></html>"
        warnings = _detect_blockers(html, "https://example.com")
        assert "short_html" not in warnings

    def test_clean_page_no_warnings(self):
        html = "<html><body>" + "x" * _MIN_HTML_LENGTH + "</body></html>"
        warnings = _detect_blockers(html, "https://example.com")
        assert warnings == []


# ---------------------------------------------------------------------------
# TestClose
# ---------------------------------------------------------------------------


class TestClose:
    """close() closes the HTTP session but NOT the browser."""

    async def test_close_calls_session_close(self):
        browser, context, page = _make_browser_mock()
        fetcher = _make_fetcher(browser=browser)
        fetcher._session.close = AsyncMock()

        await fetcher.close()

        fetcher._session.close.assert_awaited_once()

    async def test_close_does_not_close_browser(self):
        browser, context, page = _make_browser_mock()
        fetcher = _make_fetcher(browser=browser)
        fetcher._session.close = AsyncMock()
        browser.close = AsyncMock()

        await fetcher.close()

        browser.close.assert_not_awaited()
