"""Tests for diting.pipeline.prefilter — pre-scoring content filter."""

from diting.models import SearchResult
from diting.pipeline.prefilter import prefilter


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _r(url: str, snippet: str = "A sufficiently long snippet for testing purposes here.", title: str = "Title") -> SearchResult:
    return SearchResult(title=title, url=url, snippet=snippet)


# ---------------------------------------------------------------------------
# Snippet length filtering
# ---------------------------------------------------------------------------


class TestSnippetFilter:

    def test_filters_short_snippet(self):
        results = [_r("https://example.com/page", snippet="Too short")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["short_snippet"] == 1

    def test_allows_sufficient_snippet(self):
        results = [_r("https://example.com/page", snippet="A" * 30)]
        kept, _ = prefilter(results)
        assert len(kept) == 1

    def test_custom_min_snippet_length(self):
        results = [_r("https://example.com/page", snippet="A" * 15)]
        kept, _ = prefilter(results, min_snippet_length=10)
        assert len(kept) == 1

    def test_strips_whitespace_before_checking(self):
        results = [_r("https://example.com/page", snippet="   short   ")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["short_snippet"] == 1


# ---------------------------------------------------------------------------
# Fuzzy dedup
# ---------------------------------------------------------------------------


class TestFuzzyDedup:

    def test_dedup_identical_snippet_prefix(self):
        snippet = "This is a common snippet that appears on multiple mirror sites with slight variations"
        results = [
            _r("https://site-a.com/page", snippet=snippet),
            _r("https://site-b.com/page", snippet=snippet),
            _r("https://site-c.com/page", snippet=snippet),
        ]
        kept, stats = prefilter(results)
        assert len(kept) == 1
        assert kept[0].url == "https://site-a.com/page"
        assert stats["fuzzy_dedup"] == 2

    def test_allows_different_snippets(self):
        results = [
            _r("https://site-a.com/page", snippet="A" * 50),
            _r("https://site-b.com/page", snippet="B" * 50),
        ]
        kept, _ = prefilter(results)
        assert len(kept) == 2

    def test_prefix_comparison_uses_first_100_chars(self):
        base = "X" * 100
        results = [
            _r("https://a.com/page", snippet=base + " extra A"),
            _r("https://b.com/page", snippet=base + " extra B"),
        ]
        kept, stats = prefilter(results)
        assert len(kept) == 1
        assert stats["fuzzy_dedup"] == 1


# ---------------------------------------------------------------------------
# Edge cases
# ---------------------------------------------------------------------------


class TestEdgeCases:

    def test_empty_input(self):
        kept, stats = prefilter([])
        assert kept == []
        assert stats["total_removed"] == 0

    def test_all_filtered_out(self):
        results = [
            _r("https://example.com/a", snippet="short"),
            _r("https://example.com/b", snippet="tiny"),
        ]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["total_removed"] == 2

    def test_nothing_filtered(self):
        results = [
            _r("https://example.com/a", snippet="Unique snippet A for testing purposes."),
            _r("https://other.com/b", snippet="Unique snippet B for testing purposes."),
        ]
        kept, stats = prefilter(results)
        assert len(kept) == 2
        assert stats["total_removed"] == 0

    def test_stats_sum_correctly(self):
        results = [
            _r("https://ok.com/page", snippet="short"),     # short_snippet
            _r("https://a.com/page", snippet="Same long snippet content for dedup testing purposes."),
            _r("https://b.com/page", snippet="Same long snippet content for dedup testing purposes."),  # fuzzy_dedup
            _r("https://good.com/page", snippet="Unique good content that should survive all filters."),
        ]
        kept, stats = prefilter(results)
        assert len(kept) == 2  # a.com + good.com
        assert stats["short_snippet"] == 1
        assert stats["fuzzy_dedup"] == 1
        assert stats["total_removed"] == 2

    def test_preserves_order(self):
        results = [
            _r("https://c.com/page", snippet="C content long enough for testing purposes."),
            _r("https://a.com/page", snippet="A content long enough for testing purposes."),
            _r("https://b.com/page", snippet="B content long enough for testing purposes."),
        ]
        kept, _ = prefilter(results)
        assert [r.url for r in kept] == [
            "https://c.com/page",
            "https://a.com/page",
            "https://b.com/page",
        ]
