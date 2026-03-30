"""Tests for supersearch.pipeline.dedup — URL normalization and deduplication."""

from __future__ import annotations

import pytest

from supersearch.models import SearchResult
from supersearch.pipeline.dedup import deduplicate, extract_domain, normalize_url


# ---------------------------------------------------------------------------
# normalize_url
# ---------------------------------------------------------------------------

class TestNormalizeUrl:
    def test_removes_utm_parameters(self) -> None:
        url = "https://example.com/page?utm_source=google&utm_medium=cpc&key=val"
        assert normalize_url(url) == "https://example.com/page?key=val"

    def test_removes_fbclid_and_gclid(self) -> None:
        url = "https://example.com/page?fbclid=abc123&gclid=xyz789&q=test"
        assert normalize_url(url) == "https://example.com/page?q=test"

    def test_removes_trailing_slash(self) -> None:
        assert normalize_url("https://example.com/page/") == "https://example.com/page"

    def test_preserves_root_path(self) -> None:
        assert normalize_url("https://example.com/") == "https://example.com/"

    def test_lowercases_scheme_and_host(self) -> None:
        assert normalize_url("HTTPS://Example.COM/Path") == "https://example.com/Path"

    def test_removes_fragments(self) -> None:
        url = "https://example.com/page#section-2"
        assert normalize_url(url) == "https://example.com/page"

    def test_sorts_query_parameters(self) -> None:
        url = "https://example.com/search?z=last&a=first&m=middle"
        assert normalize_url(url) == "https://example.com/search?a=first&m=middle&z=last"

    def test_upgrades_http_to_https(self) -> None:
        assert normalize_url("http://example.com/page") == "https://example.com/page"

    def test_handles_url_without_scheme(self) -> None:
        assert normalize_url("example.com/page") == "https://example.com/page"

    def test_handles_url_without_scheme_with_port(self) -> None:
        assert normalize_url("example.com:8080/page") == "https://example.com:8080/page"

    def test_preserves_meaningful_query_parameters(self) -> None:
        url = "https://example.com/search?q=python&page=2"
        assert normalize_url(url) == "https://example.com/search?page=2&q=python"

    def test_removes_default_port_http(self) -> None:
        # http:80 gets upgraded to https (port stripped first).
        assert normalize_url("http://example.com:80/page") == "https://example.com/page"

    def test_removes_default_port_https(self) -> None:
        assert normalize_url("https://example.com:443/page") == "https://example.com/page"

    def test_preserves_non_default_port(self) -> None:
        assert normalize_url("https://example.com:8080/page") == "https://example.com:8080/page"

    def test_empty_url_returns_empty(self) -> None:
        assert normalize_url("") == ""

    def test_whitespace_url_returns_empty(self) -> None:
        assert normalize_url("   ") == ""

    def test_removes_all_tracking_params_leaves_no_query(self) -> None:
        url = "https://example.com/page?utm_source=x&ref=y"
        assert normalize_url(url) == "https://example.com/page"

    def test_strips_all_utm_variants(self) -> None:
        url = (
            "https://example.com/page"
            "?utm_source=a&utm_medium=b&utm_campaign=c"
            "&utm_term=d&utm_content=e&utm_id=f"
        )
        assert normalize_url(url) == "https://example.com/page"

    def test_removes_source_param(self) -> None:
        url = "https://example.com/page?source=twitter&id=42"
        assert normalize_url(url) == "https://example.com/page?id=42"

    def test_rejects_mailto_scheme(self) -> None:
        assert normalize_url("mailto:test@example.com") == ""

    def test_rejects_ftp_scheme(self) -> None:
        assert normalize_url("ftp://files.example.com/data") == ""

    def test_rejects_javascript_scheme(self) -> None:
        assert normalize_url("javascript:alert(1)") == ""

    def test_malformed_port_non_numeric(self) -> None:
        assert normalize_url("https://example.com:abc/page") == ""

    def test_malformed_port_out_of_range(self) -> None:
        # urlparse treats very large port numbers as ValueError
        assert normalize_url("https://example.com:99999/page") == ""

    def test_drops_path_params(self) -> None:
        # Path parameters (;key=val) are intentionally dropped.
        url = "https://example.com/page;v=1?q=test"
        result = normalize_url(url)
        assert ";v=1" not in result
        assert "q=test" in result


# ---------------------------------------------------------------------------
# extract_domain
# ---------------------------------------------------------------------------

class TestExtractDomain:
    def test_extracts_domain_from_full_url(self) -> None:
        assert extract_domain("https://docs.example.com/guide") == "docs.example.com"

    def test_removes_www_prefix(self) -> None:
        assert extract_domain("https://www.example.com/page") == "example.com"

    def test_returns_lowercase(self) -> None:
        assert extract_domain("https://WWW.Example.COM/page") == "example.com"

    def test_handles_url_without_scheme(self) -> None:
        assert extract_domain("example.com/page") == "example.com"

    def test_handles_url_without_scheme_with_port(self) -> None:
        assert extract_domain("example.com:8080/page") == "example.com"

    def test_empty_url_returns_empty(self) -> None:
        assert extract_domain("") == ""

    def test_subdomain_preserved(self) -> None:
        assert extract_domain("https://blog.example.com/post") == "blog.example.com"

    def test_rejects_mailto_scheme(self) -> None:
        assert extract_domain("mailto:test@example.com") == ""

    def test_rejects_ftp_scheme(self) -> None:
        assert extract_domain("ftp://files.example.com/data") == ""


# ---------------------------------------------------------------------------
# deduplicate
# ---------------------------------------------------------------------------

class TestDeduplicate:
    @staticmethod
    def _result(title: str, url: str) -> SearchResult:
        return SearchResult(title=title, url=url, snippet=f"Snippet for {title}")

    def test_removes_duplicate_urls(self) -> None:
        results = [
            self._result("Page A", "https://example.com/page"),
            self._result("Page B", "https://example.com/page"),
        ]
        unique, _ = deduplicate(results)
        assert len(unique) == 1
        assert unique[0].title == "Page A"

    def test_keeps_first_occurrence(self) -> None:
        results = [
            self._result("First", "https://example.com/page"),
            self._result("Second", "https://example.com/page?utm_source=x"),
        ]
        unique, _ = deduplicate(results)
        assert len(unique) == 1
        assert unique[0].title == "First"

    def test_handles_empty_input(self) -> None:
        unique, seen = deduplicate([])
        assert unique == []
        assert seen == set()

    def test_cross_round_dedup_with_seen_urls(self) -> None:
        seen: set[str] = {"https://example.com/old"}
        results = [
            self._result("Old", "https://example.com/old"),
            self._result("New", "https://example.com/new"),
        ]
        unique, updated_seen = deduplicate(results, seen_urls=seen)
        assert len(unique) == 1
        assert unique[0].title == "New"
        assert "https://example.com/new" in updated_seen

    def test_returns_updated_seen_urls(self) -> None:
        results = [
            self._result("A", "https://a.com/page"),
            self._result("B", "https://b.com/page"),
        ]
        _, seen = deduplicate(results)
        assert "https://a.com/page" in seen
        assert "https://b.com/page" in seen

    def test_preserves_order_of_unique_results(self) -> None:
        results = [
            self._result("C", "https://c.com"),
            self._result("A", "https://a.com"),
            self._result("B", "https://b.com"),
        ]
        unique, _ = deduplicate(results)
        assert [r.title for r in unique] == ["C", "A", "B"]

    def test_none_seen_urls_creates_new_set(self) -> None:
        results = [self._result("A", "https://a.com")]
        unique, seen = deduplicate(results, seen_urls=None)
        assert len(unique) == 1
        assert len(seen) == 1

    def test_skips_empty_url_results(self) -> None:
        results = [
            self._result("Empty", ""),
            self._result("Valid", "https://example.com/page"),
        ]
        unique, seen = deduplicate(results)
        assert len(unique) == 1
        assert unique[0].title == "Valid"
        # Empty URL should not pollute seen_urls.
        assert "" not in seen

    def test_skips_non_web_scheme_results(self) -> None:
        results = [
            self._result("Mail", "mailto:test@example.com"),
            self._result("Valid", "https://example.com/page"),
        ]
        unique, seen = deduplicate(results)
        assert len(unique) == 1
        assert unique[0].title == "Valid"

    def test_handles_schemeless_url_with_port(self) -> None:
        results = [
            self._result("Dev", "example.com:8080/page"),
            self._result("Valid", "https://example.com/page"),
        ]
        unique, _ = deduplicate(results)
        assert len(unique) == 2

    def test_skips_malformed_port_results(self) -> None:
        results = [
            self._result("Bad Port", "https://example.com:abc/page"),
            self._result("Valid", "https://example.com/page"),
        ]
        unique, _ = deduplicate(results)
        assert len(unique) == 1
        assert unique[0].title == "Valid"
