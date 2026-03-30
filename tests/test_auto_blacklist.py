"""Tests for supersearch.pipeline.auto_blacklist — automatic domain blacklisting."""

import json

from supersearch.models import ScoredResult
from supersearch.pipeline.auto_blacklist import (
    collect_low_score_domains,
    load_auto_blacklist,
    save_auto_blacklist,
    update_auto_blacklist,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _scored(url: str, score: float) -> ScoredResult:
    return ScoredResult(
        url=url, relevance=score, quality=score, final_score=score, reason="test"
    )


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
# Persistence (load / save)
# ---------------------------------------------------------------------------


class TestPersistence:

    def test_save_and_load(self, tmp_path):
        path = str(tmp_path / "bl.json")
        domains = {"spam.com", "junk.org"}
        save_auto_blacklist(domains, path)
        loaded = load_auto_blacklist(path)
        assert loaded == domains

    def test_load_nonexistent_file(self, tmp_path):
        path = str(tmp_path / "nonexistent.json")
        result = load_auto_blacklist(path)
        assert result == set()

    def test_load_malformed_json(self, tmp_path):
        path = tmp_path / "bad.json"
        path.write_text("not json", encoding="utf-8")
        result = load_auto_blacklist(str(path))
        assert result == set()

    def test_load_wrong_type(self, tmp_path):
        path = tmp_path / "wrong.json"
        path.write_text('{"key": "value"}', encoding="utf-8")
        result = load_auto_blacklist(str(path))
        assert result == set()

    def test_save_creates_parent_dirs(self, tmp_path):
        path = str(tmp_path / "nested" / "deep" / "bl.json")
        save_auto_blacklist({"test.com"}, path)
        assert load_auto_blacklist(path) == {"test.com"}

    def test_saved_format_is_sorted_json(self, tmp_path):
        path = tmp_path / "bl.json"
        save_auto_blacklist({"z.com", "a.com"}, str(path))
        data = json.loads(path.read_text(encoding="utf-8"))
        assert data == ["a.com", "z.com"]


# ---------------------------------------------------------------------------
# update_auto_blacklist (end-to-end)
# ---------------------------------------------------------------------------


class TestUpdateAutoBlacklist:

    def test_adds_new_domains(self, tmp_path):
        path = str(tmp_path / "bl.json")
        scored = [_scored("https://spam.com/page", 0.1)]
        result = update_auto_blacklist(scored, threshold=0.3, path=path)
        assert "spam.com" in result

    def test_preserves_existing_domains(self, tmp_path):
        path = str(tmp_path / "bl.json")
        save_auto_blacklist({"old.com"}, path)
        scored = [_scored("https://new-spam.com/page", 0.1)]
        result = update_auto_blacklist(scored, threshold=0.3, path=path)
        assert "old.com" in result
        assert "new-spam.com" in result

    def test_no_update_when_no_new_domains(self, tmp_path):
        path = str(tmp_path / "bl.json")
        save_auto_blacklist({"spam.com"}, path)
        scored = [_scored("https://spam.com/page", 0.1)]
        result = update_auto_blacklist(scored, threshold=0.3, path=path)
        assert result == {"spam.com"}

    def test_good_scores_no_blacklist(self, tmp_path):
        path = str(tmp_path / "bl.json")
        scored = [_scored("https://good.com/page", 0.9)]
        result = update_auto_blacklist(scored, threshold=0.3, path=path)
        assert result == set()
