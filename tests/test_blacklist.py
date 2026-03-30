"""Tests for supersearch.pipeline.blacklist — unified regex-based URL filtering."""

from __future__ import annotations

import re

from supersearch.models import ScoredResult
from supersearch.pipeline.blacklist import (
    AUTO_MARKER,
    append_auto_blacklist,
    collect_low_score_domains,
    extract_match_target,
    is_blacklisted,
    load_blacklist,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _scored(url: str, score: float) -> ScoredResult:
    return ScoredResult(
        url=url, relevance=score, quality=score, final_score=score, reason="test"
    )


# ---------------------------------------------------------------------------
# load_blacklist
# ---------------------------------------------------------------------------


class TestLoadBlacklist:

    def test_load_patterns(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text(
            "# Comment line\n"
            "\n"
            "^youtube\\.com\n"
            "bilibili\\.com/video/\n"
            "# Another comment\n",
            encoding="utf-8",
        )
        patterns = load_blacklist(str(f))
        assert len(patterns) == 2
        assert all(isinstance(p, re.Pattern) for p in patterns)

    def test_file_not_found(self, tmp_path):
        patterns = load_blacklist(str(tmp_path / "nonexistent.txt"))
        assert patterns == []

    def test_invalid_regex_skipped(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text(
            "[invalid\n"
            "^valid\\.com$\n",
            encoding="utf-8",
        )
        patterns = load_blacklist(str(f))
        assert len(patterns) == 1
        assert patterns[0].pattern == "^valid\\.com$"

    def test_empty_file(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text("", encoding="utf-8")
        patterns = load_blacklist(str(f))
        assert patterns == []


# ---------------------------------------------------------------------------
# extract_match_target
# ---------------------------------------------------------------------------


class TestExtractMatchTarget:

    def test_full_url(self):
        result = extract_match_target("https://www.youtube.com/watch?v=abc")
        assert result == "youtube.com/watch"

    def test_strips_www(self):
        result = extract_match_target("https://www.example.com/page")
        assert result == "example.com/page"

    def test_no_scheme(self):
        result = extract_match_target("example.com/page")
        assert result == "example.com/page"

    def test_empty_url(self):
        assert extract_match_target("") == ""

    def test_path_preserved(self):
        result = extract_match_target("https://bilibili.com/video/BV123")
        assert result == "bilibili.com/video/BV123"


# ---------------------------------------------------------------------------
# is_blacklisted
# ---------------------------------------------------------------------------


class TestIsBlacklisted:

    def test_domain_match(self):
        patterns = [re.compile(r"^youtube\.com")]
        assert is_blacklisted("https://www.youtube.com/watch?v=1", patterns) is True

    def test_path_match(self):
        # extract_match_target strips query strings, so match on the path segment.
        patterns = [re.compile(r"/watch$")]
        assert is_blacklisted("https://example.com/watch?v=1", patterns) is True

    def test_no_match(self):
        patterns = [re.compile(r"^youtube\.com")]
        assert is_blacklisted("https://example.com/page", patterns) is False

    def test_empty_patterns(self):
        assert is_blacklisted("https://example.com/page", []) is False

    def test_partial_path_match(self):
        patterns = [re.compile(r"bilibili\.com/video/")]
        assert is_blacklisted("https://bilibili.com/video/BV123", patterns) is True
        assert is_blacklisted("https://bilibili.com/read/cv123", patterns) is False


# ---------------------------------------------------------------------------
# collect_low_score_domains
# ---------------------------------------------------------------------------


class TestCollectLowScoreDomains:

    def test_all_below_threshold(self):
        scored = [
            _scored("https://bad.com/a", 0.1),
            _scored("https://bad.com/b", 0.2),
        ]
        result = collect_low_score_domains(scored, threshold=0.3)
        assert result == {"bad.com"}

    def test_one_above_protects_domain(self):
        scored = [
            _scored("https://mixed.com/a", 0.1),
            _scored("https://mixed.com/b", 0.5),
        ]
        result = collect_low_score_domains(scored, threshold=0.3)
        assert result == set()

    def test_multiple_bad_domains(self):
        scored = [
            _scored("https://bad1.com/a", 0.1),
            _scored("https://bad2.com/a", 0.2),
            _scored("https://good.com/a", 0.9),
        ]
        result = collect_low_score_domains(scored, threshold=0.3)
        assert result == {"bad1.com", "bad2.com"}

    def test_empty_input(self):
        result = collect_low_score_domains([], threshold=0.3)
        assert result == set()

    def test_exact_threshold_not_blacklisted(self):
        scored = [_scored("https://edge.com/a", 0.3)]
        result = collect_low_score_domains(scored, threshold=0.3)
        assert result == set()

    def test_just_below_threshold_blacklisted(self):
        scored = [_scored("https://edge.com/a", 0.29)]
        result = collect_low_score_domains(scored, threshold=0.3)
        assert result == {"edge.com"}


# ---------------------------------------------------------------------------
# append_auto_blacklist
# ---------------------------------------------------------------------------


class TestAppendAutoBlacklist:

    def test_append_new_domains(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text(
            "# Manual rules\n"
            f"{AUTO_MARKER}\n",
            encoding="utf-8",
        )
        added = append_auto_blacklist({"spam.com"}, str(f))
        assert added == {"spam.com"}
        content = f.read_text(encoding="utf-8")
        assert "^spam\\.com(/|$)" in content

    def test_dedup_existing(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text(
            f"{AUTO_MARKER}\n"
            "^spam\\.com(/|$)\n",
            encoding="utf-8",
        )
        added = append_auto_blacklist({"spam.com"}, str(f))
        assert added == set()

    def test_creates_marker_if_missing(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text("# Manual rules\n", encoding="utf-8")
        append_auto_blacklist({"bad.com"}, str(f))
        content = f.read_text(encoding="utf-8")
        assert AUTO_MARKER in content
        assert "^bad\\.com(/|$)" in content

    def test_creates_file_if_missing(self, tmp_path):
        f = tmp_path / "bl.txt"
        added = append_auto_blacklist({"new.com"}, str(f))
        assert added == {"new.com"}
        assert f.exists()
        content = f.read_text(encoding="utf-8")
        assert "^new\\.com(/|$)" in content

    def test_returns_added_set(self, tmp_path):
        f = tmp_path / "bl.txt"
        f.write_text(
            f"{AUTO_MARKER}\n"
            "^existing\\.com(/|$)\n",
            encoding="utf-8",
        )
        added = append_auto_blacklist({"existing.com", "fresh.com"}, str(f))
        assert added == {"fresh.com"}

    def test_escapes_dots(self, tmp_path):
        f = tmp_path / "bl.txt"
        append_auto_blacklist({"bad.com"}, str(f))
        content = f.read_text(encoding="utf-8")
        assert "^bad\\.com(/|$)" in content
        # Ensure the raw dot is NOT present as an unescaped pattern.
        lines = [l.strip() for l in content.splitlines() if l.strip() and not l.startswith("#")]
        for line in lines:
            assert line != "^bad.com(/|$)"
