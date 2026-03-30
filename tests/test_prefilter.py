"""Tests for supersearch.pipeline.prefilter — pre-scoring content filter."""

from supersearch.models import SearchResult
from supersearch.pipeline.prefilter import (
    DEFAULT_VIDEO_DOMAINS,
    prefilter,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _r(url: str, snippet: str = "A sufficiently long snippet for testing purposes here.", title: str = "Title") -> SearchResult:
    return SearchResult(title=title, url=url, snippet=snippet)


# ---------------------------------------------------------------------------
# Video domain filtering
# ---------------------------------------------------------------------------


class TestVideoDomainFilter:

    def test_filters_youtube(self):
        results = [_r("https://www.youtube.com/watch?v=abc"), _r("https://example.com/page")]
        kept, stats = prefilter(results)
        assert len(kept) == 1
        assert kept[0].url == "https://example.com/page"
        assert stats["video_domain"] == 1

    def test_filters_bilibili_video(self):
        results = [_r("https://www.bilibili.com/video/BV1abc/")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["video_domain"] == 1

    def test_allows_bilibili_read(self):
        results = [_r("https://www.bilibili.com/read/cv12345")]
        kept, _ = prefilter(results)
        assert len(kept) == 1

    def test_allows_bilibili_article(self):
        results = [_r("https://www.bilibili.com/article/12345")]
        kept, _ = prefilter(results)
        assert len(kept) == 1

    def test_filters_douyin(self):
        results = [_r("https://www.douyin.com/video/123")]
        kept, stats = prefilter(results)
        assert len(kept) == 0

    def test_filters_tiktok(self):
        results = [_r("https://www.tiktok.com/@user/video/123")]
        kept, stats = prefilter(results)
        assert len(kept) == 0

    def test_custom_video_domains_override(self):
        # Use a URL without a video path pattern so only domain filter applies.
        results = [
            _r("https://youtube.com/channel/abc"),
            _r("https://custom-video.com/page"),
        ]
        # Custom list does NOT include youtube, so youtube is kept.
        kept, stats = prefilter(results, video_domains=["custom-video.com"])
        assert len(kept) == 1
        assert kept[0].url == "https://youtube.com/channel/abc"

    def test_default_video_domains_list_complete(self):
        assert "youtube.com" in DEFAULT_VIDEO_DOMAINS
        assert "bilibili.com" in DEFAULT_VIDEO_DOMAINS
        assert "douyin.com" in DEFAULT_VIDEO_DOMAINS
        assert "tiktok.com" in DEFAULT_VIDEO_DOMAINS


# ---------------------------------------------------------------------------
# Video path filtering
# ---------------------------------------------------------------------------


class TestVideoPathFilter:

    def test_filters_watch_path_on_any_domain(self):
        results = [_r("https://example.com/watch?v=abc")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["video_path"] == 1

    def test_filters_video_path_on_non_video_domain(self):
        results = [_r("https://news.example.com/video/12345")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["video_path"] == 1

    def test_filters_shorts_path(self):
        results = [_r("https://example.com/shorts/abc")]
        kept, stats = prefilter(results)
        assert len(kept) == 0

    def test_allows_normal_path(self):
        results = [_r("https://example.com/article/12345")]
        kept, _ = prefilter(results)
        assert len(kept) == 1


# ---------------------------------------------------------------------------
# Search aggregator page filtering
# ---------------------------------------------------------------------------


class TestSearchPageFilter:

    def test_filters_douyin_search(self):
        # douyin.com is caught by video domain filter first; verify it's removed.
        results = [_r("https://www.douyin.com/search/some-query")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        # Counted as video_domain since that filter runs first.
        assert stats["video_domain"] == 1

    def test_filters_non_video_search_page(self):
        """Search page filter catches aggregator pages on non-video domains."""
        results = [_r("https://www.sogou.com/web?query=test")]
        kept, stats = prefilter(results)
        assert len(kept) == 0
        assert stats["search_page"] == 1

    def test_filters_baidu_search(self):
        results = [_r("https://www.baidu.com/s?wd=query")]
        kept, stats = prefilter(results)
        assert len(kept) == 0

    def test_filters_google_search(self):
        results = [_r("https://www.google.com/search?q=query")]
        kept, stats = prefilter(results)
        assert len(kept) == 0

    def test_can_disable_search_page_filter(self):
        results = [_r("https://www.douyin.com/search/some-query")]
        kept, _ = prefilter(results, filter_search_pages=False)
        # douyin.com is still filtered as video domain.
        # Use a non-video search page to test:
        results2 = [_r("https://www.baidu.com/s?wd=query")]
        kept2, _ = prefilter(results2, filter_search_pages=False)
        assert len(kept2) == 1


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
            _r("https://youtube.com/watch?v=1"),
            _r("https://bilibili.com/video/2"),
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
            _r("https://youtube.com/watch?v=1"),           # video_domain
            _r("https://example.com/video/123"),            # video_path
            _r("https://baidu.com/s?wd=test"),              # search_page
            _r("https://ok.com/page", snippet="short"),     # short_snippet
            _r("https://a.com/page", snippet="Same long snippet content for dedup testing purposes."),
            _r("https://b.com/page", snippet="Same long snippet content for dedup testing purposes."),  # fuzzy_dedup
            _r("https://good.com/page", snippet="Unique good content that should survive all filters."),
        ]
        kept, stats = prefilter(results)
        assert len(kept) == 2  # a.com + good.com
        assert stats["video_domain"] == 1
        assert stats["video_path"] == 1
        assert stats["search_page"] == 1
        assert stats["short_snippet"] == 1
        assert stats["fuzzy_dedup"] == 1
        assert stats["total_removed"] == 5

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
